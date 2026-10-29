package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"bansoko/internal/acquisition/terminal/settlement"
	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// A download cancelled before import (migration 103): the intent is recorded
// on the task, the download worker deletes the qB task with its data under
// the remove_with_data ownership rule and fails the task with the cause's
// code; the root settles cancelled and no download_failed notice is raised.

// scriptedCancelRemover answers successive delete attempts; the last effect
// repeats.
type scriptedCancelRemover struct {
	effects []string
	calls   []string
}

func (r *scriptedCancelRemover) RemoveCancelledDownload(_ context.Context, acquisitionID string, _, _ []byte) CancelRemoval {
	r.calls = append(r.calls, acquisitionID)
	effect := r.effects[0]
	if len(r.effects) > 1 {
		r.effects = r.effects[1:]
	}
	return CancelRemoval{Effect: effect, Code: "fixture"}
}

// syncHookProvider runs onSync inside the provider poll, after the poll gate
// committed and before the observation is written.
type syncHookProvider struct {
	*fakeProvider
	onSync func()
}

func (p *syncHookProvider) Sync(ctx context.Context, id string) (domain.ProviderTask, error) {
	if p.onSync != nil {
		p.onSync()
	}
	return p.fakeProvider.Sync(ctx, id)
}

func readRootVersion(t *testing.T, db *sql.DB, root string) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM acquisitions WHERE id=$1`, root).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func cancelAt(t *testing.T, db *sql.DB, root string, version int64, key string) (animeapplication.DownloadCancelResult, error) {
	t.Helper()
	service, err := animeapplication.NewDownloadCancelService(db)
	if err != nil {
		t.Fatal(err)
	}
	return service.CancelDownload(context.Background(), root, version, key)
}

// cancelNow cancels with the record version the caller just read.
func cancelNow(t *testing.T, db *sql.DB, root, key string) animeapplication.DownloadCancelResult {
	t.Helper()
	result, err := cancelAt(t, db, root, readRootVersion(t, db, root), key)
	if err != nil {
		t.Fatalf("cancel %s: %v", root, err)
	}
	if result.Replayed {
		t.Fatalf("a first cancel was replayed: %+v", result)
	}
	return result
}

func requireCancelRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 409 || business.Code != code {
		t.Fatalf("cancel error = %v, want 409 %s", err, code)
	}
}

func readCancelColumns(t *testing.T, db *sql.DB, taskID string) (cause, effect sql.NullString, dispatched bool) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), `SELECT cancel_cause, cancel_effect, cancel_dispatched_at IS NOT NULL FROM download_tasks WHERE id=$1`, taskID).
		Scan(&cause, &effect, &dispatched); err != nil {
		t.Fatal(err)
	}
	return cause, effect, dispatched
}

// settleRoot settles the task's root and returns its state, primary failure
// code and stage.
func settleRoot(t *testing.T, db *sql.DB, taskID string) (state, code, stage string) {
	t.Helper()
	ctx := context.Background()
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	root := "acq-" + taskID
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle %s: %q %v", root, status, err)
	}
	var reason, at sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT state, terminal_receipt->'primary_failure'->>'reason_code', terminal_receipt->'primary_failure'->>'stage' FROM acquisitions WHERE id=$1`, root).
		Scan(&state, &reason, &at); err != nil {
		t.Fatal(err)
	}
	return state, reason.String, at.String
}

func requireSettledCancelled(t *testing.T, db *sql.DB, taskID, code string) {
	t.Helper()
	want := settledStage(t, db, taskID)
	if state, reason, stage := settleRoot(t, db, taskID); state != "cancelled" || reason != code || stage != want {
		t.Fatalf("root settled %s/%s at %s, want cancelled/%s at %s", state, reason, stage, code, want)
	}
}

func TestUserCancelDeletesTheDownloadingTaskAndSettlesCancelled(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-user-cancel"
	root := "acq-" + taskID
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	provider := &fakeProvider{syncTask: downloadingProviderTask()}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	remover := &scriptedCancelRemover{effects: []string{CancelEffectDeletedWithData}}
	service.SetCancelRemover(remover)

	if first := handleSync(t, service, createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync)); first.OwnerResult.State != "downloading" {
		t.Fatalf("first poll: %+v", first)
	}
	if _, err := store.ClaimDue(ctx, "cancel-test", []string{workitems.KindQBSync}, time.Minute); !errors.Is(err, workitems.ErrNoDueWork) {
		t.Fatalf("the next poll must wait for its interval: %v", err)
	}

	version := readRootVersion(t, db, root)
	accepted, err := cancelAt(t, db, root, version, "cancel-key-0001")
	if err != nil || accepted.Replayed || accepted.Version != version+1 {
		t.Fatalf("cancel = %+v %v", accepted, err)
	}
	if cause, effect, dispatched := readCancelColumns(t, db, taskID); cause.String != "user_cancelled" || effect.Valid || dispatched {
		t.Fatalf("intent = %v %v %v", cause, effect, dispatched)
	}
	// The cancel wakes the waiting poll: the worker acts now, not at the
	// next poll interval.
	claim, err := store.ClaimDue(ctx, "cancel-test", []string{workitems.KindQBSync}, time.Minute)
	if err != nil {
		t.Fatalf("the cancel must wake the waiting sync: %v", err)
	}
	outcome := handleSync(t, service, claim)
	if outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != FailureUserCancelled {
		t.Fatalf("cancel outcome = %+v", outcome)
	}
	state, code, reason, _ := readTaskRow(t, db, taskID)
	if state != "failed" || code.String != FailureUserCancelled || reason.String == "" {
		t.Fatalf("task = %s %s %q", state, code.String, reason.String)
	}
	if _, effect, dispatched := readCancelColumns(t, db, taskID); effect.String != CancelEffectDeletedWithData || !dispatched {
		t.Fatalf("effect = %v dispatched=%v", effect, dispatched)
	}
	if provider.syncCalls != 1 {
		t.Fatalf("the cancel must not poll qB: %d polls", provider.syncCalls)
	}
	if len(remover.calls) != 1 || remover.calls[0] != root {
		t.Fatalf("remover calls = %v", remover.calls)
	}
	if n := countAvailableQBSyncItems(t, db, taskID); n != 0 {
		t.Fatalf("a cancelled task left %d polls", n)
	}
	if n := countDownloadFailedAdmissions(t, db, taskID); n != 0 {
		t.Fatalf("a cancel raised %d download_failed admissions", n)
	}
	requireSettledCancelled(t, db, taskID, FailureUserCancelled)

	// The accepted request replays; the key is bound to its request; a new
	// cancel of an ended record is refused.
	replay, err := cancelAt(t, db, root, version, "cancel-key-0001")
	if err != nil || !replay.Replayed || replay.Version != accepted.Version {
		t.Fatalf("replay = %+v %v", replay, err)
	}
	_, err = cancelAt(t, db, root, version+5, "cancel-key-0001")
	requireCancelRefusal(t, err, "IDEMPOTENCY_KEY_REUSED")
	_, err = cancelAt(t, db, root, readRootVersion(t, db, root), "cancel-key-0002")
	requireCancelRefusal(t, err, "DOWNLOAD_CANCEL_NOT_APPLICABLE")
}

// The 409 boundary: an unknown record, a stale version, a second cancel and a
// completed download.
func TestUserCancelRefusals(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-cancel-refusal"
	root := "acq-" + taskID
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))

	_, err := cancelAt(t, db, "acq-missing", 1, "cancel-key-missing")
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("unknown record: %v", err)
	}
	version := readRootVersion(t, db, root)
	_, err = cancelAt(t, db, root, version+1, "cancel-key-stale")
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) || conflict.CurrentVersion != version {
		t.Fatalf("stale version: %v", err)
	}
	cancelNow(t, db, root, "cancel-key-first")
	_, err = cancelAt(t, db, root, readRootVersion(t, db, root), "cancel-key-second")
	requireCancelRefusal(t, err, "DOWNLOAD_CANCEL_ALREADY_REQUESTED")

	// The download completes before any worker acted: completion wins and a
	// later cancel finds nothing to cancel.
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE download_tasks SET state='completed', version=version+1 WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if cause, _, _ := readCancelColumns(t, db, taskID); cause.Valid {
		t.Fatalf("completion must clear an unacted intent: %v", cause)
	}
	_, err = cancelAt(t, db, root, readRootVersion(t, db, root), "cancel-key-third")
	requireCancelRefusal(t, err, "DOWNLOAD_CANCEL_NOT_APPLICABLE")
}

// A cancel that arrives while the poll observes completion resolves to the
// completion: the task moves on to import and nothing is deleted.
func TestCompletionWinsOverACancelNoWorkerActedOn(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-cancel-race"
	root := "acq-" + taskID
	seedDownloadTask(t, db, taskID, "downloading", `{"external_id":"tag:qb:release-qb-test","stable_tag":"qb:release-qb-test"}`)
	provider := &syncHookProvider{fakeProvider: &fakeProvider{syncTask: domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test", State: domain.TaskCompleted, Progress: 1, Path: "/downloads/QB Test.mkv"}}}
	provider.onSync = func() {
		if _, err := cancelAt(t, db, root, readRootVersion(t, db, root), "cancel-key-race"); err != nil {
			t.Errorf("cancel during the poll: %v", err)
		}
	}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	remover := &scriptedCancelRemover{effects: []string{CancelEffectDeletedWithData}}
	service.SetCancelRemover(remover)

	outcome := handleSync(t, service, createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync))
	if outcome.OwnerResult.State != "completed" {
		t.Fatalf("completion outcome = %+v", outcome)
	}
	if cause, effect, dispatched := readCancelColumns(t, db, taskID); cause.Valid || effect.Valid || dispatched {
		t.Fatalf("completion must clear the intent: %v %v %v", cause, effect, dispatched)
	}
	if len(remover.calls) != 0 {
		t.Fatalf("a completed download was deleted: %v", remover.calls)
	}
	var requests int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM acquisition_cancel_requests WHERE acquisition_id=$1`, root).Scan(&requests); err != nil || requests != 1 {
		t.Fatalf("cancel requests = %d %v", requests, err)
	}
	_, err := cancelAt(t, db, root, readRootVersion(t, db, root), "cancel-key-after")
	requireCancelRefusal(t, err, "DOWNLOAD_CANCEL_NOT_APPLICABLE")
}

// Once a delete may have run, completion can no longer win; an intent no
// worker acted on is cleared by it.
func TestDispatchedCancelRefusesCompletion(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-cancel-guard"
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	owner := ownerTestDB(t)
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET cancel_cause='user_cancelled', cancel_requested_at=clock_timestamp(), cancel_dispatched_at=clock_timestamp() WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	_, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed', version=version+1 WHERE id=$1`, taskID)
	if err == nil || !strings.Contains(err.Error(), "DOWNLOAD_CANCEL_DISPATCHED") {
		t.Fatalf("completion after a dispatched cancel: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET cancel_dispatched_at=NULL WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed', version=version+1 WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if cause, _, _ := readCancelColumns(t, db, taskID); cause.Valid {
		t.Fatalf("completion kept an unacted intent: %v", cause)
	}
}

// A transient delete failure is spent from the sync error budget: the next
// attempt deletes, and an exhausted budget fails the task the ordinary way.
func TestCancelDeleteRetriesWithinTheSyncErrorBudget(t *testing.T) {
	t.Run("retry_then_delete", func(t *testing.T) {
		db := openDownloadDB(t)
		taskID := "task-qb-cancel-retry"
		seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
		provider := &fakeProvider{syncTask: downloadingProviderTask()}
		service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
		remover := &scriptedCancelRemover{effects: []string{CancelEffectRetry, CancelEffectDeletedWithData}}
		service.SetCancelRemover(remover)
		cancelNow(t, db, "acq-"+taskID, "cancel-key-retry")

		first := handleSync(t, service, createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync))
		if first.OwnerResult.State != "downloading" || first.OwnerResult.ReasonCode != "qb_cancel_retryable" {
			t.Fatalf("first attempt = %+v", first)
		}
		if _, errs, _ := readAbsenceCounters(t, db, taskID); errs != 1 {
			t.Fatalf("a failed delete must spend the error budget: %d", errs)
		}
		if n := countAvailableQBSyncItems(t, db, taskID); n != 1 {
			t.Fatalf("a failed delete must schedule one retry: %d", n)
		}
		second := handleSync(t, service, claimNextTypedSync(t, db, store, taskID))
		if second.OwnerResult.State != "failed" || second.OwnerResult.ReasonCode != FailureUserCancelled {
			t.Fatalf("second attempt = %+v", second)
		}
		if _, effect, _ := readCancelColumns(t, db, taskID); effect.String != CancelEffectDeletedWithData {
			t.Fatalf("effect = %v", effect)
		}
		if len(remover.calls) != 2 || provider.syncCalls != 0 {
			t.Fatalf("remover calls = %v, polls = %d", remover.calls, provider.syncCalls)
		}
		if n := countDownloadFailedAdmissions(t, db, taskID); n != 0 {
			t.Fatalf("a cancel raised %d download_failed admissions", n)
		}
	})
	t.Run("exhausted", func(t *testing.T) {
		db := openDownloadDB(t)
		taskID := "task-qb-cancel-exhausted"
		seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
		if _, err := ownerTestDB(t).ExecContext(context.Background(), `UPDATE download_tasks SET consecutive_errors=max_consecutive_errors-1, total_errors=greatest(total_errors, max_consecutive_errors-1) WHERE id=$1`, taskID); err != nil {
			t.Fatal(err)
		}
		provider := &fakeProvider{syncTask: downloadingProviderTask()}
		service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
		remover := &scriptedCancelRemover{effects: []string{CancelEffectRetry}}
		service.SetCancelRemover(remover)
		cancelNow(t, db, "acq-"+taskID, "cancel-key-exhausted")

		outcome := handleSync(t, service, createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync))
		if outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != "SYNC_ERROR_BUDGET_EXHAUSTED" {
			t.Fatalf("exhausted attempt = %+v", outcome)
		}
		if n := countAvailableQBSyncItems(t, db, taskID); n != 0 {
			t.Fatalf("an exhausted cancel left %d retries", n)
		}
		if n := countDownloadFailedAdmissions(t, db, taskID); n != 1 {
			t.Fatalf("an exhausted delete is a failure: %d download_failed admissions", n)
		}
		if state, reason, _ := settleRoot(t, db, taskID); state != "failed" || reason != "SYNC_ERROR_BUDGET_EXHAUSTED" {
			t.Fatalf("root settled %s/%s", state, reason)
		}
	})
}

// The ownership rule may keep the qB task (detached) or find it gone
// (absent); either way the task ends cancelled with nothing deleted.
func TestCancelEffectsKeepOrMissTheQBTask(t *testing.T) {
	for _, effect := range []string{CancelEffectDetached, CancelEffectAbsent} {
		t.Run(effect, func(t *testing.T) {
			db := openDownloadDB(t)
			taskID := "task-qb-cancel-" + effect
			seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
			provider := &fakeProvider{syncTask: downloadingProviderTask()}
			service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
			service.SetCancelRemover(&scriptedCancelRemover{effects: []string{effect}})
			cancelNow(t, db, "acq-"+taskID, "cancel-key-"+effect)

			outcome := handleSync(t, service, createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSync))
			if outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != FailureUserCancelled {
				t.Fatalf("outcome = %+v", outcome)
			}
			_, _, reason, _ := readTaskRow(t, db, taskID)
			if _, got, _ := readCancelColumns(t, db, taskID); got.String != effect || !strings.Contains(reason.String, "未删除任何文件") {
				t.Fatalf("effect = %v reason = %q", got, reason.String)
			}
			requireSettledCancelled(t, db, taskID, FailureUserCancelled)
		})
	}
}

// A submission that never left the process is cancelled with no qB call.
func TestUserCancelBeforeSubmitSendsNothing(t *testing.T) {
	db := openDownloadDB(t)
	taskID := "task-qb-cancel-unsent"
	seedDownloadTask(t, db, taskID, "queued", "null")
	provider := &fakeProvider{}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	remover := &scriptedCancelRemover{effects: []string{CancelEffectDeletedWithData}}
	service.SetCancelRemover(remover)
	claim := createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSubmit)
	cancelNow(t, db, "acq-"+taskID, "cancel-key-unsent")

	outcome, err := service.Handle(context.Background(), claim)
	if err != nil || outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != FailureUserCancelled {
		t.Fatalf("submit outcome = %+v %v", outcome, err)
	}
	if provider.lookupCalls != 0 || provider.fileSubmitCalls != 0 || config.calls != 0 || len(remover.calls) != 0 {
		t.Fatalf("an unsent cancel reached qB: lookups=%d adds=%d config=%d removes=%v", provider.lookupCalls, provider.fileSubmitCalls, config.calls, remover.calls)
	}
	if _, effect, dispatched := readCancelColumns(t, db, taskID); effect.String != "not_submitted" || dispatched {
		t.Fatalf("effect = %v dispatched=%v", effect, dispatched)
	}
	if n := countDownloadFailedAdmissions(t, db, taskID); n != 0 {
		t.Fatalf("a cancel raised %d download_failed admissions", n)
	}
	requireSettledCancelled(t, db, taskID, FailureUserCancelled)
}

// A submission that may have reached qB is reconciled first; the sync stage
// then cancels the task the lookup found and deletes it.
func TestUserCancelAfterAnUnknownSubmitReconcilesThenDeletes(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-cancel-unknown"
	seedDownloadTask(t, db, taskID, "queued", "null")
	provider := &fakeProvider{fileSubmitErr: context.DeadlineExceeded}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	remover := &scriptedCancelRemover{effects: []string{CancelEffectDeletedWithData}}
	service.SetCancelRemover(remover)
	claim := createClaimedQBWorkItem(t, db, store, taskID, workitems.KindQBSubmit)
	outcome, err := service.Handle(ctx, claim)
	if err != nil || outcome.Disposition != workitems.DispositionAmbiguous {
		t.Fatalf("unknown submit = %+v %v", outcome, err)
	}
	cancelNow(t, db, "acq-"+taskID, "cancel-key-unknown")

	provider.fileSubmitErr = nil
	provider.lookupFound = true
	provider.lookupTask = domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: strings.Repeat("a", 40), State: domain.TaskDownloading}
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE work_items SET recovery_blocked=true,recovery_reason='effect_unknown',lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	recoveryClaim, err := store.ClaimRecovery(ctx, []string{workitems.KindQBSubmit}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := service.SubmitHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, recoveryClaim)
	if err != nil || recovered.Kind != worker.OutcomeCommitted || recovered.OwnerResult.State != "downloading" {
		t.Fatalf("reconcile = %+v %v", recovered, err)
	}
	if len(remover.calls) != 0 {
		t.Fatalf("the reconcile deleted: %v", remover.calls)
	}
	cancelled := handleSync(t, service, claimNextTypedSync(t, db, store, taskID))
	if cancelled.OwnerResult.State != "failed" || cancelled.OwnerResult.ReasonCode != FailureUserCancelled {
		t.Fatalf("sync cancel = %+v", cancelled)
	}
	if len(remover.calls) != 1 || provider.fileSubmitCalls != 1 {
		t.Fatalf("remover calls = %v, adds = %d", remover.calls, provider.fileSubmitCalls)
	}
	if _, effect, _ := readCancelColumns(t, db, taskID); effect.String != CancelEffectDeletedWithData {
		t.Fatalf("effect = %v", effect)
	}
	requireSettledCancelled(t, db, taskID, FailureUserCancelled)
}

// A one-shot record cancels the same way and ends there: no import retry.
func TestOneShotUserCancelIsTerminal(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	taskID := "task-qb-cancel-oneshot"
	root := "acq-" + taskID
	seedDownloadTask(t, db, taskID, "downloading", stalledCorrelation(t, 0.10, "downloading"))
	seedOneShotMarker(t, db, "marker-"+taskID, taskID)
	provider := &fakeProvider{syncTask: downloadingProviderTask()}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	remover := &scriptedCancelRemover{effects: []string{CancelEffectDeletedWithData}}
	service.SetCancelRemover(remover)
	cancelNow(t, db, root, "cancel-key-oneshot")

	outcome := handleSync(t, service, createClaimedOneShotQBSyncItem(t, db, store, taskID))
	if outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != FailureUserCancelled {
		t.Fatalf("one-shot cancel = %+v", outcome)
	}
	if len(remover.calls) != 1 || remover.calls[0] != root {
		t.Fatalf("remover calls = %v", remover.calls)
	}
	if n := countDownloadFailedAdmissions(t, db, taskID); n != 0 {
		t.Fatalf("a one-shot cancel raised %d download_failed admissions", n)
	}
	requireSettledCancelled(t, db, taskID, FailureUserCancelled)
	var refusal string
	if err := db.QueryRowContext(ctx, `SELECT acquisition_import_retry_refusal($1)`, root).Scan(&refusal); err != nil || refusal != "IMPORT_RETRY_NOT_APPLICABLE" {
		t.Fatalf("import retry refusal = %q %v", refusal, err)
	}
	_, err := cancelAt(t, db, root, readRootVersion(t, db, root), "cancel-key-oneshot-2")
	requireCancelRefusal(t, err, "DOWNLOAD_CANCEL_NOT_APPLICABLE")
}
