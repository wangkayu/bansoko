package matcher

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
)

type diagRecorder struct {
	observabilityport.Recorder
	failures  []observabilityport.DiagnosticFailureRequest
	successes []observabilityport.DiagnosticSuccessRequest
}

func (r *diagRecorder) RecordDiagnosticFailure(_ context.Context, req observabilityport.DiagnosticFailureRequest) error {
	r.failures = append(r.failures, req)
	return nil
}

func (r *diagRecorder) RecordDiagnosticSuccess(_ context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	r.successes = append(r.successes, req)
	return nil
}

func failed(class outbound.Class, status int) error {
	return outbound.WithFailure(errors.New("boom"), outbound.Failure{Host: "api.example", Class: class, Status: status})
}

func trackerMatcher(now *time.Time) (*Matcher, *diagRecorder) {
	rec := &diagRecorder{}
	m := NewMatcher(nil, nil, nil, nil)
	m.SetDiagnostics(rec)
	m.diag.now = func() time.Time { return *now }
	return m, rec
}

func TestMetadataEpisodeOpensOnSecondFailureInsideWindow(t *testing.T) {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	m, rec := trackerMatcher(&now)
	ctx := WithAffectedAnime(context.Background(), "anime-1")

	m.observe(ctx, sourceTMDB, failed(outbound.ClassTimeout, 0))
	if len(rec.failures) != 0 {
		t.Fatalf("a single failure opened an episode: %+v", rec.failures)
	}
	now = now.Add(11 * time.Minute)
	m.observe(ctx, sourceTMDB, failed(outbound.ClassTimeout, 0))
	if len(rec.failures) != 0 {
		t.Fatalf("failures more than ten minutes apart opened an episode: %+v", rec.failures)
	}
	now = now.Add(time.Minute)
	m.observe(ctx, sourceTMDB, failed(outbound.ClassHTTPStatus, 503))
	if len(rec.failures) != 1 || rec.failures[0].Condition.Variant != observabilityport.VariantUnreachable || rec.failures[0].AffectedID != "anime-1" || rec.failures[0].TargetID != "tmdb" {
		t.Fatalf("failures = %+v", rec.failures)
	}
}

func TestMetadataSourcesCountSeparatelyAndAuthFailureHasItsVariant(t *testing.T) {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	m, rec := trackerMatcher(&now)
	ctx := context.Background()
	m.observe(ctx, sourceBGM, failed(outbound.ClassTimeout, 0))
	m.observe(ctx, sourceTMDB, failed(outbound.ClassHTTPStatus, 401))
	if len(rec.failures) != 0 {
		t.Fatalf("two sources must not add up: %+v", rec.failures)
	}
	m.observe(ctx, sourceTMDB, failed(outbound.ClassHTTPStatus, 403))
	if len(rec.failures) != 1 || rec.failures[0].Condition.Variant != observabilityport.VariantAuthFailed {
		t.Fatalf("failures = %+v", rec.failures)
	}
}

func TestMetadataSuccessClosesOnceAndResetsTheWindow(t *testing.T) {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	m, rec := trackerMatcher(&now)
	ctx := context.Background()
	// The first success after start closes an episode a previous process may
	// have left open; ordinary successes after that do not touch the store.
	m.observe(ctx, sourceTMDB, nil)
	m.observe(ctx, sourceTMDB, nil)
	if len(rec.successes) != 1 {
		t.Fatalf("successes = %d, want exactly the first", len(rec.successes))
	}
	m.observe(ctx, sourceTMDB, failed(outbound.ClassTimeout, 0))
	m.observe(ctx, sourceTMDB, nil)
	m.observe(ctx, sourceTMDB, failed(outbound.ClassTimeout, 0))
	if len(rec.successes) != 2 || len(rec.failures) != 0 {
		t.Fatalf("success must reset the window: successes=%d failures=%d", len(rec.successes), len(rec.failures))
	}
	// An error without an outbound fact (configuration, no result) is neither.
	m.observe(ctx, sourceTMDB, errors.New("TMDB API key is not configured"))
	if len(rec.failures) != 0 {
		t.Fatalf("a factless error was diagnosed: %+v", rec.failures)
	}
}

func TestMatcherCallsCarryFactsOnlyForActionableStatuses(t *testing.T) {
	for status, want := range map[int]bool{500: true, 503: true, 401: true, 403: true, 404: false, 429: false, 400: false} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		bgm := &BGMMatcher{Client: server.Client()}
		var target map[string]any
		err := bgm.getJSON(context.Background(), server.Client(), server.URL, &target)
		server.Close()
		if err == nil {
			t.Fatalf("status %d: no error", status)
		}
		failure, has := outbound.FailureOf(err)
		if has != want || (has && failure.Status != status) {
			t.Fatalf("status %d: has fact = %v (%+v), want %v", status, has, failure, want)
		}
	}
	// A refused connection is a fact; a cancelled request is not.
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	tmdb := &TMDBMatcher{}
	err := tmdb.getJSON(context.Background(), http.DefaultClient, url, "", &struct{}{})
	if _, has := outbound.FailureOf(err); !has {
		t.Fatalf("connection failure carried no fact: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = tmdb.getJSON(ctx, http.DefaultClient, url, "", &struct{}{})
	if _, has := outbound.FailureOf(err); has {
		t.Fatalf("a cancelled request carried a fact: %v", err)
	}
}
