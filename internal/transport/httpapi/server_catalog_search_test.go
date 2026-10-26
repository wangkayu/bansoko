package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	catalogadapter "bansoko/internal/catalog/adapter"
	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	configurationdomain "bansoko/internal/configuration/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/transport/httpapi/generated"
)

// searchUpstreamFixture is a mock AniBT /api/bgm/search endpoint that records
// the exact request URI it received and serves a configurable status/body.
type searchUpstreamFixture struct {
	server    *httptest.Server
	lastRaw   string
	status    int
	body      string
	failFetch bool
}

func newSearchUpstreamFixture(t *testing.T, status int, body string) *searchUpstreamFixture {
	t.Helper()
	fixture := &searchUpstreamFixture{status: status, body: body}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.lastRaw = r.URL.RequestURI()
		if fixture.failFetch {
			panic("connection should not reach the upstream")
		}
		w.WriteHeader(fixture.status)
		io.WriteString(w, fixture.body)
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *searchUpstreamFixture) connectionSource() catalogadapter.ConnectionSource {
	return stubConnectionLister{connections: []configurationdomain.Connection{{
		ID: "mock-anibt", Kind: configurationdomain.ConnectionAniBT,
		State: configurationdomain.ConnectionConfigured, Endpoint: f.server.URL,
	}}}
}

// stubConnectionLister backs the ConfiguredReader with one saved enabled
// connection pointing at the mock upstream.
type stubConnectionLister struct {
	connections []configurationdomain.Connection
}

func (s stubConnectionLister) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return s.connections, nil
}

func newCatalogSearchServer(t *testing.T, source catalogadapter.ConnectionSource) *Server {
	t.Helper()
	configured := catalogadapter.ConfiguredReader{
		Provider:          catalogapplication.ProviderAniBT,
		Connections:       source,
		AllowPrivateHosts: []string{"127.0.0.1"},
	}
	service := catalogapplication.NewService(nil, configured, nil)
	server := &Server{deps: Dependencies{
		Catalog:       service,
		SkipAuth:      true,
		RuntimeLogger: observabilityport.NopRuntimeLogger{},
	}}
	return server
}

func serveCatalogSearch(server *Server, rawQuery string) *httptest.ResponseRecorder {
	handler := server.Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/anibt/search?"+rawQuery, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertErrorResponseCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var parsed generated.ErrorResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("response is not an ErrorResponse: %v; body=%s", err, body)
	}
	if parsed.Code != want {
		t.Fatalf("error code = %q, want %q; body=%s", parsed.Code, want, body)
	}
}

// TestSearchProviderCatalogSpecialCharactersEndToEnd sends raw URL-encoded
// queries through the full generated route into the real application service
// and mock provider upstream. Every special-character form must return 200
// with the whole query delivered to the upstream exactly once — never a split
// parameter or an INTERNAL 500 (issue #9).
func TestSearchProviderCatalogSpecialCharactersEndToEnd(t *testing.T) {
	cases := []struct {
		name     string
		rawQuery string
		query    string
		wantRaw  string
	}{
		{name: "ampersand", rawQuery: "q=a%26b", query: "a&b", wantRaw: "/api/bgm/search?q=a%26b"},
		{name: "percent", rawQuery: "q=100%25", query: "100%", wantRaw: "/api/bgm/search?q=100%25"},
		{name: "plus", rawQuery: "q=a%2Bb", query: "a+b", wantRaw: "/api/bgm/search?q=a%2Bb"},
		{name: "literal plus sign", rawQuery: "q=a+b", query: "a b", wantRaw: "/api/bgm/search?q=a+b"},
		{name: "space", rawQuery: "q=sp+ace", query: "sp ace", wantRaw: "/api/bgm/search?q=sp+ace"},
		{name: "cjk", rawQuery: "q=%E8%BF%9B%E5%87%BB%E7%9A%84%E5%B7%A8%E4%BA%BA", query: "进击的巨人", wantRaw: "/api/bgm/search?q=%E8%BF%9B%E5%87%BB%E7%9A%84%E5%B7%A8%E4%BA%BA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newSearchUpstreamFixture(t, http.StatusOK,
				`{"ok":true,"data":[{"bgmId":400602,"name":"進撃の巨人","nameCn":"进击的巨人"}]}`)
			server := newCatalogSearchServer(t, upstream.connectionSource())

			recorder := serveCatalogSearch(server, tc.rawQuery)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}
			if upstream.lastRaw != tc.wantRaw {
				t.Fatalf("upstream request URI = %s, want %s", upstream.lastRaw, tc.wantRaw)
			}
			var items []generated.CatalogItem
			if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
				t.Fatalf("response is not a CatalogItem array: %v; body=%s", err, recorder.Body.String())
			}
			if len(items) != 1 || items[0].BgmId == nil || *items[0].BgmId != "400602" {
				t.Fatalf("items = %+v", items)
			}
		})
	}
}

// TestSearchProviderCatalogUpstreamFailureIsStructured proves that an
// upstream/provider failure surfaces as the structured contract 422
// SEARCH_UPSTREAM_FAILED — never a generic 500 INTERNAL — even when the failure
// is triggered by special characters in the query.
func TestSearchProviderCatalogUpstreamFailureIsStructured(t *testing.T) {
	upstream := newSearchUpstreamFixture(t, http.StatusBadGateway, "<html>gateway error</html>")
	server := newCatalogSearchServer(t, upstream.connectionSource())

	recorder := serveCatalogSearch(server, "q=a%26b")

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", recorder.Code, recorder.Body.String())
	}
	assertErrorResponseCode(t, recorder.Body.Bytes(), "SEARCH_UPSTREAM_FAILED")
	var parsed generated.ErrorResponse
	_ = json.Unmarshal(recorder.Body.Bytes(), &parsed)
	if parsed.Details == nil || len(*parsed.Details) == 0 || (*parsed.Details)[0].Code != "PROVIDER_UPSTREAM" {
		t.Fatalf("details must carry PROVIDER_UPSTREAM, got %v", parsed.Details)
	}
}

// TestSearchProviderCatalogLongQueryIsStructured pins the contract q bound at
// the transport: a query beyond maxLength 256 returns the structured 422
// SEARCH_QUERY_INVALID instead of SEARCH_NOT_SUPPORTED or a 500.
func TestSearchProviderCatalogLongQueryIsStructured(t *testing.T) {
	upstream := newSearchUpstreamFixture(t, http.StatusOK, `{"ok":true,"data":[]}`)
	upstream.failFetch = true
	server := newCatalogSearchServer(t, upstream.connectionSource())

	recorder := serveCatalogSearch(server, "q="+strings.Repeat("a", 257))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("long query status = %d, want 422; body=%s", recorder.Code, recorder.Body.String())
	}
	assertErrorResponseCode(t, recorder.Body.Bytes(), "SEARCH_QUERY_INVALID")
}

// TestMapSearchEntryCarriesStableBroadcastDay proves a provider search hit,
// which carries no schedule fact, still answers the required broadcast_day key
// as none instead of an empty value outside the enum.
func TestMapSearchEntryCarriesStableBroadcastDay(t *testing.T) {
	item := mapSearchEntry(catalogapplication.ProviderAniBT, catalogdomain.Entry{
		ProviderEntryRef: "bgm-1", Title: "Hit", State: catalogdomain.EntryPresent,
	})
	if item.BroadcastDay != generated.BroadcastDayNone {
		t.Fatalf("search hit broadcast_day = %q, want none", item.BroadcastDay)
	}
}

// Keyword seed search is a separate surface from program search: it persists a
// short-lived `search` snapshot and returns a bounded window of its seeds. These
// tests drive the real generated route into the real application service over a
// placeholder Torznab upstream, so the contract shape and the derived scope are
// proven at the boundary.

func newSeedSearchServer(t *testing.T, endpoint string) *Server {
	t.Helper()
	service := catalogapplication.NewProviderService(nil, nil)
	service.SetFeedSnapshotStore(seedSearchTestStore{})
	search := catalogapplication.SeedSearchApplication{
		Service: service,
		Endpoints: catalogapplication.CatalogSearchEndpointsFunc(func(context.Context, string) (string, *http.Client, error) {
			return endpoint, http.DefaultClient, nil
		}),
		AllowPrivateHosts: []string{"127.0.0.1"},
	}
	return &Server{deps: Dependencies{
		SeedSearch:    search,
		SkipAuth:      true,
		RuntimeLogger: observabilityport.NopRuntimeLogger{},
	}}
}

func TestFetchSeedSearchSnapshotContract(t *testing.T) {
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>AniBT</title>
<item><title>[示例字幕组] 示例作品 - 03 [1080p]</title><guid>rel_sample</guid>
<link>https://anibt.example.test/api/torrent/rel_sample.torrent</link>
<pubDate>Thu, 22 Oct 2026 02:00:47 +0800</pubDate>
<enclosure url="https://anibt.example.test/api/torrent/rel_sample.torrent" length="2048" type="application/x-bittorrent" />
<torznab:attr name="size" value="2048" />
<torznab:attr name="infohash" value="fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e" />
</item></channel></rss>`)
	}))
	defer upstream.Close()

	server := newSeedSearchServer(t, upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/providers/anibt/seed-search-snapshots:fetch",
		strings.NewReader(`{"query":"  示例字幕组   示例作品 03 "}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if gotQuery != "示例字幕组 示例作品 03" {
		t.Fatalf("upstream query = %q", gotQuery)
	}
	var view generated.SeedSearchSnapshotView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatalf("response is not a SeedSearchSnapshotView: %v body=%s", err, recorder.Body.String())
	}
	if view.Provider != generated.SeedSearchSnapshotViewProviderAnibt {
		t.Fatalf("provider = %q", view.Provider)
	}
	if !strings.HasPrefix(string(view.ScopeRef), "search-scope:") || !strings.HasPrefix(string(view.QueryRef), "search-query:") {
		t.Fatalf("derived refs = %q / %q", view.ScopeRef, view.QueryRef)
	}
	if view.RetentionSeconds != 86400 {
		t.Fatalf("retention_seconds = %d, want the derived 24h", view.RetentionSeconds)
	}
	if view.TotalSeeds != 1 || len(view.Seeds) != 1 {
		t.Fatalf("total=%d seeds=%d", view.TotalSeeds, len(view.Seeds))
	}
	if view.Seeds[0].InfoHash == nil || *view.Seeds[0].InfoHash != "fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e" {
		t.Fatalf("seed = %+v", view.Seeds[0])
	}
	if view.Seeds[0].GroupName == nil || *view.Seeds[0].GroupName != "示例字幕组" {
		t.Fatalf("display group = %v", view.Seeds[0].GroupName)
	}
}

// A body carrying anything besides the query is refused: the scope, the
// entry/group, the stable keys and the render window are not caller input.
func TestFetchSeedSearchSnapshotRejectsExtraFields(t *testing.T) {
	server := newSeedSearchServer(t, "https://anibt.invalid")
	for name, body := range map[string]string{
		"provider_entry_ref": `{"query":"x","provider_entry_ref":"1001"}`,
		"scope_ref":          `{"query":"x","scope_ref":"search-scope:` + strings.Repeat("a", 64) + `"}`,
		"stable_seed_key":    `{"query":"x","stable_seed_key":"seed-1"}`,
		"limit":              `{"query":"x","limit":10}`,
		"empty query":        `{"query":"   "}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/providers/anibt/seed-search-snapshots:fetch", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK {
			t.Fatalf("%s was accepted: %s", name, recorder.Body.String())
		}
	}
}

// mapSeedSearchPreview keeps absent facts absent: a seed without a size does not
// render as zero bytes, and an unknown info hash does not render as an empty
// identity.
func TestMapSeedSearchPreviewKeepsMissingFactsAbsent(t *testing.T) {
	view := mapSeedSearchPreview(catalogapplication.SearchSeedPreview{
		Provider: "mikan", ScopeRef: "search-scope:" + strings.Repeat("a", 64),
		QueryRef: "search-query:" + strings.Repeat("b", 64), SnapshotID: "snap-1", Version: 1,
		ContentHash: "sha256:" + strings.Repeat("c", 64), Freshness: catalogdomain.FeedSnapshotFresh,
		Seeds: []catalogapplication.SearchSeedPreviewSeed{{
			StableSeedKey: "seed-1", Title: "[示例字幕组] 示例作品 - 03", LocatorDisplay: "anibt.example.test",
		}},
	})
	if view.Seeds[0].SizeBytes != nil || view.Seeds[0].InfoHash != nil || view.Seeds[0].PublishedAt != nil {
		t.Fatalf("absent facts were materialized: %+v", view.Seeds[0])
	}
	if view.Seeds[0].TorrentState != generated.SeedSearchSnapshotSeedTorrentStateNotApplicable {
		t.Fatalf("torrent state = %q", view.Seeds[0].TorrentState)
	}
	if view.Query != nil {
		t.Fatal("a read without a query text reported one")
	}
}

// seedSearchTestStore accepts the search publication and remembers it, so the
// handler test needs no database.
type seedSearchTestStore struct {
	written catalogdomain.ProviderFeedSnapshotWrite
}

func (s seedSearchTestStore) ReadLatestProviderFeedSnapshot(context.Context, string, string, string) (catalogdomain.ProviderFeedSnapshot, error) {
	return catalogdomain.ProviderFeedSnapshot{}, sql.ErrNoRows
}

func (seedSearchTestStore) ReadProviderFeedSnapshotByID(context.Context, string) (catalogdomain.ProviderFeedSnapshot, error) {
	return catalogdomain.ProviderFeedSnapshot{}, sql.ErrNoRows
}

func (s seedSearchTestStore) PersistProviderFeedSnapshot(_ context.Context, input catalogdomain.ProviderFeedSnapshotWrite) (catalogdomain.ProviderFeedSnapshot, error) {
	if err := input.Validate(); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	return catalogdomain.ProviderFeedSnapshot{
		ID: "search-snapshot-1", Provider: input.Provider, ProviderEntryRef: input.ProviderEntryRef,
		ProviderGroupRef: input.ProviderGroupRef, SnapshotKind: input.NormalizedKind(), SearchParserVersion: input.SearchParserVersion,
		Version: 1, FetchedAt: input.FetchedAt, FreshUntil: input.FreshUntil, RetentionUntil: input.FetchedAt.Add(24 * time.Hour),
		ContentHash: input.ContentHash, HashSchema: input.HashSchema, FreshnessState: catalogdomain.FeedSnapshotFresh, Seeds: input.Seeds,
	}, nil
}

