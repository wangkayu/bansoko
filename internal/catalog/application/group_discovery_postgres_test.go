package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"bansoko/internal/acquisition/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/catalog/adapter/persistence"
	catalogdomain "bansoko/internal/catalog/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
)

// fakeGroupReader is a test GroupReader that returns a fixed result.
type fakeGroupReader struct {
	result providerdomain.GroupReadResult
	err    error
}

func (r fakeGroupReader) ReadGroups(context.Context, string) (providerdomain.GroupReadResult, error) {
	return r.result, r.err
}

// fakeFeedFetcher is a test ProviderFeedFetcher that returns fixed feed items.
type fakeFeedFetcher struct {
	items []domain.FeedItem
	err   error
}

func (f fakeFeedFetcher) FetchProviderFeed(context.Context, providerdomain.FeedLocator) ([]domain.FeedItem, error) {
	return f.items, f.err
}

// fakeConnectionSource returns no enabled connections so the endpoint falls
// back to the default public endpoint.
type fakeConnectionSource struct {
	connections []configurationdomain.Connection
	err         error
}

func (s fakeConnectionSource) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return s.connections, s.err
}

func TestDiscoverProviderEntryGroupsPersistsSnapshotAndReturnsGroups(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	// Use a fixed entry ref and clear any previously persisted snapshots so
	// the test is deterministic within the reset disposable schema.
	const entryRef = "12345"
	clearProviderSnapshots(t, db, "mikan", entryRef)
	observedAt := time.Now().UTC().Truncate(time.Microsecond)
	inactive := false
	reader := fakeGroupReader{result: providerdomain.GroupReadResult{
		Provider:        providerdomain.ProviderMikan,
		CatalogEntryRef: entryRef,
		ObservedAt:      observedAt,
		SourceVersion:   "mikan-source-1",
		SchemaVersion:   "mikan-detail-groups-v1",
		Complete:        true, CompletenessEvidence: "mikan-detail-group-blocks-v1",
		Groups: []providerdomain.GroupSnapshotEntry{
			{
				Ref:         providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "678"},
				DisplayName: "ANi",
				FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderMikan, CatalogEntryRef: entryRef, Group: providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "678"}},
				ObservedAt:  observedAt,
				Active:      &inactive,
			},
		},
	}}
	service := NewService(db, nil)
	service.SetGroupReaders(map[string]GroupReader{"mikan": reader})
	service.SetProviderConnectionSource(fakeConnectionSource{})

	result, err := service.DiscoverProviderEntryGroups(ctx, "mikan", entryRef)
	if err != nil {
		t.Fatalf("DiscoverProviderEntryGroups: %v", err)
	}
	if result.Provider != "mikan" || result.ProviderEntryRef != entryRef {
		t.Fatalf("unexpected result identity: %+v", result)
	}
	if result.SnapshotVersion != 1 {
		t.Fatalf("expected snapshot version 1, got %d", result.SnapshotVersion)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result.Groups))
	}
	if result.Groups[0].GroupRef != "678" || result.Groups[0].DisplayName != "ANi" {
		t.Fatalf("unexpected group: %+v", result.Groups[0])
	}
	if result.Groups[0].FeedURL == "" {
		t.Fatalf("expected non-empty feed_url from default endpoint fallback")
	}

	// Verify the snapshot row landed in provider_group_snapshots.
	var groupCount int
	err = db.QueryRowContext(ctx, `SELECT group_count FROM provider_group_snapshots WHERE provider = 'mikan' AND catalog_entry_ref = $1 AND version = 1`, entryRef).Scan(&groupCount)
	if err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if groupCount != 1 {
		t.Fatalf("expected group_count 1 in DB, got %d", groupCount)
	}
	var preserved bool
	if err := db.QueryRowContext(ctx, `SELECT observation->'completeness'->'value'->'complete'='true'::jsonb
        AND observation->'groups'->0->'available'->'value'='false'::jsonb
        AND observation_digest IS NOT NULL
        FROM provider_group_snapshots WHERE provider='mikan' AND catalog_entry_ref=$1`, entryRef).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("manual discovery lost evidence: %t %v", preserved, err)
	}

}

func TestDiscoverProviderEntryGroupsBumpsSnapshotVersionOnRerun(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "99999"
	clearProviderSnapshots(t, db, "anibt", entryRef)
	observedAt := time.Now().UTC().Truncate(time.Microsecond)
	reader := fakeGroupReader{result: providerdomain.GroupReadResult{
		Provider:        providerdomain.ProviderAniBT,
		CatalogEntryRef: entryRef,
		ObservedAt:      observedAt,
		SourceVersion:   "anibt-source-1",
		SchemaVersion:   "anibt-groups-v1",
		Groups:          []providerdomain.GroupSnapshotEntry{},
	}}
	service := NewService(db, nil)
	service.SetGroupReaders(map[string]GroupReader{"anibt": reader})
	service.SetProviderConnectionSource(fakeConnectionSource{})

	first, err := service.DiscoverProviderEntryGroups(ctx, "anibt", entryRef)
	if err != nil {
		t.Fatalf("first discover: %v", err)
	}
	if first.SnapshotVersion != 1 {
		t.Fatalf("expected first version 1, got %d", first.SnapshotVersion)
	}

	second, err := service.DiscoverProviderEntryGroups(ctx, "anibt", entryRef)
	if err != nil {
		t.Fatalf("second discover: %v", err)
	}
	if second.SnapshotVersion != 2 {
		t.Fatalf("expected second version 2, got %d", second.SnapshotVersion)
	}
}

func TestProviderGroupRetentionUsesDatabaseTimeAndKeepsLatestGood(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "77777"
	futureObservedAt := time.Now().UTC().Add(365 * 24 * time.Hour)
	firstReader := fakeGroupReader{result: providerdomain.GroupReadResult{
		Provider:        providerdomain.ProviderMikan,
		CatalogEntryRef: entryRef,
		ObservedAt:      futureObservedAt,
		SourceVersion:   "future-source",
		SchemaVersion:   "mikan-groups-v1",
		Groups: []providerdomain.GroupSnapshotEntry{{
			Ref:         providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "678"},
			DisplayName: "Future group",
			FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderMikan, CatalogEntryRef: entryRef, Group: providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "678"}},
			ObservedAt:  futureObservedAt,
		}},
	}}
	service := NewService(db, nil)
	service.SetGroupReaders(map[string]GroupReader{"mikan": firstReader})
	service.SetProviderConnectionSource(fakeConnectionSource{})
	first, err := service.DiscoverProviderEntryGroups(ctx, "mikan", entryRef)
	if err != nil {
		t.Fatalf("first group discovery: %v", err)
	}
	var deadlineWithinDatabaseWindow, observedInFuture bool
	if err := db.QueryRowContext(ctx, `
	SELECT retention_until <= CURRENT_TIMESTAMP + INTERVAL '30 days'
	       AND retention_until > CURRENT_TIMESTAMP + INTERVAL '29 days',
	       observed_at > CURRENT_TIMESTAMP + INTERVAL '300 days'
	FROM provider_group_snapshots
	WHERE provider = 'mikan' AND catalog_entry_ref = $1 AND version = $2`, entryRef, first.SnapshotVersion).
		Scan(&deadlineWithinDatabaseWindow, &observedInFuture); err != nil {
		t.Fatal(err)
	}
	if !deadlineWithinDatabaseWindow || !observedInFuture {
		t.Fatalf("group retention must use database time: deadline_in_window=%t observed_in_future=%t", deadlineWithinDatabaseWindow, observedInFuture)
	}

	now := time.Now().UTC()
	service.SetGroupReaders(map[string]GroupReader{"mikan": fakeGroupReader{result: providerdomain.GroupReadResult{
		Provider:        providerdomain.ProviderMikan,
		CatalogEntryRef: entryRef,
		ObservedAt:      now,
		SourceVersion:   "empty-source",
		SchemaVersion:   "mikan-groups-v1",
		Groups:          []providerdomain.GroupSnapshotEntry{},
	}}})
	latest, err := service.DiscoverProviderEntryGroups(ctx, "mikan", entryRef)
	if err != nil {
		t.Fatalf("empty group discovery: %v", err)
	}
	if latest.SnapshotVersion != 2 || len(latest.Groups) != 0 {
		t.Fatalf("empty group snapshot = %+v, want version 2 with no groups", latest)
	}
	if _, err := db.ExecContext(ctx, `
	UPDATE provider_group_snapshots
	SET retention_until = CURRENT_TIMESTAMP - INTERVAL '1 day'
	WHERE provider = 'mikan' AND catalog_entry_ref = $1`, entryRef); err != nil {
		t.Fatal(err)
	}

	var firstID string
	if err := db.QueryRowContext(ctx, `
	SELECT id FROM provider_group_snapshots
	WHERE provider = 'mikan' AND catalog_entry_ref = $1 AND version = 1`, entryRef).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	repository := persistence.NewRepository(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	purged, err := repository.PurgeExpiredProviderGroupSnapshots(ctx, txscope.Wrap(tx), 500)
	if err != nil {
		tx.Rollback()
		t.Fatalf("purge provider group snapshots: %v", err)
	}
	if purged != 1 {
		tx.Rollback()
		t.Fatalf("purged group snapshots = %d, want 1", purged)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var oldCount, snapshotCount, latestVersion, healthCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_group_snapshots WHERE id = $1`, firstID).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_group_snapshots WHERE provider = 'mikan' AND catalog_entry_ref = $1`, entryRef).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM provider_group_snapshots WHERE provider = 'mikan' AND catalog_entry_ref = $1`, entryRef).Scan(&latestVersion); err != nil {
		t.Fatal(err)
	}
	var healthState string
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MAX(state) FROM provider_group_refresh_health WHERE provider = 'mikan' AND catalog_entry_ref = $1`, entryRef).Scan(&healthCount, &healthState); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 || snapshotCount != 1 || latestVersion != 2 || healthCount != 1 || healthState != "empty" {
		t.Fatalf("group retention drifted: old=%d snapshots=%d latest=%d health=%d state=%q", oldCount, snapshotCount, latestVersion, healthCount, healthState)
	}
}

// clearProviderSnapshots removes any previously persisted snapshot/health rows
// for the given provider+entry so a test starts from a clean version-1 state.
func clearProviderSnapshots(t *testing.T, db *sql.DB, provider, entryRef string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_group_snapshots WHERE provider = $1 AND catalog_entry_ref = $2`, provider, entryRef); err != nil {
		t.Fatalf("clear snapshots: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM provider_group_refresh_health WHERE provider = $1 AND catalog_entry_ref = $2`, provider, entryRef); err != nil {
		t.Fatalf("clear health: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit clear: %v", err)
	}
}

func TestDiscoverProviderEntryGroupsRejectsInvalidProvider(t *testing.T) {
	db := openCatalogTestDB(t)
	service := NewService(db, nil)
	_, err := service.DiscoverProviderEntryGroups(context.Background(), "custom", "12345")
	if err == nil {
		t.Fatal("expected error for invalid provider")
	}
}

func TestDiscoverProviderEntryGroupsReturnsProviderErrorAs503(t *testing.T) {
	db := openCatalogTestDB(t)
	service := NewService(db, nil)
	service.SetGroupReaders(map[string]GroupReader{"mikan": fakeGroupReader{err: errors.New("upstream timeout")}})
	_, err := service.DiscoverProviderEntryGroups(context.Background(), "mikan", "12345")
	if err == nil {
		t.Fatal("expected error for provider failure")
	}
}

// countingFeedFetcher records every provider fetch so read paths can prove they
// never touch the network boundary.
type countingFeedFetcher struct {
	items []domain.FeedItem
	err   error
	calls int
}

func (f *countingFeedFetcher) FetchProviderFeed(context.Context, providerdomain.FeedLocator) ([]domain.FeedItem, error) {
	f.calls++
	return f.items, f.err
}

func feedSnapshotCount(t *testing.T, db *sql.DB, entryRef string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM provider_feed_snapshots WHERE provider = 'mikan' AND provider_entry_ref = $1`, entryRef).Scan(&count); err != nil {
		t.Fatalf("count feed snapshots: %v", err)
	}
	return count
}

func feedItems() []domain.FeedItem {
	publishedAt := time.Now().UTC().Truncate(time.Microsecond)
	return []domain.FeedItem{
		{Title: "Release 01", Published: publishedAt, Enclosure: "https://example.test/torrent/01.torrent", TorrentSizeBytes: 1024, TorrentInfoHashV1: "0123456789abcdef0123456789abcdef01234567", TorrentPayload: []byte("d8:announce4:teste")},
		{Title: "Release 02", Published: publishedAt, Link: "https://example.test/page/02"},
	}
}

func TestRefreshProviderEntryRSSPersistsSnapshotAndBumpsVersion(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "55555"
	clearProviderSnapshots(t, db, "mikan", entryRef)
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE provider = 'mikan' AND provider_entry_ref = $1`, entryRef); err != nil {
		t.Fatal(err)
	}
	fetcher := &countingFeedFetcher{items: feedItems()}
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(fetcher)
	service.SetFeedSnapshotStore(persistence.NewRepository(db))

	result, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("RefreshProviderEntryRSS: %v", err)
	}
	if result.Provider != "mikan" || result.ProviderEntryRef != entryRef || result.GroupRef != "678" {
		t.Fatalf("unexpected result identity: %+v", result)
	}
	if result.Freshness != "fresh" {
		t.Fatalf("freshness = %q, want fresh", result.Freshness)
	}
	if result.SnapshotID == "" || result.SnapshotVersion != 1 || result.ContentHash == "" {
		t.Fatalf("snapshot identity missing: %+v", result)
	}
	if len(result.Seeds) != 2 {
		t.Fatalf("expected 2 seeds, got %d", len(result.Seeds))
	}
	byURL := make(map[string]struct {
		SizeBytes             *int64
		InfoHash              *string
		ExpectedPayloadDigest *string
	}, len(result.Seeds))
	for _, seed := range result.Seeds {
		byURL[seed.DownloadURL] = struct {
			SizeBytes             *int64
			InfoHash              *string
			ExpectedPayloadDigest *string
		}{seed.SizeBytes, seed.InfoHash, seed.ExpectedPayloadDigest}
	}
	enclosure, ok := byURL["https://example.test/torrent/01.torrent"]
	if !ok {
		t.Fatalf("enclosure seed missing: %+v", result.Seeds)
	}
	if enclosure.SizeBytes == nil || *enclosure.SizeBytes != 1024 {
		t.Fatalf("expected size_bytes 1024, got %+v", enclosure.SizeBytes)
	}
	if enclosure.InfoHash == nil || *enclosure.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("expected info_hash 0123…4567, got %+v", enclosure.InfoHash)
	}
	if _, ok := byURL["https://example.test/page/02"]; !ok {
		t.Fatalf("link fallback seed missing: %+v", result.Seeds)
	}
	// The bounded torrent payload is persisted on the seed row with a verified
	// digest.
	if enclosure.ExpectedPayloadDigest == nil {
		t.Fatalf("expected payload digest on the torrent-backed seed: %+v", enclosure)
	}
	var payloadCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM provider_feed_snapshot_seeds seed
JOIN provider_feed_snapshots snapshot ON snapshot.id = seed.snapshot_id
WHERE snapshot.provider = 'mikan' AND snapshot.provider_entry_ref = $1 AND seed.payload IS NOT NULL`, entryRef).Scan(&payloadCount); err != nil {
		t.Fatalf("count feed snapshot payloads: %v", err)
	}
	if payloadCount != 1 {
		t.Fatalf("feed snapshot payload rows = %d, want 1", payloadCount)
	}
	// Refresh persists a feed snapshot; it must not write provider group
	// discovery snapshots or any runtime owner.
	if got := feedSnapshotCount(t, db, entryRef); got != 1 {
		t.Fatalf("feed snapshot rows = %d, want 1", got)
	}
	var groupSnapshots int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_group_snapshots WHERE provider = 'mikan' AND catalog_entry_ref = $1`, entryRef).Scan(&groupSnapshots); err != nil {
		t.Fatalf("query group snapshots: %v", err)
	}
	if groupSnapshots != 0 {
		t.Fatalf("feed refresh wrote %d group discovery snapshots, want 0", groupSnapshots)
	}

	// A second refresh allocates the next version under the scope lock; the old
	// snapshot is retained as history.
	second, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if second.SnapshotVersion != 2 || second.SnapshotID == result.SnapshotID {
		t.Fatalf("second refresh version/id = %d/%s, want 2 and a new id", second.SnapshotVersion, second.SnapshotID)
	}
	if second.ContentHash != result.ContentHash {
		t.Fatalf("identical feed facts must hash identically: %q vs %q", second.ContentHash, result.ContentHash)
	}
	if got := feedSnapshotCount(t, db, entryRef); got != 2 {
		t.Fatalf("feed snapshot rows = %d, want 2", got)
	}
}

func TestReadProviderEntryRSSNeverCallsFetcher(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "55556"
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE provider = 'mikan' AND provider_entry_ref = $1`, entryRef); err != nil {
		t.Fatal(err)
	}
	fetcher := &countingFeedFetcher{items: feedItems()}
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(fetcher)
	service.SetFeedSnapshotStore(persistence.NewRepository(db))

	// missing: no row, no fetch.
	missing, err := service.ReadProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("read missing snapshot: %v", err)
	}
	if missing.Freshness != "missing" || missing.SnapshotID != "" {
		t.Fatalf("missing read = %+v, want freshness missing without identity", missing)
	}
	if fetcher.calls != 0 {
		t.Fatalf("read invoked the provider fetcher %d times", fetcher.calls)
	}

	if _, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if fetcher.calls != 1 {
		t.Fatalf("refresh fetch calls = %d, want exactly 1", fetcher.calls)
	}

	read, err := service.ReadProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if read.Freshness != "fresh" || read.SnapshotVersion != 1 || len(read.Seeds) != 2 {
		t.Fatalf("read after refresh = %+v, want fresh version 1 with 2 seeds", read)
	}
	if fetcher.calls != 1 {
		t.Fatalf("read after refresh invoked the provider fetcher (calls = %d)", fetcher.calls)
	}
}

func TestRefreshProviderEntryRSSFailureKeepsLatestGoodSnapshot(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "55557"
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE provider = 'mikan' AND provider_entry_ref = $1`, entryRef); err != nil {
		t.Fatal(err)
	}
	fetcher := &countingFeedFetcher{items: feedItems()}
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(fetcher)
	service.SetFeedSnapshotStore(persistence.NewRepository(db))

	first, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}

	fetcher.err = errors.New("provider boom")
	_, err = service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "PROVIDER_FEED_FETCH_FAILED" {
		t.Fatalf("failed refresh error = %v, want PROVIDER_FEED_FETCH_FAILED", err)
	}
	if got := feedSnapshotCount(t, db, entryRef); got != 1 {
		t.Fatalf("failed refresh left %d feed snapshot rows, want 1", got)
	}
	read, err := service.ReadProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("read after failed refresh: %v", err)
	}
	if read.SnapshotID != first.SnapshotID || read.SnapshotVersion != 1 || read.Freshness != "fresh" {
		t.Fatalf("latest good snapshot changed after failed refresh: %+v", read)
	}
}

func TestPreviewProviderEntryRSSRejectsInvalidGroupRef(t *testing.T) {
	db := openCatalogTestDB(t)
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(fakeFeedFetcher{})
	_, err := service.PreviewProviderEntryRSS(context.Background(), "mikan", "12345", "not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid mikan group ref")
	}
}

func feedItemsWithTorrentFilename() []domain.FeedItem {
	items := feedItems()
	items[0].TorrentFilename = "[组] 测试番 - 01.mkv"
	return items
}

// TestReadProviderEntryRSSByIdentityLoadsExactSnapshotAndRejectsDrift proves the
// saved-preview identity read: an exact identity resolves that exact persisted
// snapshot (never "latest"), and every drift is a typed fail-closed sentinel.
func TestReadProviderEntryRSSByIdentityLoadsExactSnapshotAndRejectsDrift(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "55561"
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE provider = 'mikan' AND provider_entry_ref = $1`, entryRef); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(&countingFeedFetcher{items: feedItems()})
	service.SetFeedSnapshotStore(persistence.NewRepository(db))

	first, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	second, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if second.SnapshotVersion != 2 {
		t.Fatalf("second snapshot version = %d, want 2", second.SnapshotVersion)
	}

	identityOf := func(result catalogdomain.ProviderRSSPreviewResult) catalogdomain.ProviderRSSPreviewIdentity {
		return catalogdomain.ProviderRSSPreviewIdentity{SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, ContentHash: result.ContentHash}
	}

	// Exact v1 identity resolves v1 even though v2 is the latest.
	exact, err := service.ReadProviderEntryRSSByIdentity(ctx, "mikan", entryRef, "678", identityOf(first))
	if err != nil {
		t.Fatalf("read exact v1 identity: %v", err)
	}
	if exact.SnapshotID != first.SnapshotID || exact.SnapshotVersion != 1 {
		t.Fatalf("exact read returned %s v%d, want the requested v1 snapshot", exact.SnapshotID, exact.SnapshotVersion)
	}
	if latest, err := service.ReadProviderEntryRSSByIdentity(ctx, "mikan", entryRef, "678", identityOf(second)); err != nil || latest.SnapshotVersion != 2 {
		t.Fatalf("read exact v2 identity = %+v (%v), want v2", latest, err)
	}

	// Unknown id → not found.
	if _, err := service.ReadProviderEntryRSSByIdentity(ctx, "mikan", entryRef, "678", catalogdomain.ProviderRSSPreviewIdentity{SnapshotID: "does-not-exist", SnapshotVersion: 1, ContentHash: first.ContentHash}); !errors.Is(err, catalogdomain.ErrProviderFeedSnapshotNotFound) {
		t.Fatalf("unknown id error = %v, want ErrProviderFeedSnapshotNotFound", err)
	}
	// Version drift → identity mismatch.
	if _, err := service.ReadProviderEntryRSSByIdentity(ctx, "mikan", entryRef, "678", catalogdomain.ProviderRSSPreviewIdentity{SnapshotID: first.SnapshotID, SnapshotVersion: 99, ContentHash: first.ContentHash}); !errors.Is(err, catalogdomain.ErrProviderFeedSnapshotIdentityMismatch) {
		t.Fatalf("version drift error = %v, want ErrProviderFeedSnapshotIdentityMismatch", err)
	}
	// Content hash drift → identity mismatch.
	if _, err := service.ReadProviderEntryRSSByIdentity(ctx, "mikan", entryRef, "678", catalogdomain.ProviderRSSPreviewIdentity{SnapshotID: first.SnapshotID, SnapshotVersion: 1, ContentHash: "sha256:" + strings.Repeat("f", 64)}); !errors.Is(err, catalogdomain.ErrProviderFeedSnapshotIdentityMismatch) {
		t.Fatalf("hash drift error = %v, want ErrProviderFeedSnapshotIdentityMismatch", err)
	}
	// Scope drift (a real id read under another group) → mismatch, not latest.
	if _, err := service.ReadProviderEntryRSSByIdentity(ctx, "mikan", entryRef, "679", identityOf(first)); !errors.Is(err, catalogdomain.ErrProviderFeedSnapshotIdentityMismatch) {
		t.Fatalf("scope drift error = %v, want ErrProviderFeedSnapshotIdentityMismatch", err)
	}

	// Expiring the requested snapshot must fail closed as stale, never silently
	// resolve the newer v2.
	if _, err := db.ExecContext(ctx, `UPDATE provider_feed_snapshots SET fetched_at = CURRENT_TIMESTAMP - INTERVAL '30 minutes', fresh_until = CURRENT_TIMESTAMP - INTERVAL '15 minutes' WHERE id = $1`, first.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadProviderEntryRSSByIdentity(ctx, "mikan", entryRef, "678", identityOf(first)); !errors.Is(err, catalogdomain.ErrProviderFeedSnapshotStale) {
		t.Fatalf("stale identity error = %v, want ErrProviderFeedSnapshotStale", err)
	}
	// The newer snapshot remains independently readable.
	if _, err := service.ReadProviderEntryRSSByIdentity(ctx, "mikan", entryRef, "678", identityOf(second)); err != nil {
		t.Fatalf("newer snapshot became unreadable: %v", err)
	}
}

// TestRefreshProviderEntryRSSPersistsTorrentFilenameForReplay proves the
// persisted snapshot retains the torrent's own naming input so a saved preview
// can replay the same rendering it captured.
func TestRefreshProviderEntryRSSPersistsTorrentFilenameForReplay(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "55562"
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE provider = 'mikan' AND provider_entry_ref = $1`, entryRef); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(&countingFeedFetcher{items: feedItemsWithTorrentFilename()})
	service.SetFeedSnapshotStore(persistence.NewRepository(db))

	refreshed, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var persisted sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT seed.torrent_filename
FROM provider_feed_snapshot_seeds seed
WHERE seed.snapshot_id = $1 AND seed.locator = 'https://example.test/torrent/01.torrent'`, refreshed.SnapshotID).Scan(&persisted); err != nil {
		t.Fatalf("read persisted torrent filename: %v", err)
	}
	if !persisted.Valid || persisted.String != "[组] 测试番 - 01.mkv" {
		t.Fatalf("persisted torrent filename = %+v, want the parsed torrent name", persisted)
	}
	// A seed without a parsed torrent name stays NULL rather than a fabricated value.
	var nullName sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT seed.torrent_filename
FROM provider_feed_snapshot_seeds seed
WHERE seed.snapshot_id = $1 AND seed.locator = 'https://example.test/page/02'`, refreshed.SnapshotID).Scan(&nullName); err != nil {
		t.Fatalf("read absent torrent filename: %v", err)
	}
	if nullName.Valid {
		t.Fatalf("seed without a torrent name persisted %q, want NULL", nullName.String)
	}
}

// TestRefreshProviderEntryRSSCarriesTheAniBTTagSetThroughTheSnapshot proves the
// whole write path end to end: the extractor's tag set is frozen at mapping,
// survives publication, and is read back by the exact-identity read. The read
// model and one-shot admission take `anibt.*` from this value, so losing it here
// is what made a preview fail a release the subscribed path accepted.
func TestRefreshProviderEntryRSSCarriesTheAniBTTagSetThroughTheSnapshot(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "55564"
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE provider = 'anibt' AND provider_entry_ref = $1`, entryRef); err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Now().UTC().Truncate(time.Microsecond)
	tagged := domain.AniBTTagSet{Schema: domain.AniBTTagSchemaV1, Tags: []string{"resolution:1080p", "language:CHS"}}
	tagless := domain.AniBTTagSet{Schema: domain.AniBTTagSchemaV1, Tags: []string{}}
	fetcher := &countingFeedFetcher{items: []domain.FeedItem{
		{Title: "Tagged 01", Published: publishedAt, Enclosure: "https://anibt.example.test/torrent/tagged.torrent", Provider: "anibt", AniBTTags: &tagged},
		{Title: "Tagless 01", Published: publishedAt, Enclosure: "https://anibt.example.test/torrent/tagless.torrent", Provider: "anibt", AniBTTags: &tagless},
	}}
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(fetcher)
	service.SetFeedSnapshotStore(persistence.NewRepository(db))

	refreshed, err := service.RefreshProviderEntryRSS(ctx, "anibt", entryRef, "some-group")
	if err != nil {
		t.Fatalf("refresh an AniBT snapshot: %v", err)
	}
	read, err := service.ReadProviderEntryRSSByIdentity(ctx, "anibt", entryRef, "some-group", catalogdomain.ProviderRSSPreviewIdentity{
		SnapshotID: refreshed.SnapshotID, SnapshotVersion: refreshed.SnapshotVersion, ContentHash: refreshed.ContentHash,
	})
	if err != nil {
		t.Fatalf("read the snapshot back: %v", err)
	}
	byTitle := make(map[string]*domain.StoredAniBTTags, len(read.Seeds))
	for index := range read.Seeds {
		byTitle[read.Seeds[index].Title] = read.Seeds[index].AniBTTags
	}
	got := byTitle["Tagged 01"]
	if got == nil || got.Schema != domain.AniBTTagSchemaV1 || len(got.Tags) != 2 || got.Tags[0] != "resolution:1080p" || got.Tags[1] != "language:CHS" {
		t.Fatalf("tagged seed read back = %+v, want the frozen set", got)
	}
	// A tag-less AniBT item is a recorded fact: an empty set, not NULL.
	if empty := byTitle["Tagless 01"]; empty == nil || empty.Tags == nil || len(empty.Tags) != 0 {
		t.Fatalf("tag-less seed read back = %+v, want a non-nil empty set", empty)
	}
	// The tags stay outside the content identity: the snapshot still verifies.
	snapshot, err := persistence.NewRepository(db).ReadProviderFeedSnapshotByID(ctx, refreshed.SnapshotID)
	if err != nil {
		t.Fatalf("read the persisted snapshot: %v", err)
	}
	if err := snapshot.VerifyContentHash(); err != nil {
		t.Fatalf("content hash drifted with the tag column: %v", err)
	}
}

// TestProviderFeedSnapshotConcurrentRefreshAllocatesDistinctVersions proves the
// scope lock keeps concurrent refreshes CAS-clean: every refresh commits a
// distinct monotonic version and no snapshot body is duplicated or lost.
func TestProviderFeedSnapshotConcurrentRefreshAllocatesDistinctVersions(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	const entryRef = "55563"
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_feed_snapshots WHERE provider = 'mikan' AND provider_entry_ref = $1`, entryRef); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil)
	service.SetProviderFeedFetcher(&countingFeedFetcher{items: feedItems()})
	service.SetFeedSnapshotStore(persistence.NewRepository(db))

	const workers = 4
	versions := make(chan int64, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.RefreshProviderEntryRSS(ctx, "mikan", entryRef, "678")
			if err != nil {
				errs <- err
				return
			}
			versions <- result.SnapshotVersion
		}()
	}
	wg.Wait()
	close(versions)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent refresh failed: %v", err)
	}
	seen := make(map[int64]struct{}, workers)
	for version := range versions {
		if _, duplicate := seen[version]; duplicate {
			t.Fatalf("concurrent refresh reused version %d", version)
		}
		seen[version] = struct{}{}
	}
	if len(seen) != workers {
		t.Fatalf("distinct versions = %d, want %d", len(seen), workers)
	}
	if got := feedSnapshotCount(t, db, entryRef); got != workers {
		t.Fatalf("feed snapshot rows = %d, want %d", got, workers)
	}
	for version := int64(1); version <= workers; version++ {
		if _, ok := seen[version]; !ok {
			t.Fatalf("version %d was never allocated: %v", version, seen)
		}
	}
}
