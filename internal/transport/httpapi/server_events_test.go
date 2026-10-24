package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bansoko/internal/events"
)

func TestStreamEventsWritesHeadersAndEvent(t *testing.T) {
	hub := events.NewHub()
	server := &Server{deps: Dependencies{Events: hub}}
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil).WithContext(requestContext)
	response := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		server.StreamEvents(response, request)
		close(done)
	}()

	select {
	case <-response.ready:
	case <-time.After(time.Second):
		t.Fatal("stream did not write initial connection comment")
	}
	hub.Publish(events.Event{Target: events.TargetCatalog})
	select {
	case <-response.eventReady:
	case <-time.After(time.Second):
		t.Fatal("stream did not write published event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after request cancellation")
	}
	if got := response.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type=%q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache control=%q", got)
	}
	body := response.String()
	if !strings.Contains(body, ": connected\n\n") || !strings.Contains(body, `data: {"target":"catalog"}`) {
		t.Fatalf("unexpected stream body: %q", body)
	}
}

func TestWriteSSEEventCarriesOptionalManagedAnimeID(t *testing.T) {
	scoped := newFlushRecorder()
	if err := writeSSEEvent(scoped, scoped, events.Event{Target: events.TargetRSS, ManagedAnimeID: "anime-1"}); err != nil {
		t.Fatal(err)
	}
	if got := scoped.String(); got != "data: {\"managed_anime_id\":\"anime-1\",\"target\":\"rss\"}\n\n" {
		t.Fatalf("scoped event=%q", got)
	}
	unscoped := newFlushRecorder()
	if err := writeSSEEvent(unscoped, unscoped, events.Event{Target: events.TargetRSS}); err != nil {
		t.Fatal(err)
	}
	if got := unscoped.String(); got != "data: {\"target\":\"rss\"}\n\n" {
		t.Fatalf("unscoped event=%q", got)
	}
}

type flushRecorder struct {
	header     http.Header
	ready      chan struct{}
	eventReady chan struct{}
	data       strings.Builder
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: make(http.Header), ready: make(chan struct{}), eventReady: make(chan struct{})}
}

func (r *flushRecorder) Header() http.Header { return r.header }

func (r *flushRecorder) Write(data []byte) (int, error) {
	r.data.Write(data)
	select {
	case <-r.ready:
	default:
		close(r.ready)
	}
	if strings.Contains(r.data.String(), `data: {"target":"catalog"}`) {
		select {
		case <-r.eventReady:
		default:
			close(r.eventReady)
		}
	}
	return len(data), nil
}

func (r *flushRecorder) WriteHeader(_ int) {}

func (r *flushRecorder) Flush() {}

func (r *flushRecorder) String() string { return r.data.String() }
