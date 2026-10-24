// Package persistence implements the PostgreSQL adapter for immutable naming
// Vars source versions. SQL and transaction ownership stay in this package;
// namingvars owns source validation and JavaScript evaluation.
package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"bansoko/internal/namingvars"
	generated "bansoko/internal/namingvars/adapter/persistence/generated"
)

// Repository owns PostgreSQL access for naming Vars source versions.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the naming Vars PostgreSQL adapter.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Publish activates one canonical source version. The adapter owns the
// transaction because publication is a complete single-owner operation.
func (r *Repository) Publish(ctx context.Context, source, fingerprint string) error {
	if r == nil || r.db == nil {
		return errors.New("naming vars database is nil")
	}
	if source == "" {
		return errors.New("naming vars source is empty")
	}
	if fingerprint == "" {
		return errors.New("naming vars fingerprint is empty")
	}
	canonical, err := namingvars.CanonicalSource(source)
	if err != nil {
		return err
	}
	canonicalFingerprint, err := namingvars.SourceFingerprint(source)
	if err != nil {
		return err
	}
	if fingerprint != canonicalFingerprint {
		return fmt.Errorf("naming vars fingerprint mismatch: supplied %q, canonical %q", fingerprint, canonicalFingerprint)
	}
	content, err := json.Marshal(string(canonical))
	if err != nil {
		return fmt.Errorf("encode naming vars source: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin naming vars publish: %w", err)
	}
	defer tx.Rollback()

	queries := generated.New(tx)
	if err = queries.LockNamingVarsPublish(ctx); err != nil {
		return fmt.Errorf("lock naming vars publish: %w", err)
	}
	var activeFingerprint string
	activeFingerprint, err = queries.GetActiveNamingVarsFingerprint(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read active naming vars: %w", err)
	}
	if activeFingerprint != fingerprint {
		if err = queries.DeactivateNamingVars(ctx); err != nil {
			return fmt.Errorf("deactivate naming vars: %w", err)
		}
	}
	if _, err = queries.PublishNamingVars(ctx, generated.PublishNamingVarsParams{Fingerprint: fingerprint, Content: content}); err != nil {
		return fmt.Errorf("publish naming vars identity/content: %w", err)
	}
	if err = tx.Commit(); err != nil {
		if activeFingerprint == fingerprint {
			return fmt.Errorf("commit naming vars no-op: %w", err)
		}
		return fmt.Errorf("commit naming vars: %w", err)
	}
	return nil
}

// Current returns the source string and fingerprint of the active version.
// JSONB decoding remains inside the adapter so no raw persistence payload type
// crosses the application boundary.
func (r *Repository) Current(ctx context.Context) (string, string, error) {
	if r == nil || r.db == nil {
		return "", "", errors.New("naming vars database is nil")
	}
	row, err := generated.New(r.db).GetCurrentNamingVars(ctx)
	if err != nil {
		return "", "", fmt.Errorf("read active naming vars: %w", err)
	}
	fingerprint := row.Fingerprint
	content := row.Content
	var source string
	if err := json.Unmarshal(content, &source); err != nil || source == "" {
		return "", "", errors.New("active naming vars content is not source-backed")
	}
	canonical, err := namingvars.CanonicalSource(source)
	if err != nil {
		return "", "", err
	}
	actual, err := namingvars.SourceFingerprint(source)
	if err != nil || actual != fingerprint || string(canonical) != source {
		return "", "", errors.New("active naming vars fingerprint/content mismatch")
	}
	return source, fingerprint, nil
}

var _ interface {
	Publish(context.Context, string, string) error
	Current(context.Context) (string, string, error)
} = (*Repository)(nil)
