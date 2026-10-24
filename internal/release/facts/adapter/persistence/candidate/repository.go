package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/persistence/txscope"
	generated "bansoko/internal/release/facts/adapter/persistence/candidate/generated"
)

// ErrRepositoryUnavailable reports a nil application-facing facade or adapter.
var ErrRepositoryUnavailable = errors.New("candidate facts repository is nil")

// Repository owns only PostgreSQL access for candidate facts. Its public
// methods use adapter-owned row/value types so this package stays independent
// from the parent owner and application packages. The parent facade performs
// application mapping at the boundary.
type Repository struct {
	db *sql.DB
}

// CandidateFacts is the adapter-owned persistence value. It mirrors the
// durable columns while keeping the adapter independent from all parent and
// application packages.
type CandidateFacts struct {
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

// NewRepository wires the candidate-facts PostgreSQL adapter.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

var _ interface {
	UpsertForRelease(context.Context, txscope.Tx, CandidateFacts) error
	GetForRelease(context.Context, string) (*CandidateFacts, error)
	GetVersion(context.Context, string, int64) (*CandidateFacts, error)
} = (*Repository)(nil)

// UpsertForRelease persists one versioned facts row in the caller-owned
// transaction. The adapter unwraps the opaque scope exactly once and never
// begins, commits, or rolls back the transaction.
func (r *Repository) UpsertForRelease(ctx context.Context, scope txscope.Tx, facts CandidateFacts) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("candidate facts upsert requires a transaction")
	}
	coverage, err := json.Marshal(facts.CoverageKeys)
	if err != nil {
		return fmt.Errorf("encode coverage keys: %w", err)
	}
	result, err := generated.New(tx).UpsertCandidateFacts(ctx, generated.UpsertCandidateFactsParams{
		ReleaseID:        facts.ReleaseID,
		ReleaseVersion:   facts.ReleaseVersion,
		ParserVersion:    facts.ParserVersion,
		InputFingerprint: facts.InputFingerprint,
		RawEvidence:      facts.RawEvidence,
		Facts:            facts.Facts,
		CoverageKeys:     coverage,
		Resolution:       nullableString(facts.Resolution),
		Revision:         nullableString(facts.Revision),
		Source:           nullableString(facts.Source),
		Language:         nullableString(facts.Language),
		Group:            nullableString(facts.Group),
		SizeBytes:        nullableInt64(facts.SizeBytes),
		CompareValues:    compareValuesOrEmpty(facts.CompareValues),
	})
	if err != nil {
		return fmt.Errorf("upsert candidate facts: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 1 {
		return fmt.Errorf("candidate facts upsert affected %d rows", affected)
	}
	return nil
}

// GetForRelease returns the latest facts for a release.
func (r *Repository) GetForRelease(ctx context.Context, releaseID string) (*CandidateFacts, error) {
	if r == nil || r.db == nil {
		return nil, ErrRepositoryUnavailable
	}
	var version int64
	version, err := generated.New(r.db).GetLatestCandidateFactsVersion(ctx, releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest candidate facts version: %w", err)
	}
	return r.GetVersion(ctx, releaseID, version)
}

// GetVersion returns one versioned facts row.
func (r *Repository) GetVersion(ctx context.Context, releaseID string, version int64) (*CandidateFacts, error) {
	if r == nil || r.db == nil {
		return nil, ErrRepositoryUnavailable
	}
	row, err := generated.New(r.db).GetCandidateFacts(ctx, generated.GetCandidateFactsParams{
		ReleaseID:      releaseID,
		ReleaseVersion: version,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read candidate facts: %w", err)
	}
	facts := fromGenerated(row)
	return &facts, nil
}

func fromGenerated(row generated.ReleaseCandidateFacts) CandidateFacts {
	facts := CandidateFacts{
		ReleaseID:        row.ReleaseID,
		ReleaseVersion:   row.ReleaseVersion,
		ParserVersion:    row.ParserVersion,
		InputFingerprint: row.InputFingerprint,
		RawEvidence:      row.RawEvidence,
		Facts:            row.Facts,
		Resolution:       row.Resolution.String,
		Revision:         row.Revision.String,
		Source:           row.Source.String,
		Language:         row.Language.String,
		Group:            row.Group.String,
		CreatedAt:        row.CreatedAt,
		CompareValues:    row.CompareValues,
	}
	_ = json.Unmarshal(row.CoverageKeys, &facts.CoverageKeys)
	if row.SizeBytes.Valid {
		size := row.SizeBytes.Int64
		facts.SizeBytes = &size
	}
	return facts
}

// compareValuesOrEmpty stores no values as the empty document.
func compareValuesOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullableInt64(value *int64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *value, Valid: true}
}
