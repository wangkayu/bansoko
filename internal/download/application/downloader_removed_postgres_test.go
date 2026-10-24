package application

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/download/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// A download deleted in qB while it is being observed
// ends the execution record cancelled, deleted in the downloader. Only a
// successful lookup that no longer lists the task is absence; two consecutive
// absent observations confirm it, presence resets it, and lookup errors keep the
// existing error budget untouched.

func absentErr() error {
	return fmt.Errorf("qB task %q was not found: %w", "tag:qb:release-qb-test", domain.ErrProviderTaskAbsent)
}

func downloadingProviderTask() domain.ProviderTask {
	complete, total := int64(100), int64(1000)
	return domain.ProviderTask{
		ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test",
		State: domain.TaskDownloading, ProviderState: "downloading", Progress: 0.1,
		ContentCompleteBytes: &complete, PayloadTotalBytes: &total,
	}
}

func readAbsenceCounters(t *testing.T, db *sql.DB, taskID string) (absent, consecutiveErrors, totalErrors int) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), `SELECT consecutive_absent, consecutive_errors, total_errors FROM download_tasks WHERE id=$1`, taskID).
		Scan(&absent, &consecutiveErrors, &totalErrors); err != nil {
		t.Fatal(err)
	}
	return absent, consecutiveErrors, totalErrors
}

func countDownloadFailedAdmissions(t *testing.T, db *sql.DB, taskID string) int {
	t.Helper()
	var count int
	if err := ownerTestDB(t).QueryRowContext(context.Background(), `SELECT count(*) FROM notification_admissions
WHERE trigger_kind='download_failed' AND source_owner_type='download_task' AND source_owner_id=$1`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func handleSync(t *testing.T, service *Service, claim workitems.Claim) worker.Outcome {
	t.Helper()
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted {
		t.Fatalf("sync outcome=%+v", outcome)
	}
	return outcome
}

func TestQBSyncTwoConsecutiveAbsencesCancelAsDeletedInTheDownloader(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-removed"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	provider := &fakeProvider{syncErr: absentErr()}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})

	claim := createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync)
	first := handleSync(t, service, claim)
	if first.OwnerResult.State != "downloading" || first.OwnerResult.ReasonCode != SyncCodeTaskAbsent {
		t.Fatalf("one absence is not a verdict: %+v", first)
	}
	state, code, _, _ := readTaskRow(t, db, taskID)
	if state != "downloading" || code.Valid {
		t.Fatalf("first absence changed the owner: %s %s", state, code.String)
	}
	if absent, errs, total := readAbsenceCounters(t, db, taskID); absent != 1 || errs != 0 || total != 0 {
		t.Fatalf("absence must not spend the error budget: absent=%d consecutive=%d total=%d", absent, errs, total)
	}
	if countAvailableQBSyncItems(t, db, taskID) != 1 {
		t.Fatal("a first absence must leave the confirming poll scheduled")
	}

	claim = claimNextTypedSync(t, db, store, taskID)
	second := handleSync(t, service, claim)
	if second.OwnerResult.State != "failed" || second.OwnerResult.ReasonCode != FailureDownloaderTaskRemoved {
		t.Fatalf("second absence outcome=%+v", second)
	}
	state, code, reason, correlation := readTaskRow(t, db, taskID)
	if state != "failed" || code.String != FailureDownloaderTaskRemoved || reason.String == "" {
		t.Fatalf("task after confirmation: %s %s %q", state, code.String, reason.String)
	}
	if correlation.ExternalID != "tag:qb:release-qb-test" {
		t.Fatalf("the verdict dropped the correlation: %+v", correlation)
	}
	if countAvailableQBSyncItems(t, db, taskID) != 0 {
		t.Fatal("a confirmed deletion left a successor poll")
	}
	if n := countDownloadFailedAdmissions(t, db, taskID); n != 0 {
		t.Fatalf("a deletion in the downloader raised %d download_failed admissions", n)
	}
	if provider.fileSubmitCalls != 0 || provider.lookupCalls != 0 {
		t.Fatalf("nothing may be re-added: %+v", provider)
	}

	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	root := "acq-" + taskID
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle: %q %v", status, err)
	}
	var rootState, reasonCode, stage string
	if err := db.QueryRowContext(ctx, `SELECT state, terminal_receipt->'primary_failure'->>'reason_code', terminal_receipt->'primary_failure'->>'stage'
FROM acquisitions WHERE id=$1`, root).Scan(&rootState, &reasonCode, &stage); err != nil {
		t.Fatal(err)
	}
	if want := settledStage(t, db, taskID); rootState != "cancelled" || reasonCode != FailureDownloaderTaskRemoved || stage != want {
		t.Fatalf("root settled %s/%s at %s, want cancelled/%s at %s", rootState, reasonCode, stage, FailureDownloaderTaskRemoved, want)
	}
}

// settledStage is the stage readUpstreamFailure names for the task: the
// download wait once it started, the submit before.
func settledStage(t *testing.T, db *sql.DB, taskID string) string {
	t.Helper()
	var waited bool
	if err := db.QueryRowContext(context.Background(), `SELECT wait_started_at IS NOT NULL FROM download_tasks WHERE id=$1`, taskID).Scan(&waited); err != nil {
		t.Fatal(err)
	}
	if waited {
		return "download_wait"
	}
	return "submit"
}

func TestQBSyncReappearanceResetsTheAbsenceCount(t *testing.T) {
	db := openDownloadDB(t)
	taskID := "task-qb-reappears"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	provider := &fakeProvider{syncErr: absentErr()}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})

	handleSync(t, service, createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync))
	if absent, _, _ := readAbsenceCounters(t, db, taskID); absent != 1 {
		t.Fatalf("absent=%d after one absence", absent)
	}
	provider.syncErr, provider.syncTask = nil, downloadingProviderTask()
	present := handleSync(t, service, claimNextTypedSync(t, db, store, taskID))
	if present.OwnerResult.State != "downloading" {
		t.Fatalf("a present task keeps downloading: %+v", present)
	}
	if absent, _, _ := readAbsenceCounters(t, db, taskID); absent != 0 {
		t.Fatalf("presence must reset the absence count, got %d", absent)
	}
	provider.syncErr, provider.syncTask = absentErr(), domain.ProviderTask{}
	again := handleSync(t, service, claimNextTypedSync(t, db, store, taskID))
	if again.OwnerResult.State != "downloading" {
		t.Fatalf("an absence after a reappearance starts over: %+v", again)
	}
	if state, code, _, _ := readTaskRow(t, db, taskID); state != "downloading" || code.Valid {
		t.Fatalf("absence, presence, absence must not cancel: %s %s", state, code.String)
	}
}

// A lookup error between two absences neither confirms nor resets the absence,
// and it is counted by the unchanged error budget.
func TestQBSyncLookupErrorIsNotAbsence(t *testing.T) {
	db := openDownloadDB(t)
	taskID := "task-qb-absent-error"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	provider := &fakeProvider{syncErr: absentErr()}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})

	handleSync(t, service, createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync))
	provider.syncErr = fmt.Errorf("request qB task lookup: %w", context.DeadlineExceeded)
	errored := handleSync(t, service, claimNextTypedSync(t, db, store, taskID))
	if errored.OwnerResult.State != "downloading" {
		t.Fatalf("a lookup error keeps polling: %+v", errored)
	}
	if absent, errs, total := readAbsenceCounters(t, db, taskID); absent != 1 || errs != 1 || total != 1 {
		t.Fatalf("after absence+error: absent=%d consecutive=%d total=%d", absent, errs, total)
	}
	provider.syncErr = absentErr()
	confirmed := handleSync(t, service, claimNextTypedSync(t, db, store, taskID))
	if confirmed.OwnerResult.State != "failed" || confirmed.OwnerResult.ReasonCode != FailureDownloaderTaskRemoved {
		t.Fatalf("the second definitive absence confirms the deletion: %+v", confirmed)
	}
}

// Lookup errors alone never become a deletion: they exhaust the existing budget
// as SYNC_ERROR_BUDGET_EXHAUSTED and keep the failure notification.
func TestQBSyncLookupErrorsKeepTheErrorBudget(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-errors-only"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	provider := &fakeProvider{syncErr: fmt.Errorf("qB task lookup failed with HTTP %d", 502)}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	var limit int
	if err := db.QueryRowContext(ctx, `SELECT max_consecutive_errors FROM download_tasks WHERE id=$1`, taskID).Scan(&limit); err != nil {
		t.Fatal(err)
	}
	claim := createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync)
	for poll := 1; poll <= limit; poll++ {
		handleSync(t, service, claim)
		if poll < limit {
			claim = claimNextTypedSync(t, db, store, taskID)
		}
	}
	state, code, _, _ := readTaskRow(t, db, taskID)
	if state != "failed" || code.String != "SYNC_ERROR_BUDGET_EXHAUSTED" {
		t.Fatalf("errors only: %s %s", state, code.String)
	}
	if absent, _, _ := readAbsenceCounters(t, db, taskID); absent != 0 {
		t.Fatalf("errors must never count as absence: %d", absent)
	}
	if n := countDownloadFailedAdmissions(t, db, taskID); n != 1 {
		t.Fatalf("an unobservable task keeps its failure notification, got %d", n)
	}
}

// A task still in submitting (the add was confirmed, the first observation has
// not seen it yet) follows the same rule and settles at the submit stage.
func TestQBSyncAbsenceWhileSubmittingCancelsAtSubmit(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-removed-submitting"
	seedDownloadTask(t, db, taskID, "submitting", `{"external_id":"tag:qb:release-qb-test","state":"submitted"}`)
	provider := &fakeProvider{syncErr: absentErr()}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})

	first := handleSync(t, service, createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync))
	if first.OwnerResult.State != "submitting" {
		t.Fatalf("first absence while submitting: %+v", first)
	}
	second := handleSync(t, service, claimNextTypedSync(t, db, store, taskID))
	if second.OwnerResult.State != "failed" || second.OwnerResult.ReasonCode != FailureDownloaderTaskRemoved {
		t.Fatalf("second absence while submitting: %+v", second)
	}
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	root := "acq-" + taskID
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle: %q %v", status, err)
	}
	var rootState, stage string
	if err := db.QueryRowContext(ctx, `SELECT state, terminal_receipt->'primary_failure'->>'stage' FROM acquisitions WHERE id=$1`, root).Scan(&rootState, &stage); err != nil {
		t.Fatal(err)
	}
	wantStage := settledStage(t, db, taskID)
	if rootState != "cancelled" || stage != wantStage {
		t.Fatalf("root %s at %s, want cancelled at %s", rootState, stage, wantStage)
	}
}

// A one-shot download follows the same rule.
func TestOneShotQBSyncAbsenceCancels(t *testing.T) {
	db := openDownloadDB(t)
	taskID := "task-qb-oneshot-removed"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	seedOneShotMarker(t, db, "marker-"+taskID, taskID)
	provider := &fakeProvider{syncErr: absentErr()}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})

	first := handleSync(t, service, createClaimedOneShotQBSyncItem(t, db, store, taskID))
	if first.OwnerResult.State != "downloading" {
		t.Fatalf("first one-shot absence: %+v", first)
	}
	second := handleSync(t, service, claimNextTypedSync(t, db, store, taskID))
	if second.OwnerResult.State != "failed" || second.OwnerResult.ReasonCode != FailureDownloaderTaskRemoved {
		t.Fatalf("second one-shot absence: %+v", second)
	}
	if n := countDownloadFailedAdmissions(t, db, taskID); n != 0 {
		t.Fatalf("one-shot deletion raised %d download_failed admissions", n)
	}
}

// The settlement function refuses a cancelled decision that the task row does
// not back: the settler cannot cancel a root on its own say-so.
func TestAcquisitionSettleRefusesAnUnbackedCancellation(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-unbacked-cancel"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE download_tasks SET state='failed', failure_code='SYNC_ERROR_BUDGET_EXHAUSTED' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	root := "acq-" + taskID
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT facts_revision FROM acquisitions WHERE id=$1`, root).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	receipt := `{"schema_version":1,"outcome":"cancelled","requirements_status":"not_reached","counts":null,"requirements_digest":null,"target_digest":null,` +
		`"primary_failure":{"stage":"download_wait","reason_code":"DOWNLOADER_TASK_REMOVED","file_ref":null},` +
		`"effect_summary":{"complete":false,"has_confirmed":false,"has_unknown":false},"settled_at":"2026-10-20T00:00:00Z"}`
	var raw []byte
	err := db.QueryRowContext(ctx, `SELECT acquisition_settle($1,$2,$3::jsonb,'[]'::jsonb)`, root, revision, receipt).Scan(&raw)
	if err == nil {
		t.Fatalf("an unbacked cancellation settled: %s", raw)
	}
}
