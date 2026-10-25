package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	torrentparser "bansoko/internal/acquisition/torrent"
	"bansoko/internal/apperrors"
	catalogdomain "bansoko/internal/catalog/domain"
)

// The lazy expansion endpoint is the only place a torrent byte may be fetched
// after the preview stopped prefetching. These tests pin its contract: exactly
// one request per locator, no cache write for bytes that do not match the
// snapshot's identity, and a state result (not an error) for an unreachable or
// unusable upstream torrent.

const seedExpansionInfoHash = "d5d6f4b8ee05c30e48f75a1b47f22b76f9a53b0e"

// seedExpansionPayload returns a real single-file torrent plus the exact facts
// the parser derives, so verification compares two independent computations.
func seedExpansionPayload(t *testing.T) acquisitionapplication.TorrentPayload {
	t.Helper()
	data := []byte("d4:infod4:name8:show.mkv6:lengthi12345eee")
	metadata, err := torrentparser.Parse(data)
	if err != nil {
		t.Fatalf("parse torrent fixture: %v", err)
	}
	return acquisitionapplication.TorrentPayload{Bytes: data, Metadata: metadata}
}

func seedExpansionSeed(t *testing.T, infoHash string, state string) catalogdomain.ProviderFeedSnapshotSeed {
	t.Helper()
	seed := catalogdomain.ProviderFeedSnapshotSeed{
		StableSeedKey: "seed-1",
		Locator:       "https://mikanani.me/Download/20261017/" + infoHash + ".torrent",
		Title:         "[Group] Show - 01 [1080p]",
		TorrentState:  state,
	}
	if state == "" {
		seed.TorrentState = catalogdomain.TorrentAcquisitionNotApplicable
	}
	if infoHash != "" {
		hash := infoHash
		seed.InfoHash = &hash
	}
	return seed
}

func seedExpansionSnapshot(seed catalogdomain.ProviderFeedSnapshotSeed) catalogdomain.ProviderFeedSnapshot {
	return catalogdomain.ProviderFeedSnapshot{
		ID: "snap-1", Provider: "mikan", ProviderEntryRef: "123", ProviderGroupRef: "456",
		// The row always states its kind; expansion proves it, so a fixture that
		// omits it is not a snapshot the store could ever return.
		SnapshotKind: catalogdomain.FeedSnapshotKindProviderFeed,
		Version:      3, ContentHash: catalogdomain.DigestBytes([]byte("snapshot-content")),
		HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema,
		// Freshness is stated explicitly so the test never depends on wall time.
		FreshnessState: catalogdomain.FeedSnapshotFresh,
		Seeds:          []catalogdomain.ProviderFeedSnapshotSeed{seed},
	}
}

func seedExpansionInput(snapshot catalogdomain.ProviderFeedSnapshot) EnsureSeedTorrentInput {
	return EnsureSeedTorrentInput{
		Provider: "mikan", ProviderEntryRef: "123", ProviderGroupRef: "456",
		SnapshotID: snapshot.ID, SnapshotVersion: snapshot.Version, ContentHash: snapshot.ContentHash,
		StableSeedKey: "seed-1",
	}
}

type fakeSeedTorrentCache struct {
	mu      sync.Mutex
	entries map[string]catalogdomain.SeedTorrentCache
	writes  int
	// batches records each batch read's locators, in order, so a test can assert
	// both what the read model asked for and that it asked only once.
	batches [][]string
}

func newFakeSeedTorrentCache() *fakeSeedTorrentCache {
	return &fakeSeedTorrentCache{entries: map[string]catalogdomain.SeedTorrentCache{}}
}

func (c *fakeSeedTorrentCache) ReadSeedTorrentCache(_ context.Context, locator string) (catalogdomain.SeedTorrentCache, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[locator]
	return entry, ok, nil
}

// ReadSeedTorrentCaches mirrors the batch read. It counts the locators it was
// asked for so a test can prove a group whose seeds were all already expanded
// never reaches the cache in the first place.
func (c *fakeSeedTorrentCache) ReadSeedTorrentCaches(_ context.Context, locators []string) (map[string]catalogdomain.SeedTorrentCache, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches = append(c.batches, append([]string(nil), locators...))
	hits := map[string]catalogdomain.SeedTorrentCache{}
	for _, locator := range locators {
		if entry, ok := c.entries[locator]; ok {
			hits[locator] = entry
		}
	}
	return hits, nil
}

func (c *fakeSeedTorrentCache) UpsertSeedTorrentCache(_ context.Context, entry catalogdomain.SeedTorrentCache) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[entry.Locator] = entry
	c.writes++
	return nil
}

func (c *fakeSeedTorrentCache) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *fakeSeedTorrentCache) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

type fakeSeedTorrentFetcher struct {
	mu       sync.Mutex
	payload  acquisitionapplication.TorrentPayload
	err      error
	calls    int
	started  chan struct{}
	release  chan struct{}
	perAskFn func(locator string) (acquisitionapplication.TorrentPayload, error)
}

func (f *fakeSeedTorrentFetcher) FetchPayload(_ context.Context, locator string) (acquisitionapplication.TorrentPayload, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		<-f.release
	}
	if f.perAskFn != nil {
		return f.perAskFn(locator)
	}
	return f.payload, f.err
}

func (f *fakeSeedTorrentFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeFeedSnapshotStore struct {
	snapshot catalogdomain.ProviderFeedSnapshot
	found    bool
}

func (s fakeFeedSnapshotStore) ReadLatestProviderFeedSnapshot(context.Context, string, string, string) (catalogdomain.ProviderFeedSnapshot, error) {
	return catalogdomain.ProviderFeedSnapshot{}, sql.ErrNoRows
}

func (s fakeFeedSnapshotStore) ReadProviderFeedSnapshotByID(_ context.Context, id string) (catalogdomain.ProviderFeedSnapshot, error) {
	if !s.found || s.snapshot.ID != id {
		return catalogdomain.ProviderFeedSnapshot{}, sql.ErrNoRows
	}
	return s.snapshot, nil
}

func (s fakeFeedSnapshotStore) PersistProviderFeedSnapshot(context.Context, catalogdomain.ProviderFeedSnapshotWrite) (catalogdomain.ProviderFeedSnapshot, error) {
	return catalogdomain.ProviderFeedSnapshot{}, errors.New("not used")
}

// seedExpansionService wires one snapshot, one payload and one cache into a
// service that never touches a database.
func seedExpansionService(t *testing.T, snapshot catalogdomain.ProviderFeedSnapshot, fetcher SeedTorrentFetcher) (*Service, *fakeSeedTorrentCache) {
	t.Helper()
	service := NewProviderService(nil, nil)
	service.SetFeedSnapshotStore(fakeFeedSnapshotStore{snapshot: snapshot, found: true})
	cache := newFakeSeedTorrentCache()
	service.SetSeedTorrentCache(cache)
	if fetcher != nil {
		service.SetSeedTorrentFetcher(fetcher)
	}
	return service, cache
}

func TestEnsureSeedTorrentFetchesVerifiesAndCachesExactlyOnce(t *testing.T) {
	payload := seedExpansionPayload(t)
	snapshot := seedExpansionSnapshot(seedExpansionSeed(t, payload.Metadata.InfoHashV1, ""))
	fetcher := &fakeSeedTorrentFetcher{payload: payload}
	service, cache := seedExpansionService(t, snapshot, fetcher)

	fact, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(snapshot))
	if err != nil {
		t.Fatalf("first expansion: %v", err)
	}
	if fact.State != SeedTorrentStateAvailable || fact.Cached {
		t.Fatalf("first expansion = %+v, want an available uncached fact", fact)
	}
	if fact.Manifest == nil || fact.Manifest.FileCount() != 1 || fact.Manifest.TorrentName != "show.mkv" {
		t.Fatalf("manifest was not projected from the fetched bytes: %+v", fact.Manifest)
	}
	if fact.PayloadDigest != catalogdomain.DigestBytes(payload.Bytes) {
		t.Fatalf("payload digest = %q, want the digest of the fetched bytes", fact.PayloadDigest)
	}
	if cache.count() != 1 {
		t.Fatalf("cache rows = %d, want 1", cache.count())
	}

	second, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(snapshot))
	if err != nil {
		t.Fatalf("second expansion: %v", err)
	}
	if !second.Cached || second.State != SeedTorrentStateAvailable {
		t.Fatalf("second expansion = %+v, want an available cached fact", second)
	}
	if fetcher.callCount() != 1 {
		t.Fatalf("provider requests = %d, want exactly 1", fetcher.callCount())
	}
}

func TestEnsureSeedTorrentRejectsIdentityMismatchWithoutCaching(t *testing.T) {
	payload := seedExpansionPayload(t)
	for name, wrongHash := range map[string]string{
		"other v1 hash":     seedExpansionInfoHash,
		"unusable identity": "abc123def456",
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := seedExpansionSnapshot(seedExpansionSeed(t, wrongHash, ""))
			service, cache := seedExpansionService(t, snapshot, &fakeSeedTorrentFetcher{payload: payload})

			fact, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(snapshot))
			if err != nil {
				t.Fatalf("mismatch must be a state, not an error: %v", err)
			}
			if fact.State != SeedTorrentStateUnavailable || fact.ErrorCode != SeedTorrentErrorIdentityMismatch {
				t.Fatalf("fact = %+v, want unavailable/%s", fact, SeedTorrentErrorIdentityMismatch)
			}
			if cache.count() != 0 {
				t.Fatalf("mismatching bytes were cached (%d rows)", cache.count())
			}
		})
	}
}

// The cache is keyed by locator, so a later snapshot can name a cached locator
// with a different published infohash. The cached bytes are verified against
// that seed exactly like fetched bytes and are never served as its torrent.
func TestEnsureSeedTorrentVerifiesCacheHitAgainstSeedIdentity(t *testing.T) {
	payload := seedExpansionPayload(t)
	first := seedExpansionSnapshot(seedExpansionSeed(t, "", ""))
	fetcher := &fakeSeedTorrentFetcher{payload: payload}
	service, cache := seedExpansionService(t, first, fetcher)
	if fact, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(first)); err != nil || fact.State != SeedTorrentStateAvailable {
		t.Fatalf("expansion without a published hash = %+v, %v; want available", fact, err)
	}

	later := seedExpansionSnapshot(seedExpansionSeed(t, seedExpansionInfoHash, ""))
	later.Seeds[0].Locator = first.Seeds[0].Locator
	service.SetFeedSnapshotStore(fakeFeedSnapshotStore{snapshot: later, found: true})
	fact, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(later))
	if err != nil {
		t.Fatalf("mismatch must be a state, not an error: %v", err)
	}
	if fact.State != SeedTorrentStateUnavailable || fact.ErrorCode != SeedTorrentErrorIdentityMismatch {
		t.Fatalf("cache hit for a different published hash = %+v, want unavailable/%s", fact, SeedTorrentErrorIdentityMismatch)
	}
	if fetcher.callCount() != 1 || cache.writeCount() != 1 {
		t.Fatalf("fetches = %d, cache writes = %d; want the one original expansion only", fetcher.callCount(), cache.writeCount())
	}
}

// A hybrid torrent carries both hashes, and the snapshot publishes whichever
// one its URL/XML happened to name. The downloader accepts a published hash
// that equals either version, so expansion must accept exactly the same set:
// otherwise one payload would expand and then refuse to download.
func TestEnsureSeedTorrentAcceptsEitherVersionOfAPublishedInfoHash(t *testing.T) {
	parsedV1 := seedExpansionPayload(t)
	hybridHashV2 := strings.Repeat("c", 64)

	for name, testCase := range map[string]struct {
		seedHash string
		hybrid   bool
		accepted bool
	}{
		"snapshot v1, payload hybrid":                        {seedHash: parsedV1.Metadata.InfoHashV1, hybrid: true, accepted: true},
		"snapshot v2, payload hybrid":                        {seedHash: hybridHashV2, hybrid: true, accepted: true},
		"snapshot v1, payload v2-only with a different hash": {seedHash: parsedV1.Metadata.InfoHashV1, hybrid: false, accepted: false},
	} {
		t.Run(name, func(t *testing.T) {
			payload := seedExpansionPayload(t)
			if testCase.hybrid {
				payload.Metadata.InfoHashV2 = hybridHashV2
			} else {
				payload.Metadata.InfoHashV1 = ""
				payload.Metadata.InfoHashV2 = strings.Repeat("d", 64)
			}
			snapshot := seedExpansionSnapshot(seedExpansionSeed(t, testCase.seedHash, ""))
			service, cache := seedExpansionService(t, snapshot, &fakeSeedTorrentFetcher{payload: payload})

			fact, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(snapshot))
			if err != nil {
				t.Fatal(err)
			}
			if testCase.accepted {
				if fact.State != SeedTorrentStateAvailable || fact.Manifest == nil {
					t.Fatalf("fact = %+v, want an available manifest", fact)
				}
				if cache.count() != 1 {
					t.Fatalf("cache rows = %d, want 1", cache.count())
				}
				return
			}
			if fact.ErrorCode != SeedTorrentErrorIdentityMismatch {
				t.Fatalf("fact = %+v, want %s", fact, SeedTorrentErrorIdentityMismatch)
			}
			if cache.count() != 0 {
				t.Fatal("mismatching bytes must never be cached")
			}
		})
	}
}

func TestEnsureSeedTorrentVerifiesExpectedPayloadDigest(t *testing.T) {
	payload := seedExpansionPayload(t)
	seed := seedExpansionSeed(t, payload.Metadata.InfoHashV1, "")
	other := catalogdomain.DigestBytes([]byte("a different payload"))
	seed.PayloadDigest = &other
	snapshot := seedExpansionSnapshot(seed)
	service, cache := seedExpansionService(t, snapshot, &fakeSeedTorrentFetcher{payload: payload})

	fact, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if fact.ErrorCode != SeedTorrentErrorIdentityMismatch {
		t.Fatalf("a published digest must cover the fetched bytes: %+v", fact)
	}
	if cache.count() != 0 {
		t.Fatal("bytes that fail the published digest must never be cached")
	}
}

func TestEnsureSeedTorrentClassifiesFetchFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing", fmt.Errorf("%w: torrent source returned HTTP 404", acquisitionapplication.ErrTorrentPayloadMissing), SeedTorrentErrorMissing},
		{"parse", fmt.Errorf("%w: decode torrent bencode", acquisitionapplication.ErrTorrentPayloadInvalid), SeedTorrentErrorParseFailed},
		{"fetch", fmt.Errorf("%w: dial tcp: connection refused", acquisitionapplication.ErrTorrentPayloadFetch), SeedTorrentErrorFetchFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := seedExpansionPayload(t)
			snapshot := seedExpansionSnapshot(seedExpansionSeed(t, payload.Metadata.InfoHashV1, ""))
			service, cache := seedExpansionService(t, snapshot, &fakeSeedTorrentFetcher{err: tc.err})

			fact, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(snapshot))
			if err != nil {
				t.Fatalf("a fetch failure must be a state, not an error: %v", err)
			}
			if fact.State != SeedTorrentStateUnavailable || fact.ErrorCode != tc.want || fact.ErrorReason == "" {
				t.Fatalf("fact = %+v, want unavailable/%s with a reason", fact, tc.want)
			}
			if cache.count() != 0 {
				t.Fatal("a failed fetch must not write a cache row")
			}
		})
	}
}

func TestEnsureSeedTorrentSharesOneFetchAcrossConcurrentCalls(t *testing.T) {
	payload := seedExpansionPayload(t)
	snapshot := seedExpansionSnapshot(seedExpansionSeed(t, payload.Metadata.InfoHashV1, ""))
	fetcher := &fakeSeedTorrentFetcher{payload: payload, started: make(chan struct{}, 1), release: make(chan struct{})}
	service, cache := seedExpansionService(t, snapshot, fetcher)

	type expansionResult struct {
		fact SeedTorrentFact
		err  error
	}
	var wait sync.WaitGroup
	results := make(chan expansionResult, 2)
	input := seedExpansionInput(snapshot)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			fact, err := service.EnsureSeedTorrent(context.Background(), input)
			results <- expansionResult{fact: fact, err: err}
		}()
	}
	<-fetcher.started
	// Release the first fetch; the second call must join it instead of
	// issuing another provider request.
	close(fetcher.release)
	wait.Wait()
	close(results)

	if fetcher.callCount() != 1 {
		t.Fatalf("provider requests = %d, want 1", fetcher.callCount())
	}
	if cache.writeCount() != 1 {
		t.Fatalf("cache writes = %d, want 1", cache.writeCount())
	}
	seen := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent expansion: %v", result.err)
		}
		if result.fact.State != SeedTorrentStateAvailable || result.fact.Manifest == nil {
			t.Fatalf("concurrent result = %+v, want an available fact", result.fact)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("collected %d results, want 2", seen)
	}
}

func TestEnsureSeedTorrentServesPersistedSnapshotManifestWithoutNetwork(t *testing.T) {
	payload := seedExpansionPayload(t)
	item := acquisitionapplication.TorrentPayload{Bytes: payload.Bytes, Metadata: payload.Metadata}
	digest := catalogdomain.DigestBytes(item.Bytes)
	feedItem := acquisitiondomain.FeedItem{Title: item.Metadata.TorrentName, TorrentSizeBytes: item.Metadata.TotalSize}
	acquisitionapplication.ApplyTorrentMetadata(&feedItem, item.Metadata)
	manifest, err := torrentManifestForSeed(feedItem, digest)
	if err != nil {
		t.Fatal(err)
	}
	seed := seedExpansionSeed(t, item.Metadata.InfoHashV1, catalogdomain.TorrentAcquisitionAvailable)
	seed.Locator = "https://mikanani.me/Download/20230101/" + item.Metadata.InfoHashV1 + ".torrent"
	seed.Manifest = manifest
	seed.PayloadDigest = &digest
	snapshot := seedExpansionSnapshot(seed)
	fetcher := &fakeSeedTorrentFetcher{err: errors.New("a persisted snapshot must not fetch")}
	service, _ := seedExpansionService(t, snapshot, fetcher)

	fact, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if fact.State != SeedTorrentStateAvailable || !fact.Cached || fact.PayloadDigest != digest {
		t.Fatalf("persisted manifest fact = %+v", fact)
	}
	if fetcher.callCount() != 0 {
		t.Fatal("a snapshot that already carries its manifest must not hit the network")
	}
}

func TestEnsureSeedTorrentRejectsIdentityAndSeedDrift(t *testing.T) {
	payload := seedExpansionPayload(t)
	snapshot := seedExpansionSnapshot(seedExpansionSeed(t, payload.Metadata.InfoHashV1, ""))

	// Every drift class crosses this boundary as the typed 409 the expand
	// contract declares — never a raw domain sentinel, which the transport
	// would answer from the unknown-500 bucket.
	expectRefreshRequired := func(t *testing.T, err error) {
		t.Helper()
		var business *apperrors.Business
		if !errors.As(err, &business) || business.Status != 409 || business.Code != "FEED_SNAPSHOT_REFRESH_REQUIRED" {
			t.Fatalf("drift error = %v, want typed 409 FEED_SNAPSHOT_REFRESH_REQUIRED", err)
		}
	}

	t.Run("stale snapshot", func(t *testing.T) {
		service, cache := seedExpansionService(t, snapshot, &fakeSeedTorrentFetcher{payload: payload})
		input := seedExpansionInput(snapshot)
		input.SnapshotVersion = 2
		_, err := service.EnsureSeedTorrent(context.Background(), input)
		expectRefreshRequired(t, err)
		if cache.count() != 0 {
			t.Fatal("a drifted identity must not touch the cache")
		}
	})

	t.Run("missing snapshot", func(t *testing.T) {
		service, _ := seedExpansionService(t, snapshot, &fakeSeedTorrentFetcher{payload: payload})
		input := seedExpansionInput(snapshot)
		input.SnapshotID = "snap-other"
		_, err := service.EnsureSeedTorrent(context.Background(), input)
		expectRefreshRequired(t, err)
	})

	t.Run("expired snapshot", func(t *testing.T) {
		expired := seedExpansionSnapshot(seedExpansionSeed(t, payload.Metadata.InfoHashV1, ""))
		expired.FreshnessState = catalogdomain.FeedSnapshotStale
		service, cache := seedExpansionService(t, expired, &fakeSeedTorrentFetcher{payload: payload})
		_, err := service.EnsureSeedTorrent(context.Background(), seedExpansionInput(expired))
		expectRefreshRequired(t, err)
		if cache.count() != 0 {
			t.Fatal("an expired snapshot must not touch the cache")
		}
	})

	t.Run("seed not in snapshot", func(t *testing.T) {
		service, _ := seedExpansionService(t, snapshot, &fakeSeedTorrentFetcher{payload: payload})
		input := seedExpansionInput(snapshot)
		input.StableSeedKey = "seed-other"
		_, err := service.EnsureSeedTorrent(context.Background(), input)
		expectRefreshRequired(t, err)
	})

	t.Run("manifest read fails closed too", func(t *testing.T) {
		service, _ := seedExpansionService(t, snapshot, nil)
		identity := catalogdomain.ProviderRSSPreviewIdentity{
			SnapshotID: snapshot.ID, SnapshotVersion: snapshot.Version + 1, ContentHash: snapshot.ContentHash,
		}
		_, err := service.ReadSeedTorrentManifests(context.Background(), "mikan", "123", "456", identity)
		expectRefreshRequired(t, err)
	})
}
