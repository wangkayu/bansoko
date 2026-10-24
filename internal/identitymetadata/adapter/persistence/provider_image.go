package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	generated "bansoko/internal/identitymetadata/adapter/persistence/generated"
	identitymetadataport "bansoko/internal/identitymetadata/port"
)

const (
	maxProviderImageBatch = 1000
	// maxProviderImageBytes mirrors the poster cap; the proxy enforces it
	// before storing and the repository refuses anything larger.
	maxProviderImageBytes = 2 << 20
)

// ProviderImageRepository is the durable registry and byte cache behind the
// same-origin provider image proxy (table provider_image_cache).
type ProviderImageRepository struct {
	db *sql.DB
}

func NewProviderImageRepository(db *sql.DB) *ProviderImageRepository {
	return &ProviderImageRepository{db: db}
}

var _ identitymetadataport.ProviderImageStore = (*ProviderImageRepository)(nil)

// Register records the provider images the server is about to hand out by
// key. Existing rows keep their bytes; duplicate keys in one call collapse.
func (r *ProviderImageRepository) Register(ctx context.Context, provider string, images []identitymetadataport.ProviderImageRef) error {
	if r == nil || r.db == nil {
		return errors.New("provider image database is nil")
	}
	if len(images) == 0 {
		return nil
	}
	if len(images) > maxProviderImageBatch {
		return fmt.Errorf("provider image registration batch exceeds %d", maxProviderImageBatch)
	}
	seen := make(map[string]struct{}, len(images))
	keys := make([]string, 0, len(images))
	urls := make([]string, 0, len(images))
	for _, image := range images {
		if image.Key == "" || image.SourceURL == "" {
			return errors.New("provider image reference is incomplete")
		}
		if _, dup := seen[image.Key]; dup {
			continue
		}
		seen[image.Key] = struct{}{}
		keys = append(keys, image.Key)
		urls = append(urls, image.SourceURL)
	}
	if err := generated.New(r.db).RegisterProviderImages(ctx, generated.RegisterProviderImagesParams{
		ImageKeys: keys, Provider: provider, SourceUrls: urls,
	}); err != nil {
		return fmt.Errorf("register provider images: %w", err)
	}
	return nil
}

// Get returns the registry row, stale bytes included. An unknown key is
// (zero, false, nil).
func (r *ProviderImageRepository) Get(ctx context.Context, key string) (identitymetadataport.ProviderImageEntry, bool, error) {
	if r == nil || r.db == nil {
		return identitymetadataport.ProviderImageEntry{}, false, errors.New("provider image database is nil")
	}
	queries := generated.New(r.db)
	row, err := queries.GetProviderImage(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return identitymetadataport.ProviderImageEntry{}, false, nil
	}
	if err != nil {
		return identitymetadataport.ProviderImageEntry{}, false, fmt.Errorf("get provider image: %w", err)
	}
	// Best effort: keeps a viewed image out of the idle purge.
	_ = queries.TouchProviderImage(ctx, key)
	entry := identitymetadataport.ProviderImageEntry{Key: row.ImageKey, Provider: row.Provider, SourceURL: row.SourceUrl}
	if row.ImageBytes != nil && row.ContentType.Valid && row.FetchedAt.Valid && row.ExpiresAt.Valid {
		entry.HasBytes = true
		entry.ImageBytes = append([]byte(nil), row.ImageBytes...)
		entry.ContentType = row.ContentType.String
		entry.FetchedAt = row.FetchedAt.Time
		entry.ExpiresAt = row.ExpiresAt.Time
	}
	return entry, true, nil
}

// Store attaches fetched bytes to an already registered key. A key that has
// been purged since is not resurrected.
func (r *ProviderImageRepository) Store(ctx context.Context, key string, imageBytes []byte, contentType string, fetchedAt, expiresAt time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("provider image database is nil")
	}
	if len(imageBytes) == 0 || len(imageBytes) > maxProviderImageBytes {
		return fmt.Errorf("provider image bytes must be between 1 and %d bytes", maxProviderImageBytes)
	}
	if contentType == "" || len(contentType) > 256 {
		return errors.New("provider image content type is invalid")
	}
	if expiresAt.Before(fetchedAt) {
		return errors.New("provider image expiry is invalid")
	}
	if _, err := generated.New(r.db).StoreProviderImage(ctx, generated.StoreProviderImageParams{
		ImageKey:    key,
		ImageBytes:  append([]byte(nil), imageBytes...),
		ContentType: sql.NullString{String: contentType, Valid: true},
		FetchedAt:   sql.NullTime{Time: fetchedAt.UTC(), Valid: true},
		ExpiresAt:   sql.NullTime{Time: expiresAt.UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("store provider image: %w", err)
	}
	return nil
}
