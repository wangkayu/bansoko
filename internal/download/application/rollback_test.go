package application

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/workitems"
)

// qbFailureInjection installs a PostgreSQL trigger that raises inside the
// qb_submit / qb_sync fenced transaction at the stage named by the case. The
// pattern mirrors artifact/application/rollback_test.go: the trigger is the
// fault, and every owned row must be gone after the failed finish.
type qbFailureInjection struct {
	name  string
	table string
	event string
}

func installQBFailureTrigger(t *testing.T, db *sql.DB, injection qbFailureInjection) {
	t.Helper()
	trigger := fmt.Sprintf(`
CREATE FUNCTION reject_qb_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected qb write failure';
END;
$$;
CREATE TRIGGER reject_qb_write
%s ON %s
FOR EACH ROW EXECUTE FUNCTION reject_qb_write();`, injection.event, injection.table)
	if _, err := ownerTestDB(t).ExecContext(context.Background(), trigger); err != nil {
		t.Fatalf("install failure trigger on %s: %v", injection.table, err)
	}
}

// TestQBSyncCompletionTransactionRollsBackOnOwnedWriteFailures asserts that a
// failure at any stage of the qb_sync completion transaction (download task
// update, artifact creation, work item finish) rolls back every write: the
// task keeps its confirmed state, no artifact or inspect work item appears, no
// activity rows appear, and the work item never reaches a terminal state.
// The transaction writes no Activity at all (the download_changed
// entry is derived after commit by the independent consumer).
func TestQBSyncCompletionTransactionRollsBackOnOwnedWriteFailures(t *testing.T) {
	injections := []qbFailureInjection{
		{name: "early/download_task", table: "download_tasks", event: "BEFORE UPDATE OF state"},
		{name: "mid/artifact", table: "artifacts", event: "BEFORE INSERT"},
		{name: "late/work_item", table: "work_item_outcomes", event: "BEFORE INSERT"},
	}

	for _, injection := range injections {
		t.Run(injection.name, func(t *testing.T) {
			db := openDownloadDB(t)
			seedDownloadTask(t, db, "task-sync-rollback", "downloading", `{"external_id":"tag:qb:release-qb-test","stable_tag":"qb:release-qb-test"}`)
			provider := &fakeProvider{syncTask: domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test", State: domain.TaskCompleted, Progress: 1, Path: "/downloads/QB Test.mkv"}}
			service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
			claim := createClaimedQBWorkItem(t, db, store, "task-sync-rollback", workitems.KindQBSync)

			installQBFailureTrigger(t, db, injection)

			_, err := service.Handle(context.Background(), claim)
			if err == nil || !strings.Contains(err.Error(), "injected qb write failure") {
				t.Fatalf("typed completion did not fail at injected write: %v", err)
			}
			assertQBRollbackClean(t, db, "task-sync-rollback", claim.ID)
		})
	}
}

// TestQBSubmitTransactionRollsBackOnOwnedWriteFailures asserts that a failure
// during the qb_submit confirmed-submission transaction (download task
// update, release verdict promotion, work item finish) rolls back every
// write. The frozen input carries a path snapshot so the handler skips its
// separate freeze transaction and the injected failure targets the fenced
// finish transaction only.
func TestQBSubmitTransactionRollsBackOnOwnedWriteFailures(t *testing.T) {
	injections := []qbFailureInjection{
		{name: "early/download_task", table: "download_tasks", event: "BEFORE UPDATE OF state"},
		{name: "mid/release_verdict", table: "releases", event: "BEFORE UPDATE"},
		{name: "late/work_item", table: "work_item_outcomes", event: "BEFORE INSERT"},
	}

	for _, injection := range injections {
		t.Run(injection.name, func(t *testing.T) {
			db := openDownloadDB(t)
			seedDownloadTask(t, db, "task-submit-rollback", "queued", "null")
			// Pre-freeze the path snapshot so the handler skips its separate
			// freeze transaction; otherwise the trigger would fire inside the
			// handler-owned freeze transaction, not the fenced finish. The
			// frozen input is decoded strictly, so the snapshot must use the
			// exact download/domain.PathSnapshot shape (no path_mappings).
			if _, err := db.ExecContext(context.Background(), `
UPDATE download_tasks
SET frozen_input = jsonb_set(frozen_input, '{path_snapshot}', '{"remote_root":"/downloads","application_root":"/downloads"}'::jsonb)
WHERE id = 'task-submit-rollback'`); err != nil {
				t.Fatal(err)
			}
			provider := &fakeProvider{
				lookupFound: true,
				lookupTask:  domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", State: domain.TaskDownloading, Progress: 0.4},
			}
			config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
			service, store := newDownloadService(t, db, provider, config)
			claim := createClaimedQBWorkItem(t, db, store, "task-submit-rollback", workitems.KindQBSubmit)

			installQBFailureTrigger(t, db, injection)

			outcome, err := service.Handle(context.Background(), claim)
			if err == nil {
				t.Fatalf("typed Handle unexpectedly committed despite injected write failure: %+v", outcome)
			}
			var attempts int
			var ticket, effect string
			if err := db.QueryRowContext(context.Background(), `SELECT submit_attempts_started,submit_attempt_ticket->>'state' FROM download_tasks WHERE id=$1`, claim.Owner.ID).Scan(&attempts, &ticket); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(context.Background(), `SELECT submit_effect_class FROM download_tasks WHERE submit_work_item_id=$1`, claim.ID).Scan(&effect); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || ticket != "issued" || effect != "confirmed" {
				t.Fatalf("T07/receipt lost on T02 rollback: attempts=%d ticket=%s effect=%s", attempts, ticket, effect)
			}

			assertQBRollbackClean(t, db, "task-submit-rollback", claim.ID)
		})
	}
}

// assertQBRollbackClean verifies that a failed fenced write left no partial
// state: the download task is untouched, no artifact or inspect work item
// exists, the release verdict is unchanged, no activity rows exist, and the
// work item is still leased (never terminal).
func assertQBRollbackClean(t *testing.T, db *sql.DB, taskID, workID string) {
	t.Helper()
	ctx := context.Background()

	var state string
	var version int64
	var correlation sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT state, version, provider_correlation FROM download_tasks WHERE id = $1`, taskID).Scan(&state, &version, &correlation); err != nil {
		t.Fatalf("download task row missing after rollback: %v", err)
	}
	if version != 1 {
		t.Fatalf("download task version escaped rollback: version=%d", version)
	}
	if taskID == "task-submit-rollback" {
		// The submit seed starts without a confirmed correlation; a rollback
		// must not leave the confirmed submission behind.
		if correlation.Valid || state != "queued" {
			t.Fatalf("submit task escaped rollback: state=%q correlation=%v", state, correlation.Valid)
		}
	} else if state != "downloading" {
		t.Fatalf("sync task escaped rollback: state=%q", state)
	}

	var verdict string
	if err := db.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE id = 'release-qb-test'`).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	if verdict != "accepted_pending_download" {
		t.Fatalf("release verdict escaped rollback: %q", verdict)
	}

	var artifacts int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE download_task_id = $1`, taskID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if artifacts != 0 {
		t.Fatalf("artifact escaped rollback: %d rows", artifacts)
	}

	var inspectItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'artifact'
  AND EXISTS (
    SELECT 1 FROM artifacts art WHERE art.id = work_item_history.owner_id AND art.download_task_id = $1
  )`, taskID).Scan(&inspectItems); err != nil {
		t.Fatal(err)
	}
	if inspectItems != 0 {
		t.Fatalf("artifact inspect work item escaped rollback: %d rows", inspectItems)
	}

	var terminalItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE id = $1
	AND state IN ('succeeded', 'failed')`, workID).Scan(&terminalItems); err != nil {
		t.Fatal(err)
	}
	if terminalItems != 0 {
		t.Fatalf("work item escaped rollback: %d terminal rows", terminalItems)
	}
	var leasedItems int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_items
WHERE id = $1 AND state = 'leased'`, workID).Scan(&leasedItems); err != nil {
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
