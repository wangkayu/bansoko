package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	artifactapplication "bansoko/internal/artifact/application"
	downloadapplication "bansoko/internal/download/application"
	downloaddomain "bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/workitems"
)

// This file records the CURRENT behavior of ManualRetry on the subscription
// path (not one-shot) once a typed stage has already closed the owner as
// terminal. It is the pre-closure evidence for 13.16.32 item 4; the S4 mainline
// session migrates these assertions to the 409 MANUAL_RETRY_STAGE_RETIRED
// zero-write shape afterwards.
//
// Every assertion below is written as "record what happens", never as "this is
// correct". If the observed behavior differs from the expectation recorded in
// the task PRD (the retry is accepted and then the replacement work fails), the
// test reports the observed facts with a RECORDED-DIVERGENCE marker instead of
// being relaxed.

// stageRetryProvider is the qB provider port. It counts every call so the audit
// can prove whether a replacement submit reached the provider at all.
type stageRetryProvider struct {
	lookupCalls     int
	fileSubmitCalls int
	syncCalls       int
	lookupFound     bool
}

func (p *stageRetryProvider) LookupByOperationKey(context.Context, string) (downloaddomain.ProviderTask, bool, error) {
	p.lookupCalls++
	return downloaddomain.ProviderTask{}, p.lookupFound, nil
}

func (p *stageRetryProvider) Sync(context.Context, string) (downloaddomain.ProviderTask, error) {
	p.syncCalls++
	return downloaddomain.ProviderTask{}, nil
}

func (p *stageRetryProvider) ClassifyProviderError(err error) downloaddomain.ProviderErrorClass {
	if err == nil {
		return downloaddomain.ProviderErrorUnknown
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return downloaddomain.ProviderErrorRetryable
	}
	return downloaddomain.ProviderErrorAmbiguous
}

// stageRetryRuntimeConfig is the internal qB connection projection.
type stageRetryRuntimeConfig struct {
	calls int
}

func (c *stageRetryRuntimeConfig) GetQBittorrentRuntimeConfig(context.Context) (qbittorrent.RuntimeConfig, error) {
	c.calls++
	return qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}, nil
}

// stageRetryInspector is the read-only artifact inspection port. It fails on
// demand so the audit can drive a real inspection failure through the
// production handler instead of arranging a terminal artifact by hand.
type stageRetryInspector struct {
	fail      bool
	failError error
	calls     int
}

func (i *stageRetryInspector) Inspect(_ context.Context, _ string) (downloaddomain.ArtifactFacts, error) {
	i.calls++
	if i.fail {
		return downloaddomain.ArtifactFacts{}, i.failError
	}
	return downloaddomain.ArtifactFacts{Path: "/downloads/show.mkv", SizeBytes: 1, SHA256: "audit"}, nil
}

// stageRetryRow is one recorded fact about a work item.
type stageRetryRow struct {
	State     string
	ErrorCode sql.NullString
	Attempt   int
}

func readStageRetryRow(t *testing.T, db *sql.DB, workID string) stageRetryRow {
	t.Helper()
	var row stageRetryRow
	if err := db.QueryRowContext(context.Background(), `
SELECT state, last_error_code, attempt FROM work_item_history WHERE id=$1`, workID).Scan(&row.State, &row.ErrorCode, &row.Attempt); err != nil {
		t.Fatalf("read work item %s: %v", workID, err)
	}
	return row
}

func stageRetryString(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

// failedSubmitWorkID returns the single failed qb_submit work item of a task.
func failedSubmitWorkID(t *testing.T, db *sql.DB, taskID string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(), `
SELECT id FROM work_item_history WHERE owner_type='download_task' AND owner_id=$1 AND kind='qb_submit' AND state='failed' ORDER BY id LIMIT 1`, taskID).Scan(&id); err != nil {
		t.Fatalf("read failed submit work for %s: %v", taskID, err)
	}
	return id
}

// availableSubmitWorkID returns the available qb_submit work item of a task that
// is not the given original.
func availableSubmitWorkID(t *testing.T, db *sql.DB, taskID, except string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(), `
SELECT id FROM work_items WHERE owner_type='download_task' AND owner_id=$1 AND kind='qb_submit' AND state='available' AND id<>$2 ORDER BY id LIMIT 1`, taskID, except).Scan(&id); err != nil {
		t.Fatalf("read available submit work for %s: %v", taskID, err)
	}
	return id
}

func stageRetryTaskVersion(t *testing.T, db *sql.DB, taskID string) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM download_tasks WHERE id=$1`, taskID).Scan(&version); err != nil {
		t.Fatalf("read task version: %v", err)
	}
	return version
}

func stageRetryTaskState(t *testing.T, db *sql.DB, taskID string) (string, string) {
	t.Helper()
	var state string
	var code sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT state, failure_code FROM download_tasks WHERE id=$1`, taskID).Scan(&state, &code); err != nil {
		t.Fatalf("read task state: %v", err)
	}
	return state, stageRetryString(code)
}

func stageRetryArtifactState(t *testing.T, db *sql.DB, artifactID string) (string, string) {
	t.Helper()
	var state string
	var code sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT state, failure_code FROM artifacts WHERE id=$1`, artifactID).Scan(&state, &code); err != nil {
		t.Fatalf("read artifact state: %v", err)
	}
	return state, stageRetryString(code)
}

func stageRetryArtifactVersion(t *testing.T, db *sql.DB, artifactID string) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(context.Background(), `SELECT version FROM artifacts WHERE id=$1`, artifactID).Scan(&version); err != nil {
		t.Fatalf("read artifact version: %v", err)
	}
	return version
}

func stageRetryFailedInspectWork(t *testing.T, db *sql.DB, artifactID string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(), `
SELECT id FROM work_item_history WHERE owner_type='artifact' AND owner_id=$1 AND kind='artifact_inspect' AND state='failed' ORDER BY id LIMIT 1`, artifactID).Scan(&id); err != nil {
		t.Fatalf("read failed inspection work: %v", err)
	}
	return id
}

func stageRetryAvailableInspectWork(t *testing.T, db *sql.DB, artifactID, except string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(), `
SELECT id FROM work_items WHERE owner_type='artifact' AND owner_id=$1 AND kind='artifact_inspect' AND state='available' AND id<>$2 ORDER BY id LIMIT 1`, artifactID, except).Scan(&id); err != nil {
		t.Fatalf("read available inspection work: %v", err)
	}
	return id
}

// TestStageManualRetryDownloadTaskRecordsTerminalClosure records what happens
// when the subscription path retries a failed download_task whose submit stage
// has already closed the owner as terminal.
func TestStageManualRetryDownloadTaskRecordsTerminalClosure(t *testing.T) {
	db := animetest.OpenTestDB(t)
	animetest.SeedReleaseOwnerChain(t, db, "source-stage-retry-download")
	ctx := context.Background()

	// Real acceptance through the owner-held queued entry; the task is then
	// owner-arranged into the terminal failed state with a provider correlation.
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// SeedReleaseOwnerChain stops at the RSS source; the accept entry needs a
	// release (and its source runtime row) to attach the task to.
	if _, err := tx.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-stage-retry-download')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-trace-stage-retry-download', 'source-stage-retry-download', 'guid:stage-retry-download',
        '{"title":"Stage Retry Download"}', '{}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, tx, "acq-stage-retry-download", "task-stage-retry-download",
		"rel-trace-stage-retry-download", "qb:stage-retry-download", `{}`, frozenpolicy.MustJSON(),
		"failed", `{"external_id":"tag:stage-retry-download","state":"failed"}`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id='task-stage-retry-download', verdict='submitted' WHERE id='rel-trace-stage-retry-download'`); err != nil {
		t.Fatal(err)
	}
	// The accept created the first qb_submit work and the fixture closed it;
	// turn that outcome into a failure so the manual retry has a terminal row
	// to replace.
	if res, err := tx.ExecContext(ctx, `
UPDATE work_item_outcomes SET state='failed', last_error_code='qb_submit_failed', last_error_reason='the submit attempt failed'
WHERE owner_type='download_task' AND owner_id='task-stage-retry-download' AND kind='qb_submit'`); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("closed submit outcomes = %d, want 1", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	originalWork := failedSubmitWorkID(t, db, "task-stage-retry-download")
	taskVersionBefore := stageRetryTaskVersion(t, db, "task-stage-retry-download")

	provider := &stageRetryProvider{}
	config := &stageRetryRuntimeConfig{}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	downloads, err := downloadapplication.NewService(db, store, provider, config)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}

	_ = downloads
	before := stageRetryClosureSnapshot(t, db)
	err = retry.ManualRetry(ctx, workitems.OwnerDownloadTask, "task-stage-retry-download", originalWork, "retry", "stage-retry-download-key")
	assertStageRetryRetired(t, err)
	if stageRetryClosureSnapshot(t, db) != before {
		t.Fatal("retired retry wrote database")
	}
	if stageRetryTaskVersion(t, db, "task-stage-retry-download") != taskVersionBefore || provider.fileSubmitCalls != 0 || provider.lookupCalls != 0 || provider.syncCalls != 0 {
		t.Fatal("retired retry changed owner/provider")
	}
}

// TestStageManualRetryArtifactRecordsTerminalClosure records the same shape for
// the inspection stage: a real acceptance, a real inspection failure driven
// through the production handler, then a manual retry of the failed artifact.
func TestStageManualRetryArtifactRecordsTerminalClosure(t *testing.T) {
	db := animetest.OpenTestDB(t)
	animetest.SeedReleaseOwnerChain(t, db, "source-stage-retry-artifact")
	ctx := context.Background()

	// Real acceptance, then the download task is owner-arranged as completed
	// with a provider correlation so the artifact stage can run.
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-stage-retry-artifact')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-trace-stage-retry-artifact', 'source-stage-retry-artifact', 'guid:stage-retry-artifact',
        '{"title":"Stage Retry Artifact"}', '{}', 'accepted_pending_download', 'accepted')`); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, tx, "acq-stage-retry-artifact", "task-stage-retry-artifact",
		"rel-trace-stage-retry-artifact", "qb:stage-retry-artifact", `{}`, frozenpolicy.MustJSON(),
		"completed", `{"external_id":"tag:stage-retry-artifact","state":"completed","artifact_path":"/downloads/show.mkv"}`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id='task-stage-retry-artifact', verdict='submitted' WHERE id='rel-trace-stage-retry-artifact'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('artifact-stage-retry', 'task-stage-retry-artifact', 'acq-stage-retry-artifact', '{}', '{}', 'pending', NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	inspector := &stageRetryInspector{fail: true, failError: errors.New("inspection failed for the audit")}
	artifacts, err := artifactapplication.NewService(db, inspector)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}

	// Drive the first inspection through the production handler so the artifact
	// reaches its terminal failed state and its work item becomes failed.
	inspectTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer inspectTx.Rollback()
	inspectWork, err := store.Create(ctx, inspectTx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerArtifact, ID: "artifact-stage-retry"},
		Kind:                 workitems.KindArtifactInspect,
		IdempotencyKey:       "artifact_inspect:artifact-stage-retry",
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		_ = inspectTx.Rollback()
		t.Fatal(err)
	}
	if err := inspectTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id=$1`, inspectWork); err != nil {
		t.Fatal(err)
	}
	inspectClaim, err := store.ClaimDue(ctx, "stage-retry-inspector", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatalf("claim the first inspection: %v", err)
	}
	if _, err := artifacts.Handle(ctx, inspectClaim); err != nil {
		t.Fatalf("drive the first inspection: %v", err)
	}
	artifactState, artifactCode := stageRetryArtifactState(t, db, "artifact-stage-retry")
	t.Logf("artifact after the first inspection: state=%q code=%q inspector calls=%d", artifactState, artifactCode, inspector.calls)
	if artifactState != "failed" {
		t.Fatalf("RECORDED-DIVERGENCE: the first inspection left the artifact in %q, want failed", artifactState)
	}

	originalWork := stageRetryFailedInspectWork(t, db, "artifact-stage-retry")
	artifactVersionBefore := stageRetryArtifactVersion(t, db, "artifact-stage-retry")

	before := stageRetryClosureSnapshot(t, db)
	err = retry.ManualRetry(ctx, workitems.OwnerArtifact, "artifact-stage-retry", originalWork, "retry", "stage-retry-artifact-key")
	assertStageRetryRetired(t, err)
	if stageRetryClosureSnapshot(t, db) != before {
		t.Fatal("retired retry wrote database")
	}
	if stageRetryArtifactVersion(t, db, "artifact-stage-retry") != artifactVersionBefore || inspector.calls != 0 {
		t.Fatal("retired retry changed artifact/inspector")
	}
}

func assertStageRetryRetired(t *testing.T, err error) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 409 || business.Code != "MANUAL_RETRY_STAGE_RETIRED" {
		t.Fatalf("retired retry=%v", err)
	}
}
func stageRetryClosureSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var result string
	if err := db.QueryRow(`SELECT jsonb_build_object('work',(SELECT jsonb_agg(to_jsonb(w) ORDER BY id) FROM work_item_history w),'download',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM download_tasks d),'artifact',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM artifacts a))::text`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
