package persistence

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

// seedDiscoveryOwner writes one managed anime, subscription and RSS source and
// returns the source id. The lifecycle is left active; the test moves it.
func seedDiscoveryOwner(t *testing.T, db *sql.DB, anime, subscription, source string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Discovery Show ' || $1)`, anime); err != nil {
		t.Fatalf("seed managed anime: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ($1, $2, 'custom', 'Discovery', 'active')`, subscription, anime); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', 'https://feed.example.test/' || $1, 'https://feed.example.test/', 'Group', 'Discovery Show')`, source, subscription); err != nil {
		t.Fatalf("seed rss source: %v", err)
	}
}

func seedDiscoveryRelease(t *testing.T, db *sql.DB, release, source, key string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ($1, $2, $3, '{"title":"[Group] Discovery Show - 01"}', '{}', 'discovered', 'discovered')`, release, source, key); err != nil {
		t.Fatalf("seed release %s: %v", release, err)
	}
}

// stopSubscription moves the subscription to stopped with the given clock age.
func stopSubscription(t *testing.T, db *sql.DB, subscription string, age string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
UPDATE subscriptions SET state = 'stopped', updated_at = CURRENT_TIMESTAMP - $2::interval
WHERE id = $1`, subscription, age); err != nil {
		t.Fatalf("stop subscription %s: %v", subscription, err)
	}
}

// retireAnime moves the managed anime to retired with the given clock age. It
// is the last write to that owner: the lifecycle guard freezes retired rows.
func retireAnime(t *testing.T, db *sql.DB, anime, age string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
    retired_at = CURRENT_TIMESTAMP - $2::interval, retired_subscription_version = 1,
    lifecycle_version = lifecycle_version + 1
WHERE id = $1`, anime, age); err != nil {
		t.Fatalf("retire managed anime %s: %v", anime, err)
	}
}

func releaseExists(t *testing.T, db *sql.DB, release string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM releases WHERE id = $1)`, release).Scan(&exists); err != nil {
		t.Fatalf("read release %s: %v", release, err)
	}
	return exists
}

func purgeOnce(t *testing.T, repository *Repository, db *sql.DB, batchSize int) int64 {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin purge transaction: %v", err)
	}
	defer tx.Rollback()
	deleted, err := repository.PurgeExpiredDiscoveryReleases(context.Background(), txscope.Wrap(tx), batchSize)
	if err != nil {
		t.Fatalf("purge discovery releases: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit purge transaction: %v", err)
	}
	return deleted
}

// TestPurgeExpiredDiscoveryReleasesEligibilityAndGraph is the eligibility
// contract: only a release that never reached a download task and whose owner
// has been stopped or retired for 30 days is deleted, and everything that hangs
// off it goes with it. Active or recently inactive owners keep everything, as
// do a release still named by an effect, an execution root, a live one-shot
// marker or an unexpired run-log row.
func TestPurgeExpiredDiscoveryReleasesEligibilityAndGraph(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	ctx := context.Background()
	repository := NewRepository(db)

	// Old stop, no dependents: eligible.
	seedDiscoveryOwner(t, owner, "anime-old-stop", "sub-old-stop", "source-old-stop")
	stopSubscription(t, owner, "sub-old-stop", "31 days")
	seedDiscoveryRelease(t, owner, "release-old-stop", "source-old-stop", "guid:old-stop")

	// Old stop with the full discovery graph: every dependent goes first.
	seedDiscoveryOwner(t, owner, "anime-graph", "sub-graph", "source-graph")
	stopSubscription(t, owner, "sub-graph", "31 days")
	seedDiscoveryRelease(t, owner, "release-graph", "source-graph", "guid:graph")
	if _, err := owner.ExecContext(ctx, `
INSERT INTO release_torrent_inputs(id, release_id, payload, payload_digest, source_url_digest, filename, parser_version, torrent_version, payload_size_bytes)
VALUES ('input-graph', 'release-graph', decode('00', 'hex'), 'sha256:input', 'sha256:url', 'a.torrent', 'parser-v1', 'v1', 1)`); err != nil {
		t.Fatalf("seed torrent input: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO release_candidate_facts(release_id, release_version, parser_version, input_fingerprint, raw_evidence, facts, coverage_keys)
VALUES ('release-graph', 1, 'parser-v1', 'fingerprint-graph', '{}', '{}', '["episode:1"]')`); err != nil {
		t.Fatalf("seed candidate facts: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snapshot-graph', 'release-graph', 'v1', 'resolver-v1', 'regex', 'digest', '{}', '{}')`); err != nil {
		t.Fatalf("seed media snapshot: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, file_evidence, effective_facts)
VALUES ('snapshot-graph', 'file-1', 'video', 1, '{}', '{}')`); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO acquisition_targets(id, release_id, target_ref, source_path, media_kind, target_path, expected_size_bytes, intent, collision_policy, render_snapshot, config_snapshot, spec_digest)
VALUES ('target-graph', 'release-graph', 'file-1', 'file-1.mkv', 'video', '/library/Discovery Show/file-1.mkv', 1, 'ensure_present', 'block', '{}', '{}', 'spec-digest')`); err != nil {
		t.Fatalf("seed acquisition target: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, release_id, result_snapshot)
VALUES ('release_retry', 'retry-graph', 'sha256:0000000000000000000000000000000000000000000000000000000000000000', 'release-graph', '{}')`); err != nil {
		t.Fatalf("seed release retry idempotency record: %v", err)
	}

	// Old stop, retained by an unexpired run-log row.
	seedDiscoveryOwner(t, owner, "anime-logged", "sub-logged", "source-logged")
	stopSubscription(t, owner, "sub-logged", "31 days")
	seedDiscoveryRelease(t, owner, "release-logged", "source-logged", "guid:logged")
	if _, err := owner.ExecContext(ctx, `
INSERT INTO subscription_log_entries(id, subscription_id, managed_anime_id, kind, outcome, message_code, message_params, release_id, dedupe_key)
VALUES ('log-logged', 'sub-logged', 'anime-logged', 'release_processed', 'info', 'release_discovered', '{}', 'release-logged', 'logged')`); err != nil {
		t.Fatalf("seed subscription log: %v", err)
	}

	// Old stop, retained by a live one-shot marker.
	seedDiscoveryOwner(t, owner, "anime-marker", "sub-marker", "source-marker")
	stopSubscription(t, owner, "sub-marker", "31 days")
	seedDiscoveryRelease(t, owner, "release-marker", "source-marker", "guid:marker")
	if _, err := owner.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(id, managed_anime_id, subscription_id, rss_source_id, release_id, correlation_id,
    request_idempotency_key, request_hash, result_snapshot, state, terminal_outcome, terminal_reason_code,
    terminal_reason, terminal_at, retention_until)
VALUES ('marker-live', 'anime-marker', 'sub-marker', 'source-marker', 'release-marker', 'corr-marker',
    'request-marker', 'sha256:0000000000000000000000000000000000000000000000000000000000000000', '{}',
    'reclaim_pending', 'succeeded', 'one_shot_completed', 'completed', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days')`); err != nil {
		t.Fatalf("seed one-shot marker: %v", err)
	}

	// Old stop, retained by a cleaned one-shot marker is eligible: the marker
	// no longer owns the release.
	seedDiscoveryOwner(t, owner, "anime-cleaned", "sub-cleaned", "source-cleaned")
	stopSubscription(t, owner, "sub-cleaned", "31 days")
	seedDiscoveryRelease(t, owner, "release-cleaned", "source-cleaned", "guid:cleaned")
	if _, err := owner.ExecContext(ctx, `
INSERT INTO one_shot_acquisitions(id, managed_anime_id, subscription_id, rss_source_id, release_id, correlation_id,
    request_idempotency_key, request_hash, result_snapshot, state, terminal_outcome, terminal_reason_code,
    terminal_reason, terminal_at, retention_until)
VALUES ('marker-cleaned', 'anime-cleaned', 'sub-cleaned', 'source-cleaned', 'release-cleaned', 'corr-cleaned',
    'request-cleaned', 'sha256:0000000000000000000000000000000000000000000000000000000000000000', '{}',
    'cleaned', 'succeeded', 'one_shot_completed', 'completed', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days')`); err != nil {
		t.Fatalf("seed cleaned one-shot marker: %v", err)
	}

	// Retired old: eligible through the terminal retirement clock.
	seedDiscoveryOwner(t, owner, "anime-retired", "sub-retired", "source-retired")
	seedDiscoveryRelease(t, owner, "release-retired", "source-retired", "guid:retired")
	retireAnime(t, owner, "anime-retired", "31 days")

	// Retired recently: inside the window.
	seedDiscoveryOwner(t, owner, "anime-retired-new", "sub-retired-new", "source-retired-new")
	seedDiscoveryRelease(t, owner, "release-retired-new", "source-retired-new", "guid:retired-new")
	retireAnime(t, owner, "anime-retired-new", "1 day")

	// Stopped recently: inside the window.
	seedDiscoveryOwner(t, owner, "anime-stopped-new", "sub-stopped-new", "source-stopped-new")
	stopSubscription(t, owner, "sub-stopped-new", "1 day")
	seedDiscoveryRelease(t, owner, "release-stopped-new", "source-stopped-new", "guid:stopped-new")

	// Active owner, old release: never eligible.
	seedDiscoveryOwner(t, owner, "anime-active", "sub-active", "source-active")
	seedDiscoveryRelease(t, owner, "release-active", "source-active", "guid:active")
	if _, err := owner.ExecContext(ctx, `UPDATE releases SET discovered_at = CURRENT_TIMESTAMP - INTERVAL '400 days' WHERE id = 'release-active'`); err != nil {
		t.Fatalf("age active release: %v", err)
	}

	// A placement still names the release as its quality facts; it is library
	// evidence and keeps the release even for an inactive owner.
	seedDiscoveryOwner(t, owner, "anime-placed", "sub-placed", "source-placed")
	stopSubscription(t, owner, "sub-placed", "31 days")
	seedDiscoveryRelease(t, owner, "release-placed", "source-placed", "guid:placed")
	if _, err := owner.ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint,
    target_fingerprint, quality_facts_ref, facts_version)
VALUES ('placement-placed', '/library/Discovery Show/Placed Show - 01.mkv', 'anime-placed', 'file_operation',
    'operation-placed', 'sha256:source', 'sha256:target', 'release-placed', 'facts-v1')`); err != nil {
		t.Fatalf("seed placement: %v", err)
	}

	// A stopped subscription re-saved while stopped has a fresh clock.
	seedDiscoveryOwner(t, owner, "anime-resaved", "sub-resaved", "source-resaved")
	seedDiscoveryRelease(t, owner, "release-resaved", "source-resaved", "guid:resaved")
	stopSubscription(t, owner, "sub-resaved", "31 days")
	if _, err := owner.ExecContext(ctx, `UPDATE subscriptions SET state = 'stopped', updated_at = CURRENT_TIMESTAMP WHERE id = 'sub-resaved'`); err != nil {
		t.Fatalf("re-save stopped subscription: %v", err)
	}

	// Acquisition evidence named by an execution root: a running root keeps its
	// release even when the release's own task pointer is already detached. The
	// root is written with the schema's own canonical-input digest rule so the
	// row is the shape the accept path produces.
	seedDiscoveryOwner(t, owner, "anime-acquired", "sub-acquired", "source-acquired")
	seedDiscoveryRelease(t, owner, "release-acquired", "source-acquired", "guid:acquired")
	if _, err := owner.ExecContext(ctx, `
INSERT INTO acquisitions(id, execution_branch, entry_kind, access_scope, request_namespace, request_key, request_hash,
    canonical_request, admission_kind, admission_http_status, admission_receipt, input_schema_version, admission_digest,
    origin_managed_anime_ref, source_release_ref, display_title, accepted_at, retention_anchor_at,
    detail_retention_seconds, receipt_after_accept_seconds, receipt_after_terminal_seconds, receipt_until,
    state, record_state, preparation_mode, input_canonical_utf8, payload_digest, manifest_digest,
    manifest_item_count, manifest_sealed_at)
SELECT 'root-acquired', 'download', 'subscription_add', 'owner', 'manual_acquire_v1', 'request-acquired',
    'sha256:1111111111111111111111111111111111111111111111111111111111111111', '{}', 'download_queued', 202,
    '{}', 1,
    'sha256:' || encode(sha256(convert_to('acquisition-input:v1', 'UTF8') || decode('00', 'hex') || convert_to(q.input, 'UTF8')), 'hex'),
    'anime-acquired', 'release-acquired', '{"state":"known","value":"Discovery Show 02","reason_code":null}',
    CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 2592000, 2592000, 5184000, CURRENT_TIMESTAMP + INTERVAL '60 days',
    'running', 'full', 'none', q.input,
    'sha256:3333333333333333333333333333333333333333333333333333333333333333',
    'sha256:4444444444444444444444444444444444444444444444444444444444444444', 0, CURRENT_TIMESTAMP
FROM (SELECT '{"schema_version":1}'::text AS input) AS q`); err != nil {
		t.Fatalf("seed execution root: %v", err)
	}
	stopSubscription(t, owner, "sub-acquired", "31 days")

	deleted := purgeOnce(t, repository, db, 500)
	// release-old-stop, release-graph, release-cleaned and release-retired.
	if deleted != 4 {
		t.Fatalf("deleted %d releases, want 4", deleted)
	}
	for _, gone := range []string{"release-old-stop", "release-graph", "release-cleaned", "release-retired"} {
		if releaseExists(t, owner, gone) {
			t.Fatalf("eligible release %s was not deleted", gone)
		}
	}
	for _, kept := range []string{"release-logged", "release-marker", "release-retired-new", "release-stopped-new", "release-active", "release-resaved", "release-placed", "release-acquired"} {
		if !releaseExists(t, owner, kept) {
			t.Fatalf("release %s must be kept", kept)
		}
	}
	// The graph of the deleted release is gone, leaf-first.
	for _, query := range []string{
		`SELECT count(*) FROM release_torrent_inputs WHERE release_id = 'release-graph'`,
		`SELECT count(*) FROM release_candidate_facts WHERE release_id = 'release-graph'`,
		`SELECT count(*) FROM media_fact_snapshots WHERE release_id = 'release-graph'`,
		`SELECT count(*) FROM media_fact_files WHERE snapshot_id = 'snapshot-graph'`,
		`SELECT count(*) FROM acquisition_targets WHERE release_id = 'release-graph'`,
		`SELECT count(*) FROM idempotency_records WHERE release_id = 'release-graph'`,
	} {
		var count int
		if err := owner.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatalf("read dependent graph: %v", err)
		}
		if count != 0 {
			t.Fatalf("dependent graph survived the release delete: %s returned %d", query, count)
		}
	}
	// The acquisition evidence and the placement are untouched.
	var rootState, placementState string
	if err := owner.QueryRowContext(ctx, `SELECT state FROM acquisitions WHERE id = 'root-acquired'`).Scan(&rootState); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRowContext(ctx, `SELECT state FROM placements WHERE id = 'placement-placed'`).Scan(&placementState); err != nil {
		t.Fatal(err)
	}
	if rootState != "running" || placementState != "active" {
		t.Fatalf("evidence changed: root=%s placement=%s", rootState, placementState)
	}
}

// TestPurgeExpiredDiscoveryReleasesBatches proves one call deletes at most the
// requested batch, oldest discovery first, so a backlog cannot hold the janitor
// transaction open.
func TestPurgeExpiredDiscoveryReleasesBatches(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	seedDiscoveryOwner(t, owner, "anime-batch", "sub-batch", "source-batch")
	stopSubscription(t, owner, "sub-batch", "31 days")
	for _, release := range []string{"release-batch-1", "release-batch-2", "release-batch-3"} {
		seedDiscoveryRelease(t, owner, release, "source-batch", "guid:"+release)
	}
	// Explicit ordering: discovery time, then id.
	if _, err := owner.ExecContext(context.Background(), `UPDATE releases SET discovered_at = CURRENT_TIMESTAMP - INTERVAL '30 days' WHERE id = 'release-batch-1'`); err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(db)
	if deleted := purgeOnce(t, repository, db, 2); deleted != 2 {
		t.Fatalf("first batch deleted %d, want 2", deleted)
	}
	// The oldest discovery goes first, then id order: the third stays for the
	// next pass instead of holding this transaction open.
	if releaseExists(t, owner, "release-batch-1") || releaseExists(t, owner, "release-batch-2") {
		t.Fatal("oldest releases must be deleted first")
	}
	if !releaseExists(t, owner, "release-batch-3") {
		t.Fatal("a release beyond the batch limit was deleted")
	}
	if deleted := purgeOnce(t, repository, db, 2); deleted != 1 {
		t.Fatalf("second batch deleted %d, want 1", deleted)
	}
	if releaseExists(t, owner, "release-batch-3") {
		t.Fatal("the remaining release was not deleted by the next batch")
	}
}
