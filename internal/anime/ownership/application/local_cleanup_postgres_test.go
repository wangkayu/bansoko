package application

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/apperrors"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/testutil/animetest"
	draftapplication "bansoko/internal/testutil/draftfixture"
	"bansoko/internal/transport/httpapi/generated"
)

// assertPgMessage proves a database fence rejection by its stable message.
func assertPgMessage(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got success", want)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Message != want {
		t.Fatalf("error = %v, want PostgreSQL message %s", err, want)
	}
}

func subscribeCustomDraft(t *testing.T, service *ownershipTestServices, key, locator, label string) generated.SubscribeResult {
	t.Helper()
	draft := ownershipTestDraft(t)
	draft.InstanceLabel = animetest.StringPtr(label)
	draft.RssSources = []generated.RSSSourceDraft{
		{SourceKind: generated.SourceKindCustomRss, Locator: ownershipLocatorInput(t, locator), GroupName: "TestGroup", Title: "Test Show"},
	}
	result, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, key)
	if err != nil {
		t.Fatalf("subscribe %s: %v", label, err)
	}
	return result
}

func localCleanupInput(epoch int64) LocalDataCleanupInput {
	return LocalDataCleanupInput{ExpectedLifecycleEpoch: epoch, Confirmation: LocalDataCleanupConfirmation}
}

func TestCleanupAnimeLocalDataRequiresRetiredOwnerAndCAS(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := subscribeCustomDraft(t, service, "subscribe-key-cleanup-cas", "https://feeds.example.com/cleanup-cas.xml", "cleanup-cas")
	animeID := subscribed.ManagedAnime.Id

	// Active owner: never eligible.
	animetest.AssertBusinessCode(t, service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(1), "cleanup-key-active-0001"), 409, "LOCAL_CLEANUP_NOT_RETIRED")

	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	state, epoch := lifecycleForTest(t, db, animeID)
	if state != "retired" {
		t.Fatalf("lifecycle = %s, want retired", state)
	}

	animetest.AssertBusinessCode(t, service.CleanupAnimeLocalData(ctx, animeID, LocalDataCleanupInput{ExpectedLifecycleEpoch: epoch, Confirmation: "wrong"}, "cleanup-key-confirm-001"), 422, "LOCAL_CLEANUP_CONFIRMATION_REQUIRED")
	animetest.AssertBusinessCode(t, service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "short"), 422, "IDEMPOTENCY_KEY_REQUIRED")
	animetest.AssertBusinessCode(t, service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch-1), "cleanup-key-stale-0001"), 409, "LOCAL_CLEANUP_STALE_EPOCH")
	// A stale epoch must not have deleted anything.
	if count := animetest.CountRows(t, db, "managed_anime"); count != 1 {
		t.Fatalf("stale epoch deleted the owner: %d", count)
	}

	if err := service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "cleanup-key-ok-00000001"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, table := range []string{
		"managed_anime", "subscriptions", "anime_metadata", "rss_sources",
		"rss_source_runtime", "anime_business_configs", "anime_config_overrides",
	} {
		if count := animetest.CountRows(t, db, table); count != 0 {
			t.Fatalf("%s survived cleanup: %d", table, count)
		}
	}
	var receiptCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency_records WHERE kind = 'local_cleanup' AND owner_type = 'managed_anime' AND owner_id = $1`, animeID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 {
		t.Fatalf("cleanup receipt not retained: %d", receiptCount)
	}

	// Idempotent replay of the exact same request returns success without the
	// owner existing.
	if err := service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "cleanup-key-ok-00000001"); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	// Same key, different payload fails closed.
	animetest.AssertBusinessCode(t, service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch+1), "cleanup-key-ok-00000001"), 409, "IDEMPOTENCY_KEY_REUSED")
	// A new key after cleanup is an unknown owner.
	var notFound *apperrors.NotFound
	if err := service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "cleanup-key-other-00001"); !errors.As(err, &notFound) {
		t.Fatalf("new key after cleanup = %v, want NotFound", err)
	}
}

func TestCleanupAnimeLocalDataDeletesOwnerGraphAndPreservesSharedFacts(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	owned := subscribeCustomDraft(t, service, "subscribe-key-cleanup-graph", "https://feeds.example.com/cleanup-graph.xml", "cleanup-graph")
	other := subscribeCustomDraft(t, service, "subscribe-key-cleanup-other", "https://feeds.example.com/cleanup-other.xml", "cleanup-other")
	animeID := owned.ManagedAnime.Id
	sourceID := owned.RssSources[0].Id
	otherAnimeID := other.ManagedAnime.Id
	otherSourceID := other.RssSources[0].Id

	// Real download data and a real library file: cleanup is database-only and
	// must leave both byte-for-byte in place.
	downloadPath, libraryPath := writeCleanupMediaFiles(t)

	fixtureDB := workItemFixtureDB(t)
	tx, err := fixtureDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	// Deep owner chain under the target instance.
	exec(`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('cleanup-rel', $1, 'cleanup-key', '{}', '{}', 'discovered', 'discovered')`, sourceID)
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "cleanup-root", "cleanup-task", "cleanup-rel", "cleanup:task", `{}`, `{}`, "completed", `null`, `[{"file_ref":"f1","ordinal":0,"relative_path":"f1.mkv","exact_size":100,"media_kind":"video","identity_evidence":{}}]`)
	exec(`UPDATE releases SET download_task_id = 'cleanup-task', verdict = 'submitted', reason_code = 'submitted' WHERE id = 'cleanup-rel'`)
	exec(`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('cleanup-artifact', 'cleanup-task', 'cleanup-root', '{}', '{}', 'verified', CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
VALUES ('cleanup-file', 'cleanup-artifact', 'cleanup-root', 'f1', 'f1.mkv', $1, 'video', 100, '{}', 'not_applicable')`, downloadPath)
	acquisitionfixture.CommitOperations(t, tx, "cleanup-artifact", []acquisitionfixture.PlanOperation{{ID: "cleanup-fop", FileID: "cleanup-file", FileRef: "f1", Source: downloadPath, Target: libraryPath, Operation: "copy", Key: "cleanup:fop", Size: 100, Fingerprint: json.RawMessage(`{}`), Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}})
	exec(`UPDATE file_operations SET state='completed',observed_at=CURRENT_TIMESTAMP,target_fingerprint=source_fingerprint,observed_target_size_bytes=100 WHERE id='cleanup-fop'`)
	exec(`SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id='cleanup-fop'`)
	// A pending placement obligation whose file operation is already completed
	// is inert after retirement: no new continuation can be fenced in. It must
	// be cleaned, not block the command.
	exec(`INSERT INTO placement_obligations(file_operation_id, state, attempts, max_attempts)
VALUES ('cleanup-fop', 'pending', 0, 3)`)
	// A pending notification admission for a terminal effect is likewise fenced
	// (its delivery writes are rejected for a retired owner) and must not block.
	exec(`INSERT INTO notification_admissions(id, trigger_kind, source_owner_type, source_owner_id, source_owner_version, result_key, managed_anime_id, occurred_at, event_catalog_version, event_template_catalog_version, payload, state)
VALUES ('cleanup-admission', 'file_operation_completed', 'file_operation', 'cleanup-fop', 1, 'cleanup-result', $1, CURRENT_TIMESTAMP, 'v1', 'v1', '{}', 'pending_target_resolution')`, animeID)
	exec(`INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version, verification_state, state)
VALUES ('cleanup-placement', $2, $1, 'file_operation', 'cleanup-fop', '{}', '{}', 'v1', 'verified', 'active')`, animeID, libraryPath)
	exec(`INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('cleanup-snap', 'cleanup-rel', 'v1', 'v1', 'regex', 'digest', '{}', '{}')`)
	exec(`INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, file_evidence, effective_facts)
VALUES ('cleanup-snap', 'f1', 'video', 100, '{}', '{}')`)
	// The group-scope generation row automation writes per subscription.
	exec(`INSERT INTO provider_group_scope_generations(subscription_id, provider)
SELECT id, 'mikan' FROM subscriptions WHERE managed_anime_id = $1`, animeID)
	exec(`INSERT INTO work_item_outcomes(id, owner_type, owner_id, acquisition_id, managed_anime_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version, terminal_at, retention_days, retention_until)
VALUES ('cleanup-wi', 'download_task', 'cleanup-task', 'cleanup-root', $1, 'qb_sync', 'succeeded', 'cleanup:task', 3, '{}', 1, CURRENT_TIMESTAMP, 7, CURRENT_TIMESTAMP + INTERVAL '7 days')`, animeID)

	// A linked custom candidate must be detached, never deleted. Save it through
	// the real service (so the draft is the canonical typed shape) and link it to
	// the owner while the owner is still active, exactly as activation does.
	domainDraft, err := animetransport.Draft(ownershipTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	linkedCandidate, err := service.SaveAnimeConfigCandidate(ctx, draftapplication.CandidateDraftInput{Draft: presetdraft.LoadDomain(t, db, domainDraft)})
	if err != nil {
		t.Fatalf("save linked candidate: %v", err)
	}
	if linkedCandidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no current revision")
	}
	exec(`UPDATE anime_config_candidates SET activated_managed_anime_id = $1, status = 'dormant', version = version + 1 WHERE id = $2`, animeID, linkedCandidate.Id)

	// A foreign owner's chain and a shared provider snapshot must survive.
	exec(`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('other-rel', $1, 'other-key', '{}', '{}', 'discovered', 'discovered')`, otherSourceID)
	acquisitionfixture.SeedObservedDownload(t, tx, "other-root", "other-task", "other-rel", "other:task", `{}`, `{}`, "completed", `null`)
	exec(`UPDATE releases SET download_task_id = 'other-task', verdict = 'submitted', reason_code = 'submitted' WHERE id = 'other-rel'`)
	emptyHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	exec(`SELECT publish_feed_snapshot('shared-snapshot','mikan','entry-1','group-1',1,CURRENT_TIMESTAMP,$1,'[]',ARRAY[]::bytea[])`, emptyHash)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: owned.Subscription.Version}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	_, epoch := lifecycleForTest(t, db, animeID)
	// The root is still running when its owner retires and nothing of it is in
	// flight: cleanup settles it to cancelled / OWNER_RETIRED, compacts it with
	// trigger owner_cleanup and then deletes the management graph.
	if err := service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "cleanup-key-graph-0001"); err != nil {
		t.Fatalf("cleanup graph: %v", err)
	}
	assertCleanupRootSettledAndCompacted(t, db, "cleanup-root", "cancelled")
	assertCleanupMediaFilesKept(t, downloadPath, libraryPath)

	checks := []struct {
		name  string
		query string
		args  []any
	}{
		{"releases", `SELECT COUNT(*) FROM releases WHERE id = 'cleanup-rel'`, nil},
		{"group scope generations", `SELECT COUNT(*) FROM provider_group_scope_generations g LEFT JOIN subscriptions s ON s.id = g.subscription_id WHERE s.id IS NULL OR s.managed_anime_id = $1`, []any{animeID}},
		{"download tasks", `SELECT COUNT(*) FROM download_tasks WHERE id = 'cleanup-task'`, nil},
		{"artifacts", `SELECT COUNT(*) FROM artifacts WHERE id = 'cleanup-artifact'`, nil},
		{"artifact files", `SELECT COUNT(*) FROM artifact_files WHERE id = 'cleanup-file'`, nil},
		{"file operations", `SELECT COUNT(*) FROM file_operations WHERE id = 'cleanup-fop'`, nil},
		{"placement obligations", `SELECT COUNT(*) FROM placement_obligations WHERE file_operation_id = 'cleanup-fop'`, nil},
		{"notification admissions", `SELECT COUNT(*) FROM notification_admissions WHERE id = 'cleanup-admission'`, nil},
		{"placements", `SELECT COUNT(*) FROM placements WHERE managed_anime_id = $1`, []any{animeID}},
		{"media fact snapshots", `SELECT COUNT(*) FROM media_fact_snapshots WHERE id = 'cleanup-snap'`, nil},
		{"media fact files", `SELECT COUNT(*) FROM media_fact_files WHERE snapshot_id = 'cleanup-snap'`, nil},
		{"work items", `SELECT COUNT(*) FROM work_item_history WHERE id = 'cleanup-wi'`, nil},
	}
	for _, check := range checks {
		var count int
		if err := db.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("%s survived cleanup: %d", check.name, count)
		}
	}
	// The other owner's records survived intact.
	var otherCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM download_tasks WHERE id = 'other-task'`).Scan(&otherCount); err != nil {
		t.Fatal(err)
	}
	if otherCount != 1 {
		t.Fatalf("other owner download task was deleted")
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_anime WHERE id = $1`, otherAnimeID).Scan(&otherCount); err != nil {
		t.Fatal(err)
	}
	if otherCount != 1 {
		t.Fatalf("other owner was deleted")
	}
	// Shared provider snapshot survived.
	var snapshotCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_feed_snapshots WHERE id = 'shared-snapshot'`).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 1 {
		t.Fatalf("shared provider snapshot was deleted")
	}
	// The linked custom candidate was detached, not deleted.
	var activated sql.NullString
	var status string
	if err := db.QueryRowContext(ctx, `SELECT activated_managed_anime_id, status FROM anime_config_candidates WHERE id = $1`, linkedCandidate.Id).Scan(&activated, &status); err != nil {
		t.Fatalf("candidate must survive cleanup: %v", err)
	}
	if activated.Valid || status != "archived" {
		t.Fatalf("candidate not detached: activated=%v status=%s", activated, status)
	}
	// Its draft and immutable revision are preserved.
	var revisionCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_config_candidate_revisions WHERE candidate_id = $1`, linkedCandidate.Id).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != int(*linkedCandidate.CurrentRevision) {
		t.Fatalf("candidate revisions were deleted: %d", revisionCount)
	}
}

func TestCleanupAnimeLocalDataProceedsAfterRetirementMidExternalEffect(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := subscribeCustomDraft(t, service, "subscribe-key-cleanup-live", "https://feeds.example.com/cleanup-live.xml", "cleanup-live")
	animeID := subscribed.ManagedAnime.Id
	sourceID := subscribed.RssSources[0].Id

	// A real download effect admitted mid-flight: the release/download task is
	// non-terminal and the qb_submit WorkItem is leased and admitted.
	downloadPath, libraryPath := writeCleanupMediaFiles(t)
	fixtureDB := workItemFixtureDB(t)
	tx, err := fixtureDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('cleanup-live-rel', $1, 'cleanup-live-key', '{}', '{}', 'discovered', 'discovered')`, sourceID); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, tx, "cleanup-live-root", "cleanup-live-task", "cleanup-live-rel", "cleanup:live:task", `{}`, `{}`, "downloading", `null`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id='cleanup-live-task', verdict='submitted', reason_code='submitted' WHERE id='cleanup-live-rel'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, state, lease_until, fencing_token, idempotency_key, max_attempts, input_snapshot, expected_owner_version, external_effect_admitted_at)
VALUES ('cleanup-live-wi', 'download_task', 'cleanup-live-task', 'cleanup-live-root', 'qb_sync', 'leased', CURRENT_TIMESTAMP + INTERVAL '3 seconds', 1, 'cleanup:live:task', 3, '{}', 1, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	// Retirement must preserve the admitted work.
	var admittedState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id='cleanup-live-wi'`).Scan(&admittedState); err != nil {
		t.Fatal(err)
	}
	if admittedState != "leased" {
		t.Fatalf("retirement changed admitted work: %s", admittedState)
	}
	_, epoch := lifecycleForTest(t, db, animeID)
	// The late writer fails on the retirement fence instead of advancing the
	// admitted effect.
	assertPgMessage(t, func() error {
		_, err := workItemFixtureDB(t).ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE id='cleanup-live-wi'`)
		return err
	}(), "MANAGED_ANIME_RETIRED")
	// Retirement disposition: while the admitted
	// effect is unresolved (its worker still holds a live lease) cleanup is
	// refused with the typed retained reason and changes nothing.
	before := acquisitionfixture.Snapshot(t, disposabledb.Owner(t))
	blocked := service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "cleanup-key-live-0001")
	var business *apperrors.Business
	if !errors.As(blocked, &business) || business.Code != "LOCAL_CLEANUP_ACQUISITION_RETAINED" {
		t.Fatalf("live effect cleanup gate: %v", blocked)
	}
	if before != acquisitionfixture.Snapshot(t, disposabledb.Owner(t)) {
		t.Fatal("live effect cleanup changed graph")
	}
	// Once the lease has ended the effect can no longer write (the fence above)
	// and is never redone: the root settles to cancelled / OWNER_RETIRED with
	// the effect recorded as unknown, and cleanup succeeds.
	waitForLeaseEnd(t, db, "cleanup-live-wi")
	if err := service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "cleanup-key-live-0001"); err != nil {
		t.Fatalf("cleanup after retirement mid external effect: %v", err)
	}
	assertCleanupRootSettledAndCompacted(t, db, "cleanup-live-root", "cancelled")
	var hasUnknown bool
	if err := db.QueryRowContext(ctx, `SELECT (terminal_receipt->'effect_summary'->>'has_unknown')::boolean FROM acquisitions WHERE id='cleanup-live-root'`).Scan(&hasUnknown); err != nil || !hasUnknown {
		t.Fatalf("cancelled receipt must record the unresolved effect as unknown: %v %v", hasUnknown, err)
	}
	assertCleanupMediaFilesKept(t, downloadPath, libraryPath)
	for _, check := range []struct {
		query string
		args  []any
	}{
		{`SELECT COUNT(*) FROM managed_anime WHERE id = $1`, []any{animeID}},
		{`SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id = $1`, []any{animeID}},
		{`SELECT COUNT(*) FROM work_item_history WHERE managed_anime_id = $1`, []any{animeID}},
		{`SELECT COUNT(*) FROM download_tasks WHERE id = 'cleanup-live-task'`, nil},
		{`SELECT COUNT(*) FROM releases WHERE id = 'cleanup-live-rel'`, nil},
	} {
		var count int
		if err := db.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("local graph survived cleanup: %s -> %d", check.query, count)
		}
	}
}

// TestCleanupAnimeLocalDataDeletesManualRetryIdempotencyBeforeWorkItems proves
// the manual-retry HTTP dedupe record cannot replay a false success after the
// owner graph is cleaned.
func TestCleanupAnimeLocalDataDeletesManualRetryIdempotencyBeforeWorkItems(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := subscribeCustomDraft(t, service, "subscribe-key-cleanup-retry", "https://feeds.example.com/cleanup-retry.xml", "cleanup-retry")
	animeID := subscribed.ManagedAnime.Id
	sourceID := subscribed.RssSources[0].Id

	fixtureDB := workItemFixtureDB(t)
	tx, err := fixtureDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_item_outcomes(id, owner_type, owner_id, managed_anime_id, kind, state, idempotency_key, max_attempts, input_snapshot, expected_owner_version, terminal_at, retention_days, retention_until)
VALUES ('cleanup-retry-wi', 'rss_source_runtime', $1, $2, 'rss_refresh', 'failed', 'cleanup:retry:wi', 3, '{}', 1, CURRENT_TIMESTAMP, 7, CURRENT_TIMESTAMP + INTERVAL '7 days')`, sourceID, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, owner_type, owner_id, failed_work_item_id, result_work_item_id, result_snapshot)
VALUES ('manual_retry', 'cleanup-retry-key', 'sha256:' || repeat('a', 64), 'rss_source_runtime', $1, 'cleanup-retry-wi', 'cleanup-retry-wi', '{}')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	_, epoch := lifecycleForTest(t, db, animeID)
	if err := service.CleanupAnimeLocalData(ctx, animeID, localCleanupInput(epoch), "cleanup-key-retry-001"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	var records, workItems int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency_records WHERE kind='manual_retry'`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE id='cleanup-retry-wi'`).Scan(&workItems); err != nil {
		t.Fatal(err)
	}
	if records != 0 || workItems != 0 {
		t.Fatalf("manual retry evidence survived cleanup: records=%d work_items=%d", records, workItems)
	}
}

// TestMetadataPatchAndRetirementShareLifecycleLockOrder proves every visible
// ManagedAnime mutation takes the lifecycle row first: a concurrent metadata
// patch and retirement serialize deterministically with no 40P01 deadlock, and
// the metadata patch either succeeds or returns the typed retirement boundary.
func TestMetadataPatchAndRetirementShareLifecycleLockOrder(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	for iteration := 0; iteration < 8; iteration += 1 {
		subscribed := subscribeCustomDraft(t, service, fmt.Sprintf("subscribe-key-lockorder-%d", iteration), fmt.Sprintf("https://feeds.example.com/lockorder-%d.xml", iteration), fmt.Sprintf("lockorder-%d", iteration))
		animeID := subscribed.ManagedAnime.Id
		patch := MetadataPatchInput{
			ExpectedVersion: 1,
			BGM:             &BGMBinding{WorkID: "bgm-lockorder"},
			TMDB:            &TMDBBinding{WorkID: "tmdb-lockorder", MediaKind: "tv", Season: 1},
		}
		input := SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}

		var wg sync.WaitGroup
		start := make(chan struct{})
		var metadataErr, retireErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, metadataErr = service.PatchAnimeMetadataApplication(ctx, animeID, patch)
		}()
		go func() {
			defer wg.Done()
			<-start
			retireErr = service.DeleteAnimeSubscription(ctx, animeID, input)
		}()
		close(start)
		wg.Wait()

		for _, err := range []error{metadataErr, retireErr} {
			if err == nil {
				continue
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("iteration %d deadlocked (40P01): metadata=%v retire=%v", iteration, metadataErr, retireErr)
			}
			var business *apperrors.Business
			if !errors.As(err, &business) || business.Code != "MANAGED_ANIME_RETIRED" {
				t.Fatalf("iteration %d unexpected error: metadata=%v retire=%v", iteration, metadataErr, retireErr)
			}
		}
	}
}

// writeCleanupMediaFiles writes one download-data file and one library file
// for the never-delete-media guard.
func writeCleanupMediaFiles(t *testing.T) (downloadPath, libraryPath string) {
	t.Helper()
	downloadPath = filepath.Join(t.TempDir(), "f1.mkv")
	libraryPath = filepath.Join(t.TempDir(), "Show", "f1.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(downloadPath, []byte("download-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libraryPath, []byte("library-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return downloadPath, libraryPath
}

// assertCleanupMediaFilesKept is the never-delete-media guard: both files keep
// their exact bytes after cleanup.
func assertCleanupMediaFilesKept(t *testing.T, downloadPath, libraryPath string) {
	t.Helper()
	for path, want := range map[string]string{downloadPath: "download-bytes", libraryPath: "library-bytes"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("media file %s must survive cleanup unchanged: %q %v", path, got, err)
		}
	}
}

// assertCleanupRootSettledAndCompacted checks the root outlives cleanup as a
// compact terminal record with its receipt, compacted by owner_cleanup.
func assertCleanupRootSettledAndCompacted(t *testing.T, db *sql.DB, root, state string) {
	t.Helper()
	var gotState, record, trigger, reason string
	if err := db.QueryRow(`SELECT state, record_state, compact_trigger, COALESCE(terminal_receipt->'primary_failure'->>'reason_code','') FROM acquisitions WHERE id=$1`, root).Scan(&gotState, &record, &trigger, &reason); err != nil {
		t.Fatalf("root %s must outlive cleanup: %v", root, err)
	}
	if gotState != state || record != "compact" || trigger != "owner_cleanup" || (state == "cancelled" && reason != "OWNER_RETIRED") {
		t.Fatalf("root %s = %s/%s/%s/%s, want %s/compact/owner_cleanup", root, gotState, record, trigger, reason, state)
	}
}

// waitForLeaseEnd waits until the work item's lease has passed.
func waitForLeaseEnd(t *testing.T, db *sql.DB, workItem string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var ended bool
		if err := db.QueryRow(`SELECT lease_until <= clock_timestamp() FROM work_items WHERE id=$1`, workItem).Scan(&ended); err != nil {
			t.Fatal(err)
		}
		if ended {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("lease did not end")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
