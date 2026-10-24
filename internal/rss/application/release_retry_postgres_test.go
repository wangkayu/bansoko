package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/batchplan"
	"bansoko/internal/workitems"
)

const retryTitle = "[Group] Test Show - 01 1080p"

// newRetryFixture seeds one Release of the RSS test owner with a program-owned
// torrent payload and returns the service that evaluates it.
func newRetryFixture(t *testing.T, stopped, metadataComplete bool, releaseID string) (*sql.DB, *workitems.Store, *Service) {
	t.Helper()
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, stopped, metadataComplete)
	insertReleaseForEvaluation(t, db, sourceID, releaseID, retryTitle, "https://downloads.example.test/show-01.torrent")
	seedNativeTorrent(t, db, "torrent-"+releaseID, releaseID)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	return db, store, service
}

// evaluateAutomatically runs the release_evaluate worker once.
func evaluateAutomatically(t *testing.T, db *sql.DB, store *workitems.Store, service *Service, releaseID string) {
	t.Helper()
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: releaseID}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, releaseID))
	finishReleaseClaim(t, store, service, claim)
}

// failCurrentDownload fails the Release's current download task and settles
// its acquisition as failed, the state a stalled or rejected download ends in.
func failCurrentDownload(t *testing.T, releaseID string) string {
	t.Helper()
	owner := ownerTestDB(t)
	ctx := context.Background()
	var taskID, acquisitionID string
	if err := owner.QueryRowContext(ctx, `
SELECT task.id, task.acquisition_id FROM releases r JOIN download_tasks task ON task.id = r.download_task_id
WHERE r.id = $1`, releaseID).Scan(&taskID, &acquisitionID); err != nil {
		t.Fatalf("read current download: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='failed',failure_code='download_stalled',failure_reason='fixture',version=version+1 WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET state='failed',terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, acquisitionID); err != nil {
		t.Fatal(err)
	}
	return taskID
}

func releaseTaskCount(t *testing.T, db *sql.DB, releaseID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM download_tasks WHERE release_id = $1`, releaseID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertNotRetryable(t *testing.T, err error, reason string) {
	t.Helper()
	var notRetryable *ReleaseNotRetryableError
	if !errors.As(err, &notRetryable) || notRetryable.Reason != reason {
		t.Fatalf("retry error = %v, want not retryable %s", err, reason)
	}
}

// A Release whose download failed is accepted again: a new download task with
// its own request and effect keys, the failed one kept as a record. The same
// idempotency key replays the result, and a second request finds the download
// in progress.
func TestRetryFailedDownloadCreatesANewTask(t *testing.T) {
	db, store, service := newRetryFixture(t, false, true, "release-retry-failed")
	ctx := context.Background()
	evaluateAutomatically(t, db, store, service, "release-retry-failed")
	assertReleaseState(t, db, "release-retry-failed", "accepted_pending_download", "accepted", true)
	_, err := service.RetryRelease(ctx, "release-retry-failed", "retry-key-in-progress")
	assertNotRetryable(t, err, RetryReasonDownloadInProgress)

	failedTask := failCurrentDownload(t, "release-retry-failed")
	result, err := service.RetryRelease(ctx, "release-retry-failed", "retry-key-0001")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAccepted || result.DownloadTaskID == "" || result.DownloadTaskID == failedTask {
		t.Fatalf("retry result = %+v, want a new accepted task", result)
	}
	if count := releaseTaskCount(t, db, "release-retry-failed"); count != 2 {
		t.Fatalf("download tasks = %d, want the failed one kept and one new", count)
	}
	var current, failedState, effectKey, namespace, requestKey string
	if err := db.QueryRowContext(ctx, `
SELECT r.download_task_id, old.state, task.idempotency_key, acq.request_namespace, acq.request_key
FROM releases r
JOIN download_tasks task ON task.id = r.download_task_id
JOIN acquisitions acq ON acq.id = task.acquisition_id
JOIN download_tasks old ON old.id = $2
WHERE r.id = $1`, "release-retry-failed", failedTask).Scan(&current, &failedState, &effectKey, &namespace, &requestKey); err != nil {
		t.Fatal(err)
	}
	if current != result.DownloadTaskID || failedState != "failed" {
		t.Fatalf("release task = %s (failed task %s), want %s", current, failedState, result.DownloadTaskID)
	}
	if effectKey != "qb:release-retry-failed:2" || namespace != "manual_acquire_v1" || requestKey != "retry:release-retry-failed:2" {
		t.Fatalf("second download keys = %s %s/%s, want attempt 2 keys of the manual entry", effectKey, namespace, requestKey)
	}

	replay, err := service.RetryRelease(ctx, "release-retry-failed", "retry-key-0001")
	if err != nil || replay != result {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, result)
	}
	if count := releaseTaskCount(t, db, "release-retry-failed"); count != 2 {
		t.Fatalf("replay created a task: %d", count)
	}
	_, err = service.RetryRelease(ctx, "release-retry-failed", "retry-key-0002")
	assertNotRetryable(t, err, RetryReasonDownloadInProgress)
}

// A download deleted in the downloader ends its record cancelled; the Release
// is retryable exactly as after a failure, and the
// retry creates a new task instead of reviving the cancelled one.
func TestRetryDownloadDeletedInTheDownloaderCreatesANewTask(t *testing.T) {
	db, store, service := newRetryFixture(t, false, true, "release-retry-removed")
	ctx := context.Background()
	evaluateAutomatically(t, db, store, service, "release-retry-removed")
	owner := ownerTestDB(t)
	var removedTask, acquisitionID string
	if err := owner.QueryRowContext(ctx, `
SELECT task.id, task.acquisition_id FROM releases r JOIN download_tasks task ON task.id = r.download_task_id
WHERE r.id = $1`, "release-retry-removed").Scan(&removedTask, &acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='failed',failure_code='DOWNLOADER_TASK_REMOVED',failure_reason='fixture',consecutive_absent=2,version=version+1 WHERE id=$1`, removedTask); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET state='cancelled',terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, acquisitionID); err != nil {
		t.Fatal(err)
	}
	result, err := service.RetryRelease(ctx, "release-retry-removed", "retry-key-removed")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAccepted || result.DownloadTaskID == "" || result.DownloadTaskID == removedTask {
		t.Fatalf("retry result = %+v, want a new accepted task", result)
	}
	if count := releaseTaskCount(t, db, "release-retry-removed"); count != 2 {
		t.Fatalf("download tasks = %d, want the cancelled one kept and one new", count)
	}
	var state, code string
	if err := db.QueryRowContext(ctx, `SELECT state, failure_code FROM download_tasks WHERE id=$1`, removedTask).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != "DOWNLOADER_TASK_REMOVED" {
		t.Fatalf("the cancelled task changed: %s %s", state, code)
	}
}

// A filtered Release stays filtered: the retry reports the reason and creates
// nothing.
func TestRetryFilteredReleaseStaysFiltered(t *testing.T) {
	db, store, service := newRetryFixture(t, false, true, "release-retry-filtered")
	filter, err := json.Marshal(map[string]any{"must_contain": "", "not_in": "1080p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
UPDATE anime_config_overrides SET schema_version = 'release-rules-v1', value = $1
WHERE managed_anime_id = 'anime-rss-test' AND section_name = 'release_rules'`, filter); err != nil {
		t.Fatal(err)
	}
	evaluateAutomatically(t, db, store, service, "release-retry-filtered")
	assertReleaseState(t, db, "release-retry-filtered", "filtered", "filtered_by_rules", false)

	result, err := service.RetryRelease(context.Background(), "release-retry-filtered", "retry-key-filtered")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeFiltered || result.ReasonCode != "filtered_by_rules" || result.DownloadTaskID != "" {
		t.Fatalf("retry result = %+v, want filtered_by_rules", result)
	}
	assertReleaseState(t, db, "release-retry-filtered", "filtered", "filtered_by_rules", false)
}

// The metadata hold applies to a retry exactly as to automatic acceptance.
func TestRetryHeldReleaseStaysHeld(t *testing.T) {
	db, _, service := newRetryFixture(t, false, false, "release-retry-held")
	result, err := service.RetryRelease(context.Background(), "release-retry-held", "retry-key-held")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeHeld || result.ReasonCode != "metadata_incomplete" {
		t.Fatalf("retry result = %+v, want held metadata_incomplete", result)
	}
	assertReleaseState(t, db, "release-retry-held", "waiting_metadata", "metadata_incomplete", false)
}

// A stopped subscription stops automatic work only: the explicit retry still
// accepts the Release.
func TestRetryWorksWhileTheSubscriptionIsStopped(t *testing.T) {
	db, store, service := newRetryFixture(t, true, true, "release-retry-stopped")
	evaluateAutomatically(t, db, store, service, "release-retry-stopped")
	assertReleaseState(t, db, "release-retry-stopped", "subscription_stopped", "subscription_stopped", false)

	result, err := service.RetryRelease(context.Background(), "release-retry-stopped", "retry-key-stopped")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAccepted || result.DownloadTaskID == "" {
		t.Fatalf("retry result = %+v, want accepted", result)
	}
	assertReleaseState(t, db, "release-retry-stopped", "accepted_pending_download", "accepted", true)
}

// The retry runs the batch plan with the same fill-missing deduplication as
// automatic acceptance: episodes the library already holds are not
// downloaded again.
func TestRetryRespectsFillMissingDedup(t *testing.T) {
	initTestNamingVars(t)
	db, _, service := newRetryFixture(t, false, true, "release-retry-dedup")
	service.SetTargetAcquisition(missingTargetProbe{})
	service.SetTargetStore(target.NewStore(targetpersistence.NewRepository(db)))
	service.SetReplacementAdmission(presentCoverage{})

	result, err := service.RetryRelease(context.Background(), "release-retry-dedup", "retry-key-dedup")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeFiltered || result.ReasonCode != batchplan.ReasonNoPlaceableFiles {
		t.Fatalf("retry result = %+v, want filtered no_placeable_files", result)
	}
	assertReleaseState(t, db, "release-retry-dedup", "filtered", batchplan.ReasonNoPlaceableFiles, false)
}

// presentCoverage reports every candidate key as already held by the library.
type presentCoverage struct{}

func (presentCoverage) CoverageVerdicts(_ context.Context, _, _ string, candidate target.CandidateEvidence) (map[string]batchplan.Verdict, error) {
	verdicts := make(map[string]batchplan.Verdict, len(candidate.Coverage))
	for _, key := range candidate.Coverage {
		verdicts[key] = batchplan.VerdictExisting
	}
	return verdicts, nil
}

func (presentCoverage) CandidateIntents(context.Context, string, string, *target.CandidateEvidence) (map[string]target.Intent, error) {
	return map[string]target.Intent{}, nil
}

// completeCurrentDownload completes the Release's current download task and
// settles its acquisition in acquisitionState (succeeded or failed).
func completeCurrentDownload(t *testing.T, releaseID, acquisitionState string) string {
	t.Helper()
	owner := ownerTestDB(t)
	ctx := context.Background()
	var taskID, acquisitionID string
	if err := owner.QueryRowContext(ctx, `
SELECT task.id, task.acquisition_id FROM releases r JOIN download_tasks task ON task.id = r.download_task_id
WHERE r.id = $1`, releaseID).Scan(&taskID, &acquisitionID); err != nil {
		t.Fatalf("read current download: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed',version=version+1 WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE acquisitions SET state=$2,terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, acquisitionID, acquisitionState); err != nil {
		t.Fatal(err)
	}
	return taskID
}

// Run again: a Release whose download finished but
// whose post-processing failed runs the whole chain again — a new download
// task through normal acceptance, the completed one kept as a record. A
// Release whose files are in the library may not.
func TestRetryAfterFailedPostProcessingCreatesANewTask(t *testing.T) {
	db, store, service := newRetryFixture(t, false, true, "release-retry-post")
	ctx := context.Background()
	evaluateAutomatically(t, db, store, service, "release-retry-post")
	completedTask := completeCurrentDownload(t, "release-retry-post", "failed")
	result, err := service.RetryRelease(ctx, "release-retry-post", "retry-key-post-0001")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != RetryOutcomeAccepted || result.DownloadTaskID == "" || result.DownloadTaskID == completedTask {
		t.Fatalf("retry result = %+v, want a new accepted task", result)
	}
	if count := releaseTaskCount(t, db, "release-retry-post"); count != 2 {
		t.Fatalf("download tasks = %d, want the completed one kept and one new", count)
	}
}

func TestRetryOfAPlacedReleaseIsRefused(t *testing.T) {
	db, store, service := newRetryFixture(t, false, true, "release-retry-placed")
	evaluateAutomatically(t, db, store, service, "release-retry-placed")
	completeCurrentDownload(t, "release-retry-placed", "succeeded")
	_, err := service.RetryRelease(context.Background(), "release-retry-placed", "retry-key-placed-0001")
	assertNotRetryable(t, err, RetryReasonAlreadyPlaced)
}
