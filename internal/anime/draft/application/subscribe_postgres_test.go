package application

import (
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/presetdraft"
	"bansoko/internal/transport/httpapi/generated"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

func TestLegacySubscribeSnapshotReplaySanitizesLocatorDisplay(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	ctx := context.Background()
	const key = "legacy-snapshot-replay"
	first := draftSubscribeTestDraft(t, service, key)

	legacyDisplay := "https://user:pass@example.test/private-passkey?foo=secret#fragment"
	var snapshot []byte
	if err := db.QueryRowContext(ctx, `SELECT result_snapshot FROM idempotency_records WHERE kind = 'subscribe' AND idempotency_key = $1`, key).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(snapshot, &legacy); err != nil {
		t.Fatal(err)
	}
	source := legacy["rss_sources"].([]any)[0].(map[string]any)
	source["locator"] = "https://snapshot-user:snapshot-pass@example.test/snapshot-private?token=snapshot-secret"
	source["locator_display"] = legacyDisplay
	snapshot, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE idempotency_records SET result_snapshot = $2 WHERE kind = 'subscribe' AND idempotency_key = $1`, key, snapshot); err != nil {
		t.Fatal(err)
	}

	replay, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftTestDraft(t)), Start: new(true)}, key)
	if err != nil {
		t.Fatalf("replay legacy snapshot: %v", err)
	}
	if replay.ManagedAnime.Id != first.ManagedAnime.Id || replay.RssSources[0].LocatorDisplay != "https://example.test" {
		t.Fatalf("legacy replay returned unsafe or different result: %+v", replay)
	}
	raw, err := json.Marshal(replay)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte(`"locator"`), []byte("snapshot-pass"), []byte("snapshot-private"), []byte("snapshot-secret"), []byte("foo=secret")} {
		if bytes.Contains(raw, forbidden) {
			t.Fatalf("legacy replay leaked %q: %s", forbidden, raw)
		}
	}
}

func TestSubscribeCreatesCompleteOwnerSetAtomically(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	result := draftSubscribeTestDraft(t, service, "subscribe-key-0001")

	if result.ManagedAnime.Id == "" || result.Subscription.Id == "" || len(result.RssSources) != 1 {
		t.Fatalf("incomplete subscribe result: %+v", result)
	}
	if result.Subscription.State != generated.SubscriptionStateActive {
		t.Fatalf("subscription must start active: %+v", result.Subscription)
	}
	if result.Metadata.Bgm == nil || result.Metadata.Tmdb == nil {
		t.Fatalf("metadata bindings must persist: %+v", result.Metadata)
	}
	if animetest.CountRows(t, db, "managed_anime") != 1 || animetest.CountRows(t, db, "subscriptions") != 1 {
		t.Fatal("exactly one anime and one subscription must exist")
	}
	if animetest.CountRows(t, db, "anime_metadata") != 1 || animetest.CountRows(t, db, "rss_sources") != 1 {
		t.Fatal("metadata and the custom source must persist")
	}
	if animetest.CountRows(t, db, "rss_source_runtime") != 1 {
		t.Fatal("the custom source gets an initial runtime row")
	}
	// Exactly one initial refresh WorkItem is created for the custom source.
	if animetest.CountRows(t, db, "work_items") != 1 {
		t.Fatalf("expected exactly one initial refresh work item, got %d", animetest.CountRows(t, db, "work_items"))
	}
	var kind, ownerID string
	if err := db.QueryRowContext(context.Background(), `SELECT kind, owner_id FROM work_item_history`).Scan(&kind, &ownerID); err != nil {
		t.Fatal(err)
	}
	if kind != "rss_refresh" {
		t.Fatalf("initial work item must be rss_refresh, got %s", kind)
	}
	if ownerID != result.RssSources[0].Id {
		t.Fatalf("work item must target the custom source %s, got %s", result.RssSources[0].Id, ownerID)
	}
	if animetest.CountRows(t, db, "anime_business_configs") != 1 || animetest.CountRows(t, db, "anime_config_overrides") != 7 {
		t.Fatal("subgroups/rules owners and all seven section rows must persist")
	}
	// Every per-anime section is one-time input: each row stores a complete value.
	var nullOverrides int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM anime_config_overrides WHERE value IS NULL`).Scan(&nullOverrides); err != nil {
		t.Fatal(err)
	}
	if nullOverrides != 0 {
		t.Fatalf("every stored section must carry a value, got %d without one", nullOverrides)
	}
	if animetest.CountRows(t, db, "idempotency_records") != 1 {
		t.Fatal("subscribe idempotency record must persist")
	}
	if animetest.CountRows(t, db, "subscription_log_entries") != 1 {
		t.Fatal("Subscribe must append exactly one creation log entry")
	}
	var logSubscriptionID, logAnimeID, logKind, logOutcome, logCode, logParams, logDedupeKey string
	var logSourceNull, logReleaseNull, logGroupNull, logDetailsNull bool
	if err := db.QueryRowContext(context.Background(), `
SELECT subscription_id, managed_anime_id, kind, outcome, message_code, message_params::text, dedupe_key,
       rss_source_id IS NULL, release_id IS NULL, group_ref IS NULL, details IS NULL
FROM subscription_log_entries`).Scan(
		&logSubscriptionID, &logAnimeID, &logKind, &logOutcome, &logCode, &logParams, &logDedupeKey,
		&logSourceNull, &logReleaseNull, &logGroupNull, &logDetailsNull); err != nil {
		t.Fatalf("read subscription creation log: %v", err)
	}
	if logSubscriptionID != result.Subscription.Id || logAnimeID != result.ManagedAnime.Id ||
		logKind != "subscription_created" || logOutcome != "info" || logCode != "subscription_created" ||
		logParams != "{}" || logDedupeKey != "subscription_created:subscription:"+result.Subscription.Id+":v1:subscription_created" {
		t.Fatalf("subscription creation log fields mismatch: subscription=%q anime=%q kind=%q outcome=%q code=%q params=%s dedupe=%q",
			logSubscriptionID, logAnimeID, logKind, logOutcome, logCode, logParams, logDedupeKey)
	}
	if !logSourceNull || !logReleaseNull || !logGroupNull || !logDetailsNull {
		t.Fatalf("subscription creation log must leave unrelated fields NULL: source=%v release=%v group=%v details=%v",
			logSourceNull, logReleaseNull, logGroupNull, logDetailsNull)
	}
	// The Subscribe transaction performs no external reads: the draft service
	// is built without any feed or provider port, so this is structural.
}

func TestSubscribeDynamicProviderAllowsZeroSourcesUntilDiscovery(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	provider := &recordingGroupDiscoveryProvider{db: db, result: DiscoverResult{Outcome: "no_groups"}}
	service.SetGroupDiscoveryProvider(provider)
	draft := draftTestDraft(t)
	draft.Origin = generated.SubscriptionOriginAnibt
	draft.OriginEntryRef = animetest.StringPtr("123")
	draft.InstanceLabel = nil
	draft.RssSources = nil

	result, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draft), Start: new(true)}, "subscribe-key-dynamic")
	if err != nil {
		t.Fatalf("dynamic provider subscription must allow zero sources: %v", err)
	}
	if result.Subscription.Origin != generated.SubscriptionOriginAnibt || len(result.RssSources) != 0 {
		t.Fatalf("dynamic provider must preserve origin and start without sources: %+v", result)
	}
	if provider.count() != 0 {
		t.Fatalf("direct Subscribe must not run provider discovery, got %d calls", provider.count())
	}
	if animetest.CountRows(t, db, "rss_sources") != 0 || animetest.CountRows(t, db, "work_items") != 0 {
		t.Fatal("zero-source provider subscription must not create source or refresh work")
	}
	// Every per-anime section is one-time input: the zero-source instance
	// still stores a complete value for each of the seven sections.
	var stored int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM anime_config_overrides WHERE managed_anime_id = $1 AND value IS NOT NULL`, result.ManagedAnime.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 7 {
		t.Fatalf("dynamic provider must store all seven per-anime sections, got %d", stored)
	}
}

func TestSubscribeIdempotentReplayAndConflict(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	first := draftSubscribeTestDraft(t, service, "subscribe-key-0002")

	replay, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftTestDraft(t)), Start: new(true)}, "subscribe-key-0002")
	if err != nil {
		t.Fatalf("replay with same key and body must succeed: %v", err)
	}
	if replay.ManagedAnime.Id != first.ManagedAnime.Id || replay.Subscription.Id != first.Subscription.Id {
		t.Fatalf("replay must return the original owner set: %+v", replay)
	}
	if animetest.CountRows(t, db, "managed_anime") != 1 || animetest.CountRows(t, db, "work_items") != 1 {
		t.Fatal("replay must not create new rows")
	}
	if animetest.CountRows(t, db, "subscription_log_entries") != 1 {
		t.Fatal("replay must not append a duplicate subscription creation log")
	}

	changed := draftTestDraft(t)
	changed.Title = "Different Title"
	_, err = service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, changed), Start: new(true)}, "subscribe-key-0002")
	var conflictErr *apperrors.Business
	if !errors.As(err, &conflictErr) || conflictErr.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("expected IDEMPOTENCY_KEY_REUSED, got %v", err)
	}
	if animetest.CountRows(t, db, "managed_anime") != 1 {
		t.Fatal("conflict must roll back completely")
	}
}

func TestSubscribeCreatesDistinctOwnersDespiteSharedLocatorAndAllowsMetadataHold(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)

	heldDraft := draftTestDraft(t)
	heldDraft.Metadata.Bgm = nil
	heldDraft.Metadata.Tmdb = nil
	held, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, heldDraft), Start: new(true)}, "subscribe-key-held")
	if err != nil {
		t.Fatalf("metadata-held subscription must still be created: %v", err)
	}
	if held.Subscription.State != generated.SubscriptionStateActive || held.Metadata.Bgm != nil || held.Metadata.Tmdb != nil {
		t.Fatalf("metadata-held subscription must preserve intent without bindings: %+v", held)
	}
	if animetest.CountRows(t, db, "download_tasks") != 0 {
		t.Fatal("metadata hold must not create an automatic download task")
	}

	// The same RSS locator is only an allowed external collision scenario. A new
	// explicit request creates a new formal owner; the locator is not identity or
	// a deduplication key.
	second, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftTestDraft(t)), Start: new(true)}, "subscribe-key-independent")
	if err != nil {
		t.Fatalf("independent owner with the same RSS locator: %v", err)
	}
	if second.RssSources[0].LocatorDisplay != held.RssSources[0].LocatorDisplay {
		t.Fatalf("test owners must share the same RSS locator: %q vs %q", held.RssSources[0].LocatorDisplay, second.RssSources[0].LocatorDisplay)
	}
	if second.ManagedAnime.Id == held.ManagedAnime.Id {
		t.Fatalf("distinct requests with one locator must have distinct managed owner IDs: %q", second.ManagedAnime.Id)
	}
	if second.Subscription.Id == held.Subscription.Id {
		t.Fatalf("distinct requests with one locator must have distinct subscription IDs: %q", second.Subscription.Id)
	}
	if second.RssSources[0].Id == held.RssSources[0].Id {
		t.Fatal("distinct requests with one locator must have distinct source IDs")
	}
	if animetest.CountRows(t, db, "managed_anime") != 2 || animetest.CountRows(t, db, "subscriptions") != 2 || animetest.CountRows(t, db, "rss_sources") != 2 {
		t.Fatal("shared RSS locator must not collapse separately-created owners")
	}
	if animetest.CountRows(t, db, "work_items") != 2 {
		t.Fatal("each separately-created owner must retain its own initial refresh work item")
	}
}

func TestSubscribeValidationFailuresAndOpaqueLocator(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)

	noSources := draftTestDraft(t)
	noSources.RssSources = nil
	_, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, noSources), Start: new(true)}, "subscribe-key-0003")
	var validationErr *apperrors.Validation
	if !errors.As(err, &validationErr) || validationErr.Code != "RSS_SOURCE_REQUIRED" {
		t.Fatalf("expected RSS_SOURCE_REQUIRED, got %v", err)
	}

	opaqueLocator := "opaque://feed/%zz/DO_NOT_DISCLOSE_SECRET_MARKER"
	unsafe := draftTestDraft(t)
	unsafe.RssSources[0].Locator = draftLocatorInput(t, opaqueLocator)
	subscribed, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, unsafe), Start: new(true)}, "subscribe-key-0004")
	if err != nil {
		t.Fatalf("malformed opaque locator must be accepted at Subscribe: %v", err)
	}
	if len(subscribed.RssSources) != 1 {
		t.Fatalf("Subscribe must return one safe source: %+v", subscribed.RssSources)
	}
	if subscribed.RssSources[0].LocatorDisplay != "invalid locator" {
		t.Fatalf("malformed locator display = %q, want fixed safe fallback", subscribed.RssSources[0].LocatorDisplay)
	}
	responseJSON, err := json.Marshal(subscribed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(responseJSON, []byte(opaqueLocator)) || bytes.Contains(responseJSON, []byte("DO_NOT_DISCLOSE_SECRET_MARKER")) || bytes.Contains(responseJSON, []byte(`"locator"`)) {
		t.Fatalf("SubscribeResult leaked the protected locator: %s", responseJSON)
	}
	if !bytes.Contains(responseJSON, []byte(`"locator_display":"invalid locator"`)) {
		t.Fatalf("SubscribeResult must expose the fixed safe locator_display: %s", responseJSON)
	}
	var storedLocator, storedDisplay string
	if err := db.QueryRowContext(context.Background(), `
	SELECT locator, locator_display FROM rss_sources WHERE id = $1`, subscribed.RssSources[0].Id).Scan(&storedLocator, &storedDisplay); err != nil {
		t.Fatal(err)
	}
	if storedLocator != opaqueLocator || storedDisplay != "invalid locator" {
		t.Fatalf("stored locator/display mismatch: locator=%q display=%q result=%+v", storedLocator, storedDisplay, subscribed.RssSources[0])
	}

	invalid := draftTestDraft(t)
	invalid.RssSources[0].Locator = ""
	_, err = service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, invalid), Start: new(true)}, "subscribe-key-0005")
	if !errors.As(err, &validationErr) || validationErr.Code != "RSS_LOCATOR_UNSAFE" {
		t.Fatalf("expected RSS_LOCATOR_UNSAFE for an empty locator, got %v", err)
	}

	partial := draftTestDraft(t)
	partial.Metadata.Tmdb = &generated.NullableTMDBBinding{WorkId: "", MediaKind: generated.TMDBMediaKindTv, Season: 1}
	_, err = service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, partial), Start: new(true)}, "subscribe-key-0006")
	if !errors.As(err, &validationErr) || validationErr.Code != "TMDB_BINDING_INCOMPLETE" {
		t.Fatalf("expected TMDB_BINDING_INCOMPLETE, got %v", err)
	}

	// A provider-origin draft must not carry RSS sources: provider_group sources
	// are supplied by the subgroups selection after discovery, so feeding a
	// provider RSS URL directly would bypass discovery (D4).
	provider := draftTestDraft(t)
	provider.Origin = generated.SubscriptionOriginMikan
	provider.OriginEntryRef = animetest.StringPtr("mikan-entry-1")
	provider.InstanceLabel = nil
	provider.RssSources[0].SourceKind = generated.SourceKindProviderGroup
	provider.RssSources[0].ProviderProvenance = &generated.ProviderProvenance{
		Provider: generated.ProviderKindMikan, ProviderEntryRef: "mikan-entry-1",
		ProviderGroupRef: "mikan-group-1", DiscoverySnapshotVersion: "snapshot-1",
	}
	_, err = service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, provider), Start: new(true)}, "subscribe-key-0007")
	if !errors.As(err, &validationErr) || validationErr.Code != "DRAFT_INVALID" {
		t.Fatalf("expected DRAFT_INVALID for a provider draft carrying RSS sources, got %v", err)
	}
	for _, detail := range validationErr.Details {
		if detail.Field != nil && *detail.Field == "rss_sources" {
			return
		}
	}
	t.Fatalf("validation details must flag rss_sources: %+v", validationErr.Details)

	if animetest.CountRows(t, db, "managed_anime") != 1 || animetest.CountRows(t, db, "work_items") != 1 || animetest.CountRows(t, db, "idempotency_records") != 1 {
		t.Fatal("failed validation must not add product rows")
	}
}

// Subscribe freezes the provider title of the row the provider catalog observed
// last for the draft's origin entry, and freezes none for a custom draft. The
// catalog rows are written before Subscribe: the direct Subscribe path runs no
// discovery, so the rows this test inserted are the rows the transaction reads.
func TestSubscribeFreezesProviderTitleFromCatalog(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	// A provider draft carries no sources: if Subscribe consulted discovery here,
	// the call count below would catch it.
	provider := &recordingGroupDiscoveryProvider{db: db, result: DiscoverResult{Outcome: "no_groups"}}
	service.SetGroupDiscoveryProvider(provider)
	ctx := context.Background()

	// Two snapshots observed the same entry an hour apart; the older title loses.
	entries := []struct{ snapshot, season, entry, title, observedAt string }{
		{"snapshot-provider-title-old", "2026-spring", "provider-title-entry-a", "旧标题", "2026-01-01 00:00:00+00"},
		{"snapshot-provider-title-new", "2026-summer", "provider-title-entry-m", "新标题", "2026-01-01 01:00:00+00"},
	}
	for _, entry := range entries {
		if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at)
VALUES ($1, 'mikan', $2, 'fresh', $3, 'test', $3)`, entry.snapshot, entry.season, entry.observedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_entries(id, snapshot_id, provider, provider_entry_ref, title, source_hint, state, observed_at)
VALUES ($1, $2, 'mikan', '5001', $3, '{}', 'present', $4)`, entry.entry, entry.snapshot, entry.title, entry.observedAt); err != nil {
			t.Fatal(err)
		}
	}

	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, providerDraft(t, "5001", "Test Show")), Start: new(true)}, "subscribe-provider-title")
	if err != nil {
		t.Fatalf("provider subscription: %v", err)
	}
	if provider.count() != 0 {
		t.Fatalf("direct Subscribe must not run provider discovery, got %d calls", provider.count())
	}
	if title, ok := managedProviderTitle(t, db, subscribed.ManagedAnime.Id); !ok || title != "新标题" {
		t.Fatalf("frozen provider title = (%q, %v), want the latest observed title", title, ok)
	}
	// The title is fixed at subscribe from the draft title (the dialog prefills
	// it with the provider title); naming's override slot carries it from now on.
	var fixed sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT title_override FROM managed_anime WHERE id = $1`, subscribed.ManagedAnime.Id).Scan(&fixed); err != nil {
		t.Fatal(err)
	}
	if !fixed.Valid || fixed.String != "Test Show" || subscribed.ManagedAnime.TitleOverride == nil || *subscribed.ManagedAnime.TitleOverride != "Test Show" {
		t.Fatalf("fixed title = %+v (result %v), want the draft title", fixed, subscribed.ManagedAnime.TitleOverride)
	}
	if animetest.CountRows(t, db, "provider_catalog_entries") != 2 {
		t.Fatal("Subscribe must not write the provider catalog")
	}

	// A later snapshot can observe the same entry at the same instant. The order
	// breaks that tie on id (providerTitleTx: ORDER BY observed_at DESC, id
	// DESC), so the frozen title stays deterministic instead of depending on the
	// order the rows happen to come back in.
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at)
VALUES ('snapshot-provider-title-tie', 'mikan', '2026-autumn', 'fresh', '2026-01-01 01:00:00+00', 'test', '2026-01-01 01:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_entries(id, snapshot_id, provider, provider_entry_ref, title, source_hint, state, observed_at)
VALUES ('provider-title-entry-z', 'snapshot-provider-title-tie', 'mikan', '5001', '并列标题', '{}', 'present', '2026-01-01 01:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	tied, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, providerDraft(t, "5001", "Test Show")), Start: new(true)}, "subscribe-provider-title-tie")
	if err != nil {
		t.Fatalf("provider subscription with a tied observation: %v", err)
	}
	if title, ok := managedProviderTitle(t, db, tied.ManagedAnime.Id); !ok || title != "并列标题" {
		t.Fatalf("tied-observation provider title = (%q, %v), want the highest id at that instant", title, ok)
	}

	// A custom draft names no origin entry, so it freezes no provider title even
	// though the catalog carries the entry.
	custom := draftSubscribeTestDraft(t, service, "subscribe-provider-title-custom")
	if title, ok := managedProviderTitle(t, db, custom.ManagedAnime.Id); ok {
		t.Fatalf("custom subscription froze provider title %q, want NULL", title)
	}
}

// A one-shot draft's total episode count is the planned length the owner will
// later compare progress against. Admission inserts the metadata row itself, so
// this asserts the shared provisioner writes the column rather than silently
// dropping a field the draft carried.
func TestProvisionsTotalEpisodesFromTheDraft(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	draft := draftTestDraft(t)
	total := 24
	draft.Metadata.TotalEpisodes = &total

	result, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draft), Start: new(true)}, "subscribe-total-episodes")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if result.Metadata.TotalEpisodes == nil || *result.Metadata.TotalEpisodes != total {
		t.Fatalf("subscribe result total_episodes = %v, want %d", result.Metadata.TotalEpisodes, total)
	}
	var stored sql.NullInt64
	if err := db.QueryRowContext(context.Background(), `SELECT total_episodes FROM anime_metadata WHERE managed_anime_id = $1`, result.ManagedAnime.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Valid || stored.Int64 != int64(total) {
		t.Fatalf("anime_metadata.total_episodes = %v, want %d", stored, total)
	}
}

// managedProviderTitle reads one anime's frozen provider title: the value and
// whether the column holds a title at all (false when it is NULL).
func managedProviderTitle(t *testing.T, db *sql.DB, animeID string) (string, bool) {
	t.Helper()
	var title sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT provider_title FROM managed_anime WHERE id = $1`, animeID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	return title.String, title.Valid
}
