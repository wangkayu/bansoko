package adapter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configurationdomain "bansoko/internal/configuration/domain"
	identitymetadataport "bansoko/internal/identitymetadata/port"
)

// Poster resolver boundary regression: subject lookup and poster bytes are served
// through the configured BGM route, while invalid/missing posters map to not found.

type stubConnectionSource struct {
	connections []configurationdomain.Connection
}

func (s stubConnectionSource) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return s.connections, nil
}

type memoryPosterCache struct {
	mu      sync.Mutex
	entries map[string]identitymetadataport.PosterCacheEntry
	gets    int
	upserts int
}

func newMemoryPosterCache() *memoryPosterCache {
	return &memoryPosterCache{entries: map[string]identitymetadataport.PosterCacheEntry{}}
}

func (c *memoryPosterCache) Get(_ context.Context, id string) (identitymetadataport.PosterCacheEntry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	entry, ok := c.entries[id]
	if !ok {
		return identitymetadataport.PosterCacheEntry{}, false, nil
	}
	entry.ImageBytes = append([]byte(nil), entry.ImageBytes...)
	entry.LastUsed = time.Now().UTC()
	c.entries[id] = entry
	return entry, true, nil
}

func (c *memoryPosterCache) Upsert(_ context.Context, entry identitymetadataport.PosterCacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upserts++
	entry.ImageBytes = append([]byte(nil), entry.ImageBytes...)
	c.entries[entry.BGMSubjectID] = entry
	return nil
}

func newPosterTestResolver(bgmEndpoint string) *ConfiguredPosterResolver {
	return &ConfiguredPosterResolver{
		Connections: stubConnectionSource{connections: []configurationdomain.Connection{
			{ID: "bgm-main", Kind: configurationdomain.ConnectionBGM, Name: "Bangumi", Endpoint: bgmEndpoint, State: configurationdomain.ConnectionConfigured},
		}},
		CacheTTL:      time.Hour,
		cache:         map[string]posterCacheEntry{},
		maxCacheBytes: maxPosterCacheBytes,
	}
}

func newPosterTestResolverWithCache(bgmEndpoint string, cache identitymetadataport.PosterCache) *ConfiguredPosterResolver {
	resolver := newPosterTestResolver(bgmEndpoint)
	resolver.PersistentCache = cache
	return resolver
}

func TestFetchPosterResolvesSubjectThenCaches(t *testing.T) {
	var subjectCalls, imageCalls int32
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/subject/583729", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&subjectCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		// The image URL points at the same test server (the URL is only known after
		// Listen; the closure reads it).
		_, _ = w.Write([]byte(`{"id":583729,"images":{"common":"` + server.URL + `/pic/x.jpg"}}`))
	})
	mux.HandleFunc("/pic/x.jpg", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&imageCalls, 1)
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("webp-bytes"))
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	cache := newMemoryPosterCache()
	resolver := newPosterTestResolverWithCache(server.URL, cache)
	body, contentType, err := resolver.FetchPoster(context.Background(), "583729")
	if err != nil {
		t.Fatalf("initial fetch: %v", err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "webp-bytes" || contentType != "image/webp" {
		t.Fatalf("initial fetch got (%q, %q)", data, contentType)
	}
	if cache.upserts != 1 {
		t.Fatalf("persistent cache upserts = %d, want 1 after successful upstream fetch", cache.upserts)
	}

	// A byte-cache hit must not need the configured connection or either upstream.
	resolver.Connections = nil
	body, contentType, err = resolver.FetchPoster(context.Background(), "583729")
	if err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	data, _ = io.ReadAll(body)
	body.Close()
	if string(data) != "webp-bytes" || contentType != "image/webp" {
		t.Fatalf("cached fetch got (%q, %q)", data, contentType)
	}
	if atomic.LoadInt32(&subjectCalls) != 1 {
		t.Fatalf("subject calls = %d, want 1 (second fetch hits cache)", subjectCalls)
	}
	if atomic.LoadInt32(&imageCalls) != 1 {
		t.Fatalf("image calls = %d, want 1 (second fetch hits byte cache)", imageCalls)
	}
}

func TestFetchPosterNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/subject/999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":999,"images":{"common":""}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	resolver := newPosterTestResolver(server.URL)
	if _, _, err := resolver.FetchPoster(context.Background(), "999"); !errors.Is(err, ErrPosterNotFound) {
		t.Fatalf("empty images.common: err = %v, want ErrPosterNotFound", err)
	}
	if _, _, err := resolver.FetchPoster(context.Background(), "abc"); !errors.Is(err, ErrPosterNotFound) {
		t.Fatalf("invalid id: err = %v, want ErrPosterNotFound", err)
	}
}

func TestFetchPosterUpstreamFailureIsNotNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/subject/555", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	resolver := newPosterTestResolver(server.URL)
	_, _, err := resolver.FetchPoster(context.Background(), "555")
	if err == nil || errors.Is(err, ErrPosterNotFound) {
		t.Fatalf("err = %v, want a non-NotFound upstream error (maps to 502)", err)
	}
}

func TestFetchPosterExpiredRefreshServesLastGood(t *testing.T) {
	var failed atomic.Bool
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/subject/777", func(w http.ResponseWriter, r *http.Request) {
		if failed.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":777,"images":{"common":"` + server.URL + `/pic/good.jpg"}}`))
	})
	mux.HandleFunc("/pic/good.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("last-good"))
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	resolver := newPosterTestResolver(server.URL)
	resolver.CacheTTL = time.Millisecond
	body, contentType, err := resolver.FetchPoster(context.Background(), "777")
	if err != nil {
		t.Fatalf("initial fetch: %v", err)
	}
	initial, _ := io.ReadAll(body)
	body.Close()
	if string(initial) != "last-good" || contentType != "image/png" {
		t.Fatalf("initial poster = (%q, %q)", initial, contentType)
	}

	resolver.mu.Lock()
	entry := resolver.cache["777"]
	entry.posterExpiresAt = time.Now().Add(-time.Second)
	entry.urlExpiresAt = time.Now().Add(-time.Second)
	resolver.cache["777"] = entry
	resolver.mu.Unlock()
	failed.Store(true)
	body, contentType, err = resolver.FetchPoster(context.Background(), "777")
	if err != nil {
		t.Fatalf("stale fallback fetch: %v", err)
	}
	fallback, _ := io.ReadAll(body)
	body.Close()
	if string(fallback) != "last-good" || contentType != "image/png" {
		t.Fatalf("fallback poster = (%q, %q)", fallback, contentType)
	}
}

func TestPosterCacheEvictsLeastRecentlyUsed(t *testing.T) {
	resolver := &ConfiguredPosterResolver{
		CacheTTL:      time.Hour,
		cache:         map[string]posterCacheEntry{},
		maxCacheBytes: 10,
	}
	resolver.storePoster("a", []byte("aaaa"), "image/jpeg")
	resolver.storePoster("b", []byte("bbbb"), "image/jpeg")
	if _, _, ok := resolver.cachedPoster("a", false); !ok {
		t.Fatal("poster a missing before eviction")
	}
	resolver.storePoster("c", []byte("cccc"), "image/jpeg")
	if _, _, ok := resolver.cachedPoster("b", false); ok {
		t.Fatal("least-recently-used poster b was not evicted")
	}
	if _, _, ok := resolver.cachedPoster("a", false); !ok {
		t.Fatal("recently-used poster a was evicted")
	}
	if _, _, ok := resolver.cachedPoster("c", false); !ok {
		t.Fatal("new poster c was not retained")
	}
}

func TestFetchPosterRejectsOversizedImageWithoutCaching(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/subject/888", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":888,"images":{"common":"` + server.URL + `/pic/large.jpg"}}`))
	})
	mux.HandleFunc("/pic/large.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(make([]byte, maxPosterBytes+1))
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	resolver := newPosterTestResolver(server.URL)
	if _, _, err := resolver.FetchPoster(context.Background(), "888"); err == nil {
		t.Fatal("oversized poster unexpectedly succeeded")
	}
	if _, _, ok := resolver.cachedPoster("888", false); ok {
		t.Fatal("oversized poster was cached")
	}
}

func TestFetchPosterLoadsFreshPosterFromPersistentCacheAfterRestart(t *testing.T) {
	cache := newMemoryPosterCache()
	now := time.Now().UTC()
	cache.entries["583729"] = identitymetadataport.PosterCacheEntry{
		BGMSubjectID: "583729",
		ImageBytes:   []byte("persisted-bytes"),
		ContentType:  "image/webp",
		ImageURL:     "https://images.example.test/583729.webp",
		FetchedAt:    now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Hour),
		LastUsed:     now.Add(-time.Minute),
	}
	resolver := newPosterTestResolverWithCache("http://127.0.0.1:1", cache)
	resolver.Connections = nil
	body, contentType, err := resolver.FetchPoster(context.Background(), "583729")
	if err != nil {
		t.Fatalf("persistent cache fetch: %v", err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "persisted-bytes" || contentType != "image/webp" {
		t.Fatalf("persistent cache poster = (%q, %q)", data, contentType)
	}
	if cache.gets != 1 {
		t.Fatalf("persistent cache gets = %d, want 1", cache.gets)
	}
	resolver.PersistentCache = nil
	body, contentType, err = resolver.FetchPoster(context.Background(), "583729")
	if err != nil {
		t.Fatalf("L1 backfill fetch: %v", err)
	}
	data, _ = io.ReadAll(body)
	body.Close()
	if string(data) != "persisted-bytes" || contentType != "image/webp" {
		t.Fatalf("L1 backfill poster = (%q, %q)", data, contentType)
	}
}

func TestFetchPosterLoadsExpiredPersistentPosterAsLastGood(t *testing.T) {
	cache := newMemoryPosterCache()
	now := time.Now().UTC()
	cache.entries["777"] = identitymetadataport.PosterCacheEntry{
		BGMSubjectID: "777",
		ImageBytes:   []byte("expired-last-good"),
		ContentType:  "image/png",
		ImageURL:     "https://images.example.test/777.png",
		FetchedAt:    now.Add(-2 * time.Hour),
		ExpiresAt:    now.Add(-time.Minute),
		LastUsed:     now.Add(-time.Hour),
	}
	resolver := newPosterTestResolverWithCache("http://127.0.0.1:1", cache)
	body, contentType, err := resolver.FetchPoster(context.Background(), "777")
	if err != nil {
		t.Fatalf("expired persistent fallback: %v", err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "expired-last-good" || contentType != "image/png" {
		t.Fatalf("expired persistent fallback = (%q, %q)", data, contentType)
	}
}

func TestFetchPosterSingleflightSharesConcurrentMiss(t *testing.T) {
	var subjectCalls, imageCalls int32
	var server *httptest.Server
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/subject/321", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&subjectCalls, 1)
		once.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":321,"images":{"common":"` + server.URL + `/pic/321.jpg"}}`))
	})
	mux.HandleFunc("/pic/321.jpg", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&imageCalls, 1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("singleflight-bytes"))
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	resolver := newPosterTestResolver(server.URL)
	const callers = 12
	results := make(chan string, callers)
	for i := 0; i < callers; i++ {
		go func() {
			body, _, err := resolver.FetchPoster(context.Background(), "321")
			if err != nil {
				results <- err.Error()
				return
			}
			data, readErr := io.ReadAll(body)
			body.Close()
			if readErr != nil {
				results <- readErr.Error()
				return
			}
			results <- string(data)
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("singleflight upstream request did not start")
	}
	close(release)
	for i := 0; i < callers; i++ {
		if got := <-results; got != "singleflight-bytes" {
			t.Fatalf("caller %d result = %q, want singleflight-bytes", i, got)
		}
	}
	if got := atomic.LoadInt32(&subjectCalls); got != 1 {
		t.Fatalf("subject calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&imageCalls); got != 1 {
		t.Fatalf("image calls = %d, want 1", got)
	}
}

func TestFetchPosterNotFoundIsNotNegativeCached(t *testing.T) {
	var subjectCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/subject/404", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&subjectCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":404,"images":{"common":""}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	resolver := newPosterTestResolverWithCache(server.URL, newMemoryPosterCache())
	for i := 0; i < 2; i++ {
		if _, _, err := resolver.FetchPoster(context.Background(), "404"); !errors.Is(err, ErrPosterNotFound) {
			t.Fatalf("attempt %d error = %v, want ErrPosterNotFound", i, err)
		}
	}
	if got := atomic.LoadInt32(&subjectCalls); got != 2 {
		t.Fatalf("subject calls = %d, want 2 because not-found is not cached", got)
	}
}

// TestFetchPosterPrefersResizedLargeCover proves the resolver loads the r/400
// rendition of the large cover (400 px wide) instead of the 150 px common
// image, and falls back to the common image when the resize is refused.
func TestFetchPosterPrefersResizedLargeCover(t *testing.T) {
	var server *httptest.Server
	var resizeStatus atomic.Int32
	resizeStatus.Store(http.StatusOK)
	var commonCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/subject/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/subject/"):]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":` + id + `,"images":{"large":"` + server.URL + `/pic/cover/l/ab/cd/` + id + `_x.jpg","common":"` + server.URL + `/pic/cover/c/ab/cd/` + id + `_x.jpg"}}`))
	})
	mux.HandleFunc("/r/400/pic/cover/l/ab/cd/", func(w http.ResponseWriter, r *http.Request) {
		if status := int(resizeStatus.Load()); status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("resized"))
	})
	mux.HandleFunc("/pic/cover/c/ab/cd/", func(w http.ResponseWriter, r *http.Request) {
		commonCalls.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("common"))
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	resolver := newPosterTestResolver(server.URL)
	read := func(id string) string {
		t.Helper()
		body, _, err := resolver.FetchPoster(context.Background(), id)
		if err != nil {
			t.Fatalf("fetch %s: %v", id, err)
		}
		defer body.Close()
		data, _ := io.ReadAll(body)
		return string(data)
	}
	if got := read("11"); got != "resized" || commonCalls.Load() != 0 {
		t.Fatalf("poster = %q (common calls %d), want the r/400 rendition", got, commonCalls.Load())
	}
	resizeStatus.Store(http.StatusNotFound)
	if got := read("12"); got != "common" {
		t.Fatalf("poster = %q, want the common image when the resize is refused", got)
	}
}

func TestBGMResizedCoverURL(t *testing.T) {
	cases := []struct {
		large, common, want string
	}{
		{"https://lain.bgm.tv/pic/cover/l/a1/b2/123_abc.jpg", "", "https://lain.bgm.tv/r/400/pic/cover/l/a1/b2/123_abc.jpg"},
		{"", "https://lain.bgm.tv/pic/cover/c/a1/b2/123_abc.jpg", "https://lain.bgm.tv/r/400/pic/cover/l/a1/b2/123_abc.jpg"},
		{"https://lain.bgm.tv/img/no_icon_subject.png", "", ""},
		{"/pic/cover/l/a1/b2/123_abc.jpg", "", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := bgmResizedCoverURL(tc.large, tc.common); got != tc.want {
			t.Errorf("bgmResizedCoverURL(%q, %q) = %q, want %q", tc.large, tc.common, got, tc.want)
		}
	}
}
