// Package events provides the in-process invalidation broadcast used by the
// realtime read-model refresh path.
package events

import (
	"context"
	"encoding/json"
	"sync"
)

const (
	TargetCatalog    = "catalog"
	TargetSubscribed = "subscribed"
	TargetRSS        = "rss"
	TargetRecords    = "records"
)

// Event is a lightweight read-model invalidation signal. It deliberately does
// not carry a business object or durable event identity. ManagedAnimeID is an
// optional scope hint: set only when the change came from one anime's owner
// chain, so clients may narrow invalidation; absent means "unscoped".
type Event struct {
	Target         string `json:"target"`
	ManagedAnimeID string `json:"managed_anime_id,omitempty"`
}

// AfterCommit returns a hook that publishes after the caller's transaction
// has committed, while preserving any existing best-effort hook.
func (h *Hub) AfterCommit(target string, next func(context.Context)) func(context.Context) {
	return h.AfterCommitFor(target, "", next)
}

// AfterCommitFor is AfterCommit with the optional managed-anime scope hint.
func (h *Hub) AfterCommitFor(target, managedAnimeID string, next func(context.Context)) func(context.Context) {
	if h == nil {
		return next
	}
	return func(ctx context.Context) {
		h.Publish(Event{Target: target, ManagedAnimeID: managedAnimeID})
		if next != nil {
			next(ctx)
		}
	}
}

// Hub broadcasts invalidation signals to connected clients in one process.
// Subscribers use buffered channels so a slow client cannot block a writer.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[chan Event]struct{}
	closed      bool
}

// NewHub creates an in-memory event hub.
func NewHub() *Hub {
	return &Hub{subscribers: make(map[chan Event]struct{})}
}

// Publish sends an event to every current subscriber. A slow subscriber drops
// the signal; clients compensate with their disconnected polling fallback.
func (h *Hub) Publish(event Event) {
	if h == nil || event.Target == "" {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return
	}
	for subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

// Subscribe registers a buffered subscriber and returns a cancellation
// function. Cancellation is idempotent and never closes a channel that may be
// concurrently used by Publish.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	if h == nil {
		channel := make(chan Event)
		return channel, func() {}
	}
	channel := make(chan Event, 16)
	h.mu.Lock()
	if h.subscribers == nil {
		h.subscribers = make(map[chan Event]struct{})
	}
	if h.closed {
		h.mu.Unlock()
		close(channel)
		return channel, func() {}
	}
	h.subscribers[channel] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers, channel)
			h.mu.Unlock()
		})
	}
	return channel, cancel
}

// Close prevents new subscriptions and removes all current subscribers. The
// channels are intentionally not closed because Publish may be in flight; the
// request context owns stream termination.
func (h *Hub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	h.subscribers = make(map[chan Event]struct{})
	h.mu.Unlock()
}

// MarshalJSON keeps the event payload deliberately small and stable for both
// the SSE transport and future in-process consumers.
func (e Event) MarshalJSON() ([]byte, error) {
	type wireEvent struct {
		Target string `json:"target"`
	}
	return json.Marshal(wireEvent{Target: e.Target})
}
