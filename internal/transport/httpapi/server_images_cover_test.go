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

	accessapplication "bansoko/internal/accesscontrol/application"
	identitymetadataadapter "bansoko/internal/identitymetadata/adapter"
	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/transport/httpapi/generated"
)

const testCoverKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeCoverService allows a fixed host per provider, like the real proxy's
// policy, and serves one registered key.
type fakeCoverService struct {
	allowed  map[string]string // provider -> allowed URL prefix
	registry map[string]string // key -> url
	fetchErr error
	calls    []string
}

func (f *fakeCoverService) CoverPaths(_ context.Context, provider string, urls []string) map[string]string {
	result := map[string]string{}
	for _, raw := range urls {
		if prefix, ok := f.allowed[provider]; ok && strings.HasPrefix(raw, prefix) {
			key := identitymetadataadapter.CoverKey(provider, raw)
			if f.registry == nil {
				f.registry = map[string]string{}
			}
			f.registry[key] = raw
			result[raw] = identitymetadataadapter.CoverPathPrefix + key
		}
	}
	return result
}

func (f *fakeCoverService) FetchCover(_ context.Context, key string) (io.ReadCloser, string, error) {
	f.calls = append(f.calls, key)
	if f.fetchErr != nil {
		return nil, "", f.fetchErr
	}
	return io.NopCloser(strings.NewReader("png-bytes")), "image/png", nil
}

func coverServer(covers ImageCoverService) *Server {
	return &Server{deps: Dependencies{SkipAuth: true, RuntimeLogger: observabilityport.NopRuntimeLogger{}, ImageCovers: covers}}
}

func TestReadImageCoverStreamsWithPrivateCacheAndMapsErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		status   int
		wantCode string
	}{
		{name: "ok", status: http.StatusOK},
		{name: "unknown key", err: identitymetadataadapter.ErrCoverNotFound, status: http.StatusNotFound, wantCode: "COVER_NOT_FOUND"},
		{name: "upstream failure", err: identitymetadataadapter.ErrCoverUnavailable, status: http.StatusBadGateway, wantCode: "COVER_UPSTREAM_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := coverServer(&fakeCoverService{fetchErr: tc.err})
			request := httptest.NewRequest(http.MethodGet, "/api/v1/images/cover/"+testCoverKey, nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.status, recorder.Body.String())
			}
			if tc.wantCode == "" {
				if recorder.Body.String() != "png-bytes" || recorder.Header().Get("Content-Type") != "image/png" ||
					recorder.Header().Get("Cache-Control") != "private, max-age=86400, immutable" {
					t.Fatalf("image response headers/body = %v %q", recorder.Header(), recorder.Body.String())
				}
				return
			}
			assertErrorResponseCode(t, recorder.Body.Bytes(), tc.wantCode)
		})
	}
}

func TestReadImageCoverRejectsMalformedKeysBeforeTheService(t *testing.T) {
	covers := &fakeCoverService{}
	server := coverServer(covers)
	for _, key := range []string{"short", strings.ToUpper(testCoverKey), "https:%2F%2Fevil.example%2Fa.png", testCoverKey + "0"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/images/cover/"+key, nil)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK {
			t.Errorf("key %q was served", key)
		}
	}
	// The route takes no URL parameter at all: a query URL is ignored.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/images/cover/"+testCoverKey+"?url=http://169.254.169.254/", nil)
	server.Handler().ServeHTTP(httptest.NewRecorder(), request)
	for _, key := range covers.calls {
		if key != testCoverKey {
			t.Errorf("service received %q, want only the path key", key)
		}
	}
}

func TestReadImageCoverRequiresAuthentication(t *testing.T) {
	srv, err := NewServer(Dependencies{
		DB: &sql.DB{}, Access: accessapplication.NewService(nil), Environment: "development",
		ImageCovers: &fakeCoverService{},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	response, err := http.Get(httpServer.URL + "/api/v1/images/cover/" + testCoverKey)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous cover status = %d, want 401", response.StatusCode)
	}
}

func TestProxyCatalogCoversNeverLeaksThirdPartyURLs(t *testing.T) {
	covers := &fakeCoverService{allowed: map[string]string{"mikan": "https://mikanani.me/"}}
	server := coverServer(covers)
	good, foreign := "https://mikanani.me/images/1.jpg", "https://tracker.example/x.jpg"
	items := []generated.CatalogItem{
		{Provider: generated.CatalogItemProviderMikan, CoverUrl: &good},
		{Provider: generated.CatalogItemProviderMikan, CoverUrl: &foreign},
		{Provider: generated.CatalogItemProviderMikan, CoverUrl: nil},
		{Provider: generated.CatalogItemProviderAnibt, CoverUrl: &good}, // provider has no allowed host
	}
	server.proxyCatalogCovers(context.Background(), items)
	if items[0].CoverUrl == nil || !strings.HasPrefix(*items[0].CoverUrl, "/api/v1/images/cover/") {
		t.Fatalf("allowed cover = %v, want a proxy path", items[0].CoverUrl)
	}
	for index := 1; index < len(items); index++ {
		if items[index].CoverUrl != nil {
			t.Errorf("item %d kept cover %q", index, *items[index].CoverUrl)
		}
	}
	// Fail closed when no service is wired at all.
	bare := &Server{deps: Dependencies{}}
	again := []generated.CatalogItem{{Provider: generated.CatalogItemProviderMikan, CoverUrl: &good}}
	bare.proxyCatalogCovers(context.Background(), again)
	if again[0].CoverUrl != nil {
		t.Fatalf("cover survived without a cover service: %q", *again[0].CoverUrl)
	}
}

func TestProxyAnimeListCoversUsesSubscriptionOrigin(t *testing.T) {
	covers := &fakeCoverService{allowed: map[string]string{"mikan": "https://mikanani.me/"}}
	server := coverServer(covers)
	good := "https://mikanani.me/images/1.jpg"
	poster := "/api/v1/images/poster/12"
	items := []generated.AnimeListItem{{CoverUrl: &good, PosterUrl: &poster}}
	items[0].Subscription.Origin = generated.SubscriptionOrigin("mikan")
	server.proxyAnimeListCovers(context.Background(), items)
	if items[0].CoverUrl == nil || strings.Contains(*items[0].CoverUrl, "mikanani") {
		t.Fatalf("cover_url = %v", items[0].CoverUrl)
	}
	if items[0].PosterUrl == nil || *items[0].PosterUrl != poster {
		t.Fatalf("an existing poster proxy path must be untouched: %v", items[0].PosterUrl)
	}
}

func TestProxyCandidatePostersRoutesBothProvidersThroughSameOriginPaths(t *testing.T) {
	covers := &fakeCoverService{allowed: map[string]string{"tmdb": "https://image.tmdb.org/"}}
	server := coverServer(covers)
	tmdb, foreign, bgm := "https://image.tmdb.org/t/p/w342/p.jpg", "https://evil.example/p.jpg", "https://lain.bgm.tv/pic/cover/c/aa/bb/1.jpg"
	candidates := []generated.MetadataSearchCandidate{
		{Provider: generated.MetadataSearchCandidateProviderTmdb, ProviderWorkId: "9", PosterUrl: &tmdb},
		{Provider: generated.MetadataSearchCandidateProviderTmdb, ProviderWorkId: "10", PosterUrl: &foreign},
		{Provider: generated.MetadataSearchCandidateProviderBgm, ProviderWorkId: "400602", PosterUrl: &bgm},
		{Provider: generated.MetadataSearchCandidateProviderBgm, ProviderWorkId: "not-numeric", PosterUrl: &bgm},
		{Provider: generated.MetadataSearchCandidateProviderBgm, ProviderWorkId: "5"},
	}
	server.proxyCandidatePosters(context.Background(), candidates)
	if p := candidates[0].PosterUrl; p == nil || !strings.HasPrefix(*p, "/api/v1/images/cover/") {
		t.Errorf("tmdb poster = %v", p)
	}
	if candidates[1].PosterUrl != nil {
		t.Errorf("a non-TMDB host leaked: %q", *candidates[1].PosterUrl)
	}
	if p := candidates[2].PosterUrl; p == nil || *p != "/api/v1/images/poster/400602" {
		t.Errorf("bgm poster = %v", p)
	}
	if candidates[3].PosterUrl != nil || candidates[4].PosterUrl != nil {
		t.Errorf("non-numeric or absent bgm posters must stay empty")
	}
}

// End to end through the real handler: the search response a browser receives
// carries only same-origin image paths.
func TestSearchMetadataResponseHasNoThirdPartyImageURL(t *testing.T) {
	covers := &fakeCoverService{allowed: map[string]string{"tmdb": "https://image.tmdb.org/"}}
	server := &Server{deps: Dependencies{
		SkipAuth: true, RuntimeLogger: observabilityport.NopRuntimeLogger{}, ImageCovers: covers,
		MetadataSearch: staticMetadataSearch{candidates: []identitymetadatadomain.SearchCandidate{
			{Provider: "tmdb", ProviderWorkID: "9", Title: "A", MediaKind: "tv", PosterURL: "https://image.tmdb.org/t/p/w342/p.jpg", DetailsStatus: identitymetadatadomain.DetailsStatusNotApplicable},
			{Provider: "tmdb", ProviderWorkID: "10", Title: "B", MediaKind: "movie", PosterURL: "https://elsewhere.example/p.jpg", DetailsStatus: identitymetadatadomain.DetailsStatusNotApplicable},
		}},
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/metadata/search?provider=tmdb&query=x", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "image.tmdb.org") || strings.Contains(body, "elsewhere.example") {
		t.Fatalf("response carries a third-party image URL: %s", body)
	}
	var parsed generated.MetadataSearchResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Candidates[0].PosterUrl == nil || parsed.Candidates[1].PosterUrl != nil {
		t.Fatalf("posters = %v / %v", parsed.Candidates[0].PosterUrl, parsed.Candidates[1].PosterUrl)
	}
}

type staticMetadataSearch struct {
	candidates []identitymetadatadomain.SearchCandidate
}

func (s staticMetadataSearch) SearchMetadata(context.Context, string, string, int) ([]identitymetadatadomain.SearchCandidate, error) {
	return s.candidates, nil
}
