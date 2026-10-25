package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
)

// Keyword search persists a `search` snapshot and never a feed one. These tests
// pin the snapshot's identity, its derived scope, the short retention facts it
// reports, and the exact-read rules: a search identity is only served by a
// search row, and a feed identity is only served by a feed row.

// searchTestStore is a snapshot store that persists the write it receives, so a
// test can assert exactly what the application published.
type searchTestStore struct {
	written   []catalogdomain.ProviderFeedSnapshotWrite
	persisted catalogdomain.ProviderFeedSnapshot
	readErr   error
}

func (s *searchTestStore) ReadLatestProviderFeedSnapshot(context.Context, string, string, string) (catalogdomain.ProviderFeedSnapshot, error) {
	return catalogdomain.ProviderFeedSnapshot{}, sql.ErrNoRows
}

func (s *searchTestStore) ReadProviderFeedSnapshotByID(_ context.Context, id string) (catalogdomain.ProviderFeedSnapshot, error) {
	if s.readErr != nil {
		return catalogdomain.ProviderFeedSnapshot{}, s.readErr
	}
	if s.persisted.ID != id {
		return catalogdomain.ProviderFeedSnapshot{}, sql.ErrNoRows
	}
	return s.persisted, nil
}

func (s *searchTestStore) PersistProviderFeedSnapshot(_ context.Context, input catalogdomain.ProviderFeedSnapshotWrite) (catalogdomain.ProviderFeedSnapshot, error) {
	if err := input.Validate(); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	s.written = append(s.written, input)
	snapshot := catalogdomain.ProviderFeedSnapshot{
		ID: "search-snapshot-1", Provider: input.Provider, ProviderEntryRef: input.ProviderEntryRef,
		ProviderGroupRef: input.ProviderGroupRef, SnapshotKind: input.NormalizedKind(), SearchParserVersion: input.SearchParserVersion,
		Version: 1, FetchedAt: input.FetchedAt, FreshUntil: input.FreshUntil, RetentionUntil: input.FetchedAt.Add(24 * time.Hour),
		ContentHash: input.ContentHash, HashSchema: input.HashSchema,
		FreshnessState: catalogdomain.FeedSnapshotFresh, Seeds: input.Seeds,
	}
	s.persisted = snapshot
	return snapshot, nil
}

// searchTestTorznabBody is one placeholder AniBT Torznab answer.
func searchTestTorznabBody(infohash string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel><title>AniBT</title>
<item><title>[示例字幕组] 示例作品 - 03 [1080p]</title><guid>rel_sample</guid>
<link>https://anibt.example.test/api/torrent/rel_sample.torrent</link>
<pubDate>Thu, 22 Oct 2026 02:00:47 +0800</pubDate>
<enclosure url="https://anibt.example.test/api/torrent/rel_sample.torrent" length="2048" type="application/x-bittorrent" />
<torznab:attr name="size" value="2048" /><torznab:attr name="infohash" value="` + infohash + `" />
</item></channel></rss>`
}

func newSearchTestApplication(t *testing.T, store *searchTestStore, upstream *httptest.Server) SeedSearchApplication {
	t.Helper()
	service := NewProviderService(nil, nil)
	service.SetFeedSnapshotStore(store)
	return SeedSearchApplication{
		Service: service,
		Endpoints: CatalogSearchEndpointsFunc(func(context.Context, string) (string, *http.Client, error) {
			return upstream.URL, upstream.Client(), nil
		}),
		Now: func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) },
		// The fixture upstream listens on the loopback interface; production keeps
		// this list empty so a search can never reach a private address.
		AllowPrivateHosts: []string{"127.0.0.1"},
	}
}

func TestFetchSearchSnapshotPersistsASearchKindSnapshot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/torznab/api" || r.URL.Query().Get("t") != "search" {
			t.Errorf("unexpected upstream request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if got := r.URL.Query().Get("q"); got != "示例字幕组 示例作品 03" {
			t.Errorf("upstream q = %q", got)
		}
		w.Write([]byte(searchTestTorznabBody("fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e")))
	}))
	defer upstream.Close()

	store := &searchTestStore{}
	application := newSearchTestApplication(t, store, upstream)
	preview, err := application.Fetch(context.Background(), FetchSearchSnapshotInput{
		Provider: "anibt", Query: "  示例字幕组  示例作品   03 ",
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(store.written) != 1 {
		t.Fatalf("published %d snapshots, want exactly 1", len(store.written))
	}
	write := store.written[0]
	if write.NormalizedKind() != catalogdomain.FeedSnapshotKindSearch {
		t.Fatalf("published kind = %q, want search", write.NormalizedKind())
	}
	if err := catalogdomain.ValidateSearchSnapshotRef(write.ProviderEntryRef, catalogdomain.SearchSnapshotScopePrefix); err != nil {
		t.Fatalf("scope ref = %q: %v", write.ProviderEntryRef, err)
	}
	if err := catalogdomain.ValidateSearchSnapshotRef(write.ProviderGroupRef, catalogdomain.SearchSnapshotQueryPrefix); err != nil {
		t.Fatalf("query ref = %q: %v", write.ProviderGroupRef, err)
	}
	// The scope is derived from the normalized query, so two spellings of the
	// same query share one snapshot instead of superseding each other.
	if write.ProviderEntryRef != SearchScopeRef("anibt", upstream.URL, "示例字幕组 示例作品 03") {
		t.Fatal("scope ref is not the derived query scope")
	}
	if write.ProviderEntryRef == SearchScopeRef("anibt", upstream.URL, "示例字幕组 示例作品 04") {
		t.Fatal("two different queries share one scope ref")
	}
	if write.ProviderEntryRef == SearchScopeRef("mikan", upstream.URL, "示例字幕组 示例作品 03") {
		t.Fatal("two providers share one scope ref")
	}
	// The snapshot is fresh for an hour and retained for a day; both are stated
	// in the response so a client never has to guess.
	if got := write.FreshUntil.Sub(write.FetchedAt); got != defaultSearchFreshWindow {
		t.Fatalf("fresh window = %v, want %v", got, defaultSearchFreshWindow)
	}
	if preview.Query != "示例字幕组 示例作品 03" {
		t.Fatalf("preview query = %q", preview.Query)
	}
	if preview.RetainedTill.Sub(preview.FetchedAt) != 24*time.Hour {
		t.Fatalf("retention = %v", preview.RetainedTill.Sub(preview.FetchedAt))
	}
	if write.SearchParserVersion != torznabContractVersion || preview.Parser != torznabContractVersion {
		t.Fatalf("parser evidence write=%q view=%q", write.SearchParserVersion, preview.Parser)
	}
	if len(preview.Seeds) != 1 || preview.TotalSeeds != 1 {
		t.Fatalf("preview seeds = %d total = %d", len(preview.Seeds), preview.TotalSeeds)
	}
	if preview.Seeds[0].InfoHash == nil || *preview.Seeds[0].InfoHash != "fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e" {
		t.Fatalf("seed identity = %+v", preview.Seeds[0])
	}
	// Display-only title facts are parsed for the row and never become identity;
	// the parsed work-title candidate pre-fills nothing on its own.
	if preview.Seeds[0].GroupName != "示例字幕组" || preview.Seeds[0].Resolution != "1080p" {
		t.Fatalf("display facts = %+v", preview.Seeds[0])
	}
	if preview.Seeds[0].TitleHint != "示例作品" {
		t.Fatalf("title hint = %q, want the parsed work-title candidate", preview.Seeds[0].TitleHint)
	}
}

// A search snapshot is never persisted when the upstream answer is an error, an
// empty unusable page or a refused protocol answer: the store sees no write.
func TestFetchSearchSnapshotNeverPersistsOnUpstreamFailure(t *testing.T) {
	cases := map[string]struct {
		status int
		header http.Header
		body   string
	}{
		"torznab error": {status: http.StatusOK, body: `<?xml version="1.0"?><error code="201" description="bad parameter"/>`},
		"http 500":      {status: http.StatusInternalServerError, body: "boom"},
		"empty items":   {status: http.StatusOK, body: `<?xml version="1.0"?><rss version="2.0"><channel><title>AniBT</title><item><title></title><guid>x</guid></item></channel></rss>`},
		"rate limited":  {status: http.StatusTooManyRequests, header: http.Header{"Retry-After": []string{"42"}}, body: "slow down"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for key, values := range tc.header {
					w.Header()[key] = values
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			store := &searchTestStore{}
			application := newSearchTestApplication(t, store, upstream)
			if _, err := application.Fetch(context.Background(), FetchSearchSnapshotInput{Provider: "anibt", Query: "x"}); err == nil {
				t.Fatal("a failed upstream answer produced a persisted snapshot")
			}
			if len(store.written) != 0 {
				t.Fatalf("published %d snapshots after an upstream failure", len(store.written))
			}
		})
	}
}

// The two derived refs are the only search scope, and a caller cannot introduce
// a provider entry/group scope: the query is the only input that shapes it.
func TestFetchSearchSnapshotAcceptsOnlyAQuery(t *testing.T) {
	store := &searchTestStore{}
	service := NewProviderService(nil, nil)
	service.SetFeedSnapshotStore(store)
	application := SeedSearchApplication{
		Service: service,
		Endpoints: CatalogSearchEndpointsFunc(func(context.Context, string) (string, *http.Client, error) {
			return "", nil, errors.New("upstream must not be reached")
		}),
	}
	for name, input := range map[string]FetchSearchSnapshotInput{
		"too long":           {Provider: "anibt", Query: strings.Repeat("a", 257)},
		"too long multibyte": {Provider: "anibt", Query: strings.Repeat("界", 257)},
		"unknown kind":       {Provider: "custom", Query: "x"},
		"other unknown":      {Provider: "nyaa", Query: "x"},
	} {
		if _, err := application.Fetch(context.Background(), input); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if len(store.written) != 0 {
		t.Fatal("a rejected request persisted a snapshot")
	}
}

// The OpenAPI bound counts characters, not UTF-8 bytes: a 256-character CJK
// query reaches the provider while the 257-character form is refused before any
// upstream request or snapshot write.
func TestSearchQueryLengthCountsCharacters(t *testing.T) {
	query := strings.Repeat("界", 256)
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if got := r.URL.Query().Get("q"); got != query {
			t.Errorf("upstream q has %d characters, want 256", len([]rune(got)))
		}
		w.Write([]byte(searchTestTorznabBody("fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e")))
	}))
	defer upstream.Close()
	store := &searchTestStore{}
	application := newSearchTestApplication(t, store, upstream)
	if _, err := application.Fetch(context.Background(), FetchSearchSnapshotInput{Provider: "anibt", Query: query}); err != nil {
		t.Fatalf("256-character query: %v", err)
	}
	if _, err := application.Fetch(context.Background(), FetchSearchSnapshotInput{Provider: "anibt", Query: query + "界"}); !errors.Is(err, ErrSearchQueryInvalid) {
		t.Fatalf("257-character query: %v", err)
	}
	if upstreamCalls != 1 || len(store.written) != 1 {
		t.Fatalf("upstream calls = %d, writes = %d: the over-limit query must be refused before either", upstreamCalls, len(store.written))
	}
}

// An exact read serves only the search row it names, and only while it is fresh.
func TestReadWindowIsExactAndFreshOnly(t *testing.T) {
	now := time.Now().UTC()
	seed := catalogdomain.ProviderFeedSnapshotSeed{
		StableSeedKey: "seed-1", Locator: "https://anibt.example.test/a.torrent",
		LocatorDisplay: "https://anibt.example.test/a.torrent", Title: "[示例字幕组] 示例作品 - 03 [1080p]",
		TorrentState: catalogdomain.TorrentAcquisitionNotApplicable,
	}
	published := now.Add(-time.Minute)
	seed.PublishedAt = &published
	seeds := []catalogdomain.ProviderFeedSnapshotSeed{seed}
	hash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	scopeRef, queryRef := SearchScopeRef("anibt", "https://anibt.example.test", "示例作品"), SearchQueryRef("示例作品")
	// The stored freshness line is read from the row, never from the caller: the
	// fixture derives it the way the SQL read does.
	makeSnapshot := func(kind catalogdomain.FeedSnapshotKind, freshUntil time.Time) catalogdomain.ProviderFeedSnapshot {
		freshness := catalogdomain.FeedSnapshotStale
		if now.Before(freshUntil) {
			freshness = catalogdomain.FeedSnapshotFresh
		}
		return catalogdomain.ProviderFeedSnapshot{
			ID: "snap-1", Provider: "anibt", ProviderEntryRef: scopeRef, ProviderGroupRef: queryRef,
			SnapshotKind: kind, SearchParserVersion: torznabContractVersion, Version: 2, FetchedAt: now.Add(-time.Minute), FreshUntil: freshUntil,
			RetentionUntil: now.Add(24 * time.Hour), ContentHash: hash, FreshnessState: freshness, Seeds: seeds,
		}
	}
	newApplication := func(snapshot catalogdomain.ProviderFeedSnapshot) SeedSearchApplication {
		store := &searchTestStore{persisted: snapshot}
		service := NewProviderService(nil, nil)
		service.SetFeedSnapshotStore(store)
		return SeedSearchApplication{Service: service, Now: func() time.Time { return now }}
	}

	preview, err := newApplication(makeSnapshot(catalogdomain.FeedSnapshotKindSearch, now.Add(time.Hour))).
		ReadWindow(context.Background(), "anibt", scopeRef, queryRef, "snap-1", hash, 2, 10, "")
	if err != nil {
		t.Fatalf("exact fresh search read: %v", err)
	}
	if len(preview.Seeds) != 1 || preview.Seeds[0].GroupName != "示例字幕组" {
		t.Fatalf("preview = %+v", preview)
	}

	if preview.Parser != torznabContractVersion {
		t.Fatalf("exact read lost parser evidence: %q", preview.Parser)
	}

	// A feed row never answers a search identity.
	if _, err := newApplication(makeSnapshot(catalogdomain.FeedSnapshotKindProviderFeed, now.Add(time.Hour))).
		ReadWindow(context.Background(), "anibt", scopeRef, queryRef, "snap-1", hash, 2, 10, ""); !errors.Is(err, catalogdomain.ErrProviderFeedSnapshotIdentityMismatch) {
		t.Fatalf("feed row served a search read: %v", err)
	}
	// A stale search row fails closed instead of being re-issued.
	if _, err := newApplication(makeSnapshot(catalogdomain.FeedSnapshotKindSearch, now.Add(-time.Minute))).
		ReadWindow(context.Background(), "anibt", scopeRef, queryRef, "snap-1", hash, 2, 10, ""); err == nil {
		t.Fatal("a stale search snapshot was served")
	}
	// Another query's scope is a different snapshot, not the latest of this one.
	if _, err := newApplication(makeSnapshot(catalogdomain.FeedSnapshotKindSearch, now.Add(time.Hour))).
		ReadWindow(context.Background(), "anibt", SearchScopeRef("anibt", "https://anibt.example.test", "别的作品"), queryRef, "snap-1", hash, 2, 10, ""); err == nil {
		t.Fatal("a mismatching scope ref was accepted")
	}
}

func TestSearchSnapshotWindowsHaveStableCursors(t *testing.T) {
	now := time.Now().UTC()
	seeds := make([]catalogdomain.ProviderFeedSnapshotSeed, 3)
	for index := range seeds {
		seeds[index] = catalogdomain.ProviderFeedSnapshotSeed{
			StableSeedKey: "seed-" + strconv.Itoa(index), Title: "示例发布",
			TorrentState: catalogdomain.TorrentAcquisitionNotApplicable,
		}
	}
	snapshot := catalogdomain.ProviderFeedSnapshot{
		Provider: "mikan", Seeds: seeds, FetchedAt: now, FreshUntil: now.Add(time.Hour),
		RetentionUntil: now.Add(24 * time.Hour), SearchParserVersion: mikanSearchContractVersion,
	}
	first := mapSearchSnapshot(snapshot, "", 2, 0)
	if first.NextCursor != "2" || len(first.Seeds) != 2 || first.Seeds[0].StableSeedKey != "seed-0" {
		t.Fatalf("first window = %+v", first)
	}
	offset, err := searchCursorOffset(first.NextCursor, len(seeds))
	if err != nil {
		t.Fatal(err)
	}
	last := mapSearchSnapshot(snapshot, "", 2, offset)
	if last.NextCursor != "" || len(last.Seeds) != 1 || last.Seeds[0].StableSeedKey != "seed-2" {
		t.Fatalf("last window = %+v", last)
	}
	for _, cursor := range []string{"-1", "01", "4", "not-a-cursor"} {
		if _, err := searchCursorOffset(cursor, len(seeds)); err == nil {
			t.Fatalf("cursor %q was accepted", cursor)
		}
	}
}

// A feed read refuses a search scope outright: search snapshots are addressed by
// the exact search read, not by a provider entry/group.
func TestFeedReadRefusesASearchScope(t *testing.T) {
	service := NewProviderService(nil, nil)
	service.SetFeedSnapshotStore(&searchTestStore{})
	_, err := service.ReadProviderEntryRSS(context.Background(), "anibt",
		SearchScopeRef("anibt", "https://anibt.example.test", "x"), SearchQueryRef("x"))
	if err == nil {
		t.Fatal("a search scope was accepted by the feed read")
	}
	if !strings.Contains(err.Error(), "keyword search snapshot") {
		t.Fatalf("error = %v, want the typed search-scope refusal", err)
	}
}
