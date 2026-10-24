package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/persistence/txscope"
)

const retentionBatchSize = 500

// RetentionResult reports evidence rows removed by each typed cleanup query.
type RetentionResult struct {
	PluginDeliveryAttempts int64
	PluginDeliveries       int64
	SubscriptionLogEntries int64
	AutomationDispatchRuns int64
	DiagnosticEpisodes     int64
}

// Total returns the number of evidence rows removed by the purge.
func (r RetentionResult) Total() int64 {
	return r.PluginDeliveryAttempts +
		r.PluginDeliveries +
		r.SubscriptionLogEntries +
		r.AutomationDispatchRuns +
		r.DiagnosticEpisodes
}

// RetentionRepository is the typed application-owned port for bounded
// observability evidence purges. The caller owns the transaction lifecycle.
type RetentionRepository interface {
	PurgePluginDeliveryAttempts(context.Context, txscope.Tx, int) (int64, error)
	PurgePluginDeliveries(context.Context, txscope.Tx, int) (int64, error)
	PurgeSubscriptionLogEntries(context.Context, txscope.Tx, int) (int64, error)
	PurgeAutomationDispatchRuns(context.Context, txscope.Tx, int) (int64, error)
	PurgeDiagnosticEpisodes(context.Context, txscope.Tx, int) (int64, error)
}

// RetentionService owns bounded observability evidence cleanup. WorkItems are
// retained by workitems.Store and are intentionally outside this service.
type RetentionService struct {
	db         *sql.DB
	repository RetentionRepository
}

// NewRetentionService preserves the legacy constructor signature but requires
// callers to inject the application-owned persistence port explicitly.
func NewRetentionService(db *sql.DB) (*RetentionService, error) {
	if db == nil {
		return nil, errors.New("retention service database is nil")
	}
	return nil, errors.New("retention service repository is required; use NewRetentionServiceWithRepository")
}

// NewRetentionServiceWithRepository returns a retention service using the
// supplied application-owned persistence port.
func NewRetentionServiceWithRepository(db *sql.DB, repository RetentionRepository) (*RetentionService, error) {
	if db == nil {
		return nil, errors.New("retention service database is nil")
	}
	if repository == nil {
		return nil, errors.New("retention service repository is nil")
	}
	return &RetentionService{db: db, repository: repository}, nil
}

// PurgeExpired deletes only expired observability evidence using PostgreSQL's
// database clock. Dependent attempts are deleted before their deliveries.
func (s *RetentionService) PurgeExpired(ctx context.Context) (RetentionResult, error) {
	var result RetentionResult
	if s == nil || s.db == nil {
		return result, errors.New("retention service database is nil")
	}
	if s.repository == nil {
		return result, errors.New("retention service repository is nil")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin retention transaction: %w", err)
	}
	defer tx.Rollback()

	scope := txscope.Wrap(tx)
	if result.PluginDeliveryAttempts, err = s.repository.PurgePluginDeliveryAttempts(ctx, scope, retentionBatchSize); err != nil {
		return RetentionResult{}, fmt.Errorf("purge plugin delivery attempts: %w", err)
	}
	if result.PluginDeliveries, err = s.repository.PurgePluginDeliveries(ctx, scope, retentionBatchSize); err != nil {
		return RetentionResult{}, fmt.Errorf("purge plugin deliveries: %w", err)
	}
	if result.SubscriptionLogEntries, err = s.repository.PurgeSubscriptionLogEntries(ctx, scope, retentionBatchSize); err != nil {
		return RetentionResult{}, fmt.Errorf("purge subscription log entries: %w", err)
	}
	if result.AutomationDispatchRuns, err = s.repository.PurgeAutomationDispatchRuns(ctx, scope, retentionBatchSize); err != nil {
		return RetentionResult{}, fmt.Errorf("purge automation dispatch runs: %w", err)
	}
	if result.DiagnosticEpisodes, err = s.repository.PurgeDiagnosticEpisodes(ctx, scope, retentionBatchSize); err != nil {
		return RetentionResult{}, fmt.Errorf("purge diagnostic episodes: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return RetentionResult{}, fmt.Errorf("commit retention transaction: %w", err)
	}
	return result, nil
}
