package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	draftapplication "bansoko/internal/anime/draft/application"
	"bansoko/internal/apperrors"
	filemaintenancepolicy "bansoko/internal/filemaintenance/policy"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/testutil/animetest"
)

func requireBusinessCode(t *testing.T, err error, status int, code, what string) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != status || business.Code != code {
		t.Fatalf("%s: err=%v, want %d %s", what, err, status, code)
	}
}

// duplicateConfigFingerprint renders every copied configuration value of one
// instance as one comparable string; ids, owner versions and timestamps are
// left out because the copy is a new owner.
func duplicateConfigFingerprint(t *testing.T, db *sql.DB, animeID string) string {
	t.Helper()
	var value string
	err := db.QueryRowContext(context.Background(), `
SELECT concat_ws(' | ',
  (SELECT concat_ws(',', ma.title, ma.title_override, ma.provider_title, s.origin, s.origin_entry_ref)
     FROM managed_anime ma JOIN subscriptions s ON s.managed_anime_id = ma.id WHERE ma.id = $1),
  (SELECT concat_ws(',', bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, media_kind, total_episodes, year, season)
     FROM anime_metadata WHERE managed_anime_id = $1),
  (SELECT concat_ws(',', parser_kind, parser_source_default_version, admission_mode, quality_profile::text,
                    replacement_source_default_version, unclassified_route, file_effect_source_default_version,
                    candidate_id, revision, business_snapshot::text, business_digest)
     FROM anime_business_configs WHERE managed_anime_id = $1),
  (SELECT string_agg(concat_ws(',', owner_type, section_name, schema_version, value::text), ';' ORDER BY section_name)
     FROM anime_config_overrides WHERE managed_anime_id = $1),
  (SELECT string_agg(concat_ws(',', src.source_kind, src.locator, src.locator_display, src.group_name, src.title, src.episode_offset,
                               src.anibt_aux_mode, src.provenance::text, src.http_feed_connection_ref, src.network_route_ref), ';'
                     ORDER BY src.locator)
     FROM rss_sources src JOIN subscriptions s ON s.id = src.subscription_id WHERE s.managed_anime_id = $1),
  (SELECT string_agg(concat_ws(',', g.provider, g.group_ref, g.display_name, g.selection_mode, g.manual_selected,
                               g.effective_selected, g.effective_score, g.available), ';' ORDER BY g.group_ref)
     FROM provider_group_selections g JOIN subscriptions s ON s.id = g.subscription_id WHERE s.managed_anime_id = $1)
)`, animeID).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// A copy is a new pending subscription carrying the source's current saved
// configuration and nothing else: no history, no work, the source unchanged.
// Replay returns the same copy, its first start is a normal first start, and
// a retired source is refused.
func TestDuplicateSubscriptionCopiesConfigIntoNewPendingInstance(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	ctx := context.Background()

	source := ownershipSubscribeTestDraft(t, service, "duplicate-source-0001")
	sourceID := source.ManagedAnime.Id
	// Edit the saved configuration the copy must take over.
	for _, statement := range []string{
		`UPDATE managed_anime SET title_override = 'Custom Title', provider_title = 'Provider Title' WHERE id = $1`,
		`UPDATE anime_metadata SET year = 2026, season = 0, total_episodes = 12, media_kind = 'tv' WHERE managed_anime_id = $1`,
		`UPDATE rss_sources SET episode_offset = -12 WHERE subscription_id = (SELECT id FROM subscriptions WHERE managed_anime_id = $1)`,
		`INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
		 VALUES ($1, 'rules', 'tag_filters', 'tag-filters-v1', '{"include":["1080p"]}')
		 ON CONFLICT (managed_anime_id, section_name) DO UPDATE SET schema_version = EXCLUDED.schema_version, value = EXCLUDED.value`,
		`INSERT INTO filemaintenance_policies(scope_key, enabled, scan_interval_seconds, alignment_enabled, safety, version)
		 VALUES ('anime:' || $1, true, 3600, false, '{"max_operations":7}', 4)`,
		`INSERT INTO reconciliation_scans(managed_anime_id, state, generation, scan_scope, progress, capacity_stats, input_fingerprint)
		 VALUES ($1, 'completed', 3, '{}', '{}', '{}', 'fingerprint-1')`,
		`INSERT INTO provider_group_selections(id, subscription_id, provider, group_ref, display_name, selection_mode, manual_selected,
		                                       effective_selected, effective_score)
		 SELECT 'dup-selection-1', id, 'mikan', 'g-1', 'Group One', 'manual', true, true, 100 FROM subscriptions WHERE managed_anime_id = $1`,
	} {
		if _, err := db.ExecContext(ctx, statement, sourceID); err != nil {
			t.Fatalf("prepare source: %v\n%s", err, statement)
		}
	}
	before := duplicateConfigFingerprint(t, db, sourceID)

	_, err := service.Draft.DuplicateAnimeSubscription(ctx, sourceID, draftapplication.DuplicateSubscriptionInput{InstanceLabel: "  "}, "duplicate-key-blank")
	var invalid *apperrors.Validation
	if !errors.As(err, &invalid) || invalid.Code != "DRAFT_INVALID" {
		t.Fatalf("blank label: err = %v, want DRAFT_INVALID", err)
	}

	copied, err := service.Draft.DuplicateAnimeSubscription(ctx, sourceID, draftapplication.DuplicateSubscriptionInput{InstanceLabel: " Group One only "}, "duplicate-key-0001")
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	copyID := copied.ManagedAnime.ID
	if copyID == sourceID || copied.Subscription.State != subscriptiondomain.SubscriptionPending || copied.Subscription.InstanceLabel == nil ||
		*copied.Subscription.InstanceLabel != "Group One only" || len(copied.RssSources) != 1 || copied.RssSources[0].EpisodeOffset != -12 {
		t.Fatalf("copy = %+v", copied)
	}
	if got := duplicateConfigFingerprint(t, db, copyID); got != before {
		t.Fatalf("copied configuration differs\nsource: %s\ncopy:   %s", before, got)
	}
	if after := duplicateConfigFingerprint(t, db, sourceID); after != before {
		t.Fatalf("the source configuration changed\nbefore: %s\nafter:  %s", before, after)
	}
	var sourceState, sourceLabel string
	var sourceVersion int64
	if err := db.QueryRowContext(ctx, `SELECT state, instance_label, version FROM subscriptions WHERE managed_anime_id = $1`, sourceID).Scan(&sourceState, &sourceLabel, &sourceVersion); err != nil {
		t.Fatal(err)
	}
	if sourceState != "active" || sourceLabel != "test-instance" || sourceVersion != 1 {
		t.Fatalf("source subscription changed: %s %s v%d", sourceState, sourceLabel, sourceVersion)
	}
	// Nothing but the copy's own creation entry: no work, releases or copied logs.
	var work, releases, logs, runtimes int
	if err := db.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM work_items wi JOIN rss_sources src ON src.id = wi.owner_id JOIN subscriptions s ON s.id = src.subscription_id WHERE s.managed_anime_id = $1),
  (SELECT count(*) FROM releases r JOIN rss_sources src ON src.id = r.rss_source_id JOIN subscriptions s ON s.id = src.subscription_id WHERE s.managed_anime_id = $1),
  (SELECT count(*) FROM subscription_log_entries l JOIN subscriptions s ON s.id = l.subscription_id WHERE s.managed_anime_id = $1),
  (SELECT count(*) FROM rss_source_runtime rt JOIN rss_sources src ON src.id = rt.rss_source_id JOIN subscriptions s ON s.id = src.subscription_id
    WHERE s.managed_anime_id = $1 AND rt.cursor IS NULL AND rt.last_good_facts IS NULL AND rt.last_refresh_at IS NULL)`, copyID).Scan(&work, &releases, &logs, &runtimes); err != nil {
		t.Fatal(err)
	}
	if work != 0 || releases != 0 || logs != 1 || runtimes != 1 {
		t.Fatalf("copy history: work=%d releases=%d logs=%d fresh runtimes=%d; want 0 0 1 1", work, releases, logs, runtimes)
	}
	// File-maintenance settings are copied as a fresh, editable version 1.
	var sourceSettings, copySettings string
	var copySettingsVersion int64
	settings := `SELECT concat_ws(',', enabled, scan_interval_seconds, alignment_enabled, safety::text) FROM filemaintenance_policies WHERE scope_key = 'anime:' || $1`
	if err := db.QueryRowContext(ctx, settings, sourceID).Scan(&sourceSettings); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, settings, copyID).Scan(&copySettings); err != nil {
		t.Fatalf("the copy has no file maintenance settings: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version FROM filemaintenance_policies WHERE scope_key = 'anime:' || $1`, copyID).Scan(&copySettingsVersion); err != nil {
		t.Fatal(err)
	}
	if copySettings != sourceSettings || copySettingsVersion != 1 {
		t.Fatalf("copy file maintenance settings = %q v%d, want %q v1", copySettings, copySettingsVersion, sourceSettings)
	}
	edited, err := filemaintenancepolicy.NewPolicyService(db).SavePolicy(ctx, "anime:"+copyID, 1, filemaintenancepolicy.PolicyRow{Enabled: false, Safety: []byte(`{"max_operations":3}`)}, "test", "test")
	if err != nil || edited.Version != 2 {
		t.Fatalf("edit the copy's settings: %+v, %v", edited, err)
	}
	if err := db.QueryRowContext(ctx, settings, sourceID).Scan(&copySettings); err != nil || copySettings != sourceSettings {
		t.Fatalf("editing the copy changed the source settings: %q, %v", copySettings, err)
	}
	// None of the managed file state is copied: the source's scan generation
	// stays its own, and every managed-state table is empty for the copy.
	for _, table := range []string{
		"placements", "placement_companions", "reconciliation_scans", "reconciliation_files", "alignment_operations",
		"replacement_deletions", "replacement_decisions",
	} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE managed_anime_id = $1`, copyID).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("the copy has %d %s rows, want 0", n, table)
		}
	}
	var offsetChanges int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM offset_alignment_changes ch JOIN rss_sources src ON src.id = ch.rss_source_id JOIN subscriptions sub ON sub.id = src.subscription_id WHERE sub.managed_anime_id = $1`, copyID).Scan(&offsetChanges); err != nil || offsetChanges != 0 {
		t.Fatalf("the copy has %d offset changes, want 0: %v", offsetChanges, err)
	}
	var coverage int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM placement_coverage c JOIN placements p ON p.id = c.placement_id WHERE p.managed_anime_id = $1`, copyID).Scan(&coverage); err != nil {
		t.Fatal(err)
	}
	var sourceScans int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reconciliation_scans WHERE managed_anime_id = $1`, sourceID).Scan(&sourceScans); err != nil {
		t.Fatal(err)
	}
	if coverage != 0 || sourceScans != 1 {
		t.Fatalf("copy placement coverage = %d, source scans = %d; want 0 and 1", coverage, sourceScans)
	}

	replayed, err := service.Draft.DuplicateAnimeSubscription(ctx, sourceID, draftapplication.DuplicateSubscriptionInput{InstanceLabel: "Group One only"}, "duplicate-key-0001")
	if err != nil || replayed.ManagedAnime.ID != copyID {
		t.Fatalf("replay = %+v, %v; want the first copy %s", replayed.ManagedAnime, err, copyID)
	}
	_, err = service.Draft.DuplicateAnimeSubscription(ctx, sourceID, draftapplication.DuplicateSubscriptionInput{InstanceLabel: "Another"}, "duplicate-key-0001")
	requireBusinessCode(t, err, 409, "IDEMPOTENCY_KEY_REUSED", "same key, different label")
	var copies int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM subscriptions WHERE managed_anime_id <> $1`, sourceID).Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Fatalf("replay created %d copies, want 1", copies)
	}

	// The copy's first start is a normal first start: no resume baseline,
	// the initial refresh is scheduled.
	if _, err := service.PatchAnimeSubscription(ctx, copyID, SubscriptionPatchInput{ExpectedVersion: 1, State: subscriptiondomain.SubscriptionActive}); err != nil {
		t.Fatalf("start the copy: %v", err)
	}
	var resumed bool
	var trigger string
	if err := db.QueryRowContext(ctx, `SELECT resumed_at IS NOT NULL FROM subscriptions WHERE managed_anime_id = $1`, copyID).Scan(&resumed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT input_snapshot->>'trigger' FROM work_items WHERE kind = 'rss_refresh' AND owner_id = $1`, copied.RssSources[0].ID).Scan(&trigger); err != nil {
		t.Fatalf("the copy's first start must schedule the initial refresh: %v", err)
	}
	if resumed || trigger != "subscription_started" {
		t.Fatalf("copy first start: resumed=%t trigger=%q", resumed, trigger)
	}

	// A retired source is refused; an earlier copy still replays.
	if err := service.DeleteAnimeSubscription(ctx, sourceID, SubscriptionDeleteInput{ExpectedVersion: sourceVersion}); err != nil {
		t.Fatalf("retire the source: %v", err)
	}
	_, err = service.Draft.DuplicateAnimeSubscription(ctx, sourceID, draftapplication.DuplicateSubscriptionInput{InstanceLabel: "Late"}, "duplicate-key-0002")
	requireBusinessCode(t, err, 409, "MANAGED_ANIME_RETIRED", "duplicate a retired source")
	if replayed, err := service.Draft.DuplicateAnimeSubscription(ctx, sourceID, draftapplication.DuplicateSubscriptionInput{InstanceLabel: "Group One only"}, "duplicate-key-0001"); err != nil || replayed.ManagedAnime.ID != copyID {
		t.Fatalf("replay after retirement = %+v, %v", replayed.ManagedAnime, err)
	}
	var notFound *apperrors.NotFound
	if _, err := service.Draft.DuplicateAnimeSubscription(ctx, "missing-anime", draftapplication.DuplicateSubscriptionInput{InstanceLabel: "x"}, "duplicate-key-0003"); !errors.As(err, &notFound) {
		t.Fatalf("missing source: err = %v, want not found", err)
	}
}
