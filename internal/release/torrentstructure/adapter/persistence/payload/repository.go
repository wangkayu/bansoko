// Package payload reads the retained torrent payload bytes of one release.
package payload

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/release/torrentstructure/application"
)

// maxPayloadBytes mirrors the download-side acceptance bound: a payload above
// it was never accepted, so one found here is not a payload the release made.
const maxPayloadBytes = 20 << 20

// Repository reads release_torrent_inputs, the only stored torrent artifact.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the reader.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// LoadPayload returns the retained payload of one release, or
// ErrPayloadUnavailable when none is retained any more.
func (r *Repository) LoadPayload(ctx context.Context, releaseID string) ([]byte, error) {
	if r == nil || r.db == nil {
		return nil, application.ErrPayloadUnavailable
	}
	var payload []byte
	err := r.db.QueryRowContext(ctx, `
SELECT input.payload
FROM release_torrent_inputs input
WHERE input.release_id = $1`, releaseID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, application.ErrPayloadUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("read retained torrent payload: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxPayloadBytes {
		return nil, application.ErrPayloadUnavailable
	}
	return payload, nil
}
