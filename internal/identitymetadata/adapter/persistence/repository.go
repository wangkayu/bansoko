// Package persistence implements the PostgreSQL persistence boundary for
// successful BGM poster bytes and their independent retention class.
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	generated "bansoko/internal/identitymetadata/adapter/persistence/generated"
	identitymetadataport "bansoko/internal/identitymetadata/port"
	"bansoko/internal/persistence/txscope"
)

const (
	maxRetentionBatchSize = 500
	maxPosterBytes        = 2 << 20
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

var _ identitymetadataport.PosterCache = (*Repository)(nil)

func (r *Repository) Get(ctx context.Context, bgmSubjectID string) (identitymetadataport.PosterCacheEntry, bool, error) {
	if r == nil || r.db == nil {
		return identitymetadataport.PosterCacheEntry{}, false, errors.New("poster cache database is nil")
	}
	bgmSubjectID = strings.TrimSpace(bgmSubjectID)
	if bgmSubjectID == "" {
		return identitymetadataport.PosterCacheEntry{}, false, errors.New("poster cache subject id is required")
	}
	row, err := generated.New(r.db).GetMediaPosterCache(ctx, bgmSubjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return identitymetadataport.PosterCacheEntry{}, false, nil
	}
	if err != nil {
		return identitymetadataport.PosterCacheEntry{}, false, fmt.Errorf("get media poster cache: %w", err)
	}
	return identitymetadataport.PosterCacheEntry{
		BGMSubjectID: row.BgmID,
		ImageBytes:   append([]byte(nil), row.ImageBytes...),
		ContentType:  row.ContentType,
		ImageURL:     row.ImageUrl,
		FetchedAt:    row.FetchedAt,
		ExpiresAt:    row.ExpiresAt,
		LastUsed:     row.LastUsed,
	}, true, nil
}

func (r *Repository) Upsert(ctx context.Context, entry identitymetadataport.PosterCacheEntry) error {
	if r == nil || r.db == nil {
		return errors.New("poster cache database is nil")
	}
	entry, err := normalizeEntry(entry)
	if err != nil {
		return err
	}
	if err := generated.New(r.db).UpsertMediaPosterCache(ctx, generated.UpsertMediaPosterCacheParams{
		BgmID:       entry.BGMSubjectID,
		ImageBytes:  entry.ImageBytes,
		ContentType: entry.ContentType,
		ImageUrl:    entry.ImageURL,
		FetchedAt:   entry.FetchedAt,
		ExpiresAt:   entry.ExpiresAt,
		LastUsed:    entry.LastUsed,
	}); err != nil {
		return fmt.Errorf("upsert media poster cache: %w", err)
	}
	return nil
}

func (r *Repository) PurgeExpired(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	if r == nil {
		return 0, errors.New("poster cache repository is nil")
	}
	if batchSize < 1 || batchSize > maxRetentionBatchSize {
		return 0, fmt.Errorf("poster cache retention batch size must be between 1 and %d", maxRetentionBatchSize)
	}
	tx := scope.SQLTx()
	if tx == nil {
		return 0, errors.New("poster cache retention transaction is nil")
	}
	queries := generated.New(tx)
	result, err := queries.PurgeExpiredMediaPosterCache(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge expired media poster cache: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read purged media poster cache count: %w", err)
	}
	// The provider cover registry shares this class: rows idle for 30 days go
	// with the expired poster bytes (a still-listed cover is re-registered on
	// its next list read).
	images, err := queries.PurgeIdleProviderImages(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge idle provider images: %w", err)
	}
	imageCount, err := images.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read purged provider image count: %w", err)
	}
	return count + imageCount, nil
}

func normalizeEntry(entry identitymetadataport.PosterCacheEntry) (identitymetadataport.PosterCacheEntry, error) {
	entry.BGMSubjectID = strings.TrimSpace(entry.BGMSubjectID)
	entry.ContentType = strings.TrimSpace(entry.ContentType)
	entry.ImageURL = strings.TrimSpace(entry.ImageURL)
	if entry.BGMSubjectID == "" || len(entry.BGMSubjectID) > 128 {
		return identitymetadataport.PosterCacheEntry{}, errors.New("poster cache subject id is invalid")
	}
	if len(entry.ImageBytes) == 0 || len(entry.ImageBytes) > maxPosterBytes {
		return identitymetadataport.PosterCacheEntry{}, fmt.Errorf("poster cache image bytes must be between 1 and %d bytes", maxPosterBytes)
	}
	if entry.ContentType == "" || len(entry.ContentType) > 256 {
		return identitymetadataport.PosterCacheEntry{}, errors.New("poster cache content type is invalid")
	}
	if entry.ImageURL == "" || len(entry.ImageURL) > 2048 {
		return identitymetadataport.PosterCacheEntry{}, errors.New("poster cache image URL is invalid")
	}
	if entry.FetchedAt.IsZero() {
		entry.FetchedAt = time.Now().UTC()
	}
	if entry.ExpiresAt.IsZero() || entry.ExpiresAt.Before(entry.FetchedAt) {
		return identitymetadataport.PosterCacheEntry{}, errors.New("poster cache expiry is invalid")
	}
	if entry.LastUsed.IsZero() {
		entry.LastUsed = entry.FetchedAt
	}
	if entry.LastUsed.Before(entry.FetchedAt) {
		return identitymetadataport.PosterCacheEntry{}, errors.New("poster cache last-used time is invalid")
	}
	entry.ImageBytes = append([]byte(nil), entry.ImageBytes...)
	entry.FetchedAt = entry.FetchedAt.UTC()
	entry.ExpiresAt = entry.ExpiresAt.UTC()
	entry.LastUsed = entry.LastUsed.UTC()
	return entry, nil
}
