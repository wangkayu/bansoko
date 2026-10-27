//go:build linux || darwin

package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"golang.org/x/sys/unix"
)

// withCleanupEffects substitutes the capture rename and purge unlink for one
// test; nil keeps the production syscall.
func withCleanupEffects(t *testing.T, rename func(from, to verifiedEntry) error, unlink func(entry verifiedEntry) error) {
	t.Helper()
	saved := cleanupEffects
	if rename != nil {
		cleanupEffects.rename = rename
	}
	if unlink != nil {
		cleanupEffects.unlink = unlink
	}
	t.Cleanup(func() { cleanupEffects = saved })
}

type busyCleanupFixture struct {
	executor OSCleanupExecutor
	source   string
	target   string
	expected domain.FileObservation
}

func newBusyCleanupFixture(t *testing.T) busyCleanupFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "show", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("old version"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := NewOSCleanupExecutor(root)
	expected, err := e.Observe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	return busyCleanupFixture{executor: e, source: source, target: filepath.Join(root, "show", ".bansoko-cleanup-owner", "object"), expected: expected}
}

func (f busyCleanupFixture) capture(work string) maintenance.MaintenanceInvocation {
	return maintenance.MaintenanceInvocation{OwnerType: "replacement_deletion", OwnerID: "owner", Kind: "cleanup_capture", WorkItemID: work, OwnerVersion: 2, Attempt: []byte(`{"stage":"capture","work":"` + work + `"}`), Source: f.source, Target: f.target, Fingerprint: f.expected.Fingerprint}
}

func (f busyCleanupFixture) purge(t *testing.T, work string, captured domain.FileObservation) maintenance.MaintenanceInvocation {
	t.Helper()
	attempt, err := json.Marshal(map[string]any{"stage": "purge", "work": work, "capture_observation": captured})
	if err != nil {
		t.Fatal(err)
	}
	return maintenance.MaintenanceInvocation{OwnerType: "replacement_deletion", OwnerID: "owner", Kind: "cleanup_purge", WorkItemID: work, OwnerVersion: 4, Attempt: attempt, Source: f.target, Target: f.target, Fingerprint: f.expected.Fingerprint}
}

// A capture rename refused busy (SMB sharing violation = EBUSY) is a busy
// effect, leaves the source in place, and its started-without-final receipt
// resolves as not applied; the next invocation captures normally.
func TestCleanupCaptureBusyIsNotAppliedAndRetrySucceeds(t *testing.T) {
	for _, errno := range []unix.Errno{unix.EBUSY, unix.ETXTBSY, unix.EACCES, unix.EPERM, unix.EIO} {
		t.Run(errno.Error(), func(t *testing.T) {
			f := newBusyCleanupFixture(t)
			withCleanupEffects(t, func(verifiedEntry, verifiedEntry) error { return errno }, nil)
			first := f.capture("work-1")
			_, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, first)
			if !domain.FileBusy(err) || !errors.Is(err, errno) {
				t.Fatalf("capture error = %v, want busy %v", err, errno)
			}
			if raw, err := os.ReadFile(f.source); err != nil || string(raw) != "old version" {
				t.Fatalf("source after refused capture: %q %v", raw, err)
			}
			_, err = f.executor.ReadMaintenanceReceipt(context.Background(), first)
			if !errors.Is(err, maintenance.ErrMaintenanceNotApplied) {
				t.Fatalf("receipt of refused capture = %v, want not applied", err)
			}
			cleanupEffects = cleanupEffectSyscalls{rename: atomicRenameNoReplaceVerified, unlink: atomicUnlinkVerified}
			second := f.capture("work-2")
			if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, second); err != nil {
				t.Fatalf("retried capture: %v", err)
			}
			if r, err := f.executor.ReadMaintenanceReceipt(context.Background(), second); err != nil || !r.Applied {
				t.Fatalf("retried capture receipt %+v %v", r, err)
			}
		})
	}
}

// A directory the service may not write (a real EACCES, no substitution)
// refuses the private namespace before anything is captured: the capture is
// busy, the source stays, and once the directory is writable again a fresh
// invocation captures.
func TestCleanupCaptureInUnwritableDirectoryIsBusy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newBusyCleanupFixture(t)
	dir := filepath.Dir(f.source)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, f.capture("work-1"))
	if !domain.FileBusy(err) || !errors.Is(err, unix.EACCES) {
		t.Fatalf("capture in an unwritable directory = %v, want busy EACCES", err)
	}
	if raw, err := os.ReadFile(f.source); err != nil || string(raw) != "old version" {
		t.Fatalf("source after refused capture: %q %v", raw, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, f.capture("work-2")); err != nil {
		t.Fatalf("capture after the directory is writable: %v", err)
	}
	if _, err := os.Stat(f.source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still public after capture: %v", err)
	}
}

// EROFS is a read-only mount, not a busy file: it stays a plain failure.
func TestCleanupCaptureReadOnlyIsNotBusy(t *testing.T) {
	f := newBusyCleanupFixture(t)
	withCleanupEffects(t, func(verifiedEntry, verifiedEntry) error { return unix.EROFS }, nil)
	_, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, f.capture("work-1"))
	if err == nil || domain.FileBusy(err) {
		t.Fatalf("read-only capture error = %v, want a non-busy failure", err)
	}
}

// A refused capture whose source was replaced afterwards proves nothing: the
// receipt stays unresolved (neither not-started nor not-applied).
func TestCleanupCaptureBusyWithChangedSourceStaysUnresolved(t *testing.T) {
	f := newBusyCleanupFixture(t)
	withCleanupEffects(t, func(verifiedEntry, verifiedEntry) error { return unix.EBUSY }, nil)
	first := f.capture("work-1")
	if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, first); !domain.FileBusy(err) {
		t.Fatalf("capture error = %v", err)
	}
	if err := os.Remove(f.source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.source, []byte("another file entirely"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.executor.ReadMaintenanceReceipt(context.Background(), first)
	if err == nil || errors.Is(err, maintenance.ErrMaintenanceNotApplied) || errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("receipt with a changed source = %v, want unresolved", err)
	}
}

// A capture refused busy whose source vanished (and nothing captured) proves
// nothing either.
func TestCleanupCaptureBusyWithMissingSourceStaysUnresolved(t *testing.T) {
	f := newBusyCleanupFixture(t)
	withCleanupEffects(t, func(verifiedEntry, verifiedEntry) error { return unix.EBUSY }, nil)
	first := f.capture("work-1")
	if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, first); !domain.FileBusy(err) {
		t.Fatalf("capture error = %v", err)
	}
	if err := os.Remove(f.source); err != nil {
		t.Fatal(err)
	}
	_, err := f.executor.ReadMaintenanceReceipt(context.Background(), first)
	if err == nil || errors.Is(err, maintenance.ErrMaintenanceNotApplied) || errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("receipt with a missing source = %v, want unresolved", err)
	}
}

func captureForPurge(t *testing.T, f busyCleanupFixture) domain.FileObservation {
	t.Helper()
	if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, f.capture("work-c")); err != nil {
		t.Fatal(err)
	}
	captured, err := f.executor.Observe(context.Background(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	return captured
}

// A purge unlink refused busy keeps the captured object, and its receipt
// resolves as not applied; the next invocation deletes it.
func TestCleanupPurgeBusyIsNotAppliedAndRetrySucceeds(t *testing.T) {
	f := newBusyCleanupFixture(t)
	captured := captureForPurge(t, f)
	withCleanupEffects(t, nil, func(verifiedEntry) error { return unix.EBUSY })
	first := f.purge(t, "work-p1", captured)
	err := f.executor.PurgeMaintenance(context.Background(), f.target, f.expected, first)
	if !domain.FileBusy(err) || !errors.Is(err, unix.EBUSY) {
		t.Fatalf("purge error = %v, want busy", err)
	}
	if _, err := os.Stat(f.target); err != nil {
		t.Fatalf("captured object after refused purge: %v", err)
	}
	if _, err := f.executor.ReadMaintenanceReceipt(context.Background(), first); !errors.Is(err, maintenance.ErrMaintenanceNotApplied) {
		t.Fatalf("receipt of refused purge = %v, want not applied", err)
	}
	cleanupEffects = cleanupEffectSyscalls{rename: atomicRenameNoReplaceVerified, unlink: atomicUnlinkVerified}
	second := f.purge(t, "work-p2", captured)
	if err := f.executor.PurgeMaintenance(context.Background(), f.target, f.expected, second); err != nil {
		t.Fatalf("retried purge: %v", err)
	}
	if _, err := os.Stat(f.target); !os.IsNotExist(err) {
		t.Fatalf("retried purge left the object: %v", err)
	}
}

// A purge started without a final receipt whose object is gone proves
// nothing: an unlink that may have run keeps the crash gap unresolved.
func TestCleanupPurgeStartedWithoutObjectStaysUnresolved(t *testing.T) {
	f := newBusyCleanupFixture(t)
	captured := captureForPurge(t, f)
	withCleanupEffects(t, nil, func(entry verifiedEntry) error {
		if err := atomicUnlinkVerified(entry); err != nil {
			return err
		}
		return unix.EIO // the unlink ran but the answer was lost
	})
	first := f.purge(t, "work-p1", captured)
	if err := f.executor.PurgeMaintenance(context.Background(), f.target, f.expected, first); err == nil {
		t.Fatal("purge reported success")
	}
	_, err := f.executor.ReadMaintenanceReceipt(context.Background(), first)
	if err == nil || errors.Is(err, maintenance.ErrMaintenanceNotApplied) || errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		t.Fatalf("receipt of a lost unlink answer = %v, want unresolved", err)
	}
}

func unwritableDir(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// Reading the receipt of a capture whose namespace was refused never creates
// it: the namespace does not exist and cannot be created, so no start marker
// can exist and the stage did not start, on every read.
func TestCleanupReceiptOfRefusedNamespaceIsNotStarted(t *testing.T) {
	f := newBusyCleanupFixture(t)
	unwritableDir(t, filepath.Dir(f.source))
	first := f.capture("work-1")
	if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, first); !domain.FileBusy(err) {
		t.Fatalf("capture in an unwritable directory = %v, want busy", err)
	}
	for read := 1; read <= 2; read++ {
		if _, err := f.executor.ReadMaintenanceReceipt(context.Background(), first); !errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
			t.Fatalf("receipt read %d = %v, want not started", read, err)
		}
	}
	if _, err := os.Lstat(filepath.Dir(f.target)); !os.IsNotExist(err) {
		t.Fatalf("a receipt read created the namespace: %v", err)
	}
}

func namespaceEntries(t *testing.T, f busyCleanupFixture) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(f.target))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// The fault hook answers in place of the capture rename with the real
// receipts written around it, and a refused, unapplied invocation's markers
// are removed by DiscardRefusedMaintenance: the namespace keeps none of them.
func TestCleanupDiscardRefusedRemovesItsMarkers(t *testing.T) {
	f := newBusyCleanupFixture(t)
	f.executor.Hooks.Effect = func(stage string) error {
		if stage == "capture" {
			return unix.EBUSY
		}
		return nil
	}
	first := f.capture("work-1")
	if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, first); !domain.FileBusy(err) {
		t.Fatalf("hooked capture = %v, want busy", err)
	}
	if got := namespaceEntries(t, f); len(got) != 2 {
		t.Fatalf("refused capture left %v, want its lock and start marker", got)
	}
	if _, err := f.executor.ReadMaintenanceReceipt(context.Background(), first); !errors.Is(err, maintenance.ErrMaintenanceNotApplied) {
		t.Fatalf("receipt = %v, want not applied", err)
	}
	if err := f.executor.DiscardRefusedMaintenance(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if got := namespaceEntries(t, f); len(got) != 0 {
		t.Fatalf("discard left %v", got)
	}
	f.executor.Hooks.Effect = nil
	second := f.capture("work-2")
	if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, second); err != nil {
		t.Fatal(err)
	}
	if err := f.executor.DiscardRefusedMaintenance(context.Background(), second); err == nil {
		t.Fatal("a final receipt was discarded")
	}
	if r, err := f.executor.ReadMaintenanceReceipt(context.Background(), second); err != nil || !r.Applied {
		t.Fatalf("final receipt after a refused discard %+v %v", r, err)
	}
}

// A refusal of a read-only filesystem is not busy but read-only.
func TestCleanupCaptureReadOnlyIsReadOnly(t *testing.T) {
	f := newBusyCleanupFixture(t)
	withCleanupEffects(t, func(verifiedEntry, verifiedEntry) error { return unix.EROFS }, nil)
	_, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, f.capture("work-1"))
	if !domain.FileReadOnly(err) || domain.FileBusy(err) {
		t.Fatalf("read-only capture = %v, want read-only", err)
	}
}

// RestoreCaptured moves the captured object back to its public path, and
// refuses (moving nothing) a changed object or an occupied public path.
func TestCleanupRestoreCaptured(t *testing.T) {
	f := newBusyCleanupFixture(t)
	captured := captureForPurge(t, f)
	writeOccupant := func() {
		if err := os.WriteFile(f.source, []byte("new file"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeOccupant()
	if err := f.executor.RestoreCaptured(context.Background(), f.target, f.source, captured); err == nil {
		t.Fatal("restore replaced an occupied public path")
	}
	if raw, _ := os.ReadFile(f.source); string(raw) != "new file" {
		t.Fatalf("occupant changed: %q", raw)
	}
	if err := os.Remove(f.source); err != nil {
		t.Fatal(err)
	}
	if err := f.executor.RestoreCaptured(context.Background(), f.target, f.source, captured); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if raw, err := os.ReadFile(f.source); err != nil || string(raw) != "old version" {
		t.Fatalf("restored source %q %v", raw, err)
	}
	if _, err := os.Lstat(f.target); !os.IsNotExist(err) {
		t.Fatalf("captured object still present: %v", err)
	}
	if _, err := f.executor.MoveMaintenance(context.Background(), f.source, f.target, f.expected, f.capture("work-c2")); err != nil {
		t.Fatal(err)
	}
	other, err := f.executor.Observe(context.Background(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.target, []byte("changed!!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.executor.RestoreCaptured(context.Background(), f.target, f.source, other); err == nil {
		t.Fatal("restore moved a changed object")
	}
}

// An exclusive holder's sharing violation on an observation is busy; a
// permission answer is not.
func TestBusyObservationError(t *testing.T) {
	if err := busyObservationError(&os.PathError{Op: "open", Path: "/x", Err: unix.EBUSY}); !domain.FileBusy(err) || !errors.Is(err, unix.EBUSY) {
		t.Fatalf("EBUSY observation = %v", err)
	}
	if err := busyObservationError(&os.PathError{Op: "open", Path: "/x", Err: unix.EACCES}); domain.FileBusy(err) {
		t.Fatalf("EACCES observation = %v", err)
	}
}
