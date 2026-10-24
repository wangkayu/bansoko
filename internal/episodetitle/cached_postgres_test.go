package episodetitle

// The persistent cache (migration 046) is DB-backed, so its behavior tests run
// against the disposable schema. The package's plain unit tests cover the
// in-process Lookup; this file covers the store contract the whitelist relies
// on: known rows never expire, absent rows re-check after the negative TTL, a
// transient failure persists nothing, and RefreshWork — the 刷新元数据 bypass —
// re-pulls the provider exactly once per call and rewrites even known rows.

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"bansoko/internal/testutil/disposabledb"
)

// mutableClient is a fakeClient whose answer the test swaps mid-scenario, the
// way the provider would change lists between two pulls.
type mutableClient struct {
	mu    sync.Mutex
	list  List
	err   error
	calls int
}

func (f *mutableClient) Episodes(context.Context, Key) (List, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return List{}, f.err
	}
	return f.list, nil
}

func (f *mutableClient) set(list List, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list, f.err = list, err
}

func (f *mutableClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func cacheHarness(t *testing.T, clock *fakeClock, clients map[Source]Client) (*PersistentCache, *sql.DB) {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return &PersistentCache{
		DB:     db,
		Lookup: New(Options{Clients: clients, Now: clock.Now}),
		Now:    clock.Now,
	}, db
}

func TestPersistentCacheStoresAnswersAndReReadsThem(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0).UTC()}
	client := &mutableClient{list: List{Episodes: []Episode{{Number: 1, Title: "First Snow"}, {Number: 2, Title: "  "}}}}
	cache, _ := cacheHarness(t, clock, map[Source]Client{SourceBGM: client})

	title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false)
	if err != nil {
		t.Fatalf("EpisodeTitle: unexpected error %v", err)
	}
	if !title.Known || title.Title != "First Snow" {
		t.Fatalf("episode 1: want known First Snow, got %+v", title)
	}
	if client.callCount() != 1 {
		t.Fatalf("first lookup: want one client call, got %d", client.callCount())
	}

	// The second read of the same key is served from the stored row: the
	// provider hears nothing (the cache-first property of the plain render).
	if _, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false); err != nil {
		t.Fatalf("second EpisodeTitle: unexpected error %v", err)
	}
	if client.callCount() != 1 {
		t.Fatalf("second plain lookup must not re-fetch, got %d client calls", client.callCount())
	}

	// An episode the list names with no title stores the definite absent.
	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 2, false); err != nil || title.Known {
		t.Fatalf("episode 2: want absent, got %+v err=%v", title, err)
	}
	if client.callCount() != 1 {
		t.Fatalf("absent store should still be one fetch, got %d", client.callCount())
	}

	// Beyond the list's end is also a definite negative, stored from the
	// same fetched list (the Lookup's own list cache still holds it, so no
	// second provider call happens).
	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 3, false); err != nil || title.Known {
		t.Fatalf("episode 3: want absent, got %+v err=%v", title, err)
	}
	if client.callCount() != 1 {
		t.Fatalf("episode 3 stores from the same list, want one fetch total, got %d", client.callCount())
	}

	// Only the negative expires: after the negative TTL the absent rows
	// re-check the provider; the known row never re-fetches.
	clock.Advance(7 * time.Hour)
	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 2, false); err != nil || title.Known {
		t.Fatalf("expired absent: want re-checked absent, got %+v err=%v", title, err)
	}
	if client.callCount() != 2 {
		t.Fatalf("expired absent re-check: want exactly one more fetch, got %d total", client.callCount())
	}
	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false); err != nil || !title.Known || title.Title != "First Snow" {
		t.Fatalf("known row must survive the clock: got %+v err=%v", title, err)
	}
	if client.callCount() != 2 {
		t.Fatalf("known row must never re-fetch, got %d total", client.callCount())
	}
}

func TestPersistentCacheTransientFailurePersistsNothing(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0).UTC()}
	client := &mutableClient{err: errors.New("provider unreachable")}
	cache, _ := cacheHarness(t, clock, map[Source]Client{SourceBGM: client})

	if _, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false); !errors.Is(err, ErrTransient) {
		t.Fatalf("away provider: want transient error, got %v", err)
	}
	// The failure is not cached: repairing the provider answers without any
	// stale row in the way.
	client.set(List{Episodes: []Episode{{Number: 1, Title: "Recovered"}}}, nil)
	title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false)
	if err != nil || !title.Known || title.Title != "Recovered" {
		t.Fatalf("after repair: want known Recovered, got %+v err=%v", title, err)
	}
}

func TestRefreshWorkRePullsOnceAndOverwritesKnownRows(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0).UTC()}
	client := &mutableClient{list: List{Episodes: []Episode{{Number: 1, Title: "Old"}}}}
	cache, _ := cacheHarness(t, clock, map[Source]Client{SourceBGM: client})

	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false); err != nil || !title.Known || title.Title != "Old" {
		t.Fatalf("seed: want known Old, got %+v err=%v", title, err)
	}
	// The provider answers differently today; a plain render never hears it
	// (known rows are the freeze point).
	client.set(List{Episodes: []Episode{{Number: 1, Title: "New"}, {Number: 2, Title: "Second"}}}, nil)
	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false); err != nil || title.Title != "Old" {
		t.Fatalf("plain lookup after provider change: want frozen Old, got %+v err=%v", title, err)
	}
	if client.callCount() != 1 {
		t.Fatalf("frozen known: no refetch allowed, got %d", client.callCount())
	}

	// RefreshWork — the 刷新元数据 re-pull — hears the provider and rewrites
	// even the known row, exactly one fetch per call.
	if err := cache.RefreshWork(context.Background(), SourceBGM, "42", 0); err != nil {
		t.Fatalf("RefreshWork: unexpected error %v", err)
	}
	if client.callCount() != 2 {
		t.Fatalf("one refresh = one fetch, got %d", client.callCount())
	}
	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false); err != nil || !title.Known || title.Title != "New" {
		t.Fatalf("after refresh: want known New, got %+v err=%v", title, err)
	}
	if client.callCount() != 2 {
		t.Fatalf("reading must stay cache-first after the overwrite, got %d", client.callCount())
	}
	// A second refresh re-pulls again: the bypass never goes through a cache.
	if err := cache.RefreshWork(context.Background(), SourceBGM, "42", 0); err != nil {
		t.Fatalf("second RefreshWork: unexpected error %v", err)
	}
	if client.callCount() != 3 {
		t.Fatalf("second refresh re-pulls, got %d total", client.callCount())
	}
}

func TestRefreshWorkTransientFailureKeepsRows(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0).UTC()}
	client := &mutableClient{list: List{Episodes: []Episode{{Number: 1, Title: "Safe"}}}}
	cache, _ := cacheHarness(t, clock, map[Source]Client{SourceBGM: client})

	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false); err != nil || title.Title != "Safe" {
		t.Fatalf("seed: want known Safe, got %+v err=%v", title, err)
	}
	client.set(List{}, errors.New("offline"))
	if err := cache.RefreshWork(context.Background(), SourceBGM, "42", 0); !errors.Is(err, ErrTransient) {
		t.Fatalf("offline refresh must fail ErrTransient, got %v", err)
	}
	// The stored rows are untouched by the failed bypass.
	if title, err := cache.EpisodeTitle(context.Background(), SourceBGM, "42", 0, 1, false); err != nil || title.Title != "Safe" {
		t.Fatalf("failed refresh must not disturb rows: got %+v err=%v", title, err)
	}
	if client.callCount() != 2 {
		t.Fatalf("failed refresh issues no retry of its own, got %d", client.callCount())
	}
}
