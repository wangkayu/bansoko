// Package persistence implements the PostgreSQL adapter for attribute-rule
// versions. SQL and transaction ownership stay in this package; attrrules only
// supplies validated canonical content through its persistence port.
package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"bansoko/internal/attrrules"
	generated "bansoko/internal/attrrules/adapter/persistence/generated"
)

// Repository owns PostgreSQL access for immutable attribute-rule versions.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the attribute-rule PostgreSQL adapter.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Publish activates one canonical attribute-rule version. The adapter owns the
// transaction because publication is a complete single-owner operation.
func (r *Repository) Publish(ctx context.Context, rules attrrules.Rules, fingerprint string) error {
	if r == nil || r.db == nil {
		return errors.New("attr rules database is nil")
	}
	content, err := attrrules.Canonical(rules)
	if err != nil {
		return err
	}
	canonicalFingerprint, err := attrrules.Fingerprint(rules)
	if err != nil {
		return fmt.Errorf("fingerprint attr rules: %w", err)
	}
	if fingerprint != canonicalFingerprint {
		return fmt.Errorf("attr rules fingerprint mismatch: supplied %q, canonical %q", fingerprint, canonicalFingerprint)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attr rules publish: %w", err)
	}
	defer tx.Rollback()

	queries := generated.New(tx)
	if err = queries.LockAttrRulesPublish(ctx); err != nil {
		return fmt.Errorf("lock attr rules publish: %w", err)
	}
	var activeFingerprint string
	activeFingerprint, err = queries.GetActiveAttrRulesFingerprint(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read active attr rules: %w", err)
	}
	if activeFingerprint != fingerprint {
		if err = queries.DeactivateAttrRules(ctx); err != nil {
			return fmt.Errorf("deactivate attr rules: %w", err)
		}
	}
	// Even the active no-op must match the stored immutable content. A
	// conflicting historical identity cannot silently reactivate other bytes.
	if _, err = queries.PublishAttrRules(ctx, generated.PublishAttrRulesParams{Fingerprint: fingerprint, Content: content}); err != nil {
		return fmt.Errorf("publish attr rules identity/content: %w", err)
	}
	if err = tx.Commit(); err != nil {
		if activeFingerprint == fingerprint {
			return fmt.Errorf("commit attr rules no-op: %w", err)
		}
		return fmt.Errorf("commit attr rules: %w", err)
	}
	return nil
}

// Current returns the validated rules and fingerprint of the active version.
func (r *Repository) Current(ctx context.Context) (attrrules.Rules, string, error) {
	if r == nil || r.db == nil {
		return attrrules.Rules{}, "", errors.New("attr rules database is nil")
	}
	row, err := generated.New(r.db).GetCurrentAttrRules(ctx)
	if err != nil {
		return attrrules.Rules{}, "", fmt.Errorf("read active attr rules: %w", err)
	}
	fingerprint := row.Fingerprint
	content := row.Content
	var rules attrrules.Rules
	if err := json.Unmarshal(content, &rules); err != nil {
		return attrrules.Rules{}, "", fmt.Errorf("decode active attr rules: %w", err)
	}
	if err := rules.Validate(); err != nil {
		return attrrules.Rules{}, "", fmt.Errorf("validate active attr rules: %w", err)
	}
	canonicalFingerprint, err := attrrules.Fingerprint(rules)
	if err != nil {
		return attrrules.Rules{}, "", fmt.Errorf("fingerprint active attr rules: %w", err)
	}
	if fingerprint != canonicalFingerprint {
		return attrrules.Rules{}, "", fmt.Errorf("active attr rules fingerprint mismatch: stored %q, canonical %q", fingerprint, canonicalFingerprint)
	}
	return rules, fingerprint, nil
}

var _ interface {
	Publish(context.Context, attrrules.Rules, string) error
	Current(context.Context) (attrrules.Rules, string, error)
} = (*Repository)(nil)
