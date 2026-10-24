package port

import (
	"context"
	"time"
)

// PosterCacheEntry is the transport-neutral durable representation of a
// successfully fetched BGM poster.
type PosterCacheEntry struct {
	BGMSubjectID string
	ImageBytes   []byte
	ContentType  string
	ImageURL     string
	FetchedAt    time.Time
	ExpiresAt    time.Time
	LastUsed     time.Time
}

// PosterCache stores successful poster responses across resolver instances.
// Implementations may return stale entries so the resolver can preserve its
// last-good fallback during an upstream outage.
type PosterCache interface {
	Get(context.Context, string) (PosterCacheEntry, bool, error)
	Upsert(context.Context, PosterCacheEntry) error
}
