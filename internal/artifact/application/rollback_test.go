package application

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
)

// failureInjection installs a PostgreSQL trigger that raises inside the
// artifact_inspect fenced transaction, at the stage named by the case. The
// pattern mirrors the Subscribe rollback matrix in
// anime/application/candidate_postgres_test.go: the trigger is the fault, and
// every owned row must be gone after the failed finish.
type failureInjection struct {
	name  string
	table string
	event string // "BEFORE INSERT" or "BEFORE UPDATE"
}

func installArtifactFailureTrigger(t *testing.T, db *sql.DB, injection failureInjection) {
	t.Helper()
	trigger := fmt.Sprintf(`
CREATE FUNCTION reject_artifact_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected artifact write failure';
END;
$$;
CREATE TRIGGER reject_artifact_write
%s ON %s
FOR EACH ROW EXECUTE FUNCTION reject_artifact_write();`, injection.event, injection.table)
	if _, err := ownerTestDB(t).ExecContext(context.Background(), trigger); err != nil {
		t.Fatalf("install failure trigger on %s: %v", injection.table, err)
	}
}

// TestHandleVerifiedTransactionRollsBackOnOwnedWriteFailures asserts that a
// failure at any stage of the artifact_inspect fenced transaction (owner
// update, artifact file rows, work item finish) rolls back every write: the
// artifact stays pending at its original version, no artifact file rows or
// activity rows appear, and the work item never reaches a terminal state.
// The transaction writes no Activity at all (the artifact_verified
// entry is derived after commit by the independent consumer).
func TestHandleVerifiedTransactionRollsBackOnOwnedWriteFailures(t *testing.T) {
	injections := []failureInjection{
		{name: "early/artifact_owner", table: "artifacts", event: "BEFORE UPDATE"},
		{name: "mid/artifact_files", table: "artifact_files", event: "BEFORE INSERT"},
		{name: "late/work_item", table: "work_item_outcomes", event: "BEFORE INSERT"},
	}

	for _, injection := range injections {
		t.Run(injection.name, func(t *testing.T) {
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
			correlation := `{"external_id":"tag:rollback-task","artifact_path":"` + canonicalPath + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
			seedArtifactOwner(t, db, "artifact-rollback", "task-rollback", "completed", "pending", correlation, `{}`)
			claim, _ := claimArtifactWorkItem(t, db, "artifact-rollback", []byte(`{"download_task_id":"task-rollback","provider_task_id":"tag:rollback-task"}`))

			at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
			inspector := &fakeInspector{facts: domain.ArtifactFacts{Path: canonicalPath, SizeBytes: 1024, ObservedAt: at, MediaKind: "video"}}
			service, err := NewService(db, inspector, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			service.SetSettleDelay(0)
			installArtifactFailureTrigger(t, db, injection)

			// Inspection itself is outside the transaction and must succeed; the
			// injected failure fires when the fenced transaction commits the
			// verified owner result.
			_, err = service.Handle(context.Background(), claim)
			if err == nil || !strings.Contains(err.Error(), "injected artifact write failure") {
				t.Fatalf("typed commit must fail at injected owned write: %v", err)
			}

			assertArtifactRollbackClean(t, db, "artifact-rollback")
		})
	}
}

// TestHandleWaitingRetryTransactionRollsBackOnOwnedWriteFailures covers the
// failure-state path: a retry outcome that writes the waiting_retry owner
// transition must also roll back completely when the owner update fails.
func TestHandleWaitingRetryTransactionRollsBackOnOwnedWriteFailures(t *testing.T) {
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
	correlation := `{"external_id":"tag:rollback-retry","artifact_path":"` + canonicalPath + `","file_count":1,"path_snapshot":{"remote_root":"/downloads","application_root":"` + root + `"}}`
	seedArtifactOwner(t, db, "artifact-rollback-retry", "task-rollback-retry", "completed", "pending", correlation, `{}`)
	claim, _ := claimArtifactWorkItem(t, db, "artifact-rollback-retry", []byte(`{"download_task_id":"task-rollback-retry","provider_task_id":"tag:rollback-retry"}`))

	at := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	inspector := &fakeInspector{err: fmt.Errorf("injected inspection failure")}
	service, err := NewService(db, inspector, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)

	installArtifactFailureTrigger(t, db, failureInjection{name: "retry/artifact_owner", table: "artifacts", event: "BEFORE UPDATE"})

	_, err = service.Handle(context.Background(), claim)
	if err == nil || !strings.Contains(err.Error(), "injected artifact write failure") {
		t.Fatalf("typed retry must fail at injected owned write: %v", err)
	}

	assertArtifactRollbackClean(t, db, "artifact-rollback-retry")
}

// assertArtifactRollbackClean verifies that a failed fenced write left no
// partial state: the artifact is untouched, no artifact file rows or activity
// rows exist, and the work item is still leased (never terminal).
func assertArtifactRollbackClean(t *testing.T, db *sql.DB, artifactID string) {
	t.Helper()
	ctx := context.Background()

	var state string
	var version int64
	var inventory, failureCode sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT state, version, inventory_snapshot, failure_code FROM artifacts WHERE id = $1`, artifactID).Scan(&state, &version, &inventory, &failureCode); err != nil {
		t.Fatalf("artifact row missing after rollback: %v", err)
	}
	if state != "pending" || version != 1 || !inventory.Valid || inventory.String != "{}" || failureCode.Valid {
		t.Fatalf("artifact owner escaped rollback: state=%q version=%d inventory=%v failure_code=%v", state, version, inventory.String, failureCode.Valid)
	}

	var files int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_files WHERE artifact_id = $1`, artifactID).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Fatalf("artifact files escaped rollback: %d rows", files)
	}

	var terminalItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'artifact' AND owner_id = $1
  AND state IN ('succeeded', 'failed')`, artifactID).Scan(&terminalItems); err != nil {
		t.Fatal(err)
	}
	if terminalItems != 0 {
		t.Fatalf("work item escaped rollback: %d terminal rows", terminalItems)
	}
	var leasedItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_items
WHERE owner_type = 'artifact' AND owner_id = $1 AND state = 'leased'`, artifactID).Scan(&leasedItems); err != nil {
		t.Fatal(err)
	}
	if leasedItems != 1 {
		t.Fatalf("work item lease after rollback = %d, want exactly 1 (the original claim)", leasedItems)
	}

	var logs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscription_log_entries`).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if logs != 0 {
		t.Fatalf("activity rows escaped rollback: %d", logs)
	}
}
