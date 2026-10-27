//go:build linux || darwin

package filemaintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/deployment"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// finishDelete claims the due replacement_delete item and finishes it the way
// the worker runtime does, including the post-transition hook.
func finishDelete(t *testing.T, f *cleanupFixture) worker.Outcome {
	t.Helper()
	ctx := context.Background()
	claim, err := f.store.ClaimDue(ctx, "cleanup-eacces-worker", []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatalf("claim replacement_delete: %v", err)
	}
	outcome, err := f.service.DeleteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handle replacement_delete: %v", err)
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

// A real EACCES (the replaced file's directory is not writable, the way an
// SMB share answers a file held open elsewhere) never leaves the deletion
// blocked behind an unknown effect: it waits as admitted, deployment
// recovery reports nothing unresolved, and once the directory is writable
// again the next hop deletes the file.
func TestCleanupRealPermissionRefusalRetriesThenDeletes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newCleanupFixture(t)
	op := f.admit(t, f.input())
	dir := filepath.Dir(f.source)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	finishDelete(t, f)
	state, code := f.state(t, op.ID)
	t.Logf("after refused capture: state=%s code=%s live=%d", state, code, liveCleanupWorkItems(t, f.db, op.ID))
	if state != CleanupAdmitted || code != CleanupFileBusy || liveCleanupWorkItems(t, f.db, op.ID) != 1 {
		t.Fatalf("state=%s code=%s, want admitted/%s with one live item", state, code, CleanupFileBusy)
	}
	assertFilePresent(t, f.source)
	result, err := f.service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("deployment reconcile: state=%s effect=%s reason=%q", result.State, result.Effect, result.Reason)
	if result.State != deployment.StateCompleted {
		t.Fatalf("deployment reconcile = %+v, want completed", result)
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at = now() WHERE owner_type = $1 AND owner_id = $2`, workitems.OwnerReplacementDeletion, op.ID); err != nil {
		t.Fatal(err)
	}
	if outcome := finishDelete(t, f); outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("released outcome = %+v", outcome)
	}
	if state, _ := f.state(t, op.ID); state != CleanupDeleted {
		t.Fatalf("state=%s, want deleted", state)
	}
	assertFileAbsent(t, f.source)
}

// A directory that stays unwritable keeps every hop waiting: no hop finds a
// receipt to doubt (reading it never creates the refused namespace), and once
// 24 hours have passed since the first refusal the deletion fails with the
// file in place and no namespace left behind.
func TestCleanupRealPermissionRefusalAcrossHopsFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newCleanupFixture(t)
	start := time.Now().UTC()
	f.service.now = func() time.Time { return start }
	op := f.admit(t, f.input())
	dir := filepath.Dir(f.source)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	due := func() {
		t.Helper()
		if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at = now() WHERE owner_type = $1 AND owner_id = $2`, workitems.OwnerReplacementDeletion, op.ID); err != nil {
			t.Fatal(err)
		}
	}
	for hop, elapsed := range []time.Duration{0, 5 * time.Minute, 12 * time.Hour} {
		f.service.now = func() time.Time { return start.Add(elapsed) }
		finishDelete(t, f)
		if state, code := f.state(t, op.ID); state != CleanupAdmitted || code != CleanupFileBusy || liveCleanupWorkItems(t, f.db, op.ID) != 1 {
			t.Fatalf("hop %d: state=%s code=%s, want admitted/%s with one live item", hop+1, state, code, CleanupFileBusy)
		}
		result, err := f.service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{})
		if err != nil || result.State != deployment.StateCompleted {
			t.Fatalf("hop %d: deployment reconcile = %+v %v, want completed", hop+1, result, err)
		}
		due()
	}
	f.service.now = func() time.Time { return start.Add(24 * time.Hour) }
	if outcome := finishDelete(t, f); outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("exhausted outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupFailed || code != CleanupFileBusy {
		t.Fatalf("state=%s code=%s, want failed/%s", state, code, CleanupFileBusy)
	}
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 0 {
		t.Fatalf("a failed deletion keeps %d live items", got)
	}
	assertFilePresent(t, f.source)
	assertFileAbsent(t, filepath.Join(dir, ".bansoko-cleanup-"+op.ID))
}
