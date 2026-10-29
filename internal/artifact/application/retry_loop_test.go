package application

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// TestHandleInspectorFailureThenRetrySucceeds drives the full retry loop:
// a transient inspector failure lands the owner in waiting_retry and returns
// the work item to available; the next claim re-inspects from the frozen
// facts and commits verified. The version advances exactly once (pending v1
// -> verified v2) and the intermediate waiting_retry state is visible in the
// owner row.
func TestHandleInspectorFailureThenRetrySucceeds(t *testing.T) {
	db := openArtifactTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "Show", "Show - 01.mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	correlation := `{"external_id":"tag:retry-loop","artifact_path":"` + canonicalPath + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
	seedArtifactOwner(t, db, "artifact-retry-loop", "task-retry-loop", "completed", "pending", correlation, `{}`)
	claim, store := claimArtifactWorkItem(t, db, "artifact-retry-loop", []byte(`{"download_task_id":"task-retry-loop","provider_task_id":"tag:retry-loop"}`))

	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	inspector := &fakeInspector{err: errors.New("transient inspection failure")}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)

	// First pass: transient inspection failure returns a retry outcome.
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != "artifact_inspect_failed" {
		t.Fatalf("first pass outcome=%+v, want artifact_inspect_failed retry", outcome)
	}
	finishArtifactOutcome(t, db, store, claim, outcome)

	// Intermediate state: owner waiting_retry, work item back to available.
	var state, code string
	if err := db.QueryRowContext(context.Background(), `SELECT state, failure_code FROM artifacts WHERE id = 'artifact-retry-loop'`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "waiting_retry" || code != "artifact_inspect_failed" {
		t.Fatalf("intermediate artifact state=%q code=%q", state, code)
	}
	assertWorkItemState(t, db, workitems.OwnerArtifact, "artifact-retry-loop", workitems.StateAvailable, "available after retry")

	// The inspector recovers; the next claim must re-inspect and verify.
	inspector.err = nil
	inspector.facts = domain.ArtifactFacts{Path: canonicalPath, SizeBytes: 1024, ObservedAt: at, MediaKind: "video"}
	makeArtifactWorkItemDue(t, db, claim.ID)
	claim2, err := store.ClaimDue(context.Background(), "artifact-test-worker", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatalf("re-claim after retry: %v", err)
	}

	outcome2, err := service.Handle(context.Background(), claim2)
	if err != nil {
		t.Fatal(err)
	}
	if outcome2.Kind != worker.OutcomeCommitted || outcome2.OwnerResult.State != "verified" {
		t.Fatalf("second pass outcome=%+v, want verified finish", outcome2)
	}
	finishArtifactOutcome(t, db, store, claim2, outcome2)

	// Terminal state: verified, version advanced exactly once per owner
	// transition (pending v1 -> waiting_retry v2 -> verified v3), files
	// recorded, work item succeeded, and the inspector ran once per claim.
	var version int64
	var verifiedAt any
	if err := db.QueryRowContext(context.Background(), `SELECT version, verified_at FROM artifacts WHERE id = 'artifact-retry-loop'`).Scan(&version, &verifiedAt); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("artifact version after retry loop = %d, want 3 (pending v1 -> waiting_retry v2 -> verified v3)", version)
	}
	if verifiedAt == nil {
		t.Fatal("artifact verified_at is null after retry loop")
	}
	var files int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM artifact_files WHERE artifact_id = 'artifact-retry-loop'`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("artifact files after retry loop = %d, want 1", files)
	}
	assertWorkItemState(t, db, workitems.OwnerArtifact, "artifact-retry-loop", workitems.StateSucceeded, "succeeded after retry loop")
	if len(inspector.paths) != 2 {
		t.Fatalf("inspector ran %d times, want 2 (one per claim)", len(inspector.paths))
	}
}

// makeArtifactWorkItemDue moves a retried work item into the claimable window
// (store.Retry schedules it at now+retryAfter).
func makeArtifactWorkItemDue(t *testing.T, db *sql.DB, workItemID string) {
	t.Helper()
	if _, err := ownerTestDB(t).ExecContext(context.Background(), `
UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, workItemID); err != nil {
		t.Fatalf("make work item due: %v", err)
	}
}

// assertWorkItemState asserts the single live work item of an owner is in the
// expected state (used for the intermediate and terminal retry loop steps).
func assertWorkItemState(t *testing.T, db *sql.DB, ownerType, ownerID, wantState, label string) {
	t.Helper()
	var state string
	if err := db.QueryRowContext(context.Background(), `
SELECT state FROM work_item_history
WHERE owner_type = $1 AND owner_id = $2
ORDER BY created_at DESC LIMIT 1`, ownerType, ownerID).Scan(&state); err != nil {
		t.Fatalf("read work item state (%s): %v", label, err)
	}
	if state != wantState {
		t.Fatalf("work item state %s = %q, want %q", label, state, wantState)
	}
}
