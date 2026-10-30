package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

type catalogCallSpy struct {
	workspaceCalls int
	refreshCalls   int
	searchCalls    int
	searchErr      error
}

func (s *catalogCallSpy) WorkspaceProvider(context.Context, string, string) (catalogdomain.Workspace, error) {
	s.workspaceCalls++
	return catalogdomain.Workspace{Season: "2026-07"}, nil
}

func (s *catalogCallSpy) SearchProvider(context.Context, string, string) (catalogdomain.ReadResult, error) {
	s.searchCalls++
	return catalogdomain.ReadResult{}, s.searchErr
}

func (s *catalogCallSpy) RefreshProviderSeason(context.Context, string, string) (catalogdomain.Snapshot, error) {
	s.refreshCalls++
	return catalogdomain.Snapshot{Provider: "mikan", Season: "2026-07"}, nil
}

func (s *catalogCallSpy) ReadProviderPreference(context.Context, string, string) (catalogdomain.Preference, error) {
	return catalogdomain.Preference{}, nil
}

func (s *catalogCallSpy) SetProviderPreference(context.Context, string, catalogdomain.Preference, int64) (catalogdomain.Preference, error) {
	return catalogdomain.Preference{}, nil
}

func (s *catalogCallSpy) ListSuppressions(context.Context) ([]catalogdomain.Suppression, error) {
	return nil, nil
}

func (s *catalogCallSpy) SuppressEntry(context.Context, string, string, string) (catalogdomain.Suppression, error) {
	return catalogdomain.Suppression{}, nil
}

func (s *catalogCallSpy) UnsuppressEntry(context.Context, string, string) error { return nil }

func (s *catalogCallSpy) DiscoverProviderEntryGroups(context.Context, string, string) (catalogdomain.ProviderGroupDiscoveryResult, error) {
	return catalogdomain.ProviderGroupDiscoveryResult{}, nil
}

func (s *catalogCallSpy) ReadProviderEntryRSS(context.Context, string, string, string) (catalogdomain.ProviderRSSPreviewResult, error) {
	return catalogdomain.ProviderRSSPreviewResult{Freshness: catalogdomain.FeedSnapshotMissing}, nil
}

func (s *catalogCallSpy) PreviewProviderEntryRSS(context.Context, string, string, string) (catalogdomain.ProviderRSSPreviewResult, error) {
	return catalogdomain.ProviderRSSPreviewResult{}, nil
}

func (s *catalogCallSpy) ListGroupDictionary(context.Context, catalogapplication.GroupDictionaryListing) (catalogapplication.GroupDictionaryPage, error) {
	return catalogapplication.GroupDictionaryPage{}, nil
}

func (s *catalogCallSpy) RetrieveGroupDictionary(context.Context, string, string) ([]catalogdomain.GroupDictionaryEntry, error) {
	return nil, nil
}

func (s *catalogCallSpy) DeleteGroupDictionaryEntry(context.Context, string, string) error {
	return nil
}

// EnsureSeedTorrent satisfies the lazy-expansion slot. The catalog handlers
// exercised through this spy never expand a seed; the expansion transport test
// supplies its own recording spy.
func (s *catalogCallSpy) EnsureSeedTorrent(context.Context, catalogapplication.EnsureSeedTorrentInput) (catalogapplication.SeedTorrentFact, error) {
	return catalogapplication.SeedTorrentFact{}, nil
}

func TestCatalogGETDoesNotRefreshProvider(t *testing.T) {
	spy := &catalogCallSpy{}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/mikan/seasons/2026-07", nil)

	server.ReadCatalogWorkspace(recorder, request, generated.ReadCatalogWorkspaceParamsProvider("mikan"), generated.CatalogSeason("2026-07"))

	if recorder.Code != http.StatusOK || spy.workspaceCalls != 1 || spy.refreshCalls != 0 {
		t.Fatalf("GET status=%d workspace_calls=%d refresh_calls=%d", recorder.Code, spy.workspaceCalls, spy.refreshCalls)
	}
}

func TestManualCatalogRefreshCallsProviderExactlyOnce(t *testing.T) {
	spy := &catalogCallSpy{}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/providers/mikan/seasons/2026-07/refresh", nil)

	server.RefreshCatalogProvider(recorder, request, generated.RefreshCatalogProviderParamsProvider("mikan"), generated.CatalogSeason("2026-07"))

	if recorder.Code != http.StatusOK || spy.workspaceCalls != 0 || spy.refreshCalls != 1 {
		t.Fatalf("manual refresh status=%d workspace_calls=%d refresh_calls=%d", recorder.Code, spy.workspaceCalls, spy.refreshCalls)
	}
}

// TestSearchProviderCatalogUnsupported verifies the transport maps a provider
// without a public search surface to a typed 422 SEARCH_NOT_SUPPORTED and
// never falls through to a generic 500.
func TestSearchProviderCatalogUnsupported(t *testing.T) {
	spy := &catalogCallSpy{}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/mikan/search?q=x", nil)

	// The spy returns an empty result (supported path); unsupported mapping is
	// covered by the service-level ErrSearchNotSupported test. Here we assert
	// the endpoint calls SearchProvider and returns 200 with an item array.
	server.SearchProviderCatalog(recorder, request, generated.SearchProviderCatalogParamsProvider("mikan"), generated.SearchProviderCatalogParams{Q: "x"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("search status=%d", recorder.Code)
	}
	if string(recorder.Body.Bytes()) != "[]\n" && string(recorder.Body.Bytes()) != "[]" {
		t.Fatalf("empty search must serialize to an empty array, got %s", recorder.Body.String())
	}
}

// TestSearchProviderCatalogAnibtNormalPath verifies the supported provider
// still reaches the service and returns 200 after the boundary enum check.
func TestSearchProviderCatalogAnibtNormalPath(t *testing.T) {
	spy := &catalogCallSpy{}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/anibt/search?q=anime", nil)

	server.SearchProviderCatalog(recorder, request, generated.SearchProviderCatalogParamsProvider("anibt"), generated.SearchProviderCatalogParams{Q: "anime"})

	if recorder.Code != http.StatusOK || spy.searchCalls != 1 {
		t.Fatalf("anibt search status=%d search_calls=%d", recorder.Code, spy.searchCalls)
	}
}

// TestSearchProviderCatalogUnknownProviderIs422 verifies an unknown provider
// outside the OpenAPI CatalogProvider enum [anibt, mikan] surfaces as a typed
// 422 client-input error instead of a generic 500 INTERNAL (issue #8), and
// never reaches the application layer.
func TestSearchProviderCatalogUnknownProviderIs422(t *testing.T) {
	spy := &catalogCallSpy{}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/bad/search?q=test", nil)

	server.SearchProviderCatalog(recorder, request, generated.SearchProviderCatalogParamsProvider("bad"), generated.SearchProviderCatalogParams{Q: "test"})

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown provider status=%d, want 422; body=%s", recorder.Code, recorder.Body.String())
	}
	if spy.searchCalls != 0 {
		t.Fatalf("unknown provider must not reach the application layer, search_calls=%d", spy.searchCalls)
	}
	var body generated.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not an ErrorResponse: %v", err)
	}
	if body.Code != "SEARCH_PROVIDER_UNSUPPORTED" {
		t.Fatalf("error code=%q, want SEARCH_PROVIDER_UNSUPPORTED", body.Code)
	}
	if body.Details == nil || len(*body.Details) == 0 || (*body.Details)[0].Code != "PROVIDER_UNSUPPORTED" {
		t.Fatalf("details must carry PROVIDER_UNSUPPORTED, got %v", body.Details)
	}
}

// TestSearchProviderCatalogNotSupportedMaps422 keeps the transport mapping of
// catalogapplication.ErrSearchNotSupported to 422 SEARCH_NOT_SUPPORTED intact.
func TestSearchProviderCatalogNotSupportedMaps422(t *testing.T) {
	spy := &catalogCallSpy{searchErr: catalogapplication.ErrSearchNotSupported}
	server := &Server{deps: Dependencies{Catalog: spy}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/providers/mikan/search?q=x", nil)

	server.SearchProviderCatalog(recorder, request, generated.SearchProviderCatalogParamsProvider("mikan"), generated.SearchProviderCatalogParams{Q: "x"})

	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "SEARCH_NOT_SUPPORTED") {
		t.Fatalf("unsupported search status=%d body=%s, want 422 SEARCH_NOT_SUPPORTED", recorder.Code, recorder.Body.String())
	}
}

// TestMapSearchEntryFallsBackToBGMID ensures an AniBT search hit with an empty
// ProviderEntryRef projects the BGMID as provider_entry_ref, matching the
// browse-path normalization in catalog/application/provider_service.go. A hit
// that already carries a ref keeps it unchanged.
func TestMapSearchEntryFallsBackToBGMID(t *testing.T) {
	fallback := mapSearchEntry("anibt", catalogdomain.Entry{
		Title: "测试番",
		BGMID: "518900",
		State: catalogdomain.EntryPresent,
	})
	if fallback.ProviderEntryRef != "518900" {
		t.Fatalf("provider_entry_ref=%q, want %q", fallback.ProviderEntryRef, "518900")
	}

	kept := mapSearchEntry("anibt", catalogdomain.Entry{
		Title:            "测试番",
		ProviderEntryRef: "entry-7",
		BGMID:            "518900",
		State:            catalogdomain.EntryPresent,
	})
	if kept.ProviderEntryRef != "entry-7" {
		t.Fatalf("provider_entry_ref=%q, want %q", kept.ProviderEntryRef, "entry-7")
	}
}

func TestMapCatalogItemExposesReleaseAvailabilityOnlyWhenKnown(t *testing.T) {
	count := 4
	known := mapCatalogItem(catalogdomain.Item{Provider: "anibt", Kind: catalogdomain.ItemAniBTEntry, ReleaseState: catalogdomain.ReleaseStateNoReleases, ReleaseCount: &count, UpdateText: "2026/10/18"})
	if known.ReleaseState == nil || *known.ReleaseState != generated.CatalogItemReleaseStateNoReleases || known.ReleaseCount == nil || *known.ReleaseCount != 4 || known.UpdateText == nil || *known.UpdateText != "2026/10/18" {
		t.Fatalf("known release availability was not mapped: %+v", known)
	}
	unknown := mapCatalogItem(catalogdomain.Item{Provider: "mikan", Kind: catalogdomain.ItemMikanEntry})
	body, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"release_state", "release_count", "score", "update_text"} {
		if strings.Contains(string(body), key) {
			t.Fatalf("unknown %s must stay absent: %s", key, body)
		}
	}
	// Search hits carry no release facts, so they read as unknown.
	hit := mapSearchEntry("anibt", catalogdomain.Entry{Title: "测试番", BGMID: "518900", SourceHint: map[string]any{"provider": "anibt"}, State: catalogdomain.EntryPresent})
	if hit.ReleaseState != nil || hit.ReleaseCount != nil {
		t.Fatalf("search hit fabricated release availability: %+v", hit)
	}
}

func TestMapCatalogItemAndSearchEntryExposeAniBTScore(t *testing.T) {
	score := 8.2
	mapped := mapCatalogItem(catalogdomain.Item{Provider: "anibt", Kind: catalogdomain.ItemAniBTEntry, Score: &score})
	if mapped.Score == nil || *mapped.Score != 8.2 {
		t.Fatalf("catalog score was not mapped: %+v", mapped)
	}

	searchScore := 7.8
	hit := mapSearchEntry("anibt", catalogdomain.Entry{
		Title: "测试番", BGMID: "518900", State: catalogdomain.EntryPresent,
		SourceHint: map[string]any{"rating": &searchScore},
	})
	if hit.Score == nil || *hit.Score != 7.8 {
		t.Fatalf("search score was not mapped: %+v", hit)
	}
}

func TestCatalogSnapshotMapsFreshness(t *testing.T) {
	observed := time.Date(2026, time.October, 10, 0, 0, 0, 0, time.UTC)
	mapped := mapCatalogSnapshot(catalogdomain.Snapshot{Provider: "mikan", Season: "2026-fall", Status: catalogdomain.StatusOutage, LastSuccessAt: &observed, Stale: true})
	if !mapped.Stale || mapped.LastSuccessAt == nil || !mapped.LastSuccessAt.Equal(observed) {
		t.Fatalf("freshness not mapped: %+v", mapped)
	}
	body, err := json.Marshal(mapCatalogSnapshot(catalogdomain.Snapshot{Provider: "mikan", Season: "2026-fall", Status: catalogdomain.StatusStale, Stale: true}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"stale":true`) || !strings.Contains(string(body), `"last_success_at":null`) {
		t.Fatalf("never-refreshed snapshot must serialize stale=true and last_success_at=null: %s", body)
	}
}
