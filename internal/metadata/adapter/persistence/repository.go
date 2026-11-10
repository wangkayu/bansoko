// Package persistence stores the latest reusable bangumi-data reference snapshot.
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	generated "bansoko/internal/metadata/adapter/persistence/generated"
	metadataport "bansoko/internal/metadata/port"
)

// Snapshot is the storage shape exchanged with the refresh application service.
type Snapshot = metadataport.Snapshot

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Load(ctx context.Context) (Snapshot, bool, error) {
	if r == nil || r.db == nil {
		return Snapshot{}, false, errors.New("bangumi-data snapshot database is nil")
	}
	row, err := generated.New(r.db).GetBangumiDataSnapshot(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("load bangumi-data snapshot: %w", err)
	}
	return Snapshot{
		UpstreamVersion: row.UpstreamVersion,
		Payload:         append([]byte(nil), row.Payload...),
		LastModified:    row.LastModified,
		FetchedAt:       row.FetchedAt,
		CheckedAt:       row.CheckedAt,
	}, true, nil
}

func (r *Repository) Save(ctx context.Context, snapshot Snapshot) error {
	if r == nil || r.db == nil {
		return errors.New("bangumi-data snapshot database is nil")
	}
	if err := generated.New(r.db).SaveBangumiDataSnapshot(ctx, generated.SaveBangumiDataSnapshotParams{
		UpstreamVersion: snapshot.UpstreamVersion,
		Payload:         snapshot.Payload,
		LastModified:    snapshot.LastModified,
		FetchedAt:       snapshot.FetchedAt,
	}); err != nil {
		return fmt.Errorf("save bangumi-data snapshot: %w", err)
	}
	return nil
}

func (r *Repository) Touch(ctx context.Context, checkedAt time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("bangumi-data snapshot database is nil")
	}
	if err := generated.New(r.db).TouchBangumiDataSnapshot(ctx, checkedAt); err != nil {
		return fmt.Errorf("touch bangumi-data snapshot: %w", err)
	}
	return nil
}
