package episodetitle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeClient struct {
	calls atomic.Int32
	fn    func(ctx context.Context, key Key) (List, error)
}

func (f *fakeClient) Episodes(ctx context.Context, key Key) (List, error) {
	f.calls.Add(1)
	return f.fn(ctx, key)
}

func staticClient(episodes []Episode, err error) *fakeClient {
	return &fakeClient{fn: func(context.Context, Key) (List, error) {
		return List{Episodes: episodes}, err
	}}
}

func one() List { return List{Episodes: []Episode{{Number: 1, Title: "One"}}} }

var tmdbKey = Key{Source: SourceTMDB, WorkID: "42", Season: 1}

func newLookup(client Client, clock *fakeClock) *Lookup {
	return New(Options{Clients: map[Source]Client{SourceTMDB: client}, Now: clock.Now})
}

func mustSeason(t *testing.T, l *Lookup, key Key, bypass bool) List {
	t.Helper()
	list, err := l.Season(context.Background(), key, bypass)
	if err != nil {
		t.Fatalf("Season(%+v): unexpected error %v", key, err)
	}
	return list
}

func firstTitle(list List) string {
	if len(list.Episodes) == 0 {
		return ""
	}
	return list.Episodes[0].Title
}

func TestListCachedWithinTTLAndRefetchAfter(t *testing.T) {
	for name, client := range map[string]*fakeClient{
		"found":     staticClient([]Episode{{Number: 7, Title: "Seven"}}, nil),
		"not found": staticClient(nil, ErrNotFound),
	} {
		t.Run(name, func(t *testing.T) {
			clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
			l := newLookup(client, clock)
			mustSeason(t, l, tmdbKey, false)
			clock.Advance(59 * time.Minute)
			mustSeason(t, l, tmdbKey, false)
			if n := client.calls.Load(); n != 1 {
				t.Fatalf("the list must be cached within the TTL, got %d calls", n)
			}
			clock.Advance(time.Minute)
			mustSeason(t, l, tmdbKey, false)
			if n := client.calls.Load(); n != 2 {
				t.Fatalf("expected refetch after the TTL, got %d calls", n)
			}
		})
	}
}

func TestTransientFailureIsNotCached(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	boom := errors.New("HTTP 503")
	fail := true
	client := &fakeClient{fn: func(context.Context, Key) (List, error) {
		if fail {
			return List{}, boom
		}
		return one(), nil
	}}
	l := newLookup(client, clock)

	if _, err := l.Season(context.Background(), tmdbKey, false); !errors.Is(err, ErrTransient) || !errors.Is(err, boom) {
		t.Fatalf("expected transient error, got %v", err)
	}
	fail = false
	if got := firstTitle(mustSeason(t, l, tmdbKey, false)); got != "One" {
		t.Fatalf("got %q", got)
	}
	if n := client.calls.Load(); n != 2 {
		t.Fatalf("transient failure must not be cached, got %d calls", n)
	}
}

func TestTransientFailureKeepsPreviousEntry(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	fail := false
	client := &fakeClient{fn: func(context.Context, Key) (List, error) {
		if fail {
			return List{}, errors.New("HTTP 429")
		}
		return one(), nil
	}}
	l := newLookup(client, clock)
	mustSeason(t, l, tmdbKey, false)
	fail = true
	if _, err := l.Season(context.Background(), tmdbKey, true); !errors.Is(err, ErrTransient) {
		t.Fatalf("expected transient bypass failure, got %v", err)
	}
	if got := firstTitle(mustSeason(t, l, tmdbKey, false)); got != "One" {
		t.Fatalf("previous entry must survive a transient failure, got %q", got)
	}
}

func TestMissingClientAndCancelledContextAreTransient(t *testing.T) {
	l := New(Options{})
	if _, err := l.Season(context.Background(), tmdbKey, false); !errors.Is(err, ErrTransient) {
		t.Fatalf("missing client: got %v", err)
	}
	l = newLookup(staticClient(nil, nil), &fakeClock{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Season(ctx, tmdbKey, false); !errors.Is(err, ErrTransient) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx: got %v", err)
	}
}

func TestSingleflightSharesOneCall(t *testing.T) {
	const callers = 8
	entered := make(chan struct{}, callers)
	release := make(chan struct{})
	client := &fakeClient{fn: func(context.Context, Key) (List, error) {
		entered <- struct{}{}
		<-release
		return one(), nil
	}}
	l := newLookup(client, &fakeClock{now: time.Unix(1, 0)})

	var started, done sync.WaitGroup
	started.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer done.Done()
			started.Done()
			list, err := l.Season(context.Background(), tmdbKey, false)
			if err == nil && firstTitle(list) != "One" {
				err = errors.New("wrong list")
			}
			errs <- err
		}()
	}
	started.Wait()
	<-entered
	// Every caller either joins the in-flight call or, after release, hits the
	// cache it stored; either way the client sees exactly one call.
	close(release)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := client.calls.Load(); n != 1 {
		t.Fatalf("expected 1 shared client call, got %d", n)
	}
}

func TestCallerCancelDoesNotCancelSharedCall(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	client := &fakeClient{fn: func(ctx context.Context, _ Key) (List, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return one(), nil
		case <-ctx.Done():
			return List{}, ctx.Err()
		}
	}}
	l := newLookup(client, &fakeClock{now: time.Unix(1, 0)})

	first, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := l.Season(first, tmdbKey, false)
		firstErr <- err
	}()
	<-entered
	cancelFirst()
	if err := <-firstErr; !errors.Is(err, ErrTransient) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: got %v", err)
	}
	second := make(chan string, 1)
	go func() {
		list, _ := l.Season(context.Background(), tmdbKey, false)
		second <- firstTitle(list)
	}()
	close(release)
	if title := <-second; title != "One" {
		t.Fatalf("shared call must survive the first waiter's cancel, got %q", title)
	}
	if n := client.calls.Load(); n != 1 {
		t.Fatalf("expected 1 client call, got %d", n)
	}
}

func TestPerSourceConcurrencyLimit(t *testing.T) {
	const limit, callers = 2, 6
	var inFlight, maxInFlight atomic.Int32
	entered := make(chan struct{}, callers)
	release := make(chan struct{})
	client := &fakeClient{fn: func(context.Context, Key) (List, error) {
		current := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if current <= prev || maxInFlight.CompareAndSwap(prev, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		inFlight.Add(-1)
		return one(), nil
	}}
	l := New(Options{
		Clients:     map[Source]Client{SourceTMDB: client},
		Now:         (&fakeClock{now: time.Unix(1, 0)}).Now,
		Concurrency: map[Source]int{SourceTMDB: limit},
	})
	var done sync.WaitGroup
	done.Add(callers)
	for i := 0; i < callers; i++ {
		key := Key{Source: SourceTMDB, WorkID: string(rune('a' + i))}
		go func() {
			defer done.Done()
			if _, err := l.Season(context.Background(), key, false); err != nil {
				t.Error(err)
			}
		}()
	}
	for i := 0; i < limit; i++ {
		<-entered
	}
	close(release)
	done.Wait()
	if n := client.calls.Load(); n != callers {
		t.Fatalf("expected %d calls, got %d", callers, n)
	}
	if m := maxInFlight.Load(); m != limit {
		t.Fatalf("expected max %d in flight, got %d", limit, m)
	}
}

func TestQueryTimeoutIsTransient(t *testing.T) {
	client := &fakeClient{fn: func(ctx context.Context, _ Key) (List, error) {
		<-ctx.Done()
		return List{}, ctx.Err()
	}}
	l := New(Options{
		Clients:      map[Source]Client{SourceTMDB: client},
		QueryTimeout: time.Millisecond,
	})
	if _, err := l.Season(context.Background(), tmdbKey, false); !errors.Is(err, ErrTransient) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected transient deadline error, got %v", err)
	}
}

func TestSeasonReturnsTheWholeListAndReusesIt(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	client := &fakeClient{fn: func(context.Context, Key) (List, error) {
		return List{Episodes: []Episode{{Number: 1, Title: "One", AirDate: "2024-01-01"}, {Number: 2, Title: "", AirDate: "2024-01-08"}}}, nil
	}}
	l := newLookup(client, clock)
	list, err := l.Season(context.Background(), tmdbKey, false)
	if err != nil || len(list.Episodes) != 2 || list.Episodes[1].AirDate != "2024-01-08" {
		t.Fatalf("got %+v, %v", list, err)
	}
	if _, err := l.Season(context.Background(), tmdbKey, false); err != nil {
		t.Fatal(err)
	}
	if n := client.calls.Load(); n != 1 {
		t.Fatalf("a fresh list must be reused, got %d calls", n)
	}
	if _, err := l.Season(context.Background(), tmdbKey, true); err != nil {
		t.Fatal(err)
	}
	if n := client.calls.Load(); n != 2 {
		t.Fatalf("bypass must refetch, got %d calls", n)
	}
}

func TestSeasonMissingSeasonIsEmptyAndTransientFails(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	l := newLookup(staticClient(nil, ErrNotFound), clock)
	if list, err := l.Season(context.Background(), tmdbKey, false); err != nil || len(list.Episodes) != 0 {
		t.Fatalf("missing season: %+v %v", list, err)
	}
	l = newLookup(staticClient(nil, errors.New("HTTP 503")), clock)
	if _, err := l.Season(context.Background(), tmdbKey, false); !errors.Is(err, ErrTransient) {
		t.Fatalf("expected transient, got %v", err)
	}
}

type fakeSeasons struct {
	calls atomic.Int32
	fn    func() ([]Season, error)
}

func (f *fakeSeasons) Seasons(context.Context, string) ([]Season, error) {
	f.calls.Add(1)
	return f.fn()
}

func TestSeasonsAreCachedAndNotFoundIsEmpty(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	fake := &fakeSeasons{fn: func() ([]Season, error) {
		return []Season{{Number: 0, Name: "Specials"}, {Number: 1, EpisodeCount: 12}}, nil
	}}
	l := New(Options{Clients: map[Source]Client{SourceTMDB: staticClient(nil, nil)}, SeasonClients: map[Source]SeasonClient{SourceTMDB: fake}, Now: clock.Now})
	seasons, err := l.Seasons(context.Background(), SourceTMDB, "42")
	if err != nil || len(seasons) != 2 || seasons[0].Number != 0 {
		t.Fatalf("got %+v %v", seasons, err)
	}
	if _, err := l.Seasons(context.Background(), SourceTMDB, "42"); err != nil || fake.calls.Load() != 1 {
		t.Fatalf("cached read must not call again: %v, %d calls", err, fake.calls.Load())
	}
	clock.Advance(time.Hour)
	if _, err := l.Seasons(context.Background(), SourceTMDB, "42"); err != nil || fake.calls.Load() != 2 {
		t.Fatalf("expired entry must refetch: %v, %d calls", err, fake.calls.Load())
	}
	missing := New(Options{SeasonClients: map[Source]SeasonClient{SourceTMDB: &fakeSeasons{fn: func() ([]Season, error) { return nil, ErrNotFound }}}, Now: clock.Now})
	if seasons, err := missing.Seasons(context.Background(), SourceTMDB, "9"); err != nil || len(seasons) != 0 {
		t.Fatalf("missing show: %+v %v", seasons, err)
	}
	failing := New(Options{SeasonClients: map[Source]SeasonClient{SourceTMDB: &fakeSeasons{fn: func() ([]Season, error) { return nil, errors.New("HTTP 500") }}}, Now: clock.Now})
	if _, err := failing.Seasons(context.Background(), SourceTMDB, "9"); !errors.Is(err, ErrTransient) {
		t.Fatalf("expected transient, got %v", err)
	}
}
