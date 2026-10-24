package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// seedOneShotMarker inserts the running one_shot_acquisitions row that the
// marker-first gate inside CreateOrMergeOneShot requires.
func seedOneShotMarker(t *testing.T, db *sql.DB, markerID, taskID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, state
)
VALUES ($1, 'anime-qb-test', 'sub-qb-test', 'source-qb-test', 'release-qb-test', $2,
        'corr-qb-test', 'oneshot-test-key', 'sha256:0000000000000000000000000000000000000000000000000000000000000000',
        '{"test":true}'::jsonb, 'running')`, markerID, taskID); err != nil {
		t.Fatalf("seed one-shot marker: %v", err)
	}
}

// createClaimedOneShotQBSyncItem admits a one-shot qb_sync WorkItem through the
// marker gate and claims it, so the claim carries the OneShot reference exactly
// like a real download chain.
func createClaimedOneShotQBSyncItem(t *testing.T, db *sql.DB, store *workitems.Store, taskID string) workitems.Claim {
	return createClaimedOneShotItem(t, db, store, taskID, workitems.KindQBSync)
}

func createClaimedOneShotItem(t *testing.T, db *sql.DB, store *workitems.Store, taskID, kind string) workitems.Claim {
	t.Helper()
	ctx := context.Background()
	ref := workitems.OneShotRef{MarkerID: "marker-" + taskID, CorrelationID: "corr-qb-test", AdmissionEpoch: 1}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"trigger":"qb_sync_continuation"}`)
	if kind == workitems.KindQBSync {
		var ordinal int
		if err := tx.QueryRowContext(ctx, `SELECT next_poll_ordinal FROM download_tasks WHERE id=$1`, taskID).Scan(&ordinal); err != nil {
			t.Fatal(err)
		}
		input, err = json.Marshal(map[string]any{"trigger": "qb_sync_continuation", "poll_ordinal": ordinal})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.CreateOrMergeOneShot(ctx, txscope.Wrap(tx), ref, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerDownloadTask, ID: taskID},
		Kind:                 kind,
		IdempotencyKey:       "qb:release-qb-test",
		InputSnapshot:        input,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	}); err != nil {
		tx.Rollback()
		t.Fatalf("create one-shot qb_sync work item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `
UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second'
WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = $2 AND state = 'available'`, taskID, kind); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "test-worker", []string{kind}, time.Minute)
	if err != nil {
		t.Fatalf("claim one-shot qb_sync work item: %v", err)
	}
	if claim.OneShot == nil {
		t.Fatal("claimed qb_sync item carries no one-shot reference")
	}
	return claim
}

func countAvailableQBSyncItems(t *testing.T, db *sql.DB, taskID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `
SELECT count(*) FROM work_items
WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_sync' AND state = 'available'`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A one-shot download observed still downloading must leave a follow-up qb_sync
// behind: the autonomous dispatcher excludes one-shot tasks, so this
// continuation is the only thing keeping the progress chain alive.
func TestOneShotQBSyncProgressSchedulesContinuationWorkItem(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-oneshot-progress"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	seedOneShotMarker(t, db, "marker-"+taskID, taskID)
	provider := &fakeProvider{syncTask: domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskDownloading, ProviderState: "downloading", Progress: 0.42,
	}}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedOneShotQBSyncItem(t, db, store, taskID)

	_, err := store.AdmitOneShotEffect(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "downloading" {
		t.Fatalf("progressing one-shot sync outcome=%+v", outcome)
	}

	state, failureCode, _, _ := readTaskRow(t, db, taskID)
	if state != "downloading" || failureCode.Valid {
		t.Fatalf("task did not stay downloading: state=%q code=%q", state, failureCode.String)
	}
	var finishedState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id = $1`, claim.ID).Scan(&finishedState); err != nil {
		t.Fatal(err)
	}
	if finishedState != workitems.StateSucceeded {
		t.Fatalf("finished sync item state=%q, want succeeded", finishedState)
	}
	if n := countAvailableQBSyncItems(t, db, taskID); n != 1 {
		t.Fatalf("available qb_sync continuations = %d, want 1 (the chain must not terminate while the download is non-terminal)", n)
	}
	var trigger string
	var ownerVersion int64
	var availableAt time.Time
	if err := db.QueryRowContext(ctx, `
SELECT input_snapshot->>'trigger', expected_owner_version, available_at FROM work_items
WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_sync' AND state = 'available'`, taskID).Scan(&trigger, &ownerVersion, &availableAt); err != nil {
		t.Fatal(err)
	}
	if trigger != "qb_sync_progress" {
		t.Fatalf("continuation trigger=%q, want qb_sync_progress", trigger)
	}
	if ownerVersion != 2 {
		t.Fatalf("continuation expected_owner_version=%d, want the post-write task version 2", ownerVersion)
	}
	var dueIn string
	if err := db.QueryRowContext(ctx, `SELECT available_at - CURRENT_TIMESTAMP FROM work_items WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_sync' AND state = 'available'`, taskID).Scan(&dueIn); err != nil {
		t.Fatal(err)
	}
	if !availableAt.After(time.Now().UTC()) || strings.HasPrefix(dueIn, "00:00:00") {
		t.Fatalf("continuation must be delayed, available_at=%s due_in=%s", availableAt.UTC().Format(time.RFC3339Nano), dueIn)
	}

	// The continuation is the actual one-shot polling driver. Make it due and
	// execute it through the same store claim/handler/marker settlement path,
	// rather than merely asserting that a row exists.
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_sync' AND state = 'available'`, taskID); err != nil {
		t.Fatal(err)
	}
	// Advancing only available_at cannot advance the frozen poll interval.
	// Arrange the accepted monotone budget high-water at the next poll boundary.
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE download_tasks SET budget_clock_at=greatest(budget_clock_at,(active_poll_ticket->>'started_at')::timestamptz+poll_interval_seconds*INTERVAL '1 second') WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	provider.syncTask = domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskCompleted, ProviderState: "completed", Progress: 1, Path: "/downloads/QB Test.mkv",
	}
	followUp, err := store.ClaimDue(ctx, "test-worker-follow-up", []string{workitems.KindQBSync}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AdmitOneShotEffect(ctx, followUp)
	if err != nil {
		t.Fatal(err)
	}
	followUpOutcome, err := service.Handle(ctx, followUp)
	if err != nil {
		t.Fatal(err)
	}
	if followUpOutcome.Kind != worker.OutcomeCommitted || followUpOutcome.OwnerResult.State != "completed" {
		t.Fatalf("follow-up sync outcome=%+v", followUpOutcome)
	}
	state, _, _, _ = readTaskRow(t, db, taskID)
	if state != "completed" {
		t.Fatalf("follow-up sync did not complete task: state=%q", state)
	}
	if n := countAvailableQBSyncItems(t, db, taskID); n != 0 {
		t.Fatalf("completed follow-up left %d available qb_sync continuations", n)
	}
	var inspectCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE owner_type = 'artifact' AND kind = 'artifact_inspect' AND state IN ('available','leased')`).Scan(&inspectCount); err != nil {
		t.Fatal(err)
	}
	if inspectCount != 1 {
		t.Fatalf("follow-up completion handed off artifact inspect count=%d, want 1", inspectCount)
	}
}

// A submit result that leaves qB in progress must create only the initial
// continuation. The progress successor belongs exclusively to qb_sync.
func TestOneShotQBSubmitSubmissionCreatesInitialContinuationOnly(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-oneshot-submit"
	seedDownloadTask(t, db, taskID, "queued", "null")
	seedOneShotMarker(t, db, "marker-"+taskID, taskID)
	provider := &fakeProvider{fileSubmitTask: domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskSubmitted, ProviderState: "queued", Progress: 0,
	}}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedOneShotItem(t, db, store, taskID, workitems.KindQBSubmit)
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OwnerResult.State != "submitting" || outcome.Kind != worker.OutcomeCommitted {
		t.Fatalf("one-shot submit outcome=%+v", outcome)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM download_tasks WHERE id = $1`, taskID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "submitting" {
		t.Fatalf("submit owner state=%q, want submitting", state)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_sync' AND state = 'available'`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("submit continuation count=%d, want 1", count)
	}
	var trigger string
	if err := db.QueryRowContext(ctx, `SELECT input_snapshot->>'trigger' FROM work_items WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_sync' AND state = 'available'`, taskID).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if trigger != "qb_sync_continuation" {
		t.Fatalf("submit continuation trigger=%q, want qb_sync_continuation", trigger)
	}
}

// that observed the download complete.
func TestOneShotQBSyncCompletionEndsTheContinuationChain(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-oneshot-complete"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.42, "downloading"))
	seedOneShotMarker(t, db, "marker-"+taskID, taskID)
	provider := &fakeProvider{syncTask: domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskCompleted, ProviderState: "completed", Progress: 1, Path: "/downloads/QB Test.mkv",
	}}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedOneShotQBSyncItem(t, db, store, taskID)

	_, err := store.AdmitOneShotEffect(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "completed" {
		t.Fatalf("completed one-shot sync outcome=%+v", outcome)
	}

	state, _, _, _ := readTaskRow(t, db, taskID)
	if state != "completed" {
		t.Fatalf("task state=%q, want completed", state)
	}
	if n := countAvailableQBSyncItems(t, db, taskID); n != 0 {
		t.Fatalf("available qb_sync continuations = %d, want 0 (terminal completion must end the chain)", n)
	}
	var inspectCount int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM work_items
WHERE owner_type = 'artifact' AND kind = 'artifact_inspect' AND state IN ('available','leased')`).Scan(&inspectCount); err != nil {
		t.Fatal(err)
	}
	if inspectCount != 1 || !strings.Contains(claim.Owner.ID, taskID) {
		t.Fatalf("completion did not hand off to artifact inspection: inspect=%d owner=%q", inspectCount, claim.Owner.ID)
	}
}
