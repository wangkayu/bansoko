// Package application owns the transport-neutral candidate-facts row and
// repository port. PostgreSQL handles and SQL stay in adapter/persistence.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"bansoko/internal/persistence/txscope"
)

// ErrRepositoryUnavailable reports an application repository that has not been
// wired to a persistence adapter.
var ErrRepositoryUnavailable = errors.New("candidate facts repository is nil")

// ReleaseCandidateFacts is the durable versioned fact row.
type ReleaseCandidateFacts struct {
	ReleaseID        string
	ReleaseVersion   int64
	ParserVersion    string
	InputFingerprint string
	RawEvidence      json.RawMessage
	Facts            json.RawMessage
	CoverageKeys     []string
	Resolution       string
	Revision         string
	Source           string
	Language         string
	Group            string
	SizeBytes        *int64
	CreatedAt        time.Time
	// CompareValues are the frozen replacement comparison values
	// (compare-values-v1); empty = none.
	CompareValues json.RawMessage
}

// Repository is the application-owned facts port: release evaluation writes,
// read models, and future FileMaintenance consumers all go through this
// contract. Transactional writes receive an opaque caller-owned scope.
type Repository interface {
	// UpsertForRelease persists the facts for one release version
	// idempotently (same fingerprint = no-op; retries never append
	// conflicting facts).
	UpsertForRelease(context.Context, txscope.Tx, ReleaseCandidateFacts) error
	GetForRelease(context.Context, string) (*ReleaseCandidateFacts, error)
	GetVersion(context.Context, string, int64) (*ReleaseCandidateFacts, error)
}
