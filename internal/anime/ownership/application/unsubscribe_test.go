package application

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	animeconfig "bansoko/internal/anime/animeconfig"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	draftapplication "bansoko/internal/testutil/draftfixture"
)

func lifecycleForTest(t *testing.T, db *sql.DB, animeID string) (string, int64) {
	t.Helper()
	var state string
	var epoch int64
	if err := db.QueryRow(`SELECT lifecycle_state, fence_epoch FROM managed_anime WHERE id = $1`, animeID).Scan(&state, &epoch); err != nil {
		t.Fatal(err)
	}
	return state, epoch
}

func TestDeleteAnimeSubscriptionRetiresAndPreservesOwnerGraph(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := ownershipSubscribeTestDraft(t, service, "subscribe-key-retire-graph")
	animeID := subscribed.ManagedAnime.Id
	sourceID := subscribed.RssSources[0].Id

	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('retire-rel', $1, 'retire-key', '{}', '{}', 'discovered', 'discovered')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	state, epoch := lifecycleForTest(t, db, animeID)
	if state != "retired" || epoch != 2 {
		t.Fatalf("lifecycle = %s/%d, want retired/2", state, epoch)
	}
	checks := []struct {
		name  string
		query string
		arg   string
	}{
		{"managed anime", `SELECT COUNT(*) FROM managed_anime WHERE id = $1`, animeID},
		{"subscription", `SELECT COUNT(*) FROM subscriptions WHERE managed_anime_id = $1`, animeID},
		{"metadata", `SELECT COUNT(*) FROM anime_metadata WHERE managed_anime_id = $1`, animeID},
		{"RSS source", `SELECT COUNT(*) FROM rss_sources WHERE id = $1`, sourceID},
		{"release", `SELECT COUNT(*) FROM releases WHERE id = $1`, "retire-rel"},
		{"idempotency", `SELECT COUNT(*) FROM idempotency_records WHERE managed_anime_id = $1`, animeID},
	}
	for _, check := range checks {
		var count int
		if err := db.QueryRow(check.query, check.arg).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 1 {
			t.Fatalf("retirement must preserve %s, got %d rows", check.name, count)
		}
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatalf("repeat retirement with current subscription version must be idempotent: %v", err)
	}
	var conflict *apperrors.VersionConflict
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version + 1}); !errors.As(err, &conflict) {
		t.Fatalf("stale retirement CAS = %v, want VersionConflict", err)
	}
}

func TestDeleteAnimeSubscriptionIsolatesOnlyUnadmittedWork(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := ownershipSubscribeTestDraft(t, service, "subscribe-key-retire-work")
	animeID := subscribed.ManagedAnime.Id
	sourceID := subscribed.RssSources[0].Id

	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('retire-work-rel-1', $1, 'retire-work-1', '{}', '{}', 'discovered', 'discovered'),
       ('retire-work-rel-2', $1, 'retire-work-2', '{}', '{}', 'discovered', 'discovered')`, []any{sourceID}},
		{`UPDATE releases
SET download_task_id = CASE id WHEN 'retire-work-rel-1' THEN 'retire-work-task-1' ELSE 'retire-work-task-2' END,
    verdict = 'submitted'
WHERE id IN ('retire-work-rel-1', 'retire-work-rel-2')`, nil},
		{`INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, state, lease_until, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES
 ('retire-unadmitted', 'download_task', 'retire-work-task-1', 'retire-root-1', 'qb_submit', 'leased', CURRENT_TIMESTAMP + INTERVAL '5 minutes', 'retire:task:1', 3, '{}', 1),
 ('retire-admitted', 'download_task', 'retire-work-task-2', 'retire-root-2', 'qb_submit', 'leased', CURRENT_TIMESTAMP + INTERVAL '5 minutes', 'retire:task:2', 3, '{}', 1)`, nil},
	}
	fixtureDB := workItemFixtureDB(t)
	// Acceptance commits on its own, as in production: the accepted graph owns
	// exactly one submit at commit. The leased work under test is arranged after.
	acceptTx, err := fixtureDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer acceptTx.Rollback()
	if _, err := acceptTx.ExecContext(ctx, statements[0].query, statements[0].args...); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, acceptTx, "retire-root-1", "retire-work-task-1", "retire-work-rel-1", "retire:task:1", `{}`, `{}`, "queued", `null`)
	acquisitionfixture.SeedObservedDownload(t, acceptTx, "retire-root-2", "retire-work-task-2", "retire-work-rel-2", "retire:task:2", `{}`, `{}`, "queued", `null`)
	if err := acceptTx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err := fixtureDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range statements[1:] {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET external_effect_admitted_at = CURRENT_TIMESTAMP WHERE id = 'retire-admitted'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatal(err)
	}
	var unadmittedState, admittedState string
	var unadmittedToken int64
	if err := db.QueryRow(`SELECT state, fencing_token FROM work_item_history WHERE id='retire-unadmitted'`).Scan(&unadmittedState, &unadmittedToken); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM work_item_history WHERE id='retire-admitted'`).Scan(&admittedState); err != nil {
		t.Fatal(err)
	}
	if unadmittedState != "failed" || unadmittedToken != 1 || admittedState != "leased" {
		t.Fatalf("work states unadmitted=%s/token=%d admitted=%s", unadmittedState, unadmittedToken, admittedState)
	}
	var admittedEffectAt *time.Time
	if err := db.QueryRow(`SELECT external_effect_admitted_at FROM work_item_history WHERE id='retire-admitted'`).Scan(&admittedEffectAt); err != nil {
		t.Fatal(err)
	}
	if admittedEffectAt == nil {
		t.Fatal("retirement dropped the admitted external-effect record")
	}
	if _, err := service.RefreshRSSSource(ctx, sourceID, "retire-stale-refresh"); err == nil {
		t.Fatal("retired owner refresh unexpectedly succeeded")
	} else {
		var business *apperrors.Business
		if !errors.As(err, &business) || business.Code != "MANAGED_ANIME_RETIRED" {
			t.Fatalf("retired owner refresh = %v, want MANAGED_ANIME_RETIRED", err)
		}
	}
}

// TestDeleteAnimeSubscriptionIgnoresForgedSessionCapability proves retirement
// no longer accepts (or emits) a session capability: an ordinary connection can
// set the former `bansoko.retirement_owner` GUC to a decoy, and neither the real
// retirement transaction nor a later stale write is authorized by it.
func TestDeleteAnimeSubscriptionIgnoresForgedSessionCapability(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := ownershipSubscribeTestDraft(t, service, "subscribe-key-forged-capability")
	animeID := subscribed.ManagedAnime.Id
	sourceID := subscribed.RssSources[0].Id
	if _, err := db.ExecContext(ctx, `SELECT set_config('bansoko.retirement_owner', 'some-other-anime', false)`); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatalf("retirement with a decoy capability set: %v", err)
	}
	state, epoch := lifecycleForTest(t, db, animeID)
	if state != "retired" || epoch != 2 {
		t.Fatalf("lifecycle = %s/%d, want retired/2", state, epoch)
	}
	// The forged capability must authorize nothing after the fact either.
	if _, err := service.RefreshRSSSource(ctx, sourceID, "forged-capability-refresh"); err == nil {
		t.Fatal("forged capability authorized a retired owner write")
	} else {
		var business *apperrors.Business
		if !errors.As(err, &business) || business.Code != "MANAGED_ANIME_RETIRED" {
			t.Fatalf("forged capability refresh = %v, want MANAGED_ANIME_RETIRED", err)
		}
	}
	if _, err := db.ExecContext(ctx, `SELECT set_config('bansoko.retirement_owner', '', false)`); err != nil {
		t.Fatal(err)
	}
}

// TestListAnimeApplicationRetainsRetiredOwner proves the read surface keeps a
// retired instance inspectable and reports its lifecycle separately from the
// reversible Subscription state.
func TestDeleteAnimeSubscriptionRetainsRetiredOwnerRows(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := ownershipSubscribeTestDraft(t, service, "subscribe-key-retired-read")
	animeID := subscribed.ManagedAnime.Id
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err != nil {
		t.Fatal(err)
	}
	var lifecycleState string
	var lifecycleEpoch int64
	var retiredAt sql.NullTime
	var subscriptionState string
	if err := db.QueryRowContext(ctx, `
		SELECT l.lifecycle_state, l.fence_epoch, l.retired_at, s.state
		FROM managed_anime l
		JOIN subscriptions s ON s.managed_anime_id = l.id
		WHERE l.id = $1`, animeID).Scan(&lifecycleState, &lifecycleEpoch, &retiredAt, &subscriptionState); err != nil {
		t.Fatalf("read retired owner: %v", err)
	}
	if lifecycleState != "retired" || lifecycleEpoch != 2 {
		t.Fatalf("retired row lifecycle = %s/%d", lifecycleState, lifecycleEpoch)
	}
	if !retiredAt.Valid {
		t.Fatal("retired row lost its retirement timestamp")
	}
	if subscriptionState == "retired" {
		t.Fatalf("lifecycle leaked into Subscription.state: %q", subscriptionState)
	}
}

func TestDeleteAnimeSubscriptionPreservesCustomCandidateUntilExplicitCleanup(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	candidate := ownershipSaveCandidate(t, service, ownershipCustomDraft(t, "Retained Candidate"))
	result, err := service.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "retain-candidate", draftapplication.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, result.ManagedAnime.ID, SubscriptionDeleteInput{ExpectedVersion: result.Subscription.Version}); err != nil {
		t.Fatal(err)
	}
	current, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.ActivatedManagedAnimeId == nil || *current.ActivatedManagedAnimeId != result.ManagedAnime.ID || current.Status != "dormant" || current.CleanupEligibleAt != nil || current.CleanupReason != nil {
		t.Fatalf("retirement granted automatic cleanup authority to custom candidate: %+v", current)
	}
	if err := service.DeleteAnimeConfigCandidate(ctx, candidate.Id); err != nil {
		t.Fatalf("explicit cleanup after retirement: %v", err)
	}
	if err := service.DeleteAnimeConfigCandidate(ctx, candidate.Id); err != nil {
		t.Fatalf("explicit cleanup replay: %v", err)
	}
	// The retired instance keeps its frozen business, which pins the activated
	// revision: the candidate is archived with cleanup authority, not deleted.
	archived, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatalf("pinned candidate after explicit cleanup: %v", err)
	}
	if archived.Status != "archived" || archived.ActivatedManagedAnimeId != nil || archived.CleanupEligibleAt == nil {
		t.Fatalf("explicit cleanup of a pinned candidate = %+v, want archived with cleanup authority", archived)
	}
	var pinned int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_business_configs WHERE managed_anime_id=$1 AND candidate_id=$2`, result.ManagedAnime.ID, candidate.Id).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if pinned != 1 {
		t.Fatalf("retired instance lost its frozen business pin: %d", pinned)
	}
}

func TestDeleteAnimeSubscriptionArchivesProviderCandidateAndReplanStartsFreshRevision(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	draft := ownershipProviderDraft(t, "provider-retirement-entry", "Provider Retirement")
	candidate := ownershipSaveCandidate(t, service, draft)
	result, err := service.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "retire-provider-candidate", draftapplication.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, result.ManagedAnime.ID, SubscriptionDeleteInput{ExpectedVersion: result.Subscription.Version}); err != nil {
		t.Fatal(err)
	}
	archived, err := service.GetAnimeConfigCandidate(ctx, candidate.Id)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != "archived" || archived.ActivatedManagedAnimeId != nil || archived.CleanupEligibleAt == nil || archived.CleanupReason == nil || *archived.CleanupReason != "provider_owner_retired" {
		t.Fatalf("retired provider candidate = %+v", archived)
	}
	// The retired instance's frozen business pins the activated revision, so
	// the explicit cleanup keeps the archived candidate until nothing pins it.
	if err := service.DeleteAnimeConfigCandidate(ctx, candidate.Id); err != nil {
		t.Fatalf("explicit cleanup of the archived provider candidate: %v", err)
	}
	var revisionCount, configOwnerCount, overrideCount, replayCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_config_candidate_revisions WHERE candidate_id=$1`, candidate.Id).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_business_configs WHERE managed_anime_id=$1`, result.ManagedAnime.ID).Scan(&configOwnerCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM anime_config_overrides WHERE managed_anime_id=$1`, result.ManagedAnime.ID).Scan(&overrideCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency_records WHERE kind='subscribe' AND idempotency_key='retire-provider-candidate' AND result_snapshot IS NOT NULL`).Scan(&replayCount); err != nil {
		t.Fatal(err)
	}
	if revisionCount != 1 || configOwnerCount != 1 || overrideCount != 7 || replayCount != 1 {
		t.Fatalf("candidate cleanup changed frozen owner/replay evidence: revisions=%d owners=%d overrides=%d replay=%d", revisionCount, configOwnerCount, overrideCount, replayCount)
	}
	retiredEffective, err := animeconfig.LoadEffective(ctx, db, result.ManagedAnime.ID)
	if err != nil {
		t.Fatalf("read materialized instance after candidate cleanup: %v", err)
	}
	if retiredEffective.Selection.MaxRss != 3 {
		t.Fatalf("retired instance lost its frozen configuration: max_rss=%d", retiredEffective.Selection.MaxRss)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO global_config_sections(section_name, schema_version, value, version)
VALUES ('subgroups', 'subgroups-v1', '{"entries":[],"max_rss":99}', 1)`); err != nil {
		t.Fatal(err)
	}
	retiredAfterGlobal, err := animeconfig.LoadEffective(ctx, db, result.ManagedAnime.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retiredAfterGlobal.Selection.MaxRss != 3 {
		t.Fatalf("global change reached retired materialized instance: max_rss=%d", retiredAfterGlobal.Selection.MaxRss)
	}
	fresh := ownershipSaveCandidate(t, service, draft)
	if fresh.Id == candidate.Id || fresh.Version != 1 || fresh.CurrentRevision == nil || *fresh.CurrentRevision != 1 {
		t.Fatalf("provider replan reused consumed candidate/revision: old=%+v fresh=%+v", candidate, fresh)
	}
	freshRevision, err := service.GetCurrentAnimeConfigCandidateRevision(ctx, fresh.Id)
	if err != nil {
		t.Fatal(err)
	}
	freshEffective, err := freshRevision.EffectiveConfig()
	if err != nil {
		t.Fatal(err)
	}
	// Every per-anime section is one-time input: the replanned draft keeps the
	// value it holds, and the Global change made after it was created does not
	// reach it.
	if freshEffective.Selection.MaxRss != 3 {
		t.Fatalf("fresh provider revision took a Global change instead of the draft value: max_rss=%d", freshEffective.Selection.MaxRss)
	}
}

func TestDeleteAnimeSubscriptionInjectedFailureRollsBackLifecycle(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribed := ownershipSubscribeTestDraft(t, service, "subscribe-key-retire-rollback")
	animeID := subscribed.ManagedAnime.Id
	if _, err := animetest.OwnerTestDB(t).ExecContext(ctx, `
CREATE FUNCTION reject_retirement() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected retirement failure'; END;
$$;
CREATE TRIGGER aaa_reject_retirement BEFORE UPDATE ON managed_anime
FOR EACH ROW WHEN (NEW.lifecycle_state = 'retired') EXECUTE FUNCTION reject_retirement()`); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: subscribed.Subscription.Version}); err == nil {
		t.Fatal("retirement unexpectedly succeeded")
	}
	state, epoch := lifecycleForTest(t, db, animeID)
	if state != "active" || epoch != 1 {
		t.Fatalf("failed retirement leaked lifecycle change: %s/%d", state, epoch)
	}
}

func TestDeleteAnimeSubscriptionRetainsAllOwnerRows(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-delete-1")
	animeID := subscribe.ManagedAnime.Id
	subscriptionID := subscribe.Subscription.Id
	sourceID := subscribe.RssSources[0].Id

	// Build a full runtime chain under this subscription: release -> download
	// task -> artifact -> artifact file -> file operation, plus work items.
	if _, err := db.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('del-rel', $1, 'del-rel-key', '{}', '{}', 'filtered', 'submitted')`, sourceID); err != nil {
		t.Fatal(err)
	}
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "del-root", "del-task", "del-rel", "del-task-key", `{}`, `{}`, "queued", `null`, `[{"file_ref":"del-file-ref","ordinal":0,"relative_path":"ep01.mkv","exact_size":1,"media_kind":"video","identity_evidence":{}}]`)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'del-task' WHERE id = 'del-rel'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('del-art', 'del-task', 'del-root', '{}', '{}', 'verified', NOW())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ('del-artfile', 'del-art', 'del-root', 'del-file-ref', 'ep01.mkv', '/source/ep01.mkv', 'video', 1, '{}', '{}', 'not_applicable')`); err != nil {
		t.Fatal(err)
	}
	planTx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.CommitOperations(t, planTx, "del-art", []acquisitionfixture.PlanOperation{{ID: "del-fileop", FileID: "del-artfile", FileRef: "del-file-ref", Source: "/source/ep01.mkv", Target: "/target/ep01.mkv", Operation: "copy", Key: "del-fileop-key", Size: 1, Fingerprint: json.RawMessage(`{}`), Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}})
	if _, err := planTx.ExecContext(ctx, `UPDATE file_operations SET state='completed',observed_at=NOW(),target_fingerprint=source_fingerprint WHERE id='del-fileop'; SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id='del-fileop'`); err != nil {
		t.Fatal(err)
	}
	if err := planTx.Commit(); err != nil {
		t.Fatal(err)
	}
	// A retry idempotency record tied to the release (must be removed because
	// release_id is NOT NULL and the release is being deleted).
	if _, err := db.ExecContext(ctx, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, release_id, result_snapshot)
VALUES ('release_retry', 'del-retry-key', 'sha256:0000000000000000000000000000000000000000000000000000000000000000',
    'del-rel', '{}')`); err != nil {
		t.Fatal(err)
	}
	// A provider group selection for the subscription.
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_group_selections(id, subscription_id, provider, group_ref, display_name,
    selection_mode, effective_selected, effective_score)
VALUES ('del-pgs', $1, 'mikan', 'del-group', 'Del Group', 'auto', true, 100)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	// A subscription log entry (created by Subscribe already; add a second).
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscription_log_entries(id, subscription_id, managed_anime_id, kind, outcome, title, summary, dedupe_key)
VALUES ('del-log', $1, $2, 'group_changed', 'info', 't', 's', 'group_changed:$1:v9')`, subscriptionID, animeID); err != nil {
		t.Fatal(err)
	}
	// A routine activity and diagnostic episode pointing at the anime (nullable
	// FK; the rows must survive with managed_anime_id NULLed).
	if _, err := db.ExecContext(ctx, `
INSERT INTO routine_activities(id, kind, scope_kind, scope_id, managed_anime_id, outcome, last_executed_at, last_result_code, summary)
VALUES ('del-act', 'rss_refresh', 'subscription', $1, $2, 'success', NOW(), 'ok', 's')`, subscriptionID, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO diagnostic_episodes(id, severity, component, operation, error_code, scope_kind, scope_id, managed_anime_id,
    first_failed_at, last_failed_at, occurrence_count, last_error)
VALUES ('del-ep', 'warning', 'rss', 'refresh', 'err', 'subscription', $1, $2, NOW(), NOW(), 1, 'e')`, subscriptionID, animeID); err != nil {
		t.Fatal(err)
	}
	// An activated candidate linking back to the anime (nullable FK; the
	// candidate row survives with activated_managed_anime_id NULLed).
	if _, err := db.ExecContext(ctx, `
INSERT INTO anime_config_candidates(id, origin, title, instance_label, draft, activated_managed_anime_id)
VALUES ('del-cand', 'custom', 't', 'del-cand', '{"origin":"custom","origin_entry_ref":null,"title":"t","instance_label":"del-cand"}', $1)`, animeID); err != nil {
		t.Fatal(err)
	}
	// A catalog suppression keyed by (provider, entry_ref) -- must survive the
	// delete so a re-subscribe still honors the blacklist.
	if _, err := db.ExecContext(ctx, `
INSERT INTO catalog_suppressions(id, provider, provider_entry_ref, title_snapshot)
VALUES ('del-sup', 'mikan', 'del-entry-ref', 'Suppressed Title')`); err != nil {
		t.Fatal(err)
	}

	// CAS mismatch must refuse the delete.
	var conflictErr *apperrors.VersionConflict
	err = service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: 999})
	if !errors.As(err, &conflictErr) || conflictErr.Owner != "subscription" || conflictErr.CurrentVersion != 1 {
		t.Fatalf("stale CAS must return subscription version conflict, got %v", err)
	}
	// Nothing should have been deleted on the failed attempt.
	if animetest.CountRows(t, db, "subscriptions") != 1 || animetest.CountRows(t, db, "managed_anime") != 1 {
		t.Fatal("failed CAS delete must not remove any rows")
	}

	// Retire with the correct expected_version. No local owner, replay record,
	// candidate link, activity, or suppression is deleted or unlinked.
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: 1}); err != nil {
		t.Fatalf("retire subscription: %v", err)
	}
	for _, table := range []string{
		"managed_anime", "subscriptions", "anime_metadata", "rss_sources",
		"rss_source_runtime", "releases", "download_tasks", "artifacts",
		"artifact_files", "file_operations", "anime_business_configs",
		"anime_config_overrides", "provider_group_selections", "subscription_log_entries",
	} {
		if got := animetest.CountRows(t, db, table); got == 0 {
			t.Fatalf("retirement unexpectedly deleted %s", table)
		}
	}
	state, epoch := lifecycleForTest(t, db, animeID)
	if state != "retired" || epoch != 2 {
		t.Fatalf("lifecycle = %s/%d", state, epoch)
	}
	var candAnimeID, actAnimeID, epAnimeID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT activated_managed_anime_id FROM anime_config_candidates WHERE id = 'del-cand'`).Scan(&candAnimeID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT managed_anime_id FROM routine_activities WHERE id = 'del-act'`).Scan(&actAnimeID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT managed_anime_id FROM diagnostic_episodes WHERE id = 'del-ep'`).Scan(&epAnimeID); err != nil {
		t.Fatal(err)
	}
	if !candAnimeID.Valid || candAnimeID.String != animeID || !actAnimeID.Valid || !epAnimeID.Valid {
		t.Fatalf("retirement changed retained references candidate=%v activity=%v diagnostic=%v", candAnimeID, actAnimeID, epAnimeID)
	}
	if animetest.CountRows(t, db, "idempotency_records") < 2 || animetest.CountRows(t, db, "catalog_suppressions") != 1 {
		t.Fatal("retirement removed replay or independent suppression evidence")
	}
	if err := service.DeleteAnimeSubscription(ctx, animeID, SubscriptionDeleteInput{ExpectedVersion: 1}); err != nil {
		t.Fatalf("repeat retirement must be idempotent: %v", err)
	}
}
