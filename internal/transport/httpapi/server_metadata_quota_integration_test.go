package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	configuration "bansoko/internal/configuration/domain"
	metadata "bansoko/internal/identitymetadata/adapter"
	"bansoko/internal/provider/outbound"
	"bansoko/internal/transport/httpapi/generated"
)

func TestManualMetadataSearchSharesProviderCooldown(t *testing.T) {
	for _, provider := range []struct {
		name, method, path string
		kind               configuration.ConnectionKind
	}{
		{"bgm", http.MethodPost, "/v0/search/subjects", configuration.ConnectionBGM},
		{"tmdb", http.MethodGet, "/3/search/multi", configuration.ConnectionTMDB},
	} {
		t.Run(provider.name, func(t *testing.T) {
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != provider.method || r.URL.Path != provider.path {
					t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer upstream.Close()
			endpoint := upstream.URL
			if provider.kind == configuration.ConnectionTMDB {
				endpoint += "/3"
			}
			connections := previewConnections{{Kind: provider.kind, Endpoint: endpoint, State: configuration.ConnectionConfigured, SecretRef: "secret-ref"}}
			search := metadata.NewSearchService(connections, automaticSearchSecret("test-api-key"))
			search.SetOutbound(newProviderQuotaScheduler(t))
			server := &Server{deps: Dependencies{MetadataSearch: search}}
			request := func() *httptest.ResponseRecorder {
				response := httptest.NewRecorder()
				server.SearchMetadata(response, httptest.NewRequest(http.MethodGet, "/metadata/search", nil), generated.SearchMetadataParams{
					Provider: generated.SearchMetadataParamsProvider(provider.name), Query: "Show",
				})
				return response
			}
			assertProviderQuotaResponse(t, request(), "17")
			assertLocalProviderCooldown(t, request(), hits.Load())
		})
	}
}

func newProviderQuotaScheduler(t *testing.T) *outbound.Scheduler {
	t.Helper()
	scheduler, err := outbound.New(t.Context(), outbound.DefaultPolicies(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}
