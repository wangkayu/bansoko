//go:build linux || darwin

package filemaintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/deployment"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"golang.org/x/sys/unix"
)

// A purge refused busy on one hop and run to its end on the next, with the
// process stopped before that hop's settlement commits: the next hop's
// checkpoint is a fresh invocation (not the refused one), so worker recovery
// reads its final receipt and settles the deletion as deleted.
func TestCleanupPurgeAfterBusyHopInterruptedBeforeSettleRecoversDeleted(t *testing.T) {
	f := newBusyFixture(t)
	op := f.admit(t, f.input())
	f.busy.purgeErr = errBusy(unix.EBUSY)
	f.runBusyDelete(t)
	f.nextHop(t, op.ID)
	f.busy.purgeErr = nil

	ctx := context.Background()
	claim, err := f.store.ClaimDue(ctx, "cleanup-busy-worker", []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := f.service.DeleteHandler()
	outcome, err := handler.Handle(ctx, claim)
	if err != nil || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("purge hop outcome = %+v %v", outcome, err)
	}
	assertFileAbsent(t, filepath.Join(f.cleanupNamespace(op.ID), "object"))
	// The settlement is never written: the process stopped here.
	if state, _ := f.state(t, op.ID); state != CleanupAdmitted {
		t.Fatalf("state=%s before settlement, want admitted", state)
	}
	recovered := f.recoverInterrupted(t, claim)
	if recovered.Kind != worker.OutcomeFinish || recovered.OwnerResult.State != string(CleanupDeleted) || recovered.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("recovery outcome = %+v, want confirmed deleted", recovered)
	}
	if state, _ := f.state(t, op.ID); state != CleanupDeleted {
		t.Fatalf("state=%s, want deleted", state)
	}
}

// recoverInterrupted takes a claim whose process stopped before its
// settlement committed through worker recovery: the generation is fenced as
// effect_unknown, its lease runs out, the recovery claim observes the owner
// and the receipts, and the observed outcome is settled.
func (f busyFixture) recoverInterrupted(t *testing.T, interrupted workitems.Claim) worker.Outcome {
	t.Helper()
	ctx := context.Background()
	if err := f.store.BlockRecovery(ctx, interrupted, "effect_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, interrupted.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.store.ClaimRecovery(ctx, []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatalf("claim recovery: %v", err)
	}
	if claim.ID != interrupted.ID {
		t.Fatalf("recovery claimed %s, want %s", claim.ID, interrupted.ID)
	}
	recovered, err := f.service.DeleteHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Kind != worker.OutcomeFinish {
		return recovered
	}
	if err := f.store.SettleRecovery(ctx, claim, recovered.TerminalState, recovered.OwnerResult, recovered.Disposition, recovered.WriteOwner, recovered.PostTransition); err != nil {
		t.Fatalf("settle recovery: %v", err)
	}
	return recovered
}

// cleanupNamespace is the deletion's private namespace beside the file.
func (f busyFixture) cleanupNamespace(id string) string {
	return filepath.Join(filepath.Dir(f.source), ".bansoko-cleanup-"+id)
}

// namespaceEntries lists the private namespace (none when it is absent).
func (f busyFixture) namespaceEntries(t *testing.T, id string) []string {
	t.Helper()
	entries, err := os.ReadDir(f.cleanupNamespace(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func (f busyFixture) failureActivities(t *testing.T, id string) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM subscription_log_entries WHERE kind = 'cleanup_failed' AND subject_id = $1`, id)
}

func (f busyFixture) failureNotices(t *testing.T, id string) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM notification_admissions WHERE trigger_kind = 'replacement_deletion_failed' AND source_owner_id = $1`, id)
}

// A capture refused by an error that proves nothing blocks with the real
// receipts the refused invocation wrote: a start marker without a final one.
// A scan release reads them as not applied (the source is intact, nothing
// was captured) and re-admits the deletion, whose next claim runs the
// capture again and deletes the file.
func TestCleanupRefusedCaptureStartedWithoutFinalReruns(t *testing.T) {
	f := newBusyFixture(t)
	op := f.admit(t, f.input())
	f.busy.moveErr = errors.New("injected capture failure before the effect")
	f.runBusyDelete(t)
	if state, code := f.state(t, op.ID); state != CleanupBlocked || code != "cleanup_capture_failed" {
		t.Fatalf("state=%s code=%s, want blocked/cleanup_capture_failed", state, code)
	}
	if got := f.namespaceEntries(t, op.ID); len(got) != 2 {
		t.Fatalf("refused capture left %v, want its lock and start marker", got)
	}
	assertFilePresent(t, f.source)
	if n, err := f.service.ReleaseBlockedDeletions(context.Background(), "anime-1"); err != nil || n != 1 {
		t.Fatalf("release = %d %v, want 1", n, err)
	}
	f.busy.moveErr = nil
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("rerun outcome = %+v", outcome)
	}
	if state, _ := f.state(t, op.ID); state != CleanupDeleted {
		t.Fatalf("state=%s, want deleted", state)
	}
	assertFileAbsent(t, f.source)
}

// Every refused hop removes the lock and start marker its invocation wrote:
// a capture retried across hops leaves the namespace empty, a purge retried
// across hops keeps only what the finished capture left.
func TestCleanupBusyHopsLeaveNoMarkers(t *testing.T) {
	t.Run("capture", func(t *testing.T) {
		f := newBusyFixture(t)
		op := f.admit(t, f.input())
		f.busy.moveErr = errBusy(unix.EBUSY)
		for hop := 1; hop <= 3; hop++ {
			f.runBusyDelete(t)
			if got := f.namespaceEntries(t, op.ID); len(got) != 0 {
				t.Fatalf("hop %d left %v", hop, got)
			}
			f.nextHop(t, op.ID)
		}
	})
	t.Run("purge", func(t *testing.T) {
		f := newBusyFixture(t)
		op := f.admit(t, f.input())
		f.busy.purgeErr = errBusy(unix.EBUSY)
		f.runBusyDelete(t)
		first := f.namespaceEntries(t, op.ID)
		for hop := 2; hop <= 4; hop++ {
			f.nextHop(t, op.ID)
			f.runBusyDelete(t)
			if got := f.namespaceEntries(t, op.ID); len(got) != len(first) {
				t.Fatalf("hop %d namespace %v, first hop %v", hop, got, first)
			}
		}
		if state, code := f.state(t, op.ID); state != CleanupAdmitted || code != CleanupFileBusy {
			t.Fatalf("state=%s code=%s", state, code)
		}
	})
}

// A purge still refused when the window ends moves the captured file back to
// its public path before failing: the file is where it was, the Activity and
// the notice say it was not deleted.
func TestCleanupPurgeBusyWindowExhaustedRestoresFile(t *testing.T) {
	f := newBusyFixture(t)
	start := time.Now().UTC()
	f.service.now = func() time.Time { return start }
	op := f.admit(t, f.input())
	f.busy.purgeErr = errBusy(unix.EBUSY)
	f.runBusyDelete(t)
	assertFileAbsent(t, f.source)
	f.nextHop(t, op.ID)
	f.service.now = func() time.Time { return start.Add(cleanupBusyWindow) }
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("exhausted outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupFailed || code != CleanupFileBusy {
		t.Fatalf("state=%s code=%s, want failed/%s", state, code, CleanupFileBusy)
	}
	if got, err := os.ReadFile(f.source); err != nil || string(got) != "replaced episode" {
		t.Fatalf("restored file = %q %v", got, err)
	}
	assertFileAbsent(t, filepath.Join(f.cleanupNamespace(op.ID), "object"))
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 0 {
		t.Fatalf("a failed deletion keeps %d live items", got)
	}
	if a, n := f.failureActivities(t, op.ID), f.failureNotices(t, op.ID); a != 1 || n != 1 {
		t.Fatalf("activities=%d notices=%d, want 1/1", a, n)
	}
}

// failureCopy returns the summary of the deletion's failure Activity and the
// category its notice carries for the template.
func (f busyFixture) failureCopy(t *testing.T, id string) (summary, category string) {
	t.Helper()
	if err := f.db.QueryRow(`SELECT summary FROM subscription_log_entries WHERE kind = 'cleanup_failed' AND subject_id = $1`, id).Scan(&summary); err != nil {
		t.Fatalf("read failure activity: %v", err)
	}
	if err := f.db.QueryRow(`SELECT payload->>'result.category' FROM notification_admissions WHERE trigger_kind = 'replacement_deletion_failed' AND source_owner_id = $1`, id).Scan(&category); err != nil {
		t.Fatalf("read failure notice: %v", err)
	}
	return summary, category
}

// giveUpAfterCapture captures the file, refuses its purge busy and runs the
// hop at the end of the window, where the deletion gives up and restores.
func (f busyFixture) giveUpAfterCapture(t *testing.T, beforeGiveUp func()) *ReplacementDeletion {
	t.Helper()
	start := time.Now().UTC()
	f.service.now = func() time.Time { return start }
	op := f.admit(t, f.input())
	f.busy.purgeErr = errBusy(unix.EBUSY)
	f.runBusyDelete(t)
	if beforeGiveUp != nil {
		beforeGiveUp()
	}
	f.nextHop(t, op.ID)
	f.service.now = func() time.Time { return start.Add(cleanupBusyWindow) }
	return op
}

// When another file has taken the public path, the captured file cannot be
// moved back: the deletion fails with cleanup_restore_occupied and the file
// kept hidden, says so in its Activity and notice, and neither a scan release
// nor deployment recovery re-admits it (that would only repeat the window).
func TestCleanupPurgeRestoreOccupiedFails(t *testing.T) {
	f := newBusyFixture(t)
	op := f.giveUpAfterCapture(t, func() {
		if err := os.WriteFile(f.source, []byte("a new file"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("occupied outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupFailed || code != CleanupRestoreOccupied {
		t.Fatalf("state=%s code=%s, want failed/%s", state, code, CleanupRestoreOccupied)
	}
	assertFilePresent(t, filepath.Join(f.cleanupNamespace(op.ID), "object"))
	if got, err := os.ReadFile(f.source); err != nil || string(got) != "a new file" {
		t.Fatalf("occupying file = %q %v", got, err)
	}
	if a, n := f.failureActivities(t, op.ID), f.failureNotices(t, op.ID); a != 1 || n != 1 {
		t.Fatalf("activities=%d notices=%d, want 1/1", a, n)
	}
	if summary, category := f.failureCopy(t, op.ID); summary != "旧版本未删除：旧文件已移到同一文件夹的隐藏目录，原位置已有同名文件" || category != "path_occupied" {
		t.Fatalf("failure copy = %q / %q", summary, category)
	}
	if n, err := f.service.ReleaseBlockedDeletions(context.Background(), "anime-1"); err != nil || n != 0 {
		t.Fatalf("release = %d %v, want 0", n, err)
	}
	if result, err := f.service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{OperationID: "cleanup-reconcile"}); err != nil || result.State != deployment.StateCompleted {
		t.Fatalf("reconcile = %+v %v", result, err)
	}
	if state, _ := f.state(t, op.ID); state != CleanupFailed {
		t.Fatalf("state=%s after recovery, want failed", state)
	}
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 0 {
		t.Fatalf("an occupied restore keeps %d live items", got)
	}
}

// A restore that moved the file back but answered an error (a lost reply, a
// failed sync) is judged by the files: the captured object is at its public
// path, so the deletion fails busy like a clean restore.
func TestCleanupPurgeRestoreLandedDespiteErrorFails(t *testing.T) {
	f := newBusyFixture(t)
	op := f.giveUpAfterCapture(t, nil)
	f.busy.restore = func(real func() error) error {
		if err := real(); err != nil {
			return err
		}
		return errBusy(unix.EIO)
	}
	f.runBusyDelete(t)
	if state, code := f.state(t, op.ID); state != CleanupFailed || code != CleanupFileBusy {
		t.Fatalf("state=%s code=%s, want failed/%s", state, code, CleanupFileBusy)
	}
	if got, err := os.ReadFile(f.source); err != nil || string(got) != "replaced episode" {
		t.Fatalf("restored file = %q %v", got, err)
	}
	if _, category := f.failureCopy(t, op.ID); category != "busy" {
		t.Fatalf("category = %q, want busy", category)
	}
}

// A restore that did not move the file and proves nothing else blocks with
// cleanup_restore_failed and its attempt kept; the Activity and notice say
// the file is hidden. A scan release leaves it, deployment recovery
// re-admits it from the intact captured object, and the next claim deletes
// the file.
func TestCleanupPurgeRestoreFailureBlocksForRecovery(t *testing.T) {
	f := newBusyFixture(t)
	op := f.giveUpAfterCapture(t, nil)
	f.busy.restore = func(func() error) error { return errBusy(unix.EIO) }
	f.runBusyDelete(t)
	if state, code := f.state(t, op.ID); state != CleanupBlocked || code != CleanupRestoreFailed {
		t.Fatalf("state=%s code=%s, want blocked/%s", state, code, CleanupRestoreFailed)
	}
	assertFileAbsent(t, f.source)
	assertFilePresent(t, filepath.Join(f.cleanupNamespace(op.ID), "object"))
	if a, n := f.failureActivities(t, op.ID), f.failureNotices(t, op.ID); a != 1 || n != 1 {
		t.Fatalf("activities=%d notices=%d, want 1/1", a, n)
	}
	if summary, category := f.failureCopy(t, op.ID); summary != "旧版本未删除：旧文件已移到同一文件夹的隐藏目录，未能放回原位置" || category != "not_restored" {
		t.Fatalf("failure copy = %q / %q", summary, category)
	}
	if n, err := f.service.ReleaseBlockedDeletions(context.Background(), "anime-1"); err != nil || n != 0 {
		t.Fatalf("release = %d %v, want 0", n, err)
	}
	if state, _ := f.state(t, op.ID); state != CleanupBlocked {
		t.Fatalf("state=%s after release, want blocked", state)
	}
	f.busy.restore, f.busy.purgeErr = nil, nil
	if result, err := f.service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{OperationID: "cleanup-reconcile"}); err != nil || result.State != deployment.StateCompleted {
		t.Fatalf("reconcile = %+v %v", result, err)
	}
	if state, _ := f.state(t, op.ID); state != CleanupAdmitted {
		t.Fatalf("state=%s after recovery, want admitted", state)
	}
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("re-admitted outcome = %+v", outcome)
	}
	if state, _ := f.state(t, op.ID); state != CleanupDeleted {
		t.Fatalf("state=%s, want deleted", state)
	}
	assertFileAbsent(t, filepath.Join(f.cleanupNamespace(op.ID), "object"))
}

// A give-up that moved the file back and stopped before its settlement
// committed leaves the refused purge attempt with nothing captured and the
// object at its public path: worker recovery reads that as not captured and
// fails the deletion with the file in place instead of an unknown effect.
func TestCleanupRestoreInterruptedBeforeSettleRecovers(t *testing.T) {
	f := newBusyFixture(t)
	op := f.giveUpAfterCapture(t, nil)
	ctx := context.Background()
	claim, err := f.store.ClaimDue(ctx, "cleanup-busy-worker", []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := f.service.DeleteHandler()
	if outcome, err := handler.Handle(ctx, claim); err != nil || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("give-up outcome = %+v %v", outcome, err)
	}
	// The settlement is never written: the process stopped here.
	assertFilePresent(t, f.source)
	if state, _ := f.state(t, op.ID); state != CleanupAdmitted {
		t.Fatalf("state=%s before settlement, want admitted", state)
	}
	recovered := f.recoverInterrupted(t, claim)
	if recovered.Kind != worker.OutcomeFinish || recovered.OwnerResult.State != string(CleanupFailed) {
		t.Fatalf("recovery outcome = %+v, want failed", recovered)
	}
	if state, _ := f.state(t, op.ID); state != CleanupFailed {
		t.Fatalf("state=%s, want failed", state)
	}
	if got, err := os.ReadFile(f.source); err != nil || string(got) != "replaced episode" {
		t.Fatalf("restored file = %q %v", got, err)
	}
}

// A read-only filesystem fails the deletion on the first refusal with
// cleanup_file_refused, its Activity and notice; nothing retries it and a
// scan release leaves it failed.
func TestCleanupReadOnlyFailsWithoutRetry(t *testing.T) {
	f := newBusyFixture(t)
	op := f.admit(t, f.input())
	f.busy.moveErr = errBusy(unix.EROFS)
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("read-only outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupFailed || code != CleanupFileRefused {
		t.Fatalf("state=%s code=%s, want failed/%s", state, code, CleanupFileRefused)
	}
	assertFilePresent(t, f.source)
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 0 {
		t.Fatalf("a read-only deletion keeps %d live items", got)
	}
	if a, n := f.failureActivities(t, op.ID), f.failureNotices(t, op.ID); a != 1 || n != 1 {
		t.Fatalf("activities=%d notices=%d, want 1/1", a, n)
	}
	if summary, category := f.failureCopy(t, op.ID); summary != "旧版本未删除：文件所在位置只读，未能删除" || category != "read_only" {
		t.Fatalf("failure copy = %q / %q", summary, category)
	}
	if n, err := f.service.ReleaseBlockedDeletions(context.Background(), "anime-1"); err != nil || n != 0 {
		t.Fatalf("release = %d %v, want 0", n, err)
	}
	if got := f.namespaceEntries(t, op.ID); len(got) != 0 {
		t.Fatalf("read-only refusal left %v", got)
	}
}

// An exclusive holder refuses even the observation of the replaced file
// (EBUSY on open): the deletion waits on the same schedule without starting
// a stage, deletes the file once it is released, and gives up with its
// Activity and notice when the window ends.
func TestCleanupObservationBusyRetries(t *testing.T) {
	t.Run("released", func(t *testing.T) {
		f := newBusyFixture(t)
		op := f.admit(t, f.input())
		f.busy.observeBusy = true
		f.runBusyDelete(t)
		if state, code := f.state(t, op.ID); state != CleanupAdmitted || code != CleanupFileBusy {
			t.Fatalf("state=%s code=%s, want admitted/%s", state, code, CleanupFileBusy)
		}
		since, wait := f.nextHop(t, op.ID)
		if since == nil || wait < 4*time.Minute || wait > 6*time.Minute {
			t.Fatalf("next hop busy_since=%v due in %s", since, wait)
		}
		f.busy.observeBusy = false
		if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateSucceeded {
			t.Fatalf("released outcome = %+v", outcome)
		}
		assertFileAbsent(t, f.source)
	})
	t.Run("exhausted", func(t *testing.T) {
		f := newBusyFixture(t)
		start := time.Now().UTC()
		f.service.now = func() time.Time { return start }
		op := f.admit(t, f.input())
		f.busy.observeBusy = true
		f.runBusyDelete(t)
		f.nextHop(t, op.ID)
		f.service.now = func() time.Time { return start.Add(cleanupBusyWindow) }
		f.runBusyDelete(t)
		if state, code := f.state(t, op.ID); state != CleanupFailed || code != CleanupFileBusy {
			t.Fatalf("state=%s code=%s, want failed/%s", state, code, CleanupFileBusy)
		}
		assertFilePresent(t, f.source)
		if a, n := f.failureActivities(t, op.ID), f.failureNotices(t, op.ID); a != 1 || n != 1 {
			t.Fatalf("activities=%d notices=%d, want 1/1", a, n)
		}
	})
}

// A subscription deleted during the window does not stop the deletion from
// failing: it fails with its notice and without an Activity (there is no
// subscription to log it under).
func TestCleanupBusyGiveUpWithoutSubscription(t *testing.T) {
	f := newBusyFixture(t)
	start := time.Now().UTC()
	f.service.now = func() time.Time { return start }
	op := f.admit(t, f.input())
	f.busy.moveErr = errBusy(unix.EBUSY)
	f.runBusyDelete(t)
	if _, err := workItemFixtureDB(t).Exec(`DELETE FROM subscriptions WHERE id = 'sub-1'`); err != nil {
		t.Fatal(err)
	}
	f.nextHop(t, op.ID)
	f.service.now = func() time.Time { return start.Add(cleanupBusyWindow) }
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("exhausted outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupFailed || code != CleanupFileBusy {
		t.Fatalf("state=%s code=%s, want failed/%s", state, code, CleanupFileBusy)
	}
	if a, n := f.failureActivities(t, op.ID), f.failureNotices(t, op.ID); a != 0 || n != 1 {
		t.Fatalf("activities=%d notices=%d, want 0/1", a, n)
	}
}
