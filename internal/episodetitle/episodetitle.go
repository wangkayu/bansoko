// Package episodetitle reads provider season and episode lists with an
// in-process TTL cache; the file manager's match lists the bound show's seasons
// and episodes from it. Naming never reads it: provider episode titles are not
// naming facts. One client call fetches the whole episode list of a season.
package episodetitle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Source names an episode-list provider.
type Source string

const SourceTMDB Source = "tmdb"

// Key identifies one cached episode list. Season is the TMDB season number.
type Key struct {
	Source Source
	WorkID string
	Season int
}

// List is the provider's ordered episode list of one season with air dates;
// the file manager's match lists it.
type List struct {
	Episodes []Episode
}

// Episode is one entry of a provider season list.
type Episode struct {
	Number  int
	Title   string
	AirDate string
}

// Season is one season of a provider show.
type Season struct {
	Number       int
	Name         string
	EpisodeCount int
	AirDate      string
}

// SeasonClient fetches the seasons of a show. Like Client it returns
// ErrNotFound when the provider says the show does not exist.
type SeasonClient interface {
	Seasons(ctx context.Context, workID string) ([]Season, error)
}

// Client fetches one whole episode list. It returns ErrNotFound when the
// provider positively says the work/season does not exist; every other error
// is treated as transient.
type Client interface {
	Episodes(ctx context.Context, key Key) (List, error)
}

var (
	// ErrNotFound is a definite "work/season does not exist" answer.
	ErrNotFound = errors.New("episode list not found")
	// ErrTransient wraps every failure that is not a definite answer; callers retry.
	ErrTransient = errors.New("episode list lookup failed transiently")
)

// Options configures a Lookup. Zero values take the documented defaults.
type Options struct {
	Clients map[Source]Client
	// SeasonClients list the seasons of a show per source (TMDB only).
	SeasonClients map[Source]SeasonClient
	Now           func() time.Time // default time.Now
	TTL           time.Duration    // cache lifetime of a list or season set, default 1h
	QueryTimeout  time.Duration    // per client call, default 5s
	Concurrency   map[Source]int   // per-source in-flight client calls, default 2
}

const (
	defaultTTL          = time.Hour
	defaultQueryTimeout = DefaultQueryTimeout
	defaultConcurrency  = 2
)

// DefaultQueryTimeout bounds one client call (and, separately, the wait for a
// per-source query slot).
const DefaultQueryTimeout = 5 * time.Second

type entry struct {
	list      List
	fetchedAt time.Time
}

// Lookup is a concurrency-safe cached season and episode list reader.
type Lookup struct {
	clients       map[Source]Client
	seasonClients map[Source]SeasonClient
	now           func() time.Time
	ttl           time.Duration
	queryTimeout  time.Duration
	sems          map[Source]chan struct{}

	mu      sync.Mutex
	cache   map[Key]entry
	seasons map[seasonKey]seasonEntry
	group   singleflight.Group
}

// New builds a Lookup from o, applying defaults.
func New(o Options) *Lookup {
	l := &Lookup{
		clients:      make(map[Source]Client, len(o.Clients)),
		now:          o.Now,
		ttl:          o.TTL,
		queryTimeout: o.QueryTimeout,
		sems:         make(map[Source]chan struct{}, len(o.Clients)),
		cache:        map[Key]entry{},
		seasons:      map[seasonKey]seasonEntry{},
	}
	l.seasonClients = make(map[Source]SeasonClient, len(o.SeasonClients))
	for source, client := range o.SeasonClients {
		if client == nil {
			continue
		}
		l.seasonClients[source] = client
		if l.sems[source] == nil {
			limit := o.Concurrency[source]
			if limit <= 0 {
				limit = defaultConcurrency
			}
			l.sems[source] = make(chan struct{}, limit)
		}
	}
	if l.now == nil {
		l.now = time.Now
	}
	if l.ttl <= 0 {
		l.ttl = defaultTTL
	}
	if l.queryTimeout <= 0 {
		l.queryTimeout = defaultQueryTimeout
	}
	for source, client := range o.Clients {
		if client == nil {
			continue
		}
		l.clients[source] = client
		limit := o.Concurrency[source]
		if limit <= 0 {
			limit = defaultConcurrency
		}
		l.sems[source] = make(chan struct{}, limit)
	}
	return l
}

// Season returns the whole episode list of key's season. A missing season is
// an empty list. Within the cache lifetime (TTL) the stored list is reused; bypassCache forces one fresh client call. A
// transient failure returns an error wrapping ErrTransient and is not cached.
func (l *Lookup) Season(ctx context.Context, key Key, bypassCache bool) (List, error) {
	if err := ctx.Err(); err != nil {
		return List{}, transient(err)
	}
	client := l.clients[key.Source]
	if client == nil {
		return List{}, transient(fmt.Errorf("no episode list client for source %q", key.Source))
	}
	if !bypassCache {
		if list, ok := l.fresh(key); ok {
			return list, nil
		}
	}
	return l.fetchShared(ctx, client, key, bypassCache)
}

// fetchShared runs one client call shared by concurrent callers of the same
// key, waiting for it only as long as ctx allows.
func (l *Lookup) fetchShared(ctx context.Context, client Client, key Key, bypassCache bool) (List, error) {
	flightKey := fmt.Sprintf("%s\x00%s\x00%d", key.Source, key.WorkID, key.Season)
	if bypassCache {
		flightKey += "\x00bypass"
	}
	detached := context.WithoutCancel(ctx)
	ch := l.group.DoChan(flightKey, func() (any, error) {
		// A flight that finished between the cache check and DoChan leaves a
		// just-stored entry; reuse it instead of refetching.
		if !bypassCache {
			if list, ok := l.fresh(key); ok {
				return list, nil
			}
		}
		return l.fetch(detached, client, key)
	})
	select {
	case <-ctx.Done():
		return List{}, transient(ctx.Err())
	case result := <-ch:
		if result.Err != nil {
			return List{}, result.Err
		}
		return result.Val.(List), nil
	}
}

type seasonKey struct {
	Source Source
	WorkID string
}

type seasonEntry struct {
	seasons   []Season
	fetchedAt time.Time
}

// Seasons returns the seasons of a show, cached for the TTL; the
// work's absence is an empty list. A transient failure returns an error
// wrapping ErrTransient and is not cached.
func (l *Lookup) Seasons(ctx context.Context, source Source, workID string) ([]Season, error) {
	if err := ctx.Err(); err != nil {
		return nil, transient(err)
	}
	client := l.seasonClients[source]
	if client == nil {
		return nil, transient(fmt.Errorf("no season client for source %q", source))
	}
	key := seasonKey{Source: source, WorkID: workID}
	l.mu.Lock()
	cached, ok := l.seasons[key]
	l.mu.Unlock()
	if ok && l.now().Sub(cached.fetchedAt) < l.ttl {
		return append([]Season(nil), cached.seasons...), nil
	}
	detached := context.WithoutCancel(ctx)
	ch := l.group.DoChan(fmt.Sprintf("seasons\x00%s\x00%s", source, workID), func() (any, error) {
		sem := l.sems[source]
		waitCtx, cancelWait := context.WithTimeout(detached, l.queryTimeout)
		select {
		case sem <- struct{}{}:
			cancelWait()
		case <-waitCtx.Done():
			cancelWait()
			return nil, transient(fmt.Errorf("wait for %s query slot: %w", source, waitCtx.Err()))
		}
		defer func() { <-sem }()
		callCtx, cancel := context.WithTimeout(detached, l.queryTimeout)
		defer cancel()
		seasons, err := client.Seasons(callCtx, workID)
		switch {
		case errors.Is(err, ErrNotFound):
			seasons = nil
		case err != nil:
			return nil, transient(err)
		}
		l.mu.Lock()
		l.seasons[key] = seasonEntry{seasons: append([]Season(nil), seasons...), fetchedAt: l.now()}
		l.mu.Unlock()
		return seasons, nil
	})
	select {
	case <-ctx.Done():
		return nil, transient(ctx.Err())
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return append([]Season(nil), result.Val.([]Season)...), nil
	}
}

// fresh returns the cached list when it is younger than the TTL.
func (l *Lookup) fresh(key Key) (List, bool) {
	l.mu.Lock()
	e, ok := l.cache[key]
	l.mu.Unlock()
	age := l.now().Sub(e.fetchedAt)
	return e.list, ok && age < l.ttl
}

// fetch performs one rate-limited, time-bounded client call and caches a
// definite answer. ctx is detached from caller cancellation.
func (l *Lookup) fetch(ctx context.Context, client Client, key Key) (List, error) {
	sem := l.sems[key.Source]
	waitCtx, cancelWait := context.WithTimeout(ctx, l.queryTimeout)
	select {
	case sem <- struct{}{}:
		cancelWait()
	case <-waitCtx.Done():
		cancelWait()
		return List{}, transient(fmt.Errorf("wait for %s query slot: %w", key.Source, waitCtx.Err()))
	}
	defer func() { <-sem }()

	callCtx, cancel := context.WithTimeout(ctx, l.queryTimeout)
	defer cancel()
	list, err := client.Episodes(callCtx, key)
	switch {
	case errors.Is(err, ErrNotFound):
		list = List{}
	case err != nil:
		return List{}, transient(err)
	}
	list = List{Episodes: append([]Episode(nil), list.Episodes...)}
	l.store(key, list)
	return list, nil
}

func (l *Lookup) store(key Key, list List) {
	fetchedAt := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, ok := l.cache[key]; ok && prev.fetchedAt.After(fetchedAt) {
		return
	}
	l.cache[key] = entry{list: list, fetchedAt: fetchedAt}
}

func transient(err error) error {
	if errors.Is(err, ErrTransient) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrTransient, err)
}
