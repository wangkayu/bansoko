package port

import (
	"context"
	"time"
)

// ProviderImageRef is one registered upstream image: Key is the opaque
// SHA-256 identifier the browser sees, SourceURL the provider URL only the
// server ever fetches.
type ProviderImageRef struct {
	Key       string
	SourceURL string
}

// ProviderImageEntry is a registry row with its optional cached bytes.
// HasBytes is false until the first successful fetch.
type ProviderImageEntry struct {
	Key         string
	Provider    string
	SourceURL   string
	HasBytes    bool
	ImageBytes  []byte
	ContentType string
	FetchedAt   time.Time
	ExpiresAt   time.Time
}

// ProviderImageStore is the durable registry and byte cache behind the
// same-origin provider image proxy. Get returns stale bytes so the proxy can
// keep serving the last good image through an upstream outage.
type ProviderImageStore interface {
	Register(ctx context.Context, provider string, images []ProviderImageRef) error
	Get(ctx context.Context, key string) (ProviderImageEntry, bool, error)
	Store(ctx context.Context, key string, imageBytes []byte, contentType string, fetchedAt, expiresAt time.Time) error
}
