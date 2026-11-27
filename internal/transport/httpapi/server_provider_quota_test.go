package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	acquisition "bansoko/internal/acquisition/application"
	"bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	configuration "bansoko/internal/configuration/domain"
	provideradapter "bansoko/internal/provider/adapter"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
)

type previewConnections []configuration.Connection

func (c previewConnections) ListConnections(context.Context) ([]configuration.Connection, error) {
	return c, nil
}

func TestPreviewProviderEntryRSSReturnsTypedQuota(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/RSS/Bangumi" {
			t.Errorf("RSS path=%s", r.URL.Path)
		}
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	service := catalogapplication.NewService(nil, nil)
	service.SetProviderFeedFetcher(acquisition.ConfiguredProviderFeedFetcher{
		Connections:       previewConnections{{Kind: configuration.ConnectionMikan, Endpoint: upstream.URL, State: configuration.ConnectionConfigured}},
		AllowPrivateHosts: []string{"127.0.0.1"},
	})
	service.SetFeedSnapshotStore(persistence.NewRepository(nil))
	server := &Server{deps: Dependencies{Catalog: service}}
	request := httptest.NewRequest(http.MethodPost, "/catalog/providers/mikan/entries/123/rss", strings.NewReader(`{"group_ref":"456"}`))
	response := httptest.NewRecorder()
	server.PreviewProviderEntryRSS(response, request, "mikan", "123")
	assertProviderQuotaResponse(t, response, "17")
}

func TestDiscoverMikanGroupsReturnsTypedQuota(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/Home/Bangumi/123" {
			t.Errorf("Mikan detail path = %s", r.URL.Path)
		}
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	service := catalogapplication.NewService(db, nil)
	service.SetGroupReaders(map[string]catalogapplication.GroupReader{
		"mikan": provideradapter.MikanGroupReader{Endpoint: upstream.URL, Client: upstream.Client(), Outbound: newProviderQuotaScheduler(t)},
	})
	server := &Server{deps: Dependencies{Catalog: service}}
	request := httptest.NewRequest(http.MethodPost, "/catalog/providers/mikan/entries/123/groups:discover", nil)
	response := httptest.NewRecorder()
	server.DiscoverProviderEntryGroups(response, request, "mikan", "123")
	assertProviderQuotaResponse(t, response, "17")
	response = httptest.NewRecorder()
	server.DiscoverProviderEntryGroups(response, httptest.NewRequest(http.MethodPost, request.URL.Path, nil), "mikan", "123")
	assertLocalProviderCooldown(t, response, hits.Load())
}

func TestRefreshMikanCatalogReturnsTypedQuota(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/Home/BangumiCoverFlowByDayOfWeek" {
			t.Errorf("Mikan season path = %s", r.URL.Path)
		}
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	service := catalogapplication.NewProviderService(db, map[string]catalogapplication.Reader{
		"mikan": catalogapplication.MikanHTTPReader{Endpoint: upstream.URL, Client: upstream.Client(), Outbound: newProviderQuotaScheduler(t)},
	})
	server := &Server{deps: Dependencies{Catalog: service}}
	request := httptest.NewRequest(http.MethodPost, "/catalog/providers/mikan/2026-summer/refresh", nil)
	response := httptest.NewRecorder()
	server.RefreshCatalogProvider(response, request, "mikan", "2026-summer")
	assertProviderQuotaResponse(t, response, "17")
	var recorded bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM provider_catalog_snapshots
		WHERE provider='mikan' AND season='2026-summer' AND upstream_state='rate_limited'
		AND upstream_cooldown_until > statement_timestamp())`).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("Mikan catalog rate limit persisted = %v, err = %v", recorded, err)
	}
	response = httptest.NewRecorder()
	server.RefreshCatalogProvider(response, httptest.NewRequest(http.MethodPost, request.URL.Path, nil), "mikan", "2026-summer")
	assertLocalProviderCooldown(t, response, hits.Load())
}

func assertProviderQuotaResponse(t *testing.T, response *httptest.ResponseRecorder, wait string) {
	t.Helper()
	var body generated.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 429 || response.Header().Get("Retry-After") != wait ||
		body.Code != "PROVIDER_RATE_LIMITED" || body.Upstream == nil || body.Upstream.State != "rate_limited" || body.Upstream.CooldownUntil == nil || body.Upstream.RetryAfterSeconds == nil {
		t.Fatalf("status=%d retry=%q body=%s err=%v", response.Code, response.Header().Get("Retry-After"), response.Body, err)
	}
}

func assertLocalProviderCooldown(t *testing.T, response *httptest.ResponseRecorder, hits int32) {
	t.Helper()
	var body generated.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 429 || hits != 1 ||
		body.Code != "PROVIDER_COOLDOWN" || body.Upstream == nil || body.Upstream.State != "cooldown" || response.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d upstream hits=%d retry=%q body=%s err=%v", response.Code, hits, response.Header().Get("Retry-After"), response.Body, err)
	}
}
