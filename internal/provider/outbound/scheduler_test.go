package outbound

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

func testScheduler(t *testing.T, clock Clock, store Store) *Scheduler {
	t.Helper()
	s, err := New(t.Context(), DefaultPolicies(), clock, store)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time             { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (*manualClock) NewTimer(time.Duration) Timer { panic("unexpected queue timer") }
func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestMetadataQuotaFor41Requests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(t, nil, nil)
		start := time.Now()
		for i := 0; i < 41; i++ {
			if err := s.Acquire(t.Context(), AniBTMetadata); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed != time.Duration(i)*2*time.Second {
				t.Fatalf("request %d at %v", i+1, elapsed)
			}
		}
	})
}

func TestInteractiveQueueFairnessBudgetAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(t, nil, nil)
		normal := t.Context()
		interactive := WithPriority(normal, PriorityInteractive)
		// One permit is consumed up front so the queue below has a known next slot.
		if err := s.Acquire(normal, AniBTTorznab); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		done := make(chan string, 8)
		startWaiter := func(ctx context.Context, label string) {
			go func() {
				if err := s.Acquire(ctx, AniBTTorznab); err != nil {
					done <- err.Error()
					return
				}
				done <- label
			}()
			synctest.Wait()
		}
		background, cancelBackground := context.WithCancel(normal)
		defer cancelBackground()
		startWaiter(background, "background")
		startWaiter(interactive, "interactive-1")
		startWaiter(interactive, "interactive-2")
		// The direct interactive request would only start after two interactive and
		// one background slot, past its two-second budget, so it is refused with the
		// estimated start instead of displacing background work.
		var refusal *CooldownError
		if err := s.Acquire(interactive, AniBTTorznab); !errors.As(err, &refusal) || refusal.ReasonCode != ReasonWaitBudget || refusal.RetryAfter != 3*time.Second || !refusal.CooldownUntil.Equal(start.Add(3*time.Second)) {
			t.Fatalf("unexpected budget error: %v", err)
		}
		if got := <-done; got != "interactive-1" || time.Since(start) != time.Second {
			t.Fatalf("%s at %s", got, time.Since(start))
		}
		if got := <-done; got != "interactive-2" || time.Since(start) != 2*time.Second {
			t.Fatalf("%s at %s", got, time.Since(start))
		}
		// Two interactive grants in a row left one interactive slot before the
		// stream cap, so interactive-3 starts first and the streaming background
		// request keeps its turn right after it.
		startWaiter(interactive, "interactive-3")
		if got := <-done; got != "interactive-3" || time.Since(start) != 3*time.Second {
			t.Fatalf("%s at %s", got, time.Since(start))
		}
		// interactive-4 asks at 3s and waits behind the background turn, which is
		// still inside its own two-second budget, so it is queued, not refused.
		startWaiter(interactive, "interactive-4")
		for _, want := range []string{"background", "interactive-4"} {
			if got := <-done; got != want {
				t.Fatalf("want %s, got %s at %s", want, got, time.Since(start))
			}
		}
		// A canceled waiter consumes no quota: the next grant is still one interval
		// after the previous one.
		canceled, stop := context.WithCancel(normal)
		startWaiter(canceled, "must not happen")
		stop()
		if got := <-done; got != context.Canceled.Error() {
			t.Fatal(got)
		}
		synctest.Wait()
		before := time.Since(start)
		if err := s.Acquire(normal, AniBTTorznab); err != nil || time.Since(start)-before != time.Second {
			t.Fatalf("canceled waiter consumed quota: %v", err)
		}
	})
}

type memoryStore struct{ state Health }

func (s *memoryStore) Load(context.Context) ([]Health, error) { return []Health{s.state}, nil }
func (s *memoryStore) Save(_ context.Context, h Health) error { s.state = h; return nil }

func TestCooldownMaxRestartAndBackoff(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	clock, store := &manualClock{now: now}, &memoryStore{}
	s := testScheduler(t, clock, store)
	check := func(err error, status int, d time.Duration) {
		t.Helper()
		var limited *ThrottleError
		if !errors.As(err, &limited) || limited.StatusCode != status || limited.RetryAfter != d {
			t.Fatalf("want %d %s, got %+v", status, d, limited)
		}
	}
	check(s.ObserveThrottle(t.Context(), AniBTMetadata, 429, "60"), 429, time.Minute)
	clock.advance(time.Second)
	check(s.ObserveThrottle(t.Context(), AniBTMetadata, 503, now.Add(30*time.Second).Format(http.TimeFormat)), 503, 59*time.Second)
	s = testScheduler(t, clock, store)
	var blocked *CooldownError
	if err := s.Acquire(t.Context(), AniBTMetadata); !errors.As(err, &blocked) || blocked.RetryAfter != 59*time.Second {
		t.Fatalf("restart: %v", err)
	}
	if err := s.Acquire(t.Context(), AniBTTorznab); err != nil {
		t.Fatal(err)
	}
	clock.advance(59 * time.Second)
	if err := s.Acquire(t.Context(), AniBTMetadata); err != nil {
		t.Fatal(err)
	}
	check(s.ObserveThrottle(t.Context(), AniBTMetadata, 429, "invalid"), 429, 20*time.Second)
	check(s.ObserveThrottle(t.Context(), AniBTMetadata, 429, ""), 429, 40*time.Second)
	for range 6 {
		_ = s.ObserveThrottle(t.Context(), AniBTMetadata, 429, "invalid")
	}
	check(s.ObserveThrottle(t.Context(), AniBTMetadata, 429, "invalid"), 429, maxBackoff)
}

func TestRSSIndependentOriginLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		policies := DefaultPolicies()
		policies[AniBTRSS] = Policy{Interval: time.Nanosecond, Also: AniBTRSSOrigin}
		s, err := New(t.Context(), policies, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		for range 7 {
			if err := s.Acquire(t.Context(), AniBTRSS); err != nil {
				t.Fatal(err)
			}
		}
		if time.Since(start) < time.Second {
			t.Fatalf("more than six origin requests in one second")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPAttemptsChargeNotModifiedButNotCacheHits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(t, nil, nil)
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 304, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
		})}
		get := func(cacheHit bool) {
			if cacheHit {
				return
			}
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/rss", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := Do(client, s, AniBTRSS, r)
			if err != nil || resp.StatusCode != 304 {
				t.Fatalf("got %v, %v", resp, err)
			}
			resp.Body.Close()
		}
		get(false)
		now := time.Now()
		get(true)
		if calls != 1 || time.Since(now) != 0 {
			t.Fatal("cache caused admission or a network call")
		}
		get(false)
		if calls != 2 || time.Since(now) != 500*time.Millisecond {
			t.Fatal("304 attempt was not charged")
		}
	})
}

func TestRSSExplicitRetryChargesOnlyAniBTRedirectHop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testScheduler(t, nil, nil)
		var attempts []time.Time
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			response := &http.Response{Header: make(http.Header), Body: http.NoBody, Request: r}
			switch r.URL.Hostname() {
			case "feed.example":
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://anibt.net/rss")
			case "anibt.net":
				attempts = append(attempts, time.Now())
				if len(attempts) == 1 {
					return nil, errors.New("temporary transport error")
				}
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://other.example/rss")
			case "other.example":
				response.StatusCode = http.StatusNotModified
			}
			return response, nil
		})}
		get := func() (*http.Response, error) {
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://feed.example/rss", nil)
			if err != nil {
				t.Fatal(err)
			}
			return DoRSS(client, s, r)
		}
		if _, err := get(); err == nil {
			t.Fatal("first attempt should fail")
		}
		response, err := get()
		if err != nil || response.StatusCode != http.StatusNotModified {
			t.Fatalf("retry = %v, %v", response, err)
		}
		response.Body.Close()
		if len(attempts) != 2 || attempts[1].Sub(attempts[0]) != 500*time.Millisecond || !time.Now().Equal(attempts[1]) {
			t.Fatalf("AniBT attempts = %v, finish = %v", attempts, time.Now())
		}
	})
}

type captureRuntimeLogger struct {
	observabilityport.NopRuntimeLogger
	warnings []observabilityport.RuntimeEvent
}

func (l *captureRuntimeLogger) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	l.warnings = append(l.warnings, event)
}

func TestObserveThrottleLogsWarningEvent(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	s := testScheduler(t, &manualClock{now: now}, nil)
	logger := &captureRuntimeLogger{}
	s.SetLogger(logger)

	if err := s.ObserveThrottle(t.Context(), AniBTMetadata, http.StatusTooManyRequests, "30"); err == nil {
		t.Fatal("expected throttle error")
	}
	if len(logger.warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(logger.warnings))
	}
	got := logger.warnings[0]
	if got.Component != "provider_outbound" || got.Kind != string(AniBTMetadata) || got.HTTPStatus != http.StatusTooManyRequests || got.ReasonCode != ReasonRateLimited || got.DurationMS != 30000 {
		t.Fatalf("unexpected runtime warning event: %+v", got)
	}
}
