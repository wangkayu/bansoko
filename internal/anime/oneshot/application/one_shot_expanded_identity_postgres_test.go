package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/testutil/animetest"
)

// PostgreSQL coverage for a seed's identity under lazy expansion. The snapshot
// holds feed facts only; a seed whose feed published no infohash takes the
// infohash of its expanded (fetched and cached) torrent, and a feed infohash the
// fetched torrent contradicts refuses the download with a typed reason.

type countingSeedTorrentFetcher struct {
	mu      sync.Mutex
	payload acquisitionapplication.TorrentPayload
	calls   int
}

func (f *countingSeedTorrentFetcher) FetchPayload(context.Context, string) (acquisitionapplication.TorrentPayload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.payload, nil
}

func (f *countingSeedTorrentFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type lazySeedFixture struct {
	service *oneShotTestServices
	catalog *catalogapplication.Service
	fetcher *countingSeedTorrentFetcher
	payload animeapplication.TorrentPayload
	input   AcquireSeedInput
	item    acquisitiondomain.FeedItem
}

// lazySeedOneShotFixture persists a feed-only snapshot (no torrent bytes) with
// one seed, wires the real expansion cache and a counting torrent fetcher, and
// returns a human admission input in the reference form the UI sends.
func lazySeedOneShotFixture(t *testing.T, publishedInfoHash string) lazySeedFixture {
	t.Helper()
	db := animetest.OpenTestDB(t)
	payload := parseOneShotTestTorrent(t)
	// The agent-path fetcher must never be used by a human snapshot seed.
	service := oneShotNewTestService(t, db, &oneShotFakeTorrentFetcher{err: errors.New("the agent fetcher must not run for a snapshot seed")}, nil)
	animetest.SeedQbConnection(t, db)

	const entryRef, groupRef = "70021", "678"
	item := acquisitiondomain.FeedItem{
		GUID:              "seed-lazy-01",
		Title:             "[Group] Example Anime - 01 [1080p]",
		Enclosure:         "https://torrent.example.test/lazy-01.torrent",
		Published:         time.Now().UTC().Truncate(time.Microsecond),
		TorrentInfoHashV1: publishedInfoHash,
	}
	catalogService := catalogapplication.NewService(db, nil)
	catalogService.SetProviderFeedFetcher(&fixedCatalogFeedFetcher{items: []acquisitiondomain.FeedItem{item}})
	repository := catalogpersistence.NewRepository(db)
	catalogService.SetFeedSnapshotStore(repository)
	catalogService.SetSeedTorrentCache(repository)
	fetcher := &countingSeedTorrentFetcher{payload: acquisitionapplication.TorrentPayload{Bytes: payload.Bytes, Metadata: payload.Metadata}}
	catalogService.SetSeedTorrentFetcher(fetcher)
	service.SetSeedPreviewSource(animeapplication.SeedPreviewSourceFromProviderFeedSnapshot(catalogService.ReadProviderEntryRSSByIdentity, catalogService.ReadProviderEntrySeedPayload))

	// The admission renders anime.group from the display name the newest group
	// observation of the seed's entry published, so the fixture records the
	// observation this entry's group is bound to.
	recordOneShotSeedGroup(t, db, SelectedSeed{Provider: animedomain.ProviderKindMikan, ProviderEntryRef: entryRef, ProviderGroupRef: groupRef}, oneShotSeedGroupName)

	snapshot, err := catalogService.RefreshProviderEntryRSS(context.Background(), "mikan", entryRef, groupRef)
	if err != nil {
		t.Fatalf("refresh snapshot: %v", err)
	}
	reference := SelectedSeed{
		Provider:            animedomain.ProviderKindMikan,
		ProviderEntryRef:    entryRef,
		ProviderGroupRef:    groupRef,
		SnapshotID:          snapshot.SnapshotID,
		SnapshotVersion:     snapshot.SnapshotVersion,
		SnapshotContentHash: snapshot.ContentHash,
		StableSeedKey:       item.GUID,
	}
	return lazySeedFixture{
		service: service,
		catalog: catalogService,
		fetcher: fetcher,
		payload: payload,
		input:   humanCandidateForSeed(t, service, reference),
		item:    item,
	}
}

func (f lazySeedFixture) expand(t *testing.T) catalogapplication.SeedTorrentFact {
	t.Helper()
	seed := f.input.Seed
	fact, err := f.catalog.EnsureSeedTorrent(context.Background(), catalogapplication.EnsureSeedTorrentInput{
		Provider: string(seed.Provider), ProviderEntryRef: seed.ProviderEntryRef, ProviderGroupRef: seed.ProviderGroupRef,
		SnapshotID: seed.SnapshotID, SnapshotVersion: seed.SnapshotVersion, ContentHash: seed.SnapshotContentHash,
		StableSeedKey: seed.StableSeedKey,
	})
	if err != nil {
		t.Fatalf("expand seed: %v", err)
	}
	return fact
}

// A seed whose feed published no infohash is downloadable once expanded: the
// expanded torrent's infohash becomes its identity, is frozen with the marker,
// and the same request replays to the same admission without a second fetch.
func TestAcquireSeedOnceAdmitsExpandedSeedWithoutFeedInfoHash(t *testing.T) {
	fixture := lazySeedOneShotFixture(t, "")
	ctx := context.Background()
	db := fixture.service.Service.db

	if fact := fixture.expand(t); fact.State != catalogapplication.SeedTorrentStateAvailable {
		t.Fatalf("expansion = %+v, want available", fact)
	}
	result, err := fixture.service.AcquireSeedOnce(ctx, fixture.input, "one-shot-lazy-expanded-0001")
	if err != nil {
		t.Fatalf("admit expanded seed: %v", err)
	}
	if result.DownloadTaskID == nil || animetest.CountRows(t, db, "download_tasks") != 1 {
		t.Fatal("the expanded seed created no download task")
	}
	// The one-shot item's identity falls back to the seed's torrent URL, which
	// can carry a tracker key: the release stores its digest, as RSS does.
	var stableItemKey string
	if err := db.QueryRowContext(ctx, `SELECT stable_item_key FROM releases`).Scan(&stableItemKey); err != nil {
		t.Fatal(err)
	}
	if stableItemKey == fixture.item.Enclosure || strings.Contains(stableItemKey, "torrent.example.test") || !strings.HasPrefix(stableItemKey, "sha256:") {
		t.Fatalf("release stable_item_key = %q, want the digest of the torrent URL", stableItemKey)
	}

	marker, err := fixture.service.oneShotMarkers.GetByRequestIdempotencyKey(ctx, "one-shot-lazy-expanded-0001")
	if err != nil || marker == nil {
		t.Fatalf("read marker: %v", err)
	}
	frozen := decodeOneShotInputSnapshot(marker.InputSnapshot)
	want := strings.ToLower(fixture.payload.Metadata.InfoHashV1)
	if frozen == nil || frozen.SelectedSeed.InfoHash == nil || *frozen.SelectedSeed.InfoHash != want {
		t.Fatalf("frozen seed infohash = %v, want the expanded torrent's %s", frozen, want)
	}
	replay, err := fixture.service.AcquireSeedOnce(ctx, fixture.input, "one-shot-lazy-expanded-0001")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.OneShotID != result.OneShotID || animetest.CountRows(t, db, "download_tasks") != 1 {
		t.Fatalf("replay = %+v, want the same admission %s", replay, result.OneShotID)
	}
	if fixture.fetcher.count() != 1 {
		t.Fatalf("torrent fetches = %d, want the one expansion", fixture.fetcher.count())
	}
}

// A feed infohash the fetched torrent contradicts is refused with a typed
// reason and creates nothing.
func TestAcquireSeedOnceRefusesExpandedIdentityMismatch(t *testing.T) {
	fixture := lazySeedOneShotFixture(t, "d5d6f4b8ee05c30e48f75a1b47f22b76f9a53b0e")
	ctx := context.Background()

	fact := fixture.expand(t)
	if fact.State != catalogapplication.SeedTorrentStateUnavailable || fact.ErrorCode != catalogapplication.SeedTorrentErrorIdentityMismatch {
		t.Fatalf("expansion = %+v, want unavailable/%s", fact, catalogapplication.SeedTorrentErrorIdentityMismatch)
	}
	_, err := fixture.service.AcquireSeedOnce(ctx, fixture.input, "one-shot-lazy-mismatch-0001")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "SEED_IDENTITY_MISMATCH" {
		t.Fatalf("error = %v, want SEED_IDENTITY_MISMATCH", err)
	}
	assertNoOneShotOwnerRows(t, fixture.service)
}
