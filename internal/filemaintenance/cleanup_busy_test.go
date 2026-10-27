//go:build linux || darwin

package filemaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	observabilityapplication "bansoko/internal/observability/application"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"golang.org/x/sys/unix"
)

// busyCleanupExecutor is the real OS cleanup executor whose capture or purge
// can answer busy before the syscall runs (the way a held-open SMB file
// refuses the rename or unlink); refused, if set, runs first to perturb the
// files.
type busyCleanupExecutor struct {
	filesystem.OSCleanupExecutor
	moveErr, purgeErr error
	refused           func()
}

func (e *busyCleanupExecutor) MoveMaintenance(ctx context.Context, source, target string, expected domain.FileObservation, i maintenance.MaintenanceInvocation) (string, error) {
	if e.moveErr != nil {
		if e.refused != nil {
			e.refused()
		}
		return "", e.moveErr
	}
	return e.OSCleanupExecutor.MoveMaintenance(ctx, source, target, expected, i)
}

func (e *busyCleanupExecutor) PurgeMaintenance(ctx context.Context, path string, expected domain.FileObservation, i maintenance.MaintenanceInvocation) error {
	if e.purgeErr != nil {
		return e.purgeErr
	}
	return e.OSCleanupExecutor.PurgeMaintenance(ctx, path, expected, i)
}

func errBusy(errno unix.Errno) error {
	return fmt.Errorf("%w: %w", domain.ErrFileBusy, errno)
}

type busyFixture struct {
	*cleanupFixture
	busy *busyCleanupExecutor
}

func newBusyFixture(t *testing.T) busyFixture {
	t.Helper()
	f := newCleanupFixture(t)
	busy := &busyCleanupExecutor{OSCleanupExecutor: f.executor.OSCleanupExecutor}
	f.service.SetFileExecutor(busy)
	ctx := context.Background()
	if _, err := f.db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-1', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-1', 'anime-1', 'custom', 'Show', 'active')`); err != nil {
		t.Fatal(err)
	}
	observability, err := observabilityapplication.NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.service.SetActivityWriter(observability)
	targets, err := plugintargetapplication.NewService(f.db, nil, f.store)
	if err != nil {
		t.Fatal(err)
	}
	f.service.SetBusinessDeliveryEnqueuer(targets)
	return busyFixture{cleanupFixture: f, busy: busy}
}

// runBusyDelete claims the due replacement_delete item and finishes it the
// way the worker runtime does, including the post-transition hook.
func (f busyFixture) runBusyDelete(t *testing.T) worker.Outcome {
	t.Helper()
	ctx := context.Background()
	claim, err := f.store.ClaimDue(ctx, "cleanup-busy-worker", []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatalf("claim replacement_delete: %v", err)
	}
	outcome, err := f.service.DeleteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handle replacement_delete: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("replacement_delete outcome = %+v", outcome)
	}
	if outcome.PostTransition != nil {
		err = f.store.FinishWithResultPostTransition(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition)
	} else {
		err = f.store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner)
	}
	if err != nil {
		t.Fatalf("finish replacement_delete: %v", err)
	}
	return outcome
}

// nextHop returns the live item's busy_since and how far in the future it is
// due, then makes it due now.
func (f busyFixture) nextHop(t *testing.T, id string) (*time.Time, time.Duration) {
	t.Helper()
	var raw []byte
	var wait float64
	if err := f.db.QueryRow(`SELECT input_snapshot, EXTRACT(EPOCH FROM available_at - now()) FROM work_items WHERE owner_type = $1 AND owner_id = $2 AND kind = $3`,
		workitems.OwnerReplacementDeletion, id, workitems.KindReplacementDelete).Scan(&raw, &wait); err != nil {
		t.Fatalf("read next hop: %v", err)
	}
	var input cleanupWorkInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at = now() WHERE owner_type = $1 AND owner_id = $2`, workitems.OwnerReplacementDeletion, id); err != nil {
		t.Fatal(err)
	}
	return input.BusySince, time.Duration(wait * float64(time.Second))
}

func (f busyFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A capture refused busy keeps the obligation admitted and hands it to the
// next item, due after five minutes; once the file is released the next hop
// deletes it.
func TestCleanupCaptureBusyRetriesThenDeletes(t *testing.T) {
	f := newBusyFixture(t)
	op := f.admit(t, f.input())
	f.busy.moveErr = errBusy(unix.EBUSY)
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("busy outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupAdmitted || code != CleanupFileBusy {
		t.Fatalf("state=%s code=%s, want admitted/%s", state, code, CleanupFileBusy)
	}
	assertFilePresent(t, f.source)
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 1 {
		t.Fatalf("busy retry must leave one live item, got %d", got)
	}
	since, wait := f.nextHop(t, op.ID)
	if since == nil || wait < 4*time.Minute || wait > 6*time.Minute {
		t.Fatalf("next hop busy_since=%v due in %s, want a window start and ~5m", since, wait)
	}

	// Still busy: the window start travels with the chain.
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("second busy outcome = %+v", outcome)
	}
	again, _ := f.nextHop(t, op.ID)
	if again == nil || !again.Equal(*since) {
		t.Fatalf("busy_since changed across hops: %v -> %v", since, again)
	}

	f.busy.moveErr = nil
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("released outcome = %+v", outcome)
	}
	if state, _ := f.state(t, op.ID); state != CleanupDeleted {
		t.Fatalf("state=%s, want deleted", state)
	}
	assertFileAbsent(t, f.source)
	if n := f.count(t, `SELECT count(*) FROM subscription_log_entries WHERE kind = 'cleanup_failed'`); n != 0 {
		t.Fatalf("a retried deletion wrote %d failure activities", n)
	}
}

// A purge refused busy retries the purge stage alone: the captured object is
// kept and the next hop deletes it.
func TestCleanupPurgeBusyRetriesThenDeletes(t *testing.T) {
	f := newBusyFixture(t)
	op := f.admit(t, f.input())
	f.busy.purgeErr = errBusy(unix.EACCES)
	f.runBusyDelete(t)
	captured := filepath.Join(filepath.Dir(f.source), ".bansoko-cleanup-"+op.ID, "object")
	if state, code := f.state(t, op.ID); state != CleanupAdmitted || code != CleanupFileBusy {
		t.Fatalf("state=%s code=%s", state, code)
	}
	assertFileAbsent(t, f.source)
	assertFilePresent(t, captured)
	f.nextHop(t, op.ID)
	f.busy.purgeErr = nil
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("released outcome = %+v", outcome)
	}
	assertFileAbsent(t, captured)
}

// Once 24 hours have passed since the first refusal the deletion fails with
// cleanup_file_busy, writes the cleanup_failed Activity and admits the
// replacement_deletion_failed notice, in the same transaction.
func TestCleanupBusyWindowExhaustedFailsWithActivityAndNotice(t *testing.T) {
	f := newBusyFixture(t)
	start := time.Now().UTC()
	f.service.now = func() time.Time { return start }
	op := f.admit(t, f.input())
	f.busy.moveErr = errBusy(unix.EBUSY)
	f.runBusyDelete(t)
	f.nextHop(t, op.ID)
	f.service.now = func() time.Time { return start.Add(cleanupBusyWindow) }
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("exhausted outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupFailed || code != CleanupFileBusy {
		t.Fatalf("state=%s code=%s, want failed/%s", state, code, CleanupFileBusy)
	}
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 0 {
		t.Fatalf("a failed deletion keeps %d live items", got)
	}
	assertFilePresent(t, f.source)
	if n := f.count(t, `SELECT count(*) FROM subscription_log_entries WHERE kind = 'cleanup_failed' AND outcome = 'failure' AND managed_anime_id = 'anime-1' AND subject_id = $1`, op.ID); n != 1 {
		t.Fatalf("cleanup_failed activities = %d, want 1", n)
	}
	var payload []byte
	if err := f.db.QueryRow(`SELECT payload FROM notification_admissions WHERE trigger_kind = 'replacement_deletion_failed' AND source_owner_type = 'replacement_deletion' AND source_owner_id = $1`, op.ID).Scan(&payload); err != nil {
		t.Fatalf("replacement_deletion_failed admission: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["file.name"] != filepath.Base(f.source) || fields["anime.title"] != "Show" || fields["result.reason"] != cleanupBusySummary {
		t.Fatalf("notice payload = %s", payload)
	}
}

// A busy answer whose files no longer prove the effect did not happen (the
// source vanished meanwhile) blocks like any other uncertain effect.
func TestCleanupCaptureBusyWithoutProofBlocks(t *testing.T) {
	f := newBusyFixture(t)
	op := f.admit(t, f.input())
	f.busy.moveErr = errBusy(unix.EBUSY)
	f.busy.refused = func() {
		if err := os.Remove(f.source); err != nil {
			t.Fatal(err)
		}
	}
	f.runBusyDelete(t)
	if state, code := f.state(t, op.ID); state != CleanupBlocked || code != "cleanup_capture_failed" {
		t.Fatalf("state=%s code=%s, want blocked/cleanup_capture_failed", state, code)
	}
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 0 {
		t.Fatalf("a blocked deletion keeps %d live items", got)
	}
}

// A scan completion re-releases the anime's blocked deletions whose receipts
// prove their state and leaves the others blocked.
func TestCleanupReleaseBlockedDeletions(t *testing.T) {
	f := newBusyFixture(t)
	op := f.admit(t, f.input())
	f.busy.purgeErr = errors.New("injected purge failure before the effect")
	f.runBusyDelete(t)
	if state, _ := f.state(t, op.ID); state != CleanupBlocked {
		t.Fatalf("state=%s, want blocked", state)
	}
	if n, err := f.service.ReleaseBlockedDeletions(context.Background(), "anime-other"); err != nil || n != 0 {
		t.Fatalf("release of another anime = %d %v", n, err)
	}
	if n, err := f.service.ReleaseBlockedDeletions(context.Background(), "anime-1"); err != nil || n != 1 {
		t.Fatalf("release = %d %v, want 1", n, err)
	}
	if state, code := f.state(t, op.ID); state != CleanupAdmitted || code != "cleanup_readmitted" {
		t.Fatalf("state=%s code=%s, want admitted/cleanup_readmitted", state, code)
	}
	f.busy.purgeErr = nil
	if outcome := f.runBusyDelete(t); outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("released outcome = %+v", outcome)
	}
}

func TestCleanupReleaseKeepsUnprovableBlocked(t *testing.T) {
	f := newBusyFixture(t)
	op := f.admit(t, f.input())
	f.busy.purgeErr = errors.New("injected purge failure before the effect")
	f.runBusyDelete(t)
	captured := filepath.Join(filepath.Dir(f.source), ".bansoko-cleanup-"+op.ID, "object")
	if err := os.Remove(captured); err != nil {
		t.Fatal(err)
	}
	if n, err := f.service.ReleaseBlockedDeletions(context.Background(), "anime-1"); err != nil || n != 0 {
		t.Fatalf("release = %d %v, want 0", n, err)
	}
	if state, _ := f.state(t, op.ID); state != CleanupBlocked {
		t.Fatalf("state=%s, want blocked", state)
	}
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 0 {
		t.Fatalf("an unprovable deletion was enqueued: %d", got)
	}
}
