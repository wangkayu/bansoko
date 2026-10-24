// Package torrentstructure owns the release torrent-structure read contract:
// the structure of the bencode payload a release was accepted with, parsed on
// read and never re-fetched. PostgreSQL handles live in the adapter
// subpackage.
package torrentstructure

import (
	"context"
	"database/sql"

	persistence "bansoko/internal/release/torrentstructure/adapter/persistence/payload"
	"bansoko/internal/release/torrentstructure/application"
)

// ErrPayloadUnavailable reports a release whose torrent payload is not
// retained any more.
var ErrPayloadUnavailable = application.ErrPayloadUnavailable

// View and File are the application-owned read model.
type View = application.View
type File = application.File

// MaxFiles bounds the file list one response carries.
const MaxFiles = application.MaxFiles

// Loader reads the retained payload bytes of one release.
type Loader = application.Loader

// Service adapts a loader to the read contract.
type Service = application.Service

// NewService wires the read adapter.
func NewService(loader Loader) *Service { return application.NewService(loader) }

// PGRepository is the PostgreSQL payload reader.
type PGRepository struct {
	adapter *persistence.Repository
}

// NewPGRepository wires the retained-payload reader.
func NewPGRepository(db *sql.DB) *PGRepository {
	return &PGRepository{adapter: persistence.NewRepository(db)}
}

// LoadPayload returns the retained payload of one release.
func (r *PGRepository) LoadPayload(ctx context.Context, releaseID string) ([]byte, error) {
	return r.adapter.LoadPayload(ctx, releaseID)
}
