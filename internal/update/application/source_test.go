package application

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestUpdateHTTPSourceStripsCallerAuthHeaders locks the credential boundary:
// an update source is anonymous by construction, so a caller-supplied
// Authorization or any well-known token header is cleared on every request.
func TestUpdateHTTPSourceStripsCallerAuthHeaders(t *testing.T) {
	type observation struct {
		authorization    string
		proxyAuthorization string
		deployToken      string
		privateToken     string
		jobToken         string
	}
	var mu sync.Mutex
	var seen []observation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, observation{
			authorization:      r.Header.Get("Authorization"),
			proxyAuthorization: r.Header.Get("Proxy-Authorization"),
			deployToken:        r.Header.Get("DEPLOY-TOKEN"),
			privateToken:       r.Header.Get("PRIVATE-TOKEN"),
			jobToken:           r.Header.Get("JOB-TOKEN"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: server.URL + "/packages/v1/release-manifest.json",
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source.ManifestURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer caller-supplied")
	req.Header.Set("Proxy-Authorization", "Basic caller-supplied")
	req.Header.Set("DEPLOY-TOKEN", "caller-supplied")
	req.Header.Set("PRIVATE-TOKEN", "caller-supplied")
	req.Header.Set("JOB-TOKEN", "caller-supplied")
	resp, err := source.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	mu.Lock()
	got := append([]observation(nil), seen...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("stripped-header requests = %d, want 1", len(got))
	}
	if got[0] != (observation{}) {
		t.Fatalf("a caller-supplied credential header survived: %+v", got[0])
	}
}

func TestUpdateHTTPSourceRejectsUnsafeURLsAndRedirects(t *testing.T) {
	for _, raw := range []string{
		"https://user:pass@example.test/package/manifest.json",
		"https://example.test/package/manifest.json?token=secret",
		"https://example.test/package/manifest.json#secret",
		"https://example.test/package/./manifest.json",
		"https://example.test/package/../manifest.json",
		"https://example.test/package\\manifest.json",
	} {
		if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{ManifestURL: raw}); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("cross-host redirect carried Authorization %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/package/release-manifest.json":
			w.Header().Set("Location", "/package/redirected-manifest.json")
			w.WriteHeader(http.StatusFound)
		case "/package/redirected-manifest.json":
			w.WriteHeader(http.StatusNoContent)
		case "/package/cross-host.json":
			w.Header().Set("Location", other.URL+"/package/release-manifest.json")
			w.WriteHeader(http.StatusFound)
		case "/outside/release-manifest.json":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer redirect.Close()

	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: redirect.URL + "/package/release-manifest.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Do(context.Background(), http.MethodGet, source.ManifestURL()); err != nil {
		t.Fatalf("same-host package redirect rejected: %v", err)
	}
	if _, err := source.Do(context.Background(), http.MethodGet, redirect.URL+"/package/cross-host.json"); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("cross-host redirect result = %v", err)
	}
	if _, err := source.Do(context.Background(), http.MethodGet, redirect.URL+"/outside/release-manifest.json"); err == nil {
		t.Fatal("same-host out-of-package request was accepted")
	}
}

func TestUpdateHTTPSourceProductionRequiresHTTPS(t *testing.T) {
	if _, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: "http://example.test/package/manifest.json",
		Production:  true,
	}); err == nil {
		t.Fatal("production HTTP source accepted")
	}
}

func TestManifestAndPayloadUseSameSource(t *testing.T) {
	var publicKey ed25519.PublicKey
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("update source unexpectedly carried a credential")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()

	source, err := NewUpdateHTTPSource(UpdateHTTPSourceConfig{
		ManifestURL: server.URL + "/packages/v1/release-manifest.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := source.PayloadURL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(payload, "/packages/v1/"+PayloadFileName) {
		t.Fatalf("payload URL = %q", payload)
	}
	if _, err := source.Do(context.Background(), http.MethodGet, payload); err != nil {
		t.Fatal(err)
	}
	_, _, _ = fetchAndVerifyManifest(context.Background(), source, publicKey)
}
