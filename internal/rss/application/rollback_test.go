package application

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// failureInjection installs a PostgreSQL trigger that raises inside the
// release_evaluate fenced transaction at the stage named by the case. The
// pattern mirrors artifact/application/rollback_test.go: the trigger is the
// fault, and every owned row must be gone after the failed finish.
type releaseFailureInjection struct {
	name  string
	table string
	event string
}

func installReleaseFailureTrigger(t *testing.T, db *sql.DB, injection releaseFailureInjection) {
	t.Helper()
	trigger := fmt.Sprintf(`
CREATE FUNCTION reject_release_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected release evaluate write failure';
END;
$$;
CREATE TRIGGER reject_release_write
%s ON %s
FOR EACH ROW EXECUTE FUNCTION reject_release_write();`, injection.event, injection.table)
	if _, err := ownerTestDB(t).ExecContext(context.Background(), trigger); err != nil {
		t.Fatalf("install failure trigger on %s: %v", injection.table, err)
	}
}

// TestReleaseEvaluateTransactionRollsBackOnOwnedWriteFailures asserts that a
// failure at any stage of the release_evaluate fenced transaction (download
// task creation, release owner update, activity append, work item finish)
// rolls back every write: the release stays discovered at its original
// version with no task, no qB sub-item appears, no activity rows appear, and
// the work item never reaches a terminal state.
func TestReleaseEvaluateTransactionRollsBackOnOwnedWriteFailures(t *testing.T) {
	injections := []releaseFailureInjection{
		{name: "early/download_task", table: "download_tasks", event: "BEFORE INSERT"},
		{name: "mid/release_owner", table: "releases", event: "BEFORE UPDATE"},
		{name: "observability/activity", table: "subscription_log_entries", event: "BEFORE INSERT"},
		{name: "late/work_item", table: "work_item_outcomes", event: "BEFORE INSERT"},
	}

	for _, injection := range injections {
		t.Run(injection.name, func(t *testing.T) {
			db := openRSSDatabase(t)
			sourceID, _ := seedRSSOwner(t, db, false, true)
			insertReleaseForEvaluation(t, db, sourceID, "release-rollback", "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")
			// A program-owned torrent payload and its manifest let the accepted
			// release create a DownloadTask at the task boundary (same seed as
			// the correlation lineage test).
			seedNativeTorrent(t, db, "torrent-rollback", "release-rollback")

			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
			if err != nil {
				t.Fatal(err)
			}
			initialVersion := currentReleaseVersion(t, db, "release-rollback")
			claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-rollback"}, workitems.KindReleaseEvaluate, initialVersion)

			installReleaseFailureTrigger(t, db, injection)

			// Evaluation itself happens outside the transaction; the injected
			// failure fires when the fenced transaction commits the owner result.
			outcome, err := service.Handle(context.Background(), claim)
			if err != nil {
				t.Fatalf("handle failed before the fenced write: %v", err)
			}
			if outcome.Kind != worker.OutcomeFinish {
				t.Fatalf("outcome = %+v, want finish before the injected failure", outcome)
			}
			if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err == nil {
				t.Fatal("finish unexpectedly succeeded despite injected owned-write failure")
			}

			assertReleaseRollbackClean(t, db, "release-rollback", initialVersion)
		})
	}
}

// assertReleaseRollbackClean verifies that a failed fenced write left no
// partial state: the release is untouched, no download task, qB sub-item,
// candidate/media fact, or activity row exists, and the release work item is
// still leased (never terminal).
func assertReleaseRollbackClean(t *testing.T, db *sql.DB, releaseID string, expectedVersion int64) {
	t.Helper()
	ctx := context.Background()

	var verdict, reasonCode string
	var version int64
	var taskID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT verdict, reason_code, version, download_task_id FROM releases WHERE id = $1`, releaseID).Scan(&verdict, &reasonCode, &version, &taskID); err != nil {
		t.Fatalf("release row missing after rollback: %v", err)
	}
	if verdict != "discovered" || reasonCode != "discovered" || version != expectedVersion || taskID.Valid {
		t.Fatalf("release owner escaped rollback: verdict=%q reason=%q version=%d task=%v", verdict, reasonCode, version, taskID.Valid)
	}

	var tasks int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Fatalf("download task escaped rollback: %d rows", tasks)
	}

	var qbItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'download_task'
  AND EXISTS (
    SELECT 1 FROM download_tasks task WHERE task.id = work_item_history.owner_id AND task.release_id = $1
  )`, releaseID).Scan(&qbItems); err != nil {
		t.Fatal(err)
	}
	if qbItems != 0 {
		t.Fatalf("qb submit work item escaped rollback: %d rows", qbItems)
	}

	var terminalItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'release' AND owner_id = $1
  AND state IN ('succeeded', 'failed')`, releaseID).Scan(&terminalItems); err != nil {
		t.Fatal(err)
	}
	if terminalItems != 0 {
		t.Fatalf("work item escaped rollback: %d terminal rows", terminalItems)
	}
	var leasedItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_items
WHERE owner_type = 'release' AND owner_id = $1 AND state = 'leased'`, releaseID).Scan(&leasedItems); err != nil {
		t.Fatal(err)
	}
	if leasedItems != 1 {
		t.Fatalf("work item lease after rollback = %d, want exactly 1 (the original claim)", leasedItems)
	}

	for _, table := range []string{"subscription_log_entries", "release_candidate_facts", "media_fact_snapshots"} {
		var rows int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s rows after rollback = %d, want 0", table, rows)
		}
	}
}
