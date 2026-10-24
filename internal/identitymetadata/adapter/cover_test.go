package adapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	identitymetadataport "bansoko/internal/identitymetadata/port"
)

// Cover proxy boundary: the server resolves the upstream URL from its own
// registry, only for provider hosts, within size/type/redirect limits.

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01}, 64)...)

type memoryImageStore struct {
	mu      sync.Mutex
	entries map[string]identitymetadataport.ProviderImageEntry
	stores  int
}

func newMemoryImageStore() *memoryImageStore {
	return &memoryImageStore{entries: map[string]identitymetadataport.ProviderImageEntry{}}
}

func (s *memoryImageStore) Register(_ context.Context, provider string, images []identitymetadataport.ProviderImageRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, image := range images {
		if _, ok := s.entries[image.Key]; !ok {
			s.entries[image.Key] = identitymetadataport.ProviderImageEntry{Key: image.Key, Provider: provider, SourceURL: image.SourceURL}
		}
	}
	return nil
}

func (s *memoryImageStore) Get(_ context.Context, key string) (identitymetadataport.ProviderImageEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	return entry, ok, nil
}

func (s *memoryImageStore) Store(_ context.Context, key string, data []byte, contentType string, fetchedAt, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[key]
	entry.HasBytes, entry.ImageBytes, entry.ContentType = true, append([]byte(nil), data...), contentType
	entry.FetchedAt, entry.ExpiresAt = fetchedAt, expiresAt
	s.entries[key] = entry
	s.stores++
	return nil
}

func newCoverTestProxy(mikanEndpoint string, store *memoryImageStore) *ConfiguredCoverProxy {
	return &ConfiguredCoverProxy{
		Connections: stubConnectionSource{connections: []configurationdomain.Connection{
			{ID: "mikan-main", Kind: configurationdomain.ConnectionMikan, Endpoint: mikanEndpoint, State: configurationdomain.ConnectionConfigured},
		}},
		Store: store,
		TTL:   time.Hour,
	}
}

func coverKeyFromPath(t *testing.T, path string) string {
	t.Helper()
	if !strings.HasPrefix(path, CoverPathPrefix) {
		t.Fatalf("path %q is not a cover proxy path", path)
	}
	return strings.TrimPrefix(path, CoverPathPrefix)
}

func TestCoverPathsRegistersOnlyProviderHosts(t *testing.T) {
	store := newMemoryImageStore()
	proxy := newCoverTestProxy("https://mikanani.me", store)
	allowed := []string{
		"https://mikanani.me/images/Bangumi/1.jpg?width=400",
		"https://img.mikanani.me/a.jpg",
	}
	rejected := []string{
		"http://mikanani.me/images/1.jpg",               // scheme downgrade
		"https://mikanani.me.evil.example/images/1.jpg", // lookalike suffix
		"https://evilmikanani.me/images/1.jpg",          // lookalike prefix
		"https://mikanani.me:8443/images/1.jpg",         // other port
		"https://user:pw@mikanani.me/images/1.jpg",      // userinfo
		"http://169.254.169.254/latest/meta-data",       // metadata endpoint
		"http://127.0.0.1:8080/api/v1/system/health",    // loopback
		"file:///etc/passwd",
		"javascript:alert(1)",
		"//mikanani.me/images/1.jpg", // scheme-relative
		"/images/1.jpg",              // relative
		"",
	}
	paths := proxy.CoverPaths(context.Background(), CoverProviderMikan, append(append([]string{}, allowed...), rejected...))
	for _, raw := range allowed {
		path, ok := paths[raw]
		if !ok {
			t.Errorf("%q should be allowed", raw)
			continue
		}
		key := coverKeyFromPath(t, path)
		if len(key) != 64 || strings.Contains(path, "mikanani") {
			t.Errorf("path %q must be an opaque key", path)
		}
		if entry, found, _ := store.Get(context.Background(), key); !found || entry.SourceURL != providerCoverRendition(CoverProviderMikan, raw) || entry.Provider != CoverProviderMikan {
			t.Errorf("registry entry for %q = %+v found=%v", raw, entry, found)
		}
	}
	for _, raw := range rejected {
		if path, ok := paths[raw]; ok {
			t.Errorf("%q must not be registered, got %q", raw, path)
		}
	}
	if len(store.entries) != len(allowed) {
		t.Fatalf("registry holds %d rows, want %d", len(store.entries), len(allowed))
	}
}

// TestCoverPathsNormalisesMikanCoverRendition proves a stored Mikan cover
// (homepage form: a centre-cropped 400x400 square) is registered as the
// uncropped width-only rendition, while the path stays keyed by the raw hint
// and other providers' URLs are untouched.
func TestCoverPathsNormalisesMikanCoverRendition(t *testing.T) {
	store := newMemoryImageStore()
	proxy := newCoverTestProxy("https://mikanani.me", store)
	cases := map[string]string{
		"https://mikanani.me/images/Bangumi/202510/abc.jpg?width=400&height=400&format=webp": "https://mikanani.me/images/Bangumi/202510/abc.jpg?width=600&format=webp",
		"https://mikanani.me/images/Bangumi/202510/def.jpg":                                  "https://mikanani.me/images/Bangumi/202510/def.jpg?width=600&format=webp",
	}
	raws := make([]string, 0, len(cases))
	for raw := range cases {
		raws = append(raws, raw)
	}
	paths := proxy.CoverPaths(context.Background(), CoverProviderMikan, raws)
	for raw, want := range cases {
		path, ok := paths[raw]
		if !ok {
			t.Fatalf("%q was not registered", raw)
		}
		entry, found, _ := store.Get(context.Background(), coverKeyFromPath(t, path))
		if !found || entry.SourceURL != want {
			t.Errorf("%q registered as %q, want %q", raw, entry.SourceURL, want)
		}
		if coverKeyFromPath(t, path) != CoverKey(CoverProviderMikan, want) {
			t.Errorf("%q key is not derived from the normalised URL", raw)
		}
	}
	if got := providerCoverRendition(CoverProviderAniBT, "https://r2.anibt.net/a.jpg?w=1"); got != "https://r2.anibt.net/a.jpg?w=1" {
		t.Errorf("anibt cover rewritten to %q", got)
	}
}

func TestCoverPathsTMDBIsFixedToImageCDN(t *testing.T) {
	store := newMemoryImageStore()
	proxy := newCoverTestProxy("https://mikanani.me", store)
	proxy.Connections = stubConnectionSource{} // falls back to the default TMDB connection
	paths := proxy.CoverPaths(context.Background(), CoverProviderTMDB, []string{
		"https://image.tmdb.org/t/p/w342/poster.jpg",
		"http://image.tmdb.org/t/p/w342/poster.jpg",
		"https://api.themoviedb.org/3/configuration",
		"https://mikanani.me/images/1.jpg",
	})
	if len(paths) != 1 || paths["https://image.tmdb.org/t/p/w342/poster.jpg"] == "" {
		t.Fatalf("tmdb paths = %v, want only the image CDN poster", paths)
	}
}

func TestCoverPathsFailClosedOnUnknownProviderOrStoreFailure(t *testing.T) {
	proxy := newCoverTestProxy("https://mikanani.me", newMemoryImageStore())
	if got := proxy.CoverPaths(context.Background(), "bgm", []string{"https://lain.bgm.tv/a.jpg"}); len(got) != 0 {
		t.Fatalf("unknown provider produced paths: %v", got)
	}
	proxy.Store = &failingImageStore{}
	if got := proxy.CoverPaths(context.Background(), CoverProviderMikan, []string{"https://mikanani.me/a.jpg"}); len(got) != 0 {
		t.Fatalf("registry failure must emit no path (and no URL): %v", got)
	}
	var nilProxy *ConfiguredCoverProxy
	if got := nilProxy.CoverPaths(context.Background(), CoverProviderMikan, []string{"https://mikanani.me/a.jpg"}); len(got) != 0 {
		t.Fatalf("nil proxy produced paths: %v", got)
	}
}

type failingImageStore struct{ memoryImageStore }

func (*failingImageStore) Register(context.Context, string, []identitymetadataport.ProviderImageRef) error {
	return errors.New("db down")
}

func TestFetchCoverRejectsUnknownAndMalformedKeys(t *testing.T) {
	proxy := newCoverTestProxy("https://mikanani.me", newMemoryImageStore())
	for _, key := range []string{"", "abc", strings.Repeat("g", 64), strings.Repeat("A", 64), strings.Repeat("a", 63), "https://mikanani.me/a.jpg", "../../etc/passwd", strings.Repeat("a", 64)} {
		if _, _, err := proxy.FetchCover(context.Background(), key); !errors.Is(err, ErrCoverNotFound) {
			t.Errorf("FetchCover(%q) err = %v, want not found", key, err)
		}
	}
}

func registerCover(t *testing.T, proxy *ConfiguredCoverProxy, raw string) string {
	t.Helper()
	paths := proxy.CoverPaths(context.Background(), CoverProviderMikan, []string{raw})
	if paths[raw] == "" {
		t.Fatalf("%q was not registered", raw)
	}
	return coverKeyFromPath(t, paths[raw])
}

func TestFetchCoverFetchesOnceThenServesFromCache(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "image/jpeg") // lies about the type; bytes decide
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()
	store := newMemoryImageStore()
	proxy := newCoverTestProxy(server.URL, store)
	key := registerCover(t, proxy, server.URL+"/images/Bangumi/1.jpg?width=400")

	for i := 0; i < 3; i++ {
		body, contentType, err := proxy.FetchCover(context.Background(), key)
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		data, _ := io.ReadAll(body)
		body.Close()
		if !bytes.Equal(data, pngBytes) || contentType != "image/png" {
			t.Fatalf("fetch %d = (%d bytes, %q), want png", i, len(data), contentType)
		}
	}
	if atomic.LoadInt32(&calls) != 1 || store.stores != 1 {
		t.Fatalf("upstream calls = %d, stores = %d, want 1 and 1 (cache hit afterwards)", calls, store.stores)
	}
}

func TestFetchCoverRefusesNonImagesAndOversize(t *testing.T) {
	cases := map[string]func(http.ResponseWriter){
		"html": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
		},
		"svg": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
		},
		"oversize": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(append(append([]byte(nil), pngBytes...), bytes.Repeat([]byte{0}, maxCoverBytes)...))
		},
		"empty": func(w http.ResponseWriter) { w.Header().Set("Content-Type", "image/png") },
	}
	for name, respond := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respond(w) }))
			defer server.Close()
			store := newMemoryImageStore()
			proxy := newCoverTestProxy(server.URL, store)
			key := registerCover(t, proxy, server.URL+"/x")
			if _, _, err := proxy.FetchCover(context.Background(), key); !errors.Is(err, ErrCoverUnavailable) {
				t.Fatalf("err = %v, want unavailable", err)
			}
			if store.stores != 0 {
				t.Fatalf("a refused response was cached")
			}
		})
	}
}

func TestFetchCoverUpstreamNotFoundAndErrors(t *testing.T) {
	status := http.StatusNotFound
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer server.Close()
	proxy := newCoverTestProxy(server.URL, newMemoryImageStore())
	key := registerCover(t, proxy, server.URL+"/gone.jpg")
	if _, _, err := proxy.FetchCover(context.Background(), key); !errors.Is(err, ErrCoverNotFound) {
		t.Fatalf("404 err = %v, want not found", err)
	}
	status = http.StatusInternalServerError
	key2 := registerCover(t, proxy, server.URL+"/err.jpg")
	if _, _, err := proxy.FetchCover(context.Background(), key2); !errors.Is(err, ErrCoverUnavailable) {
		t.Fatalf("500 err = %v, want unavailable", err)
	}
}

func TestFetchCoverDoesNotFollowRedirectsOffTheProviderHost(t *testing.T) {
	var foreignHits int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		_, _ = w.Write(pngBytes)
	}))
	defer foreign.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/internal.png", http.StatusFound)
	}))
	defer provider.Close()
	proxy := newCoverTestProxy(provider.URL, newMemoryImageStore())
	key := registerCover(t, proxy, provider.URL+"/cover.png")
	if _, _, err := proxy.FetchCover(context.Background(), key); !errors.Is(err, ErrCoverUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if atomic.LoadInt32(&foreignHits) != 0 {
		t.Fatal("the proxy followed a redirect to a host outside the provider policy")
	}
}

func TestFetchCoverServesStaleWhenRefreshFails(t *testing.T) {
	healthy := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()
	store := newMemoryImageStore()
	proxy := newCoverTestProxy(server.URL, store)
	key := registerCover(t, proxy, server.URL+"/c.png")
	if _, _, err := proxy.FetchCover(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	entry := store.entries[key]
	entry.ExpiresAt = time.Now().Add(-time.Minute)
	store.entries[key] = entry
	healthy = false
	body, contentType, err := proxy.FetchCover(context.Background(), key)
	if err != nil {
		t.Fatalf("stale fetch: %v", err)
	}
	data, _ := io.ReadAll(body)
	if !bytes.Equal(data, pngBytes) || contentType != "image/png" {
		t.Fatalf("stale fetch = (%d bytes, %q)", len(data), contentType)
	}
}

func TestFetchCoverRevalidatesPolicyBeforeFetching(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()
	proxy := newCoverTestProxy(server.URL, newMemoryImageStore())
	key := registerCover(t, proxy, server.URL+"/c.png")
	// The Mikan endpoint is reconfigured elsewhere: the registered host is no
	// longer one of the provider's, so the row must not be fetched.
	proxy.Connections = stubConnectionSource{connections: []configurationdomain.Connection{
		{ID: "mikan-main", Kind: configurationdomain.ConnectionMikan, Endpoint: "https://mikanani.me", State: configurationdomain.ConnectionConfigured},
	}}
	if _, _, err := proxy.FetchCover(context.Background(), key); !errors.Is(err, ErrCoverNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("a source outside the current provider policy was fetched")
	}
}

func TestCoverKeyIsStableAndProviderScoped(t *testing.T) {
	a := CoverKey(CoverProviderMikan, "https://mikanani.me/a.jpg")
	if a != CoverKey(CoverProviderMikan, "https://mikanani.me/a.jpg") || a == CoverKey(CoverProviderAniBT, "https://mikanani.me/a.jpg") {
		t.Fatal("cover key must be deterministic and provider scoped")
	}
	if !coverKeyPattern.MatchString(a) {
		t.Fatalf("cover key %q is not 64 lowercase hex", a)
	}
}
