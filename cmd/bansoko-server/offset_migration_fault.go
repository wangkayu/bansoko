package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
)

const (
	offsetMigrationPauseSourceEnv = "BANSOKO_FIXTURE_OFFSET_MIGRATION_PAUSE_SOURCE"
	offsetMigrationPauseMax       = 10 * time.Minute
)

var (
	errOffsetMigrationPauseTimeout    = errors.New("fixture offset migration pause timed out")
	errOffsetMigrationPauseNoReceipts = errors.New("fixture offset migration pause requires a receipt-backed executor")
)

type offsetMigrationPauseExecutor struct {
	inner      executor.FileExecutor
	sourcePath string
	timeout    time.Duration
}

func newOffsetMigrationPauseExecutor(inner executor.FileExecutor, lookup func(string) string) (executor.FileExecutor, error) {
	sourcePath := strings.TrimSpace(lookup(offsetMigrationPauseSourceEnv))
	if sourcePath == "" {
		return inner, nil
	}
	if strings.TrimSpace(lookup("BANSOKO_TEST_DB_DISPOSABLE")) != "1" {
		return nil, fmt.Errorf("%s is a disposable-fixture-only switch and requires BANSOKO_TEST_DB_DISPOSABLE=1", offsetMigrationPauseSourceEnv)
	}
	if inner == nil {
		return nil, fmt.Errorf("%s requires a filesystem executor", offsetMigrationPauseSourceEnv)
	}
	if !strings.HasPrefix(sourcePath, "/tmp/bansoko-harness-library.") && !strings.HasPrefix(sourcePath, "/private/tmp/bansoko-harness-library.") {
		return nil, fmt.Errorf("%s must name a file under a disposable harness library root", offsetMigrationPauseSourceEnv)
	}
	return &offsetMigrationPauseExecutor{inner: inner, sourcePath: sourcePath, timeout: offsetMigrationPauseMax}, nil
}
func (e *offsetMigrationPauseExecutor) Execute(ctx context.Context, operation domain.FileOperation) (domain.FileOperation, error) {
	if operation.SourcePath != e.sourcePath {
		return e.inner.Execute(ctx, operation)
	}
	return operation, e.pause(ctx)
}

// ExecuteMaintenance keeps the production receipt-backed path. The pause sits
// after the owner checkpoint and before the executor's start marker, which is
// exactly where a real crash leaves an attempt that never reached its effect.
func (e *offsetMigrationPauseExecutor) ExecuteMaintenance(ctx context.Context, operation domain.FileOperation, invocation executor.MaintenanceInvocation) (domain.FileOperation, error) {
	inner, ok := e.inner.(executor.MaintenanceFileExecutor)
	if !ok {
		return operation, errOffsetMigrationPauseNoReceipts
	}
	if operation.SourcePath == e.sourcePath {
		return operation, e.pause(ctx)
	}
	return inner.ExecuteMaintenance(ctx, operation, invocation)
}

func (e *offsetMigrationPauseExecutor) ReadMaintenanceReceipt(ctx context.Context, invocation executor.MaintenanceInvocation) (executor.MaintenanceReceipt, error) {
	inner, ok := e.inner.(executor.MaintenanceFileExecutor)
	if !ok {
		return executor.MaintenanceReceipt{}, errOffsetMigrationPauseNoReceipts
	}
	return inner.ReadMaintenanceReceipt(ctx, invocation)
}

func (e *offsetMigrationPauseExecutor) pause(ctx context.Context) error {
	timer := time.NewTimer(e.timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errOffsetMigrationPauseTimeout
	}
}
