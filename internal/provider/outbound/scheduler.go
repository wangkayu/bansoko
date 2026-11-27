// Package outbound coordinates provider request admission and shared cooldowns.
package outbound

import (
	"context"
	"fmt"
	"sync"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

type BucketKey string

const (
	AniBTMetadata        BucketKey = "anibt:metadata"
	AniBTRSS             BucketKey = "anibt:rss"
	AniBTRSSOrigin       BucketKey = "anibt:rss_origin"
	AniBTTorznab         BucketKey = "anibt:torznab"
	AniBTGroupDirectory  BucketKey = "anibt:group_directory"
	Mikan                BucketKey = "mikan:public"
	BGM                  BucketKey = "bgm:public"
	TMDB                 BucketKey = "tmdb:public"
	InteractiveBudget              = 2 * time.Second
	maxInteractiveStreak           = 3
)

// Policy spaces starts evenly, with a burst of one. In any half-open 60s
// window this permits at most 30 metadata, 120 RSS or 60 other AniBT starts.
// Also charges an independent egress ceiling atomically with the primary.
type Policy struct {
	Interval time.Duration
	Also     BucketKey
}

func DefaultPolicies() map[BucketKey]Policy {
	return map[BucketKey]Policy{
		AniBTMetadata:       {Interval: 2 * time.Second},
		AniBTRSS:            {Interval: 500 * time.Millisecond, Also: AniBTRSSOrigin},
		AniBTRSSOrigin:      {Interval: (time.Second + 5) / 6},
		AniBTTorznab:        {Interval: time.Second},
		AniBTGroupDirectory: {Interval: time.Second},
		Mikan:               {Interval: 2 * time.Second}, BGM: {Interval: time.Second}, TMDB: {Interval: time.Second},
	}
}

type Priority uint8

const (
	PriorityBackground Priority = iota
	PriorityInteractive
)

type priorityKey struct{}

func WithPriority(ctx context.Context, p Priority) context.Context {
	return context.WithValue(ctx, priorityKey{}, p)
}
func priority(ctx context.Context) Priority {
	p, _ := ctx.Value(priorityKey{}).(Priority)
	return p
}

type Timer interface {
	C() <-chan time.Time
	Stop()
}
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type systemClock struct{}

func (systemClock) Now() time.Time                 { return time.Now().UTC() }
func (systemClock) NewTimer(d time.Duration) Timer { return systemTimer{time.NewTimer(d)} }

type systemTimer struct{ *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.Timer.C }
func (t systemTimer) Stop()               { t.Timer.Stop() }

// Health is bounded diagnostic evidence for the most recent throttle event.
// Consecutive counts consecutive throttle responses within the backoff horizon.
type Health struct {
	Bucket        BucketKey
	CooldownUntil time.Time
	LastLimitedAt time.Time
	ReasonCode    string
	Summary       string
	Consecutive   int
	Version       int64
	UpdatedAt     time.Time
}

type Store interface {
	Load(context.Context) ([]Health, error)
	// Save must preserve the greatest cooldown deadline across concurrent writes.
	Save(context.Context, Health) error
}

type Coordinator interface {
	Acquire(context.Context, BucketKey) error
	ObserveThrottle(context.Context, BucketKey, int, string) error
}

type Scheduler struct {
	mu      sync.Mutex
	clock   Clock
	store   Store
	logger  observabilityport.RuntimeLogger
	buckets map[BucketKey]*bucket
}

func (s *Scheduler) SetLogger(logger observabilityport.RuntimeLogger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
}

type bucket struct {
	policy  Policy
	next    time.Time
	health  Health
	changed chan struct{}
	queue   []*waiter
	streak  int
}
type waiter struct {
	ctx      context.Context
	priority Priority
}

func New(ctx context.Context, policies map[BucketKey]Policy, clock Clock, store Store) (*Scheduler, error) {
	if clock == nil {
		clock = systemClock{}
	}
	s := &Scheduler{clock: clock, store: store, buckets: make(map[BucketKey]*bucket, len(policies))}
	for key, policy := range policies {
		if key == "" || policy.Interval <= 0 {
			return nil, fmt.Errorf("invalid outbound policy for %q", key)
		}
		if policy.Also != "" {
			secondary, ok := policies[policy.Also]
			if !ok || policy.Also == key || secondary.Also != "" {
				return nil, fmt.Errorf("invalid secondary outbound bucket for %q", key)
			}
		}
		s.buckets[key] = &bucket{policy: policy, changed: make(chan struct{})}
	}
	if store != nil {
		states, err := store.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("load provider outbound health: %w", err)
		}
		for _, health := range states {
			if b := s.buckets[health.Bucket]; b != nil {
				b.health = health
			}
		}
	}
	return s, nil
}

func (s *Scheduler) Health() []Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]Health, 0, len(s.buckets))
	for _, b := range s.buckets {
		if b.health.Version != 0 {
			states = append(states, b.health)
		}
	}
	return states
}

func (s *Scheduler) Acquire(ctx context.Context, key BucketKey) error {
	w := &waiter{ctx: ctx, priority: priority(ctx)}
	s.mu.Lock()
	b, ok := s.buckets[key]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown outbound bucket %q", key)
	}
	secondary := s.buckets[b.policy.Also]
	deadline := s.clock.Now().Add(InteractiveBudget)
	b.queue = append(b.queue, w)
	b.signal()
	defer func() {
		for i, queued := range b.queue {
			if queued == w {
				copy(b.queue[i:], b.queue[i+1:])
				b.queue[len(b.queue)-1] = nil
				b.queue = b.queue[:len(b.queue)-1]
				break
			}
		}
		b.signal()
		s.mu.Unlock()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := s.clock.Now()
		until := b.health.CooldownUntil
		next := b.next
		interval := b.policy.Interval
		var secondaryChanged <-chan struct{}
		if secondary != nil {
			until = later(until, secondary.health.CooldownUntil)
			next = later(next, secondary.next)
			interval = max(interval, secondary.policy.Interval)
			secondaryChanged = secondary.changed
		}
		if until.After(now) {
			return newCooldown(key, ReasonBucketCooldown, now, until)
		}
		head, fallback, ahead := b.position(w)
		if head && !next.After(now) && (w.priority != PriorityInteractive || !now.After(deadline)) {
			b.next = now.Add(b.policy.Interval)
			if secondary != nil {
				secondary.next = now.Add(secondary.policy.Interval)
				secondary.signal()
			}
			if fallback || w.priority == PriorityBackground {
				b.streak = 0
			} else {
				b.streak++
			}
			return nil
		}
		var timer Timer
		var wake <-chan time.Time
		if w.priority == PriorityInteractive {
			eligible := later(now, next).Add(time.Duration(ahead) * interval)
			if eligible.After(deadline) || !now.Before(deadline) {
				return newCooldown(key, ReasonWaitBudget, now, eligible)
			}
			delay := deadline.Sub(now)
			if next.After(now) {
				delay = min(delay, next.Sub(now))
			}
			timer = s.clock.NewTimer(delay)
		} else if head {
			timer = s.clock.NewTimer(next.Sub(now))
		}
		if timer != nil {
			wake = timer.C()
		}
		changed := b.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		case <-secondaryChanged:
		case <-wake:
		}
		if timer != nil {
			timer.Stop()
		}
		s.mu.Lock()
	}
}

// position simulates this queue's grant order for an interactive budget check.
// Within each priority FIFO holds, with a background grant after at most three
// interactive grants while a background waiter is present.
func (b *bucket) position(w *waiter) (head, noBackground bool, ahead int) {
	var interactive, background int
	var first Priority
	found := false
	for _, candidate := range b.queue {
		if candidate.ctx.Err() != nil {
			continue
		}
		if candidate == w {
			found = true
		}
		if candidate.priority == PriorityInteractive {
			if !found {
				interactive++
			}
		} else {
			if !found || w.priority == PriorityInteractive {
				background++
			}
			first = PriorityBackground
		}
	}
	if w.priority == PriorityBackground {
		if background > 0 {
			return false, false, background
		}
		// Count interactive waiters anywhere in the queue.
		for _, candidate := range b.queue {
			if candidate.priority == PriorityInteractive && candidate.ctx.Err() == nil && b.streak < maxInteractiveStreak {
				return false, false, 1
			}
		}
		return true, false, 0
	}
	slotsBeforeBackground := maxInteractiveStreak - b.streak
	for i := 0; i <= interactive; i++ {
		if background > 0 && slotsBeforeBackground == 0 {
			ahead++
			background--
			slotsBeforeBackground = maxInteractiveStreak
		}
		if i == interactive {
			break
		}
		ahead++
		if slotsBeforeBackground > 0 {
			slotsBeforeBackground--
		}
	}
	return ahead == 0, first != PriorityBackground, ahead
}

func (b *bucket) signal() { close(b.changed); b.changed = make(chan struct{}) }
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
