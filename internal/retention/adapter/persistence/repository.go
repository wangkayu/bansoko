// Package persistence implements the PostgreSQL persistence boundary for
// discovery-history and maintenance-record retention. SQL and affected-row handling stay here;
// callers retain ownership of transaction lifecycle through txscope.Tx.
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/persistence/txscope"
	generated "bansoko/internal/retention/adapter/persistence/generated"
)

// Repository is the PostgreSQL discovery-retention adapter. The database
// handle is kept for constructor compatibility and wiring validation;
// transactional methods execute exclusively through the caller-owned scope.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the discovery retention PostgreSQL adapter.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const maxRetentionBatchSize = 500

func (r *Repository) transaction(scope txscope.Tx, batchSize int) (*sql.Tx, error) {
	if r == nil {
		return nil, errors.New("discovery retention repository is nil")
	}
	if batchSize < 1 || batchSize > maxRetentionBatchSize {
		return nil, fmt.Errorf("discovery retention batch size must be between 1 and %d", maxRetentionBatchSize)
	}
	tx := scope.SQLTx()
	if tx == nil {
		return nil, errors.New("discovery retention transaction is nil")
	}
	return tx, nil
}

// PurgeExpiredDiscoveryReleases deletes one bounded batch of discovery-only
// Releases of inactive owners, leaf-first, in the caller-owned transaction.
// A Release that reached a download task, an execution root, a live one-shot
// marker or an unexpired run-log row is not eligible.
func (r *Repository) PurgeExpiredDiscoveryReleases(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	deleted, err := generated.New(tx).PurgeExpiredDiscoveryReleases(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge discovery releases: %w", err)
	}
	return deleted, nil
}

// PurgeExpiredIdempotencyRecords deletes one batch of HTTP replay records older
// than the 30-day replay window.
func (r *Repository) PurgeExpiredIdempotencyRecords(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	deleted, err := generated.New(tx).PurgeExpiredIdempotencyRecords(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge idempotency records: %w", err)
	}
	return deleted, nil
}

// PurgeExpiredNotificationAdmissions deletes one batch of settled notification
// admissions older than the 90-day dedupe window.
func (r *Repository) PurgeExpiredNotificationAdmissions(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	deleted, err := generated.New(tx).PurgeExpiredNotificationAdmissions(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge notification admissions: %w", err)
	}
	return deleted, nil
}

// PurgeExpiredAlignmentOperations deletes one batch of settled alignment
// operations of superseded scan generations with no WorkItem left.
func (r *Repository) PurgeExpiredAlignmentOperations(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	deleted, err := generated.New(tx).PurgeExpiredAlignmentOperations(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge alignment operations: %w", err)
	}
	return deleted, nil
}

// PurgeSupersededReconciliationFiles deletes one batch of observation rows of
// scan generations no reader uses any more.
func (r *Repository) PurgeSupersededReconciliationFiles(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	tx, err := r.transaction(scope, batchSize)
	if err != nil {
		return 0, err
	}
	deleted, err := generated.New(tx).PurgeSupersededReconciliationFiles(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge reconciliation files: %w", err)
	}
	return deleted, nil
}
