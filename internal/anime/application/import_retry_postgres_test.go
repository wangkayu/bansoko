package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

// The import-stage retry fixtures reuse the real release -> task -> artifact ->
// plan -> file operation chain of animetest. The settled outcome (a failed
// delivery and a terminal root) is written by the owner the way settlement
// leaves it; the retry itself runs as the runtime role.
const (
	importRetryRoot = "acq-task-rel-ep-1"
	importRetryOp   = "op-rel-ep-1"
)

func settleImportRoot(t *testing.T, owner *sql.DB, outcome string) {
	t.Helper()
	settleImportRootAs(t, owner, outcome, "subscription_add")
}

// settleImportRootAs settles the seeded root as the given entry kind. The
// fixture accepts every root as entry kind one_shot without a marker, a shape
// production never writes (a one-shot always has its marker, and the import
// retry refuses a one-shot whose marker is gone). Subscription tests re-label
// it as the subscription add it stands for, in the same request namespace;
// one-shot tests keep one_shot and seed the marker themselves.
func settleImportRootAs(t *testing.T, owner *sql.DB, outcome, entryKind string) {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE acquisition_id=$1`, importRetryRoot); err != nil {
		t.Fatalf("close live work: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET entry_kind=$2 WHERE id=$1 AND entry_kind<>$2`, importRetryRoot, entryKind); err != nil {
		t.Fatalf("label root as %s: %v", entryKind, err)
	}
	receipt := `{"schema_version":1,"outcome":"` + outcome + `","requirements_status":"sealed","counts":{"total":1,"required":1,"excluded":0,"succeeded":0,"failed":1},"primary_failure":{"stage":"file_execution","reason_code":"typed_publication_failed","file_ref":"primary"},"effect_summary":{"complete":false,"has_confirmed":false,"has_unknown":false},"settled_at":"2026-10-20T00:00:00Z"}`
	if outcome == "succeeded" {
		receipt = `{"schema_version":1,"outcome":"succeeded","requirements_status":"sealed","counts":{"total":1,"required":1,"excluded":0,"succeeded":1,"failed":0},"primary_failure":null,"effect_summary":{"complete":true,"has_confirmed":true,"has_unknown":false},"settled_at":"2026-10-20T00:00:00Z"}`
	}
	if _, err := owner.ExecContext(ctx, `SELECT public.acquisition_write_terminal($1,$2,$3::jsonb,clock_timestamp())`, importRetryRoot, outcome, receipt); err != nil {
		t.Fatalf("settle root: %v", err)
	}
}

// seedFailedImport seeds a root whose one file failed in the import stage,
// the shape the VM case has: a hardlink whose publication failed.
func seedFailedImport(t *testing.T, db, owner *sql.DB, sourceID string) {
	t.Helper()
	seedFailedImportAs(t, db, owner, sourceID, "subscription_add")
}

func seedFailedImportAs(t *testing.T, db, owner *sql.DB, sourceID, entryKind string) {
	t.Helper()
	animetest.SeedReleaseOwnerChain(t, db, sourceID)
	animetest.SeedCompletedFileOp(t, db, sourceID, 1, false, "failed")
	ctx := context.Background()
	if _, err := owner.ExecContext(ctx, `UPDATE file_operations SET failure_code='typed_publication_failed',failure_reason='TARGET_PROOF_UNAVAILABLE',observed_at=clock_timestamp() WHERE id=$1`, importRetryOp); err != nil {
		t.Fatalf("fail operation: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `
UPDATE acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
 delivery_source_cleanup='not_required',delivery_reason_code='typed_publication_failed',
 evidence_digest='sha256:'||repeat('0',64),operation_id=$2,receipt='{"reason_code":"typed_publication_failed","schema_version":1}'::jsonb,
 delivered_at=clock_timestamp()
WHERE acquisition_id=$1 AND file_ref='primary'`, importRetryRoot, importRetryOp); err != nil {
		t.Fatalf("record failed delivery: %v", err)
	}
	settleImportRootAs(t, owner, "failed", entryKind)
}

// pruneFileWork removes every closed WorkItem of the operation, as retention
// does: the retry must not need them.
func pruneFileWork(t *testing.T, owner *sql.DB) {
	t.Helper()
	if _, err := owner.ExecContext(context.Background(),
		`DELETE FROM work_item_outcomes WHERE owner_type='file_operation' AND owner_id=$1`, importRetryOp); err != nil {
		t.Fatalf("prune work items: %v", err)
	}
}

func rootVersion(t *testing.T, db *sql.DB) (state string, version int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT state, version FROM acquisitions WHERE id=$1`, importRetryRoot).Scan(&state, &version); err != nil {
		t.Fatal(err)
	}
	return state, version
}

func liveFileWork(t *testing.T, db *sql.DB) (count int, snapshot string) {
	t.Helper()
	if err := db.QueryRow(`
SELECT count(*), COALESCE(max(input_snapshot::text), '') FROM work_items
WHERE owner_type='file_operation' AND owner_id=$1 AND kind='file_execute' AND state IN ('available','leased')`, importRetryOp).Scan(&count, &snapshot); err != nil {
		t.Fatal(err)
	}
	return count, snapshot
}

func assertVersionConflict(t *testing.T, err error, current int64) {
	t.Helper()
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) || conflict.CurrentVersion != current {
		t.Fatalf("want version conflict at %d, got %v", current, err)
	}
}

func openImportOwner(t *testing.T) *sql.DB {
	t.Helper()
	owner := disposabledb.Owner(t)
	t.Cleanup(func() { _ = owner.Close() })
	return owner
}

func newImportRetry(t *testing.T, db *sql.DB) *ImportRetryService {
	t.Helper()
	service, err := NewImportRetryService(db)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// TestImportRetryRequeuesFailedFileAfterWorkItemsWerePruned is the VM case: the
// failed hardlink has no WorkItem left. The retry resets the operation from
// durable state, clears its delivery result, queues fresh work and reopens the
// root, recording the attempt and the outcome it reopened.
func TestImportRetryRequeuesFailedFileAfterWorkItemsWerePruned(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	owner := openImportOwner(t)
	seedFailedImport(t, db, owner, "source-import-pruned")
	pruneFileWork(t, owner)
	var history int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type='file_operation' AND owner_id=$1`, importRetryOp).Scan(&history); err != nil || history != 0 {
		t.Fatalf("fixture must have no work item left: %d %v", history, err)
	}
	var available bool
	if err := db.QueryRow(`SELECT acquisition_import_retry_refusal($1) IS NULL`, importRetryRoot).Scan(&available); err != nil || !available {
		t.Fatalf("failed import must be retryable: %v %v", available, err)
	}
	_, version := rootVersion(t, db)

	result, err := newImportRetry(t, db).RetryImport(ctx, importRetryRoot, version, "import-retry-pruned")
	if err != nil {
		t.Fatalf("import retry: %v", err)
	}
	if result.Attempt != 2 || result.RetriedFiles != 1 || result.ReopenedPlacements != 0 || result.Replayed {
		t.Fatalf("result = %+v", result)
	}
	state, after := rootVersion(t, db)
	if state != "running" || result.Version != after || after <= version {
		t.Fatalf("root must run again: %s v%d (result v%d, before v%d)", state, after, result.Version, version)
	}
	var opState, key string
	var failure sql.NullString
	var retries, attempts int
	var permit sql.NullString
	if err := db.QueryRow(`SELECT state, failure_code, import_retry_count, file_attempts_started, publication_permit_id, idempotency_key FROM file_operations WHERE id=$1`, importRetryOp).
		Scan(&opState, &failure, &retries, &attempts, &permit, &key); err != nil {
		t.Fatal(err)
	}
	if opState != "pending" || failure.Valid || retries != 1 || attempts != 0 || permit.Valid {
		t.Fatalf("operation reset = %s/%v/%d/%d/%v", opState, failure.String, retries, attempts, permit.String)
	}
	var outcome sql.NullString
	if err := db.QueryRow(`SELECT outcome FROM acquisition_files WHERE acquisition_id=$1 AND file_ref='primary'`, importRetryRoot).Scan(&outcome); err != nil || outcome.Valid {
		t.Fatalf("delivery result must be cleared: %v %v", outcome.String, err)
	}
	live, snapshot := liveFileWork(t, db)
	if live != 1 || !strings.Contains(snapshot, `"operation_id": "`+importRetryOp+`"`) || strings.Contains(snapshot, "placement_retry") {
		t.Fatalf("one fresh file_execute item must be queued: %d %s", live, snapshot)
	}
	var settle int
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE acquisition_id=$1 AND kind='acquisition_settle'`, importRetryRoot).Scan(&settle); err != nil || settle != 1 {
		t.Fatalf("root must have a settlement item: %d %v", settle, err)
	}
	var ordinal int
	var previous string
	if err := db.QueryRow(`SELECT ordinal, previous_state FROM acquisition_import_retries WHERE acquisition_id=$1`, importRetryRoot).Scan(&ordinal, &previous); err != nil || ordinal != 1 || previous != "failed" {
		t.Fatalf("retry record = %d/%s %v", ordinal, previous, err)
	}
	if err := db.QueryRow(`SELECT acquisition_import_retry_refusal($1)`, importRetryRoot).Scan(&failure); err != nil || failure.String != "IMPORT_RETRY_IN_FLIGHT" {
		t.Fatalf("a running root must refuse another retry: %v %v", failure.String, err)
	}
}

// TestImportRetryReplaysAndRefusesWhileInFlight proves the request key replays
// the accepted result, a reused key with another version is refused, and a new
// request is refused with a typed 409 while the retry is in flight.
func TestImportRetryReplaysAndRefusesWhileInFlight(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	owner := openImportOwner(t)
	seedFailedImport(t, db, owner, "source-import-replay")
	_, version := rootVersion(t, db)
	retry := newImportRetry(t, db)

	first, err := retry.RetryImport(ctx, importRetryRoot, version, "import-retry-replay")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := retry.RetryImport(ctx, importRetryRoot, version, "import-retry-replay")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.Attempt != first.Attempt || replay.Version != first.Version || replay.RetriedFiles != first.RetriedFiles {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	if live, _ := liveFileWork(t, db); live != 1 {
		t.Fatalf("replay must not queue more work: %d", live)
	}
	_, err = retry.RetryImport(ctx, importRetryRoot, first.Version, "import-retry-replay")
	animetest.AssertBusinessCode(t, err, 409, "IDEMPOTENCY_KEY_REUSED")

	_, err = retry.RetryImport(ctx, importRetryRoot, first.Version, "import-retry-second")
	animetest.AssertBusinessCode(t, err, 409, "IMPORT_RETRY_IN_FLIGHT")
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM acquisition_import_retries WHERE acquisition_id=$1`, importRetryRoot).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("refused requests must write nothing: %d %v", rows, err)
	}

	// The root is settled again (the retried file failed once more): the
	// next retry is the third run.
	if _, err := owner.Exec(`UPDATE file_operations SET state='failed',failure_code='typed_publication_failed',observed_at=clock_timestamp() WHERE id=$1`, importRetryOp); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`
UPDATE acquisition_files SET delivery_schema_version=1,outcome='failed',effect_class='no_external_effect',
 delivery_source_cleanup='not_required',delivery_reason_code='typed_publication_failed',
 evidence_digest='sha256:'||repeat('0',64),operation_id=$2,receipt='{"reason_code":"typed_publication_failed","schema_version":1}'::jsonb,
 delivered_at=clock_timestamp()
WHERE acquisition_id=$1 AND file_ref='primary'`, importRetryRoot, importRetryOp); err != nil {
		t.Fatal(err)
	}
	settleImportRoot(t, owner, "failed")
	_, version = rootVersion(t, db)
	third, err := retry.RetryImport(ctx, importRetryRoot, version, "import-retry-third")
	if err != nil || third.Attempt != 3 {
		t.Fatalf("third run = %+v %v", third, err)
	}
	var retries int
	if err := db.QueryRow(`SELECT import_retry_count FROM file_operations WHERE id=$1`, importRetryOp).Scan(&retries); err != nil || retries != 2 {
		t.Fatalf("import_retry_count = %d %v", retries, err)
	}
}

// TestImportRetryRefusesStaleVersionAndRetiredOwner proves a stale version and
// a retired owner are typed 409s that change nothing.
func TestImportRetryRefusesStaleVersionAndRetiredOwner(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	owner := openImportOwner(t)
	seedFailedImport(t, db, owner, "source-import-stale")
	_, version := rootVersion(t, db)
	retry := newImportRetry(t, db)

	_, err := retry.RetryImport(ctx, importRetryRoot, version-1, "import-retry-stale")
	assertVersionConflict(t, err, version)

	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1, retired_at = CURRENT_TIMESTAMP,
    retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1
WHERE id = 'anime-trace' AND lifecycle_state = 'active'`); err != nil {
		t.Fatalf("retire managed anime: %v", err)
	}
	_, err = retry.RetryImport(ctx, importRetryRoot, version, "import-retry-retired")
	animetest.AssertBusinessCode(t, err, 409, "MANAGED_ANIME_RETIRED")
	state, after := rootVersion(t, db)
	if state != "failed" || after != version {
		t.Fatalf("refused retries must not touch the root: %s v%d", state, after)
	}
	if live, _ := liveFileWork(t, db); live != 0 {
		t.Fatalf("refused retries must not queue work: %d", live)
	}
	_, err = retry.RetryImport(ctx, "acq-missing", 1, "import-retry-missing")
	var missing *apperrors.NotFound
	if !errors.As(err, &missing) {
		t.Fatalf("missing root: %v", err)
	}
	_, err = retry.RetryImport(ctx, importRetryRoot, version, "short")
	animetest.AssertBusinessCode(t, err, 422, "IDEMPOTENCY_KEY_REQUIRED")
}

// TestImportRetryReopensFailedPlacementOfCompletedFile proves a failed
// placement handoff is part of the import stage: the obligation reopens with a
// fresh typed continuation (even with its failed continuation pruned) while the
// completed file effect and the settled root stay untouched.
func TestImportRetryReopensFailedPlacementOfCompletedFile(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	animetest.SeedReleaseOwnerChain(t, db, "source-import-placement")
	animetest.SeedCompletedFileOp(t, db, "source-import-placement", 1, false, "completed")
	animetest.SeedPlacementHandoff(t, db, importRetryOp, "failed", 3, 3, "")
	settleImportRoot(t, openImportOwner(t), "succeeded")
	var opVersion int64
	if err := db.QueryRow(`SELECT version FROM file_operations WHERE id=$1`, importRetryOp).Scan(&opVersion); err != nil {
		t.Fatal(err)
	}
	_, version := rootVersion(t, db)

	result, err := newImportRetry(t, db).RetryImport(ctx, importRetryRoot, version, "import-retry-placement")
	if err != nil {
		t.Fatalf("import retry: %v", err)
	}
	if result.RetriedFiles != 0 || result.ReopenedPlacements != 1 {
		t.Fatalf("result = %+v", result)
	}
	var obligation string
	var attempts int
	if err := db.QueryRow(`SELECT state, attempts FROM placement_obligations WHERE file_operation_id=$1`, importRetryOp).Scan(&obligation, &attempts); err != nil || obligation != "pending" || attempts != 0 {
		t.Fatalf("obligation = %s/%d %v", obligation, attempts, err)
	}
	live, snapshot := liveFileWork(t, db)
	if live != 1 || !strings.Contains(snapshot, `"placement_retry": true`) || !strings.Contains(snapshot, "file-execute-continuation/v1") {
		t.Fatalf("one typed continuation must be queued: %d %s", live, snapshot)
	}
	var opState string
	var after int64
	if err := db.QueryRow(`SELECT state, version FROM file_operations WHERE id=$1`, importRetryOp).Scan(&opState, &after); err != nil || opState != "completed" || after != opVersion {
		t.Fatalf("completed effect must stay untouched: %s v%d (was v%d) %v", opState, after, opVersion, err)
	}
	state, rootAfter := rootVersion(t, db)
	if state != "succeeded" || rootAfter != result.Version || rootAfter != version+1 {
		t.Fatalf("root = %s v%d", state, rootAfter)
	}
	_, err = newImportRetry(t, db).RetryImport(ctx, importRetryRoot, rootAfter, "import-retry-placement-2")
	animetest.AssertBusinessCode(t, err, 409, "IMPORT_RETRY_IN_FLIGHT")
}

// TestImportRetryNotApplicableWithoutImportFailure proves a settled root with
// nothing failed in the import stage is refused.
func TestImportRetryNotApplicableWithoutImportFailure(t *testing.T) {
	db := animetest.OpenTestDB(t)
	animetest.SeedReleaseOwnerChain(t, db, "source-import-na")
	animetest.SeedCompletedFileOp(t, db, "source-import-na", 1, false, "completed")
	settleImportRoot(t, openImportOwner(t), "succeeded")
	_, version := rootVersion(t, db)
	_, err := newImportRetry(t, db).RetryImport(context.Background(), importRetryRoot, version, "import-retry-na")
	animetest.AssertBusinessCode(t, err, 409, "IMPORT_RETRY_NOT_APPLICABLE")
}

// TestImportRetryReopensOneShotMarker mirrors the VM one-shot case: a failed
// one_shot root (one failed execute file, its operation failed with
// TARGET_PROOF_UNAVAILABLE, its WorkItems pruned) whose marker already drained
// to retention_pending with a failed terminal outcome and a future retention
// deadline. A second retention_pending marker that succeeded shares the
// correlation id: the root resolves its marker only by request key
// (one_shot_acquisitions.request_idempotency_key = acquisitions.request_key),
// never by correlation. The retry re-opens exactly that marker to running
// with a new 'retry' attempt at file_execution, clears its terminal evidence
// and retention deadline, and queues file_execute work carrying the marker
// reference; the other marker is untouched.
func TestImportRetryReopensOneShotMarker(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	owner := openImportOwner(t)
	seedFailedImportAs(t, db, owner, "source-import-oneshot", "one_shot")
	pruneFileWork(t, owner)
	var requestKey, taskID string
	if err := owner.QueryRow(`SELECT r.request_key, t.id FROM acquisitions r JOIN download_tasks t ON t.acquisition_id=r.id WHERE r.id=$1`, importRetryRoot).Scan(&requestKey, &taskID); err != nil {
		t.Fatal(err)
	}
	if len(requestKey) < 8 {
		requestKey = requestKey + "-padding"
		if _, err := owner.Exec(`UPDATE acquisitions SET request_key=$2 WHERE id=$1`, importRetryRoot, requestKey); err != nil {
			t.Fatal(err)
		}
	}
	for _, marker := range []struct{ id, anime, key, outcome, code, reason string }{
		{"marker-other", "anime-other", "other-request-key", "succeeded", "one_shot_completed", "one-shot acquisition completed"},
		{"marker-import", "anime-trace", requestKey, "failed", "one_shot_failed", "one-shot acquisition failed in file_execution"},
	} {
		if _, err := owner.Exec(`
INSERT INTO one_shot_acquisitions(id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, state,
    terminal_outcome, terminal_reason_code, terminal_reason, terminal_at, retention_until)
VALUES ($1, $2, 'subscription-trace', 'source-import-oneshot', 'rel-ep-1', $3,
    'corr-import', $4, 'sha256:'||repeat('0',64), '{}'::jsonb, 'retention_pending',
    $5, $6, $7, clock_timestamp(), clock_timestamp() + interval '7 days')`,
			marker.id, marker.anime, taskID, marker.key, marker.outcome, marker.code, marker.reason); err != nil {
			t.Fatalf("seed marker %s: %v", marker.id, err)
		}
		attemptState := "failed"
		var failedStage any = "file_execution"
		if marker.outcome == "succeeded" {
			attemptState, failedStage = "succeeded", nil
		}
		if _, err := owner.Exec(`
INSERT INTO one_shot_execution_attempts(id, acquisition_id, sequence, mode, input_snapshot, input_digest, state,
    failed_stage, outcome_code, outcome_reason, terminal_at)
VALUES ($1, $2, 1, 'initial', '{}'::jsonb, 'sha256:'||repeat('1',64), $3, $4, $5, $6, clock_timestamp())`,
			"attempt-"+marker.id, marker.id, attemptState, failedStage, marker.code, marker.reason); err != nil {
			t.Fatalf("seed attempt of %s: %v", marker.id, err)
		}
		if _, err := owner.Exec(`UPDATE one_shot_acquisitions SET current_attempt_id=$2 WHERE id=$1`, marker.id, "attempt-"+marker.id); err != nil {
			t.Fatal(err)
		}
	}
	var history int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type='file_operation' AND owner_id=$1`, importRetryOp).Scan(&history); err != nil || history != 0 {
		t.Fatalf("fixture must have no work item left: %d %v", history, err)
	}
	var refusal sql.NullString
	if err := db.QueryRow(`SELECT acquisition_import_retry_refusal($1)`, importRetryRoot).Scan(&refusal); err != nil || refusal.Valid {
		t.Fatalf("a failed one-shot import with a retention_pending marker must be retryable: %v %v", refusal, err)
	}
	_, version := rootVersion(t, db)

	result, err := newImportRetry(t, db).RetryImport(ctx, importRetryRoot, version, "import-retry-oneshot")
	if err != nil {
		t.Fatalf("one-shot import retry: %v", err)
	}
	if result.RetriedFiles != 1 || result.Attempt != 2 {
		t.Fatalf("result = %+v", result)
	}
	if state, _ := rootVersion(t, db); state != "running" {
		t.Fatalf("root = %s, want running", state)
	}
	var opState string
	if err := db.QueryRow(`SELECT state FROM file_operations WHERE id=$1`, importRetryOp).Scan(&opState); err != nil || opState != "pending" {
		t.Fatalf("operation = %s %v, want pending", opState, err)
	}
	var markerState, current string
	var outcome sql.NullString
	var retention sql.NullTime
	if err := db.QueryRow(`SELECT state, current_attempt_id, terminal_outcome, retention_until FROM one_shot_acquisitions WHERE id='marker-import'`).Scan(&markerState, &current, &outcome, &retention); err != nil {
		t.Fatal(err)
	}
	if markerState != "running" || outcome.Valid || retention.Valid || current == "attempt-marker-import" {
		t.Fatalf("marker = %s/%s/%v/%v", markerState, current, outcome, retention.Valid)
	}
	var mode, attemptState, resume, previous string
	var sequence int
	if err := db.QueryRow(`SELECT mode, state, resume_stage, previous_attempt_id, sequence FROM one_shot_execution_attempts WHERE id=$1`, current).
		Scan(&mode, &attemptState, &resume, &previous, &sequence); err != nil {
		t.Fatal(err)
	}
	if mode != "retry" || attemptState != "running" || resume != "file_execution" || previous != "attempt-marker-import" || sequence != 2 {
		t.Fatalf("retry attempt = %s/%s/%s/%s/%d", mode, attemptState, resume, previous, sequence)
	}
	var otherState, otherAttempt string
	if err := db.QueryRow(`SELECT state, current_attempt_id FROM one_shot_acquisitions WHERE id='marker-other'`).Scan(&otherState, &otherAttempt); err != nil {
		t.Fatal(err)
	}
	if otherState != "retention_pending" || otherAttempt != "attempt-marker-other" {
		t.Fatalf("the other marker changed: %s/%s", otherState, otherAttempt)
	}
	count, snapshot := liveFileWork(t, db)
	if count != 1 || !strings.Contains(snapshot, `"marker_id": "marker-import"`) || !strings.Contains(snapshot, `"operation_id": "`+importRetryOp+`"`) {
		t.Fatalf("one file_execute item carrying the marker reference must be queued: %d %s", count, snapshot)
	}
}
