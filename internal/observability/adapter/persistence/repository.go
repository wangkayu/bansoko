// Package persistence implements the PostgreSQL persistence boundary for
// observability retention. SQL and affected-row handling stay here; callers
// retain ownership of transaction lifecycle through txscope.Tx.
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	generated "bansoko/internal/observability/adapter/persistence/generated"
	"bansoko/internal/persistence/txscope"
)

// Repository is the PostgreSQL retention adapter. The database handle is kept
// for constructor compatibility and wiring validation; transactional methods
// execute exclusively through the caller-owned scope.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the observability retention PostgreSQL adapter.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const maxRetentionBatchSize = 500

func (r *Repository) transaction(scope txscope.Tx, batchSize int) (*sql.Tx, error) {
	if r == nil {
		return nil, errors.New("retention repository is nil")
	}
	if batchSize < 1 || batchSize > maxRetentionBatchSize {
		return nil, fmt.Errorf("retention batch size must be between 1 and %d", maxRetentionBatchSize)
	}
	tx := scope.SQLTx()
	if tx == nil {
		return nil, errors.New("retention transaction is nil")
	}
	return tx, nil
}

func replacementRows(result sql.Result, operation string) (int64, error) {
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read %s deleted row count: %w", operation, err)
	}
	return count, nil
}

// PurgePluginDeliveryAttempts deletes completed, expired delivery attempts in
// the caller-owned transaction. The adapter unwraps the opaque transaction
// scope at this persistence boundary and never controls its lifecycle.
func (r *Repository) PurgePluginDeliveryAttempts(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	result, err := generated.New(tx).PurgePluginDeliveryAttempts(ctx, int32(batchSize))
	if err != nil {
		return 0, err
	}
	return replacementRows(result, "plugin delivery attempts")
}

// PurgePluginDeliveries deletes completed, expired deliveries after their
// dependent attempts have been removed in an earlier cleaner transaction.
func (r *Repository) PurgePluginDeliveries(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	result, err := generated.New(tx).PurgePluginDeliveries(ctx, int32(batchSize))
	if err != nil {
		return 0, err
	}
	return replacementRows(result, "plugin deliveries")
}

// PurgeSubscriptionLogEntries deletes expired subscription evidence in the
// caller-owned transaction.
func (r *Repository) PurgeSubscriptionLogEntries(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	result, err := generated.New(tx).PurgeSubscriptionLogEntries(ctx, int32(batchSize))
	if err != nil {
		return 0, err
	}
	return replacementRows(result, "subscription log entries")
}

// PurgeAutomationDispatchRuns deletes expired terminal dispatch summaries in
// the caller-owned transaction.
func (r *Repository) PurgeAutomationDispatchRuns(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	result, err := generated.New(tx).PurgeAutomationDispatchRuns(ctx, int32(batchSize))
	if err != nil {
		return 0, err
	}
	return replacementRows(result, "automation dispatch runs")
}

// PurgeDiagnosticEpisodes deletes expired closed diagnostic episodes and expired
// open internal ones (a source with no natural success never closes). Open
// owner-facing episodes stay until a success closes them.
func (r *Repository) PurgeDiagnosticEpisodes(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	result, err := generated.New(tx).PurgeDiagnosticEpisodes(ctx, int32(batchSize))
	if err != nil {
		return 0, err
	}
	return replacementRows(result, "diagnostic episodes")
}
