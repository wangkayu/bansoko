package application

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bansoko/internal/acquisition/torrent"
	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type fakeProvider struct {
	lookupTask         domain.ProviderTask
	lookupFound        bool
	lookupErr          error
	lookupResults      []bool
	fileSubmitTask     domain.ProviderTask
	fileSubmitErr      error
	fileSubmitPayload  []byte
	fileSubmitPayloads [][]byte
	fileSubmitFilename string
	fileSubmitSnapshot domain.PathSnapshot
	syncTask           domain.ProviderTask
	syncErr            error
	lookupCalls        int
	fileSubmitCalls    int
	syncCalls          int
}

type fakeSubmissionTransport struct {
	provider *fakeProvider
	payload  []byte
	filename string
	snapshot domain.PathSnapshot
	key      string
}

func (r fakeSubmissionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/auth/login") {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("Ok.")), Header: make(http.Header)}, nil
	}
	if strings.HasSuffix(req.URL.Path, "/torrents/info") {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]")), Header: make(http.Header)}, nil
	}
	if !strings.HasSuffix(req.URL.Path, "/torrents/add") {
		return nil, errors.New("unexpected prepared submission request")
	}
	_, err := r.provider.SubmitFileWithSnapshot(req.Context(), r.payload, r.filename, r.snapshot, r.key)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("Ok.")), Header: make(http.Header)}, nil
}

func (p *fakeProvider) PrepareSubmission(ctx context.Context, payload []byte, filename string, snapshot domain.PathSnapshot, key string) (*qbittorrent.PreparedSubmission, domain.ProviderTask, bool, error) {
	observed, found, err := p.LookupByOperationKey(ctx, key)
	if err != nil || found {
		return nil, observed, found, err
	}
	client := qbittorrent.Client{Endpoint: "http://qb.example:8080", HTTP: &http.Client{Transport: fakeSubmissionTransport{p, append([]byte(nil), payload...), filename, snapshot, key}}}
	return client.PrepareSubmission(ctx, payload, filename, snapshot, key)
}

func (p *fakeProvider) LookupByOperationKey(context.Context, string) (domain.ProviderTask, bool, error) {
	p.lookupCalls++
	found := p.lookupFound
	if len(p.lookupResults) >= p.lookupCalls {
		found = p.lookupResults[p.lookupCalls-1]
	}
	return p.lookupTask, found, p.lookupErr
}

func (p *fakeProvider) SubmitFileWithSnapshot(_ context.Context, payload []byte, filename string, snapshot domain.PathSnapshot, _ string) (domain.ProviderTask, error) {
	p.fileSubmitCalls++
	p.fileSubmitPayload = append([]byte(nil), payload...)
	p.fileSubmitPayloads = append(p.fileSubmitPayloads, append([]byte(nil), payload...))
	p.fileSubmitFilename = filename
	p.fileSubmitSnapshot = snapshot
	return p.fileSubmitTask, p.fileSubmitErr
}

func (p *fakeProvider) Sync(context.Context, string) (domain.ProviderTask, error) {
	p.syncCalls++
	return p.syncTask, p.syncErr
}

func (p *fakeProvider) ClassifyProviderError(err error) domain.ProviderErrorClass {
	if err == nil {
		return domain.ProviderErrorUnknown
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.ProviderErrorRetryable
	}
	return domain.ProviderErrorAmbiguous
}

type fakeRuntimeConfig struct {
	config qbittorrent.RuntimeConfig
	err    error
	calls  int
}

func (c *fakeRuntimeConfig) GetQBittorrentRuntimeConfig(context.Context) (qbittorrent.RuntimeConfig, error) {
	c.calls++
	return c.config, c.err
}

func openDownloadDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return db
}

// ownerTestDB returns the schema-owner handle for a test-only DDL path.
func ownerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.Owner(t)
}

func testTorrentPayload() []byte {
	return []byte("d4:infod4:name7:QB Test6:lengthi1eee")
}

const testTorrentFilename = "QB Test.torrent"

func seedDownloadTask(t *testing.T, db *sql.DB, taskID, state string, correlation string) {
	seedDownloadTaskWithInput(t, db, taskID, state, correlation, true)
}

func seedDownloadTaskWithInput(t *testing.T, db *sql.DB, taskID, state string, correlation string, withInput bool) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, query := range []string{
		`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config) VALUES ('qb-main', 'qbittorrent', 'QB Main', 'http://qb.example:8080', 'test-secret', 'configured', '{"username":"admin","category":"","save_root":"/downloads","downloader_root":"","user_tags":[]}'::jsonb)`,
		`INSERT INTO managed_anime(id, title) VALUES ('anime-qb-test', 'QB Test')`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-qb-test', 'anime-qb-test', 'custom', 'QB Test', 'active')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ('source-qb-test', 'sub-qb-test', 'custom_rss', 'https://example.test/rss', 'https://example.test/rss', 'Test', 'QB Test')`,
		`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-qb-test')`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code) VALUES ('release-qb-test', 'source-qb-test', 'guid:test', '{}'::jsonb, '{}'::jsonb, 'accepted_pending_download', 'accepted_pending_download')`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
	}
	payload := testTorrentPayload()
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatalf("parse test torrent: %v", err)
	}
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if withInput {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO release_torrent_inputs(
    id, release_id, payload, payload_digest, source_url_digest, filename,
    parser_version, torrent_version, info_hash_v1, info_hash_v2, payload_size_bytes
)
VALUES ('torrent-input-qb-test', 'release-qb-test', $1, $2, 'sha256:test', 'QB Test.torrent',
        'torrent-bencode-v1', $3, NULLIF($4, ''), NULLIF($5, ''), $6)`, payload, digest,
			metadata.TorrentVersion, metadata.InfoHashV1, metadata.InfoHashV2, len(payload)); err != nil {
			t.Fatalf("seed torrent input: %v", err)
		}
	}
	frozenInputRaw, err := json.Marshal(frozenInput{
		ReleaseStableItemKey: "guid:test", SourceLocatorDisplay: "https://example.test/rss",
		TorrentInputID: "torrent-input-qb-test", TorrentPayloadDigest: digest,
		TorrentFilename: testTorrentFilename, TorrentParserVersion: "torrent-bencode-v1",
		TorrentVersion: metadata.TorrentVersion, InfoHash: metadata.InfoHashV1,
		InfoHashV1: metadata.InfoHashV1, InfoHashV2: metadata.InfoHashV2,
	})
	if err != nil {
		t.Fatalf("encode frozen input: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit owner seed: %v", err)
	}
	acquisitionfixture.SeedDownload(t, ownerTestDB(t), "acq-"+taskID, taskID, "release-qb-test", "anime-qb-test", "sub-qb-test", "source-qb-test", "qb:release-qb-test", payload, metadata.InfoHashV1, string(frozenInputRaw))
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE download_tasks SET state=$2,provider_correlation=NULLIF($3,'null')::jsonb WHERE id=$1`, taskID, state, correlation); err != nil {
		t.Fatal(err)
	}
}

func createClaimedQBWorkItem(t *testing.T, db *sql.DB, store *workitems.Store, taskID, kind string) workitems.Claim {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"test":true}`)
	if kind == workitems.KindQBSync {
		var ordinal int
		if err := tx.QueryRowContext(ctx, `SELECT next_poll_ordinal FROM download_tasks WHERE id=$1`, taskID).Scan(&ordinal); err != nil {
			t.Fatal(err)
		}
		input, err = json.Marshal(map[string]any{"test": true, "poll_ordinal": ordinal})
		if err != nil {
			t.Fatal(err)
		}
	}
	id, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerDownloadTask, ID: taskID},
		Kind:                 kind,
		IdempotencyKey:       "qb:release-qb-test",
		InputSnapshot:        input,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("create work item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "test-worker", []string{kind}, time.Minute)
	if err != nil {
		t.Fatalf("claim work item: %v", err)
	}
	return claim
}

// claimNextTypedSync advances the accepted budget clock to the frozen due
// boundary and then claims the persisted successor through the runtime store.
func claimNextTypedSync(t *testing.T, db *sql.DB, store *workitems.Store, task string) workitems.Claim {
	t.Helper()
	ctx := context.Background()
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE download_tasks SET budget_clock_at=greatest(budget_clock_at,(active_poll_ticket->>'started_at')::timestamptz+poll_interval_seconds*INTERVAL '1 second') WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE work_items SET available_at=statement_timestamp()-INTERVAL '1 second' WHERE owner_id=$1 AND kind='qb_sync' AND state='available'`, task); err != nil {
		t.Fatal(err)
	}
	c, err := store.ClaimDue(ctx, "typed-followup", []string{workitems.KindQBSync}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if c.Owner.ID != task {
		t.Fatalf("claimed wrong task %s", c.Owner.ID)
	}
	return c
}

func newDownloadService(t *testing.T, db *sql.DB, provider Provider, config RuntimeConfigSource) (*Service, *workitems.Store) {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, store, provider, config)
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

func TestQBSubmitLookupBeforeAddAndNoDuplicateAdd(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-lookup", "queued", "null")
	provider := &fakeProvider{
		lookupFound: true,
		lookupTask:  domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", State: domain.TaskDownloading, Progress: 0.4},
	}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-lookup", workitems.KindQBSubmit)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || provider.lookupCalls != 1 || provider.fileSubmitCalls != 0 {
		t.Fatalf("lookup-before-add outcome=%+v lookup=%d file_submit=%d", outcome, provider.lookupCalls, provider.fileSubmitCalls)
	}
	var state, connectionID, releaseVerdict string
	if err := db.QueryRowContext(context.Background(), `SELECT state, qbittorrent_connection_id FROM download_tasks WHERE id = 'task-qb-lookup'`).Scan(&state, &connectionID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT verdict FROM releases WHERE id = 'release-qb-test'`).Scan(&releaseVerdict); err != nil {
		t.Fatal(err)
	}
	if state != "downloading" || connectionID != "qb-main" || releaseVerdict != "submitted" {
		t.Fatalf("lookup result state=%q connection=%q release_verdict=%q", state, connectionID, releaseVerdict)
	}
}

func TestQBSubmitCompletedCreatesOneArtifactAndOneInspectWorkItem(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-submit-complete", "queued", "null")
	provider := &fakeProvider{
		lookupFound: true,
		lookupTask:  domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test", State: domain.TaskCompleted, Progress: 1, Path: "/downloads/QB Test.mkv"},
	}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-submit-complete", workitems.KindQBSubmit)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "completed" || provider.fileSubmitCalls != 0 || provider.lookupCalls != 1 {
		t.Fatalf("submit completion outcome=%+v file_submit_calls=%d", outcome, provider.fileSubmitCalls)
	}
	var artifactCount, inspectCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM artifacts WHERE download_task_id = 'task-qb-submit-complete'`).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM work_items WHERE owner_type = 'artifact' AND kind = 'artifact_inspect' AND state IN ('available','leased')`).Scan(&inspectCount); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 1 || inspectCount != 1 {
		t.Fatalf("submit completion created artifact=%d inspect=%d", artifactCount, inspectCount)
	}
}

func TestQBSubmitMissingConnectionReturnsRetryableOwnerOutcome(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-no-config", "queued", "null")
	provider := &fakeProvider{}
	config := &fakeRuntimeConfig{err: errors.New("no usable qBittorrent connection is available")}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-no-config", workitems.KindQBSubmit)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != "qb_connection_unavailable" || outcome.WriteOwner != nil {
		t.Fatalf("missing connection outcome=%+v", outcome)
	}
	if provider.fileSubmitCalls != 0 || config.calls != 1 {
		t.Fatalf("missing connection must not submit: file_submit_calls=%d config_calls=%d", provider.fileSubmitCalls, config.calls)
	}
	var state, code string
	if err := db.QueryRowContext(context.Background(), `SELECT state, failure_code FROM download_tasks WHERE id = 'task-qb-no-config'`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "waiting_retry" || code != "qb_connection_unavailable" {
		t.Fatalf("missing connection owner state=%q code=%q", state, code)
	}
}

func TestQBSyncPathSnapshotMismatchFailsTerminallyAtTheRetryBudget(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-snap-cap", "downloading", `{"external_id":"tag:qb:release-qb-test","state":"downloading","path_snapshot":{"remote_root":"/downloads","application_root":"/moved","path_mappings":[]}}`)
	if _, err := db.ExecContext(ctx, `UPDATE download_tasks SET frozen_input=jsonb_set(frozen_input,'{path_snapshot}','{"remote_root":"/downloads","application_root":"/downloads","path_mappings":[]}'::jsonb) WHERE id='task-qb-snap-cap'`); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	config := &fakeRuntimeConfig{}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-snap-cap", workitems.KindQBSync)
	var limit int
	if err := db.QueryRowContext(ctx, `SELECT max_consecutive_errors FROM download_tasks WHERE id=$1`, claim.Owner.ID).Scan(&limit); err != nil {
		t.Fatal(err)
	}
	for poll := 1; poll <= limit; poll++ {
		outcome, err := service.Handle(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Kind != worker.OutcomeCommitted {
			t.Fatalf("poll %d outcome=%+v", poll, outcome)
		}
		state, code, _, _ := readTaskRow(t, db, claim.Owner.ID)
		if poll < limit {
			if state != "downloading" || code.Valid {
				t.Fatalf("poll %d changed owner: %s %s", poll, state, code.String)
			}
			claim = claimNextTypedSync(t, db, store, claim.Owner.ID)
		} else if state != "failed" || code.String != "SYNC_ERROR_BUDGET_EXHAUSTED" {
			t.Fatalf("budget result %s %s", state, code.String)
		}
	}
	if provider.syncCalls != 0 || provider.lookupCalls != 0 || provider.fileSubmitCalls != 0 || config.calls != 0 {
		t.Fatalf("mismatch touched provider/config")
	}
	if countAvailableQBSyncItems(t, db, claim.Owner.ID) != 0 {
		t.Fatal("budget exhaustion left successor")
	}
}

func TestQBSyncConnectionRetryPreservesConfirmedDownloadState(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-sync-retry", "waiting_retry", `{"external_id":"tag:qb:release-qb-test","state":"downloading","path_snapshot":{"remote_root":"/downloads","application_root":"/downloads","path_mappings":[]}}`)
	provider := &fakeProvider{}
	config := &fakeRuntimeConfig{err: errors.New("no usable qBittorrent connection is available")}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-sync-retry", workitems.KindQBSync)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != "qb_sync_retryable" || outcome.OwnerResult.State != "downloading" {
		t.Fatalf("qB sync connection outcome=%+v", outcome)
	}
	if countAvailableQBSyncItems(t, db, claim.Owner.ID) != 1 {
		t.Fatal("connection error did not schedule successor")
	}
	var state string
	var code sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT state, failure_code FROM download_tasks WHERE id = 'task-qb-sync-retry'`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "downloading" || code.Valid {
		t.Fatalf("qB sync retry changed confirmed task state=%q code=%q", state, code.String)
	}
}

func TestDispatchQBSyncRecoversConfirmedWaitingRetryTask(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-dispatch-retry", "waiting_retry", `{"external_id":"tag:qb:release-qb-test","state":"downloading"}`)
	service, _ := newDownloadService(t, db, &fakeProvider{}, &fakeRuntimeConfig{})
	result, err := service.DispatchQBSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Queued != 1 || result.Merged != 0 || result.Skipped != 0 {
		t.Fatalf("qB sync recovery dispatch=%+v", result)
	}
	var liveCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM work_items WHERE owner_type = 'download_task' AND owner_id = 'task-qb-dispatch-retry' AND kind = 'qb_sync' AND state IN ('available','leased')`).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 1 {
		t.Fatalf("expected one recovery qB sync work item, got %d", liveCount)
	}
}

func TestDispatchQBSyncCreatesAndMergesOneWorkItem(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-dispatch", "downloading", `{"external_id":"tag:qb:release-qb-test"}`)
	service, _ := newDownloadService(t, db, &fakeProvider{}, &fakeRuntimeConfig{})
	first, err := service.DispatchQBSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Scanned != 1 || first.Queued != 1 || first.Merged != 0 || first.Skipped != 0 {
		t.Fatalf("first qB sync dispatch=%+v", first)
	}
	second, err := service.DispatchQBSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Scanned != 1 || second.Queued != 0 || second.Merged != 1 || second.Skipped != 0 {
		t.Fatalf("duplicate qB sync dispatch=%+v", second)
	}
	var liveCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM work_items WHERE owner_type = 'download_task' AND owner_id = 'task-qb-dispatch' AND kind = 'qb_sync' AND state IN ('available','leased')`).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 1 {
		t.Fatalf("expected one live qB sync work item, got %d", liveCount)
	}
}

func TestDispatchQBSyncExcludesRetiredOwner(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-dispatch-retired", "downloading", `{"external_id":"tag:qb:release-qb-test"}`)
	if _, err := db.ExecContext(context.Background(), `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
    retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1,
    lifecycle_version = lifecycle_version + 1
WHERE id = 'anime-qb-test'`); err != nil {
		t.Fatal(err)
	}
	service, _ := newDownloadService(t, db, &fakeProvider{}, &fakeRuntimeConfig{})
	result, err := service.DispatchQBSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 0 || result.Queued != 0 || result.Merged != 0 || result.Skipped != 0 {
		t.Fatalf("retired qB sync dispatch=%+v, want empty", result)
	}
	var liveCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM work_items WHERE owner_type = 'download_task' AND owner_id = 'task-qb-dispatch-retired' AND kind = 'qb_sync' AND state IN ('available','leased')`).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 0 {
		t.Fatalf("retired owner gained %d live qB sync work items", liveCount)
	}
}

func TestDispatchQBSyncExcludesOneShotAndPreservesOrdinaryOwner(t *testing.T) {
	db := openDownloadDB(t)
	ctx := context.Background()
	ordinaryID := "task-qb-dispatch-ordinary"
	oneShotID := "task-qb-dispatch-oneshot"
	seedDownloadTask(t, db, ordinaryID, "downloading", `{"external_id":"tag:qb:release-qb-test"}`)

	// Build a second independent owner graph without changing the shared test
	// seed helper's fixed release/source identities.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-qb-dispatch-oneshot', 'QB One Shot')`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-qb-dispatch-oneshot', 'anime-qb-dispatch-oneshot', 'oneshot', 'QB One Shot', 'stopped')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ('source-qb-dispatch-oneshot', 'sub-qb-dispatch-oneshot', 'custom_rss', 'https://example.test/one-shot', 'https://example.test/one-shot', 'Test', 'QB One Shot')`,
		`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('source-qb-dispatch-oneshot')`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code) VALUES ('release-qb-dispatch-oneshot', 'source-qb-dispatch-oneshot', 'guid:one-shot', '{}'::jsonb, '{}'::jsonb, 'accepted_pending_download', 'accepted_pending_download')`,
	} {
		_, execErr := tx.ExecContext(ctx, query)
		if execErr != nil {
			tx.Rollback()
			t.Fatal(execErr)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	metadata, err := torrent.Parse(testTorrentPayload())
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedDownload(t, ownerTestDB(t), "acq-qb-dispatch-oneshot", oneShotID, "release-qb-dispatch-oneshot", "anime-qb-dispatch-oneshot", "sub-qb-dispatch-oneshot", "source-qb-dispatch-oneshot", "qb:release-qb-dispatch-oneshot", testTorrentPayload(), metadata.InfoHashV1, `{"torrent_input_id":"torrent-input-qb-test","torrent_payload_digest":"sha256:test"}`)
	if _, err := db.ExecContext(ctx, `UPDATE download_tasks SET state='downloading',provider_correlation='{"external_id":"tag:qb:one-shot"}'::jsonb WHERE id=$1`, oneShotID); err != nil {
		t.Fatal(err)
	}
	seedOneShotMarker(t, db, "marker-"+oneShotID, oneShotID)

	service, store := newDownloadService(t, db, &fakeProvider{}, &fakeRuntimeConfig{})
	first, err := service.DispatchQBSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Scanned != 1 || first.Queued != 1 || first.Merged != 0 || first.Skipped != 0 {
		t.Fatalf("dispatcher isolation result=%+v, want only ordinary task queued", first)
	}
	if countAvailableQBSyncItems(t, db, oneShotID) != 0 {
		t.Fatal("ordinary dispatcher created a live qb_sync for one-shot task")
	}
	var ordinaryWorkItemID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_sync' AND state IN ('available','leased')`, ordinaryID).Scan(&ordinaryWorkItemID); err != nil {
		t.Fatal(err)
	}
	second, err := service.DispatchQBSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Scanned != 1 || second.Queued != 0 || second.Merged != 1 || second.Skipped != 0 {
		t.Fatalf("dispatcher isolation merge=%+v", second)
	}
	var mergedWorkItemID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE owner_type = 'download_task' AND owner_id = $1 AND kind = 'qb_sync' AND state IN ('available','leased')`, ordinaryID).Scan(&mergedWorkItemID); err != nil {
		t.Fatal(err)
	}
	if mergedWorkItemID != ordinaryWorkItemID || countAvailableQBSyncItems(t, db, oneShotID) != 0 {
		t.Fatalf("dispatcher changed owner isolation: ordinary=%q/%q one_shot=%d", ordinaryWorkItemID, mergedWorkItemID, countAvailableQBSyncItems(t, db, oneShotID))
	}
	_ = store
}
func TestQBSubmitMissingTorrentInputReturnsTerminalOwnerOutcome(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTaskWithInput(t, db, "task-qb-missing-input", "queued", "null", false)
	provider := &fakeProvider{}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-missing-input", workitems.KindQBSubmit)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "failed" || outcome.OwnerResult.ReasonCode != "torrent_payload_missing" || outcome.WriteOwner != nil {
		t.Fatalf("missing torrent input outcome=%+v", outcome)
	}
	if provider.fileSubmitCalls != 0 || provider.lookupCalls != 0 || config.calls != 0 {
		t.Fatalf("missing torrent input crossed effect boundary: add=%d lookup=%d config=%d", provider.fileSubmitCalls, provider.lookupCalls, config.calls)
	}
	var state, code string
	if err := db.QueryRowContext(context.Background(), `SELECT state, failure_code FROM download_tasks WHERE id = 'task-qb-missing-input'`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != "torrent_payload_missing" {
		t.Fatalf("missing torrent input owner state=%q code=%q", state, code)
	}
}

func TestLoadTorrentInputRejectsFrozenPayloadIdentityMismatch(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-identity", "queued", "null")
	var raw []byte
	if err := db.QueryRowContext(context.Background(), `SELECT frozen_input FROM download_tasks WHERE id = 'task-qb-identity'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	input, err := decodeFrozenInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := newDownloadService(t, db, &fakeProvider{}, &fakeRuntimeConfig{})
	task := taskSnapshot{releaseID: "release-qb-test"}
	tests := []struct {
		name   string
		mutate func(*frozenInput)
	}{
		{name: "digest", mutate: func(value *frozenInput) { value.TorrentPayloadDigest = "sha256:" + strings.Repeat("0", 64) }},
		{name: "filename", mutate: func(value *frozenInput) { value.TorrentFilename = "other.torrent" }},
		{name: "parser version", mutate: func(value *frozenInput) { value.TorrentParserVersion = "other-parser" }},
		{name: "torrent version", mutate: func(value *frozenInput) { value.TorrentVersion = "v2" }},
		{name: "v1 hash", mutate: func(value *frozenInput) { value.InfoHashV1 = strings.Repeat("1", 40) }},
		{name: "v2 hash", mutate: func(value *frozenInput) { value.InfoHashV2 = strings.Repeat("2", 64) }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			changed := input
			testCase.mutate(&changed)
			if _, err := service.loadTorrentInput(context.Background(), task, changed); err == nil {
				t.Fatal("mismatched frozen payload identity was accepted")
			}
		})
	}
}

func TestDecodeFrozenInputAcceptsTargetSetDigest(t *testing.T) {
	raw := []byte(`{
        "release_stable_item_key":"stable",
        "target_set_digest":"sha256:target-set",
        "source_locator_display":"https://feed.example.test/rss",
        "torrent_input_id":"torrent-input",
        "torrent_payload_digest":"sha256:payload",
        "torrent_filename":"episode.torrent",
        "torrent_parser_version":"torrent-bencode-v1",
        "torrent_version":"v1"
    }`)
	input, err := decodeFrozenInput(raw)
	if err != nil {
		t.Fatalf("target-set digest should be accepted as frozen input: %v", err)
	}
	if input.TargetSetDigest != "sha256:target-set" {
		t.Fatalf("target-set digest was not preserved: %q", input.TargetSetDigest)
	}
}

func TestQBSubmitTimeoutReturnsRetryWithRedactedReason(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-timeout", "queued", "null")
	provider := &fakeProvider{fileSubmitErr: context.DeadlineExceeded}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-timeout", workitems.KindQBSubmit)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Disposition != workitems.DispositionAmbiguous || outcome.WriteOwner != nil {
		t.Fatalf("timeout outcome=%+v", outcome)
	}
	var effect, reason string
	if err := db.QueryRowContext(context.Background(), `SELECT submit_effect_class,submit_observation->>'error' FROM download_tasks WHERE submit_work_item_id=$1`, claim.ID).Scan(&effect, &reason); err != nil {
		t.Fatal(err)
	}
	if effect != "unknown" || reason == "" || reason == context.DeadlineExceeded.Error() || len(reason) > maxReasonLength {
		t.Fatalf("unsafe timeout receipt: effect=%s reason=%q", effect, reason)
	}
	if provider.fileSubmitCalls != 1 || len(provider.fileSubmitPayloads) != 1 || !bytes.Equal(provider.fileSubmitPayloads[0], testTorrentPayload()) {
		t.Fatalf("first timeout attempt did not use exactly the frozen payload: calls=%d payloads=%q", provider.fileSubmitCalls, provider.fileSubmitPayloads)
	}

	provider.fileSubmitErr = nil
	provider.lookupFound = true
	provider.lookupTask = domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: strings.Repeat("a", 40), State: domain.TaskDownloading}
	if _, err := ownerTestDB(t).ExecContext(context.Background(), `UPDATE work_items SET recovery_blocked=true,recovery_reason='effect_unknown',lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id = $1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	retryClaim, err := store.ClaimRecovery(context.Background(), []string{workitems.KindQBSubmit}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := service.SubmitHandler().(worker.RecoveryObserver).ObserveRecovery(context.Background(), retryClaim)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Kind != worker.OutcomeCommitted || recovered.OwnerResult.State != "downloading" {
		t.Fatalf("accepted-before-timeout recovery outcome=%+v", recovered)
	}
	if provider.lookupCalls != 2 || provider.fileSubmitCalls != 1 || len(provider.fileSubmitPayloads) != 1 {
		t.Fatalf("retry duplicated add: lookups=%d adds=%d payloads=%d", provider.lookupCalls, provider.fileSubmitCalls, len(provider.fileSubmitPayloads))
	}
}

func TestQBSubmitRetryWithoutProviderEvidenceReusesExactFrozenPayload(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-retry-payload", "queued", "null")
	provider := &fakeProvider{fileSubmitErr: context.DeadlineExceeded}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-retry-payload", workitems.KindQBSubmit)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil || outcome.Disposition != workitems.DispositionAmbiguous {
		t.Fatalf("first submit outcome=%+v err=%v", outcome, err)
	}
	if _, err := ownerTestDB(t).ExecContext(context.Background(), `UPDATE work_items SET recovery_blocked=true,recovery_reason='effect_unknown',lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	retryClaim, err := store.ClaimRecovery(context.Background(), []string{workitems.KindQBSubmit}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	retryOutcome, err := service.SubmitHandler().(worker.RecoveryObserver).ObserveRecovery(context.Background(), retryClaim)
	if err != nil || retryOutcome.Disposition != workitems.DispositionAmbiguous {
		t.Fatalf("reconcile outcome=%+v err=%v", retryOutcome, err)
	}
	if provider.lookupCalls != 2 || provider.fileSubmitCalls != 1 || len(provider.fileSubmitPayloads) != 1 || !bytes.Equal(provider.fileSubmitPayloads[0], testTorrentPayload()) {
		t.Fatalf("unknown mutation duplicated or changed payload: lookups=%d adds=%d payloads=%q", provider.lookupCalls, provider.fileSubmitCalls, provider.fileSubmitPayloads)
	}
}

func TestQBSyncCompletionCreatesOneArtifactAndOneInspectWorkItem(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-sync", "downloading", `{"external_id":"tag:qb:release-qb-test","stable_tag":"qb:release-qb-test"}`)
	provider := &fakeProvider{syncTask: domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", Name: "QB Test", State: domain.TaskCompleted, Progress: 1, Path: "/downloads/QB Test.mkv"}}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-sync", workitems.KindQBSync)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "completed" {
		t.Fatalf("sync completion outcome=%+v", outcome)
	}
	var workState string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM work_item_history WHERE id=$1`, claim.ID).Scan(&workState); err != nil {
		t.Fatal(err)
	}
	if workState != workitems.StateSucceeded {
		t.Fatalf("committed work state=%s", workState)
	}
	var artifactCount, inspectCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM artifacts WHERE download_task_id = 'task-qb-sync'`).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM work_items WHERE owner_type = 'artifact' AND kind = 'artifact_inspect' AND state IN ('available','leased')`).Scan(&inspectCount); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 1 || inspectCount != 1 {
		t.Fatalf("completion created artifact=%d inspect=%d", artifactCount, inspectCount)
	}
}

func TestTaskStateFromProvider(t *testing.T) {
	tests := []struct {
		providerState domain.TaskState
		want          string
	}{
		{providerState: domain.TaskQueued, want: "submitting"},
		{providerState: domain.TaskSubmitted, want: "submitting"},
		{providerState: domain.TaskDownloading, want: "downloading"},
		{providerState: domain.TaskCompleted, want: "completed"},
		{providerState: domain.TaskFailed, want: "failed"},
		{providerState: domain.TaskCanceled, want: "submitting"},
	}
	for _, testCase := range tests {
		if got := taskStateFromProvider(testCase.providerState); got != testCase.want {
			t.Errorf("taskStateFromProvider(%q) = %q, want %q", testCase.providerState, got, testCase.want)
		}
	}
}

func TestFrozenInfoHashCandidatesPreserveFallbackOrderAndDeduplicate(t *testing.T) {
	v1 := strings.Repeat("1", 40)
	v2 := strings.Repeat("2", 64)
	got := frozenInfoHashCandidates(frozenInput{
		InfoHash:   " " + v1 + " ",
		InfoHashV1: v1,
		InfoHashV2: v2,
	})
	want := []string{v1, v2, v2[:40]}
	if len(got) != len(want) {
		t.Fatalf("frozen infohash candidates = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("frozen infohash candidates = %v, want %v", got, want)
		}
	}
}

func TestProviderCorrelationEncodingIsBoundedAndStable(t *testing.T) {
	raw, correlation, err := encodeCorrelation(domain.ProviderTask{
		ID:       " tag:stable-operation ",
		Name:     strings.Repeat("n", 600),
		Path:     strings.Repeat("p", 5000),
		Progress: 2,
		Files:    []domain.ProviderFile{{Path: "/downloads/show.mkv"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if correlation.ExternalID != "tag:stable-operation" || correlation.StableTag != "stable-operation" {
		t.Fatalf("unexpected stable correlation: %+v", correlation)
	}
	if len(correlation.Name) != 512 || correlation.ArtifactPath != "/downloads/show.mkv" || correlation.Progress != 1 || correlation.FileCount != 1 || !correlation.FilesComplete {
		t.Fatalf("correlation bounds or primary file facts were not applied: %+v", correlation)
	}
	if strings.Contains(string(raw), "download_url") || strings.Contains(string(raw), "source_url") {
		t.Fatalf("provider correlation must not contain source URL fields: %s", raw)
	}
	if _, _, err := encodeCorrelation(domain.ProviderTask{}); !errors.Is(err, ErrProviderCorrelation) {
		t.Fatalf("empty provider ID must be rejected, got %v", err)
	}
	if correlation.DownloadSpeed != nil || strings.Contains(string(raw), "download_speed") {
		t.Fatalf("an observation without a speed records none: %s", raw)
	}
	speed := int64(4096)
	raw, correlation, err = encodeCorrelation(domain.ProviderTask{ID: "tag:speed", DownloadSpeed: &speed})
	if err != nil || correlation.DownloadSpeed == nil || *correlation.DownloadSpeed != speed || !strings.Contains(string(raw), `"download_speed":4096`) {
		t.Fatalf("the observed speed must be persisted for the record display: %s %v", raw, err)
	}
}

func TestProviderCorrelationEncodingPreservesCompleteMultiFileSnapshot(t *testing.T) {
	primary := "/downloads/show/show.mkv"
	subtitle := "/downloads/show/show.zh.ass"
	raw, correlation, err := encodeCorrelation(domain.ProviderTask{
		ID:       "tag:multi-file",
		Path:     primary,
		State:    domain.TaskCompleted,
		Progress: 1,
		Files: []domain.ProviderFile{
			{Path: primary, SizeBytes: 100, MediaKind: "video", Progress: 1},
			{Path: subtitle, SizeBytes: 20, MediaKind: "subtitle", Language: "zh", Progress: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !correlation.FilesComplete || correlation.FileCount != 2 || len(correlation.Files) != 2 {
		t.Fatalf("multi-file correlation is incomplete: %+v", correlation)
	}
	if correlation.Files[0].Path != primary || correlation.Files[1].Path != subtitle || correlation.Files[1].Language != "zh" {
		t.Fatalf("multi-file snapshot lost provider facts: %+v", correlation.Files)
	}
	if len(raw) == 0 || strings.Contains(string(raw), "https://") || strings.Contains(string(raw), "token=") {
		t.Fatalf("multi-file correlation contains unsafe data: %s", raw)
	}

	_, pathsCorrelation, err := encodeCorrelation(domain.ProviderTask{
		ID:    "tag:paths-fallback",
		Paths: []string{primary, subtitle},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pathsCorrelation.FilesComplete || pathsCorrelation.FileCount != 2 || len(pathsCorrelation.Files) != 2 {
		t.Fatalf("Paths fallback should be bounded but incomplete: %+v", pathsCorrelation)
	}

	_, primaryCorrelation, err := encodeCorrelation(domain.ProviderTask{ID: "tag:primary-fallback", Path: primary})
	if err != nil {
		t.Fatal(err)
	}
	if primaryCorrelation.FilesComplete || primaryCorrelation.FileCount != 1 || len(primaryCorrelation.Files) != 1 || primaryCorrelation.Files[0].Path != primary {
		t.Fatalf("primary fallback was not retained: %+v", primaryCorrelation)
	}
}

func TestProviderCorrelationPresenceRejectsNullAndInvalidPayloads(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "empty", raw: "", want: false},
		{name: "null", raw: " null ", want: false},
		{name: "empty object", raw: `{}`, want: false},
		{name: "valid", raw: `{"external_id":"tag:stable"}`, want: true},
		{name: "invalid", raw: `{not-json}`, want: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := hasProviderCorrelation([]byte(testCase.raw)); got != testCase.want {
				t.Fatalf("hasProviderCorrelation(%q) = %t, want %t", testCase.raw, got, testCase.want)
			}
		})
	}
}

func TestProviderErrorReasonIsRedacted(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "timeout", err: errors.New("request timeout for https://admin:secret@example.test/private"), want: "timed out"},
		{name: "connection", err: errors.New("connection refused with credentials=secret"), want: "unavailable"},
		{name: "not found", err: errors.New("qB task was not found at /private/downloads"), want: "not found"},
		{name: "generic", err: errors.New("provider response contained private payload"), want: "failed"},
		{name: "nil", err: nil, want: "failed"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			reason := redactProviderError(testCase.err)
			if !strings.Contains(reason, testCase.want) {
				t.Fatalf("redacted reason %q does not contain %q", reason, testCase.want)
			}
			if strings.Contains(reason, "example.test") || strings.Contains(reason, "secret") || strings.Contains(reason, "/private") {
				t.Fatalf("provider reason leaked sensitive detail: %q", reason)
			}
		})
	}
}

func TestProviderConnectionReasonIsRedacted(t *testing.T) {
	reason := redactProviderError(errors.New("missing qB connection credentials"))
	if strings.Contains(reason, "credentials") || strings.Contains(reason, "missing") {
		t.Fatalf("connection error was not redacted: %q", reason)
	}
}

func TestQBSubmitFailureOpensAndSuccessClosesDiagnosticEpisode(t *testing.T) {
	db := openDownloadDB(t)
	ctx := context.Background()
	seedDownloadTaskWithInput(t, db, "task-qb-diag", "queued", "null", false)
	provider := &fakeProvider{
		fileSubmitTask: domain.ProviderTask{ID: "tag:qb:release-qb-test", Hash: "abc", State: domain.TaskDownloading, Progress: 0.1},
	}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	recorder, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetDiagnosticRecorder(recorder, nil)

	// A terminal submit failure opens one folded diagnostic episode.
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-diag", workitems.KindQBSubmit)
	outcome, err := service.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "failed" {
		t.Fatalf("expected terminal submit failure: %+v", outcome)
	}
	if outcome.AfterCommit == nil {
		t.Fatal("terminal submit failure must carry the diagnostic episode after-commit work")
	}
	outcome.AfterCommit(ctx)

	var component, operation, errorCode, scopeKind, scopeID string
	var succeededAfter sql.NullTime
	if err := db.QueryRowContext(ctx, `
SELECT component, operation, error_code, scope_kind, scope_id, succeeded_after_at
FROM diagnostic_episodes`).Scan(&component, &operation, &errorCode, &scopeKind, &scopeID, &succeededAfter); err != nil {
		t.Fatalf("read diagnostic episode: %v", err)
	}
	if component != "download" || operation != "qb_submit" || errorCode != "qb_submit_failed" ||
		scopeKind != "owner" || scopeID != "release-qb-test" || succeededAfter.Valid {
		t.Fatalf("unexpected open episode: %s/%s/%s scope=%s/%s succeeded=%v",
			component, operation, errorCode, scopeKind, scopeID, succeededAfter)
	}

	// Restore the frozen input, retry the submit successfully, and the open
	// episode closes for the same identity.
	payload := testTorrentPayload()
	metadata, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if _, err := db.ExecContext(ctx, `
INSERT INTO release_torrent_inputs(
    id, release_id, payload, payload_digest, source_url_digest, filename,
    parser_version, torrent_version, info_hash_v1, info_hash_v2, payload_size_bytes
)
VALUES ('torrent-input-qb-test', 'release-qb-test', $1, $2, 'sha256:test', 'QB Test.torrent',
        'torrent-bencode-v1', $3, NULLIF($4, ''), NULLIF($5, ''), $6)`, payload,
		"sha256:"+hex.EncodeToString(sum[:]), metadata.TorrentVersion, metadata.InfoHashV1, metadata.InfoHashV2, len(payload)); err != nil {
		t.Fatal(err)
	}
	var frozen string
	if err := db.QueryRowContext(ctx, `SELECT frozen_input FROM download_tasks WHERE id='task-qb-diag'`).Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	// A distinct release/task succeeds within the same diagnostic scope.
	if _, err := ownerTestDB(t).ExecContext(ctx, `INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES ('release-qb-diag-success','source-qb-test','guid:diag-success','{}','{}','accepted_pending_download','accepted_pending_download')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `INSERT INTO release_torrent_inputs(id,release_id,payload,payload_digest,source_url_digest,filename,parser_version,torrent_version,info_hash_v1,info_hash_v2,payload_size_bytes) SELECT 'torrent-input-diag-success','release-qb-diag-success',payload,payload_digest,source_url_digest,filename,parser_version,torrent_version,info_hash_v1,info_hash_v2,payload_size_bytes FROM release_torrent_inputs WHERE id='torrent-input-qb-test'`); err != nil {
		t.Fatal(err)
	}
	frozen = strings.ReplaceAll(frozen, "torrent-input-qb-test", "torrent-input-diag-success")
	acquisitionfixture.SeedDownload(t, ownerTestDB(t), "acq-task-qb-diag-success", "task-qb-diag-success", "release-qb-diag-success", "anime-qb-test", "sub-qb-test", "source-qb-test", "qb:release-qb-diag-success", payload, metadata.InfoHashV1, frozen)
	observabilityport.DiagnosticFailureAfterCommit(recorder, nil, observabilityport.DiagnosticFailureRequest{Identity: observabilityport.DownloadSubmitFailure(), Severity: observabilityport.SeverityError, ScopeID: "release-qb-diag-success", LastError: "fixture prior failure"})(ctx)
	var ownerVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM download_tasks WHERE id = 'task-qb-diag-success'`).Scan(&ownerVersion); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	retryID, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerDownloadTask, ID: "task-qb-diag-success"},
		Kind:  workitems.KindQBSubmit, IdempotencyKey: "qb:release-qb-diag-success",
		InputSnapshot: []byte(`{"test":true}`), MaxAttempts: 3, ExpectedOwnerVersion: ownerVersion,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("create retry submit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE work_items SET available_at = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, retryID); err != nil {
		t.Fatal(err)
	}
	retryClaim, err := store.ClaimDue(ctx, "test-worker", []string{workitems.KindQBSubmit}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	retryOutcome, err := service.Handle(ctx, retryClaim)
	if err != nil {
		t.Fatal(err)
	}
	if retryOutcome.Kind != worker.OutcomeCommitted || retryOutcome.OwnerResult.State != "submitting" {
		t.Fatalf("expected successful retry submit: %+v", retryOutcome)
	}
	if retryOutcome.AfterCommit == nil {
		t.Fatal("successful submit must carry the diagnostic close after-commit work")
	}
	retryOutcome.AfterCommit(ctx)

	if err := db.QueryRowContext(ctx, `SELECT succeeded_after_at FROM diagnostic_episodes WHERE scope_id='release-qb-diag-success'`).Scan(&succeededAfter); err != nil {
		t.Fatal(err)
	}
	if !succeededAfter.Valid {
		t.Fatal("successful submit must close the open diagnostic episode")
	}
}
