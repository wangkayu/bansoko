package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		Provider:    catalogapplication.ProviderAniBT,
		Connections: source,
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
