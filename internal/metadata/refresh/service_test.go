package refresh

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bansoko/internal/metadata/matcher"
	metadataport "bansoko/internal/metadata/port"
)

const testPayload = `{"items":[{"title":"Show","sites":[{"site":"bangumi","id":"123"},{"site":"tmdb","id":"tv/42"}]}]}`

type memoryStore struct {
	mu       sync.Mutex
	snapshot metadataport.Snapshot
	found    bool
	touches  int
	saves    int
	saveErr  error
}

func (s *memoryStore) Load(context.Context) (metadataport.Snapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.Payload = append([]byte(nil), s.snapshot.Payload...)
	return s.snapshot, s.found, nil
}
func (s *memoryStore) Save(_ context.Context, snapshot metadataport.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.snapshot, s.found, s.saves = snapshot, true, s.saves+1
	return nil
}
func (s *memoryStore) Touch(_ context.Context, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.CheckedAt, s.touches = at, s.touches+1
	return nil
}

type testPublisher struct {
	index *matcher.BangumiDataIndex
	done  chan struct{}
	once  sync.Once
}

func (p *testPublisher) ReplaceBangumiDataIndex(index *matcher.BangumiDataIndex) {
	p.index = index
	if p.done != nil {
		p.once.Do(func() { close(p.done) })
	}
}

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme, clone.URL.Host = t.target.Scheme, t.target.Host
	return t.base.RoundTrip(clone)
}

func testCDN(t *testing.T, handler http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	server := httptest.NewServer(handler)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return server, &http.Client{Transport: rewriteTransport{target: target, base: server.Client().Transport}}
}

func TestRestoreUsesStoredSnapshot(t *testing.T) {
	store := &memoryStore{snapshot: metadataport.Snapshot{Payload: []byte(testPayload)}, found: true}
	index, err := Restore(context.Background(), store)
	if err != nil || index == nil || index.LookupByBgmID("123") == nil {
		t.Fatalf("stored snapshot must restore without network access: index=%+v err=%v", index, err)
	}
}

func TestRefreshSameVersionTouchesWithoutDownloadingData(t *testing.T) {
	var dataRequests atomic.Int32
	server, client := testCDN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bangumi-data@0.3/package.json" {
			dataRequests.Add(1)
		}
		_, _ = w.Write([]byte(`{"version":"0.3.235"}`))
	}))
	defer server.Close()
	old := matcher.NewBangumiDataIndex(nil)
	publisher := &testPublisher{index: old}
	store := &memoryStore{snapshot: metadataport.Snapshot{UpstreamVersion: "0.3.235", Payload: []byte(testPayload)}, found: true}
	service := NewService(store, publisher, client, nil, true)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.touches != 1 || store.saves != 0 || dataRequests.Load() != 0 || publisher.index != old {
		t.Fatalf("same version must only touch the snapshot: touches=%d saves=%d data=%d", store.touches, store.saves, dataRequests.Load())
	}
}

func TestRefreshPublishesExactNewVersion(t *testing.T) {
	server, client := testCDN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bangumi-data@0.3/package.json":
			_, _ = w.Write([]byte(`{"version":"0.3.235"}`))
		case "/bangumi-data@0.3.235/dist/data.json":
			w.Header().Set("Last-Modified", "Mon, 10 Nov 2026 00:00:00 GMT")
			_, _ = w.Write([]byte(testPayload))
		default:
			t.Fatalf("unexpected CDN path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	publisher := &testPublisher{}
	store := &memoryStore{snapshot: metadataport.Snapshot{UpstreamVersion: "0.3.234", Payload: []byte(testPayload)}, found: true}
	service := NewService(store, publisher, client, nil, true)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || store.snapshot.UpstreamVersion != "0.3.235" || store.snapshot.LastModified == "" || publisher.index == nil || publisher.index.LookupByBgmID("123") == nil {
		t.Fatalf("new version must save and publish a complete index: snapshot=%+v publisher=%+v", store.snapshot, publisher.index)
	}
}

func TestRefreshFailureKeepsOldIndexAndSnapshot(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		saveErr       error
	}{{"bad JSON", `not-json`, nil}, {"database failure", testPayload, errors.New("save failed")}} {
		t.Run(test.name, func(t *testing.T) {
			server, client := testCDN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/bangumi-data@0.3/package.json" {
					_, _ = w.Write([]byte(`{"version":"0.3.235"}`))
					return
				}
				_, _ = w.Write([]byte(test.payload))
			}))
			defer server.Close()
			old := matcher.NewBangumiDataIndex(nil)
			publisher := &testPublisher{index: old}
			store := &memoryStore{snapshot: metadataport.Snapshot{UpstreamVersion: "0.3.234", Payload: []byte(testPayload)}, found: true, saveErr: test.saveErr}
			service := NewService(store, publisher, client, nil, true)
			if err := service.Refresh(context.Background()); err == nil {
				t.Fatal("invalid response or failed save must be retryable")
			}
			if publisher.index != old || store.snapshot.UpstreamVersion != "0.3.234" {
				t.Fatal("failed refresh must retain the old published index and snapshot")
			}
		})
	}
}

func TestRunRetriesFailureAndStopsOnCancellation(t *testing.T) {
	var manifests atomic.Int32
	server, client := testCDN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bangumi-data@0.3/package.json" && manifests.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/bangumi-data@0.3/package.json" {
			_, _ = w.Write([]byte(`{"version":"0.3.235"}`))
			return
		}
		_, _ = w.Write([]byte(testPayload))
	}))
	defer server.Close()
	publisher := &testPublisher{done: make(chan struct{})}
	service := NewService(&memoryStore{}, publisher, client, nil, false)
	service.RetryInterval, service.CheckInterval = 10*time.Millisecond, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	select {
	case <-publisher.done:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("refresh did not retry after the failed manifest request")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh producer did not stop after cancellation")
	}
}

func TestRunDefersFreshSnapshotCheckAndHonorsCancellation(t *testing.T) {
	var requests atomic.Int32
	server, client := testCDN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	store := &memoryStore{snapshot: metadataport.Snapshot{CheckedAt: time.Now()}, found: true}
	service := NewService(store, &testPublisher{}, client, nil, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.Run(ctx)
	if requests.Load() != 0 {
		t.Fatal("a fresh restored snapshot must not trigger an immediate CDN request")
	}
}
