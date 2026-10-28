package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	acquisitionsettlement "bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/library/domain"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// flakyPlacementSink fails the first `failures` admissions with a typed reason
// code and succeeds after, modeling a transient placement/index-stage fault
// that later recovers.
type flakyPlacementSink struct {
	code     string
	failures int
	calls    int
}

func (s *flakyPlacementSink) AdmitFromFileOperation(_ context.Context, _ string) error {
	s.calls++
	if s.calls <= s.failures {
		return &PlacementSinkError{Code: s.code, Err: errors.New("placement stage is unavailable")}
	}
	return nil
}

// wireRealDiagnostics returns the production observability service so tests
// can assert durable diagnostic episodes in diagnostic_episodes.
func wireRealDiagnostics(t *testing.T, db *sql.DB, service *Service) *observabilityapplication.Service {
	t.Helper()
	recorder, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetDiagnosticRecorder(recorder, nil)
	return recorder
}

func placementObligationState(t *testing.T, db *sql.DB, operationID string) (state string, attempts int, reasonCode string) {
	t.Helper()
	var reason sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT state, attempts, reason_code FROM placement_obligations WHERE file_operation_id = $1`, operationID).
		Scan(&state, &attempts, &reason); err != nil {
		t.Fatalf("read placement obligation: %v", err)
	}
	return state, attempts, reason.String
}

func liveContinuationCount(t *testing.T, db *sql.DB, operationID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM work_items WHERE owner_type = 'file_operation' AND owner_id = $1 AND kind = 'file_execute' AND state IN ('available', 'leased')`,
		operationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertOperationCompleted(t *testing.T, db *sql.DB, operationID string) {
	t.Helper()
	var state string
	if err := db.QueryRowContext(context.Background(),
		`SELECT state FROM file_operations WHERE id = $1`, operationID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != ownerStateCompleted {
		t.Fatalf("file operation must stay completed, got %q", state)
	}
}

func assertPlacementEpisode(t *testing.T, db *sql.DB, operationID string, wantClosed bool) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM diagnostic_episodes
WHERE kind = 'internal' AND target_id = 'placement.sink'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("placement diagnostic episode count = %d, want 1", count)
	}
	var closed sql.NullTime
	if err := db.QueryRowContext(context.Background(),
		`SELECT succeeded_after_at FROM diagnostic_episodes
WHERE kind = 'internal' AND target_id = 'placement.sink'`).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if wantClosed && !closed.Valid {
		t.Fatal("settled placement stage must close the diagnostic episode")
	}
	if !wantClosed && closed.Valid {
		t.Fatal("a failed placement stage must leave the diagnostic episode open")
	}
}

// TestPlacementObligationCommittedWithCompletion proves the durability fix: the
// obligation row and its continuation WorkItem commit in the SAME transaction
// as the FileOperation completion, so a process crash before any AfterCommit
// callback loses nothing. With the callback never run, a restarted worker
// claims the continuation and settles the placement stage while the file
// operation stays completed.
func TestPlacementObligationCommittedWithCompletion(t *testing.T) {
	sink := &flakyPlacementSink{}
	db, service, fixture, operationID := typedPlacementCompletion(t, sink)

	state, attempts, _ := placementObligationState(t, db, operationID)
	if state != placementObligationPending || attempts != 0 {
		t.Fatalf("obligation must commit pending with the completion, got %s attempts=%d", state, attempts)
	}
	if live := liveContinuationCount(t, db, operationID); live != 1 {
		t.Fatalf("exactly one live continuation item must commit with the completion, got %d", live)
	}

	// Post-restart worker claims the continuation and settles the stage.
	continuationOutcome := runClaim(t, db, service, fixture)
	if continuationOutcome.Kind != worker.OutcomeFinish || continuationOutcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("continuation must settle succeeded, got %+v", continuationOutcome)
	}
	if sink.calls != 1 {
		t.Fatalf("placement sink calls = %d, want 1 (only the continuation claim)", sink.calls)
	}
	state, attempts, _ = placementObligationState(t, db, operationID)
	if state != placementObligationAdmitted || attempts != 1 {
		t.Fatalf("obligation must settle admitted after one attempt, got %s attempts=%d", state, attempts)
	}
	if live := liveContinuationCount(t, db, operationID); live != 0 {
		t.Fatalf("settled continuation must leave no live item, got %d", live)
	}
	assertOperationCompleted(t, db, operationID)
}

// A settle that runs while the placement continuation is still live defers on
// it and consumes the settle item. The continuation's exit wakes settlement
// again (migration 091), so the root settles without the continuation arming
// anything itself.
func TestPlacementContinuationWakesConsumedSettlement(t *testing.T) {
	sink := &flakyPlacementSink{}
	db, service, fixture, operationID := typedPlacementCompletion(t, sink)
	root := acquisitionOf(t, db, operationID)
	settler, err := acquisitionsettlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(context.Background(), root); err != nil || status != "deferred" {
		t.Fatalf("early settlement = %q, %v; want deferred", status, err)
	}
	if n := settleItemCount(t, db, root); n != 0 {
		t.Fatalf("a defer on live work left %d settle items, want it consumed", n)
	}
	continuationOutcome := runClaim(t, db, service, fixture)
	if continuationOutcome.Kind != worker.OutcomeFinish || continuationOutcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("continuation must settle succeeded, got %+v", continuationOutcome)
	}
	if n := settleItemCount(t, db, root); n != 1 {
		t.Fatalf("the continuation exit left %d settle items, want 1", n)
	}
	if status, err := settler.SettleRoot(context.Background(), root); err != nil || status != "settled" {
		t.Fatalf("post-continuation settlement = %q, %v; want settled", status, err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM acquisitions WHERE id=$1`, root).Scan(&state); err != nil || state != "succeeded" {
		t.Fatalf("root state=%q, %v; want succeeded", state, err)
	}
}

func settleItemCount(t *testing.T, db *sql.DB, root string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE owner_type='acquisition' AND owner_id=$1 AND kind='acquisition_settle'`, root).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestPlacementSinkFailureIsVisibleAndRetried proves the independent
// placement/index stage for each typed failure class: a transient stage
// failure never rewrites the completed file effect, records the durable
// obligation state/attempt/reason plus a visible diagnostic with the stable
// reason, and the bounded continuation re-drives only the stage until it
// settles. Replacement-trigger failures retry independently: the settled
// placement admission is never rolled back or duplicated.
func TestPlacementSinkFailureIsVisibleAndRetried(t *testing.T) {
	for _, reasonCode := range []string{
		"placement_evidence_missing",
		"placement_evidence_read_failed",
		"placement_admission_failed",
		"placement_media_facts_unwired",
		"placement_unknown_admission_failed",
		"replacement_trigger_failed",
	} {
		t.Run(reasonCode, func(t *testing.T) {
			sink := &flakyPlacementSink{code: reasonCode, failures: 1}
			db, service, fixture, operationID := typedPlacementCompletion(t, sink)

			// First continuation claim: transient failure → bounded retry.
			retryOutcome := runClaim(t, db, service, fixture)
			if retryOutcome.Kind != worker.OutcomeRetry {
				t.Fatalf("transient placement failure must return a retry outcome, got %+v", retryOutcome)
			}
			if retryOutcome.ReasonCode != reasonCode {
				t.Fatalf("retry reason = %q, want %q", retryOutcome.ReasonCode, reasonCode)
			}
			state, attempts, reason := placementObligationState(t, db, operationID)
			if state != placementObligationWaitingRetry || attempts != 1 || reason != reasonCode {
				t.Fatalf("obligation must record waiting_retry/1/%s, got %s/%d/%s", reasonCode, state, attempts, reason)
			}
			if live := liveContinuationCount(t, db, operationID); live != 1 {
				t.Fatalf("retryable failure must keep exactly one live continuation, got %d", live)
			}
			assertPlacementEpisode(t, db, operationID, false)
			assertOperationCompleted(t, db, operationID)

			// The retry re-drives only the placement stage; the file effect
			// stays terminal and the retry settles once the stage recovers.
			makeContinuationDueNow(t, db, operationID)
			settleOutcome := runClaim(t, db, service, fixture)
			if settleOutcome.Kind != worker.OutcomeFinish || settleOutcome.TerminalState != workitems.StateSucceeded {
				t.Fatalf("recovered placement continuation must finish succeeded, got %+v", settleOutcome)
			}
			if sink.calls != 2 {
				t.Fatalf("placement stage calls = %d, want 2 (failed claim + settled claim)", sink.calls)
			}
			state, attempts, reason = placementObligationState(t, db, operationID)
			if state != placementObligationAdmitted || reason != "" {
				t.Fatalf("settled obligation must be admitted without a reason, got %s/%q", state, reason)
			}
			if attempts != 2 {
				t.Fatalf("settled obligation attempts = %d, want 2", attempts)
			}
			assertPlacementEpisode(t, db, operationID, true)
			if live := liveContinuationCount(t, db, operationID); live != 0 {
				t.Fatalf("settled placement continuation must leave no live item, got %d", live)
			}
			assertOperationCompleted(t, db, operationID)
		})
	}
}

// TestPlacementObligationExhaustionKeepsOwnerCompleted proves the bounded
// continuation never converts a completed file effect into a failed owner:
// after the retry budget is exhausted the obligation is terminally failed with
// its stable reason, the diagnostic is visible, and the file operation stays
// completed.
func TestPlacementObligationExhaustionKeepsOwnerCompleted(t *testing.T) {
	sink := &flakyPlacementSink{code: "placement_admission_failed", failures: 1000}
	db, service, _, operationID := typedPlacementCompletion(t, sink)

	// Drive the continuation item to its attempt budget; every attempt fails.
	for attempt := 0; attempt < placementRetryMaxAttempts+1; attempt++ {
		store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
		claim, err := store.ClaimDue(context.Background(), "placement-exhaustion", []string{workitems.KindFileExecute}, time.Minute)
		if errors.Is(err, workitems.ErrNoDueWork) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		retryOutcome, err := service.Handle(context.Background(), claim)
		if err != nil {
			t.Fatal(err)
		}
		if retryOutcome.Kind != worker.OutcomeRetry {
			break
		}
		if _, err := store.Retry(context.Background(), claim, retryOutcome.ReasonCode, retryOutcome.Reason, retryOutcome.RetryAfter, retryOutcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		// The default retry delay is minutes; make the item due again so the
		// bounded budget is exercised in one test.
		makeContinuationDueNow(t, db, operationID)
	}
	state, attempts, reason := placementObligationState(t, db, operationID)
	if state != placementObligationFailed || reason != "placement_admission_failed" {
		t.Fatalf("exhausted obligation must fail with its reason, got %s/%q", state, reason)
	}
	if attempts != placementRetryMaxAttempts {
		t.Fatalf("exhausted obligation attempts = %d, want %d", attempts, placementRetryMaxAttempts)
	}
	if live := liveContinuationCount(t, db, operationID); live != 0 {
		var itemState, code string
		var attempt, maxAttempts int
		_ = db.QueryRowContext(context.Background(),
			`SELECT state, attempt, max_attempts, COALESCE(last_error_code, '') FROM work_item_history WHERE owner_id = $1 ORDER BY created_at DESC LIMIT 1`,
			operationID).Scan(&itemState, &attempt, &maxAttempts, &code)
		t.Fatalf("exhausted continuation must be terminal, got %d live items (state=%s attempt=%d/%d code=%s)", live, itemState, attempt, maxAttempts, code)
	}
	assertPlacementEpisode(t, db, operationID, false)
	assertOperationCompleted(t, db, operationID)
}

// TestPlacementContinuationManualRetryReopensObligation proves the terminal
// state is manually re-drivable: after exhaustion the obligation re-opens in
// one transaction (the completed file operation untouched) and a fresh
// continuation claim settles the recovered stage.
func TestPlacementContinuationManualRetryReopensObligation(t *testing.T) {
	sink := &flakyPlacementSink{code: "placement_admission_failed", failures: 1000}
	db, service, fixture, operationID := typedPlacementCompletion(t, sink)

	for attempt := 0; attempt < placementRetryMaxAttempts+1; attempt++ {
		store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
		claim, err := store.ClaimDue(context.Background(), "placement-manual-retry", []string{workitems.KindFileExecute}, time.Minute)
		if errors.Is(err, workitems.ErrNoDueWork) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		retryOutcome, err := service.Handle(context.Background(), claim)
		if err != nil {
			t.Fatal(err)
		}
		if retryOutcome.Kind != worker.OutcomeRetry {
			break
		}
		if _, err := store.Retry(context.Background(), claim, retryOutcome.ReasonCode, retryOutcome.Reason, retryOutcome.RetryAfter, retryOutcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		makeContinuationDueNow(t, db, operationID)
	}
	state, _, _ := placementObligationState(t, db, operationID)
	if state != placementObligationFailed {
		t.Fatalf("precondition: exhausted obligation must be failed, got %s", state)
	}

	// Manual retry re-drive: re-open the obligation inside one transaction and
	// create the fresh continuation item from the durable snapshot. The
	// composite file-operation FK requires the operation's own effect key.
	ctx := context.Background()
	var effectKey string
	if err := db.QueryRowContext(ctx, `SELECT idempotency_key FROM file_operations WHERE id = $1`, operationID).Scan(&effectKey); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// The reopen is the shape acquisition_import_retry writes: the same
	// obligation back to pending with a clean budget.
	var ownerVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM file_operations WHERE id = $1 FOR UPDATE`, operationID).Scan(&ownerVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE placement_obligations
SET state = 'pending', attempts = 0, reason_code = NULL, reason = NULL, settled_at = NULL, updated_at = CURRENT_TIMESTAMP
WHERE file_operation_id = $1 AND state = 'failed'`, operationID); err != nil {
		t.Fatalf("reopen placement obligation: %v", err)
	}
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	snapshot, err := marshalPlacementContinuationInput(operationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: operationID},
		Kind:                 workitems.KindFileExecute,
		IdempotencyKey:       effectKey,
		InputSnapshot:        snapshot,
		MaxAttempts:          placementRetryMaxAttempts,
		ExpectedOwnerVersion: ownerVersion,
	}); err != nil {
		t.Fatalf("create manual retry continuation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	state, attempts, reason := placementObligationState(t, db, operationID)
	if state != placementObligationPending || attempts != 0 || reason != "" {
		t.Fatalf("re-opened obligation must be pending/clean, got %s/%d/%q", state, attempts, reason)
	}
	sink.failures = 0
	sink.calls = 0
	settleOutcome := runClaim(t, db, service, fixture)
	if settleOutcome.Kind != worker.OutcomeFinish || settleOutcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("re-driven continuation must settle, got %+v", settleOutcome)
	}
	state, _, _ = placementObligationState(t, db, operationID)
	if state != placementObligationAdmitted {
		t.Fatalf("re-driven obligation must settle admitted, got %s", state)
	}
	assertOperationCompleted(t, db, operationID)
}

// countingExecutor proves the file effect never re-runs for placement
// continuation claims, including malformed ones. It completes the effect
// exactly like the shared test executor, so the claim reaches the real
// completion path (target fingerprint + exact size observed).
type countingExecutor struct {
	calls int
}

func (e *countingExecutor) Execute(_ context.Context, operation domain.FileOperation) (domain.FileOperation, error) {
	e.calls++
	operation.State = domain.OperationSucceeded
	operation.TargetFingerprint = operation.SourceFingerprint
	operation.TargetSizeBytes = operation.SourceSizeBytes
	return operation, nil
}

// dropLiveContinuation deletes the live continuation item so a synthetic
// replacement claim exercises the one-live-item invariant exactly as the real
// runtime does.
func dropLiveContinuation(t *testing.T, db *sql.DB, operationID string) {
	t.Helper()
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(),
		`DELETE FROM work_items WHERE owner_type = 'file_operation' AND owner_id = $1 AND kind = 'file_execute' AND state IN ('available', 'leased')`, operationID); err != nil {
		t.Fatal(err)
	}
}

// makeContinuationDueNow makes the live continuation item claimable again. The
// production retry delay is minutes; a test that drives several attempts in one
// run must move the item's available_at forward explicitly, exactly as the
// runtime's backoff would over wall-clock time.
func makeContinuationDueNow(t *testing.T, db *sql.DB, operationID string) {
	t.Helper()
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(),
		`UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE owner_type = 'file_operation' AND owner_id = $1 AND kind = 'file_execute' AND state = 'available'`, operationID); err != nil {
		t.Fatal(err)
	}
}

// seedContinuationItem creates a live file_execute WorkItem carrying the given
// snapshot so the claim path — not a synthetic in-memory claim — decides how it
// is handled. The composite file-operation foreign key requires the operation's
// own effect key.
func seedContinuationItem(t *testing.T, db *sql.DB, operationID string, snapshot json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	var effectKey string
	var ownerVersion int64
	if err := db.QueryRowContext(ctx,
		`SELECT idempotency_key, version FROM file_operations WHERE id = $1`, operationID).
		Scan(&effectKey, &ownerVersion); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: operationID},
		Kind:                 workitems.KindFileExecute,
		IdempotencyKey:       effectKey,
		InputSnapshot:        snapshot,
		MaxAttempts:          placementRetryMaxAttempts,
		ExpectedOwnerVersion: ownerVersion,
	}); err != nil {
		t.Fatalf("create continuation work item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestPlacementMalformedContinuationFailsClosed proves a claim that declares
// placement-continuation intent with a broken discriminator fails closed: the
// file effect is never re-attempted, the continuation item terminalizes with a
// visible reason, and the durable obligation freezes failed instead of being
// mistaken for a normal file_execute claim.
func TestPlacementMalformedContinuationFailsClosed(t *testing.T) {
	sink := &flakyPlacementSink{}
	db, service, fixture, operationID := typedPlacementCompletion(t, sink)

	if state, _, _ := placementObligationState(t, db, operationID); state != placementObligationPending {
		t.Fatalf("precondition: completion must leave a pending obligation, got %s", state)
	}
	dropLiveContinuation(t, db, operationID)
	seedContinuationItem(t, db, operationID, json.RawMessage(`{"placement_retry": "yes", "file_operation_id": "`+operationID+`"}`))

	outcome := runClaim(t, db, service, fixture)
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("malformed continuation must fail closed terminally, got %+v", outcome)
	}
	if outcome.OwnerResult.ReasonCode != "placement_continuation_malformed" {
		t.Fatalf("malformed reason = %q", outcome.OwnerResult.ReasonCode)
	}
	if attempts := fileAttemptsStarted(t, db, operationID); attempts != 1 {
		t.Fatalf("malformed continuation must never redo the file effect, attempts = %d", attempts)
	}
	if sink.calls != 0 {
		t.Fatalf("malformed continuation must never run the placement handoff, sink calls = %d", sink.calls)
	}
	state, _, reason := placementObligationState(t, db, operationID)
	if state != placementObligationFailed || reason != "placement_continuation_malformed" {
		t.Fatalf("malformed continuation must freeze the obligation failed with its reason, got %s/%q", state, reason)
	}
	// The worker runs the outcome's post-commit work only after the transition
	// committed; the terminal malformed path records its diagnostic there.
	outcome.AfterCommit(context.Background())
	assertPlacementEpisode(t, db, operationID, false)
	assertOperationCompleted(t, db, operationID)
}

// TestPlacementContinuationWithoutObligationFailsClosed proves a continuation
// claim whose durable obligation row does not exist (inconsistent database)
// never attempts the placement stage: it terminalizes visibly without touching
// the file effect.
func TestPlacementContinuationWithoutObligationFailsClosed(t *testing.T) {
	sink := &flakyPlacementSink{}
	db, service, fixture, operationID := typedPlacementCompletion(t, sink)

	// Simulate the inconsistency: the obligation row is gone but a continuation
	// claim still exists.
	if _, err := db.ExecContext(context.Background(),
		`DELETE FROM placement_obligations WHERE file_operation_id = $1`, operationID); err != nil {
		t.Fatal(err)
	}
	dropLiveContinuation(t, db, operationID)
	seedContinuationItem(t, db, operationID, mustContinuationSnapshot(t, operationID))

	outcome := runClaim(t, db, service, fixture)
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("missing obligation must fail closed terminally, got %+v", outcome)
	}
	if outcome.OwnerResult.ReasonCode != "placement_obligation_missing" {
		t.Fatalf("missing obligation reason = %q", outcome.OwnerResult.ReasonCode)
	}
	if attempts := fileAttemptsStarted(t, db, operationID); attempts != 1 {
		t.Fatalf("missing obligation must never redo the file effect, attempts = %d", attempts)
	}
	if sink.calls != 0 {
		t.Fatalf("missing obligation must never run the placement handoff, sink calls = %d", sink.calls)
	}
	outcome.AfterCommit(context.Background())
	assertPlacementEpisode(t, db, operationID, false)
	assertOperationCompleted(t, db, operationID)
}

func mustContinuationSnapshot(t *testing.T, operationID string) json.RawMessage {
	t.Helper()
	snapshot, err := marshalPlacementContinuationInput(operationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestPlacementContinuationIsRetirementFenced proves the durable placement
// continuation is a real effect-bearing owner claim, not an ungoverned
// post-completion task: it freezes the resolved managed_anime_id and lifecycle
// epoch at creation, and once that owner is retired the retirement fence both
// keeps it from being claimed and rejects an already-leased claim. The
// obligation then stays pending and visible for the retirement path instead of
// silently admitting a placement for a retired anime.
func TestPlacementContinuationIsRetirementFenced(t *testing.T) {
	sink := &flakyPlacementSink{}
	db, _, fixture, operationID := typedPlacementCompletion(t, sink)

	// The continuation item resolves through the file-operation owner chain to
	// the real ManagedAnime and freezes its lifecycle epoch, which is what makes
	// the retirement fence apply to this stage at all.
	var managedAnimeID, lifecycleEpoch sql.NullString
	if err := db.QueryRowContext(context.Background(), `
SELECT managed_anime_id, owner_lifecycle_epoch::text FROM work_items
WHERE owner_type = 'file_operation' AND owner_id = $1 AND kind = 'file_execute' AND state = 'available'`,
		operationID).Scan(&managedAnimeID, &lifecycleEpoch); err != nil {
		t.Fatalf("read continuation owner fence: %v", err)
	}
	if managedAnimeID.String != fixture.animeID || !lifecycleEpoch.Valid {
		t.Fatalf("continuation must freeze its lifecycle fence, got %q/%q", managedAnimeID.String, lifecycleEpoch.String)
	}

	// Lease the continuation while its owner is still active, with an already
	// expired lease: the item is then "reclaimable" in the ordinary sense, which
	// is what the retirement fence must overrule. The row cannot be touched once
	// the owner retires (the fence rejects any write to a retired owner's item),
	// so this has to happen first.
	fixtureDB := workItemFixtureDB(t)
	leaseTx, err := fixtureDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leaseTx.Rollback() }()
	if _, err := leaseTx.ExecContext(context.Background(), `
UPDATE work_items
SET state = 'leased', lease_until = CURRENT_TIMESTAMP - INTERVAL '1 minute',
    fencing_token = fencing_token + 1
WHERE owner_type = 'file_operation' AND owner_id = $1 AND kind = 'file_execute'
  AND state IN ('available', 'leased')`, operationID); err != nil {
		t.Fatal(err)
	}
	if err := leaseTx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	if _, err := store.ClaimDue(context.Background(), "placement-pre-retirement", []string{workitems.KindFileExecute}, time.Minute); err != nil {
		t.Fatalf("precondition: the expired continuation must be reclaimable while the owner is active, got %v", err)
	}
	// Return it to a reclaimable expired lease for the post-retirement check.
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `
UPDATE work_items
SET state = 'leased', lease_until = CURRENT_TIMESTAMP - INTERVAL '1 minute'
WHERE owner_type = 'file_operation' AND owner_id = $1 AND kind = 'file_execute'
  AND state IN ('available', 'leased')`, operationID); err != nil {
		t.Fatal(err)
	}
	var claimID string
	var fencingToken int64
	if err := db.QueryRowContext(context.Background(), `
SELECT id, fencing_token FROM work_items
WHERE owner_type = 'file_operation' AND owner_id = $1 AND kind = 'file_execute'
  AND state IN ('available', 'leased')`,
		operationID).Scan(&claimID, &fencingToken); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(context.Background(), `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1, retired_at = CURRENT_TIMESTAMP,
    retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1
WHERE id = $1 AND lifecycle_state = 'active'`, fixture.animeID); err != nil {
		t.Fatalf("retire managed anime: %v", err)
	}

	// The claim loop can no longer see the continuation: a retired owner's
	// expired-lease item is not reclaimable, so the stage cannot run behind the
	// retirement.
	if _, err := store.ClaimDue(context.Background(), "placement-retirement", []string{workitems.KindFileExecute}, time.Minute); !errors.Is(err, workitems.ErrNoDueWork) {
		t.Fatalf("retired owner's placement continuation must not be claimable, got %v", err)
	}
	if sink.calls != 0 {
		t.Fatalf("retirement must stop the placement handoff before it runs, sink calls = %d", sink.calls)
	}
	if state, attempts, _ := placementObligationState(t, db, operationID); state != placementObligationPending || attempts != 0 {
		t.Fatalf("obligation must stay pending and unexplained by the fence, got %s/%d", state, attempts)
	}

	// The fence also refuses a writer that tries to advance the retired owner's
	// item, so the continuation cannot be re-armed behind the retirement.
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(),
		`UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE owner_type = 'file_operation' AND owner_id = $1 AND kind = 'file_execute' AND state IN ('available', 'leased')`, operationID); err == nil {
		t.Fatal("the retirement fence must reject a work item update for a retired owner")
	}

	// An already-leased claim is rejected by the pre-effect admission gate even
	// though its lease is still nominally held.
	if err := store.AdmitManagedAnimeEffect(context.Background(), workitems.Claim{
		ID:             claimID,
		Owner:          workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: operationID},
		Kind:           workitems.KindFileExecute,
		FencingToken:   fencingToken,
		ManagedAnimeID: managedAnimeID.String,
	}); !errors.Is(err, workitems.ErrOwnerRetired) {
		t.Fatalf("retired owner must reject the leased placement continuation, got %v", err)
	}
	var placements int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM placements`).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if placements != 0 {
		t.Fatalf("a fenced placement continuation must write no placement, got %d", placements)
	}
	assertOperationCompleted(t, db, operationID)
}

// seedOneShotLegacyFileOperation seeds a no-plan legacy file_execute graph
// carrying the exact one_shot reference — the production one-shot (manual
// acquire) shape whose completion commits the placement handoff through the
// legacy owner writer. The typed isolation must not engage (no plan).
func seedOneShotLegacyFileOperation(t *testing.T, db *sql.DB, markerID string) (string, workitems.OneShotRef) {
	t.Helper()
	seedOneShotMarkerFixture(t, db, markerID)
	ref := workitems.OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}
	// The operation must mirror the fixture's artifact file row exactly (the
	// insert trigger verifies source_path against artifact_files); only the
	// plan-free operation and its one-shot work are arranged here.
	operationID := "op-oneshot-placement"
	if _, err := db.Exec(`
INSERT INTO file_operations(id, artifact_id, artifact_version, artifact_file_id, operation, source_path, target_path, source_fingerprint, frozen_render_snapshot, frozen_config_snapshot, collision_policy, state, idempotency_key, version, acquisition_id, file_ref)
SELECT $1, a.id, a.version, f.id, 'copy', f.source_path, f.source_path||'.placed', f.fingerprint, '{}'::jsonb, '{}'::jsonb, 'block', 'pending', $1||':placement', 1, a.acquisition_id, f.file_ref
FROM artifacts a JOIN artifact_files f ON f.artifact_id=a.id WHERE a.id='artifact-file-test' AND f.id='artifact-file-primary'`,
		operationID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(map[string]any{
		"one_shot":         ref,
		"operation_id":     operationID,
		"artifact_id":      "artifact-file-test",
		"artifact_file_id": "artifact-file-primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
SELECT public.work_item_create('oneshot-place-work','file_operation',$1,'file_execute',$1||':placement',3,$2::jsonb,1,0::double precision,7::smallint)`,
		operationID, snapshot); err != nil {
		t.Fatal(err)
	}
	return operationID, ref
}

// arrangeOneShotCompletionTerminal claims the one-shot execution work and
// finishes it through the PRODUCTION store API (FinishWithResult writes the
// full terminal shape: terminal_at, retention_until, lease clearing — never a
// bare UPDATE), mirroring the moment the completion's post-transition creates
// the placement handoff. Returns the live-continuation baseline (0).
func arrangeOneShotCompletionTerminal(t *testing.T, db *sql.DB, operationID string) {
	t.Helper()
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	claim, err := store.ClaimDue(context.Background(), "oneshot-place-worker", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Owner.ID != operationID {
		t.Fatalf("claimed owner %s, want %s", claim.Owner.ID, operationID)
	}
	if err := store.FinishWithResult(context.Background(), claim, workitems.StateSucceeded, workitems.OwnerResult{State: "completed"}, nil); err != nil {
		t.Fatalf("finish the original execution work: %v", err)
	}
}

// Positive: with the marker validated FIRST (running + epoch),
// createPlacementObligationTx commits the obligation and the continuation
// whose input_snapshot carries the claim's exact one_shot reference — the
// marker-first gate and the reclaim proof cover it.
func TestPlacementContinuationCarriesOneShotUnderRunningMarker(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	defer db.Close()
	service := newFixtureService(t, db, fixture)
	sink := &recordingPlacementSink{}
	service.SetPlacementSink(sink)
	seedOneShotMarkerFixture(t, db, "db-marker")
	ref := workitems.OneShotRef{MarkerID: "db-marker", CorrelationID: "corr-db-marker", AdmissionEpoch: 1}
	prepareOneShotFixture(t, db, service, fixture.artifactID, ref)
	operationID := fileOperationID(t, db, fixture.artifactID)
	arrangeOneShotCompletionTerminal(t, db, operationID)

	owner := ownerTestDB(t)
	tx, err := owner.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var ownerVersion int64
	var effectKey string
	if err := tx.QueryRowContext(context.Background(),
		`SELECT version, idempotency_key FROM file_operations WHERE id = $1`, operationID).Scan(&ownerVersion, &effectKey); err != nil {
		t.Fatal(err)
	}
	continuationID, err := service.createPlacementObligationTx(context.Background(), tx, operationID, ownerVersion, effectKey, &ref)
	if err != nil {
		t.Fatalf("one-shot continuation under a running marker: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	state, attempts, _ := placementObligationState(t, db, operationID)
	if state != placementObligationPending || attempts != 0 {
		t.Fatalf("obligation must commit pending, got %s attempts=%d", state, attempts)
	}
	var snapshot []byte
	if err := db.QueryRow(`SELECT input_snapshot FROM work_item_history WHERE id = $1`, continuationID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	decoded := oneShotRefFromSnapshot(t, snapshot)
	if decoded == nil || *decoded != ref {
		t.Fatalf("continuation snapshot one_shot = %+v, want %+v", decoded, ref)
	}
	t.Logf("sql_positive continuation one_shot=%+v under running marker (marker-first, obligation pending)", ref)
}

// Negative: a reclaiming marker (non-running or stale epoch)
// refuses the whole write set with ONE_SHOT_RECLAIMING and zero writes — no
// obligation, no continuation, no owner mutation. The check happens before
// the obligation insert (never insert-then-check).
func TestPlacementContinuationRefusedWhenMarkerReclaiming(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		epoch int64
	}{
		{"reclaim_pending", "reclaim_pending", 1},
		{"cleanup_blocked", "cleanup_blocked", 1},
		{"reclaiming", "reclaiming", 1},
		{"stale_epoch", "running", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, fixture := openFileOperationDB(t)
			defer db.Close()
			service := newFixtureService(t, db, fixture)
			sink := &recordingPlacementSink{}
			service.SetPlacementSink(sink)
			seedOneShotMarkerFixture(t, db, "db-marker-"+tc.name)
			ref := workitems.OneShotRef{MarkerID: "db-marker-" + tc.name, CorrelationID: "corr-db-marker-" + tc.name, AdmissionEpoch: 1}
			prepareOneShotFixture(t, db, service, fixture.artifactID, ref)
			operationID := fileOperationID(t, db, fixture.artifactID)
			arrangeOneShotCompletionTerminal(t, db, operationID)
			if tc.state != "running" {
				setOneShotMarkerStateForPrepareFixture(t, db, ref.MarkerID, tc.state)
			} else {
				setOneShotMarkerEpochForPrepareFixture(t, db, ref.MarkerID, tc.epoch)
			}
			var operationsBefore, obligationsBefore int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM file_operations WHERE artifact_id=$1),(SELECT count(*) FROM placement_obligations WHERE file_operation_id=$2)`, fixture.artifactID, operationID).Scan(&operationsBefore, &obligationsBefore); err != nil {
				t.Fatal(err)
			}
			owner := ownerTestDB(t)
			tx, err := owner.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var ownerVersion int64
			var effectKey string
			if err := tx.QueryRowContext(context.Background(),
				`SELECT version, idempotency_key FROM file_operations WHERE id = $1`, operationID).Scan(&ownerVersion, &effectKey); err != nil {
				t.Fatal(err)
			}
			_, err = service.createPlacementObligationTx(context.Background(), tx, operationID, ownerVersion, effectKey, &ref)
			if !errors.Is(err, workitems.ErrOneShotReclaiming) {
				t.Fatalf("reclaiming marker must return ONE_SHOT_RECLAIMING, got %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("refusal must leave the transaction committable-empty, got %v", err)
			}
			var operationsAfter, obligationsAfter int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM file_operations WHERE artifact_id=$1),(SELECT count(*) FROM placement_obligations WHERE file_operation_id=$2)`, fixture.artifactID, operationID).Scan(&operationsAfter, &obligationsAfter); err != nil {
				t.Fatal(err)
			}
			if operationsAfter != operationsBefore || obligationsAfter != obligationsBefore {
				t.Fatalf("zero-write violated: operations %d->%d obligations %d->%d", operationsBefore, operationsAfter, obligationsBefore, obligationsAfter)
			}
			if live := liveContinuationCount(t, db, operationID); live != 0 {
				t.Fatalf("reclaiming marker must create no continuation, got %d", live)
			}
			t.Logf("producer_rejection %s ONE_SHOT_RECLAIMING zero obligation zero continuation (marker-first pre-check)", tc.name)
		})
	}
}

// typedPlacementCompletion completes one prepared copy through the real
// typed worker entry with the placement sink wired, so the fixed finish
// has committed the pending obligation and one live placement continuation.
func typedPlacementCompletion(t *testing.T, sink PlacementSink) (*sql.DB, *Service, fileFixture, string) {
	t.Helper()
	x := newTypedRuntimeFixture(t, nil, false)
	wireRealDiagnostics(t, x.db, x.s)
	x.s.SetPlacementSink(sink)
	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("typed completion: handled=%v err=%v", handled, err)
	}
	assertOperationCompleted(t, x.db, x.opID)
	return x.db, x.s, x.f, x.opID
}

func fileAttemptsStarted(t *testing.T, db *sql.DB, operationID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT file_attempts_started FROM file_operations WHERE id=$1`, operationID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
