package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	catalogapp "bansoko/internal/catalog/application"
	"bansoko/internal/provider/outbound"
	"bansoko/internal/transport/httpapi/generated"
)

func TestAniBTSeedSearchThrottledResponseCompatibility(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	scheduler, err := outbound.New(t.Context(), outbound.DefaultPolicies(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	searcher := catalogapp.HTTPAniBTTorznabSearcher{Endpoint: upstream.URL, Client: upstream.Client(), Outbound: scheduler}
	_, err = searcher.SearchAniBTKeywords(t.Context(), "anime")
	if err == nil {
		t.Fatal("upstream 429 treated as a success")
	}
	response := httptest.NewRecorder()
	writeSeedSearchError(response, httptest.NewRequest(http.MethodPost, "/", nil), err)
	var body generated.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusTooManyRequests || body.Code != "PROVIDER_RATE_LIMITED" || response.Header().Get("Retry-After") != "45" {
		t.Fatalf("HTTP %d, code %q, Retry-After %q", response.Code, body.Code, response.Header().Get("Retry-After"))
	}
}
