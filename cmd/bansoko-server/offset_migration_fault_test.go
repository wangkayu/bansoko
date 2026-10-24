package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
)

type offsetPauseTestExecutor struct{ calls int }

func (e *offsetPauseTestExecutor) Execute(_ context.Context, operation domain.FileOperation) (domain.FileOperation, error) {
	e.calls++
	return operation, nil
}

func TestOffsetMigrationPauseExecutorDisposableGate(t *testing.T) {
	inner := &offsetPauseTestExecutor{}
	source := filepath.Join("/tmp/bansoko-harness-library.fixture", "show.mkv")
	_, err := newOffsetMigrationPauseExecutor(inner, func(key string) string {
		if key == offsetMigrationPauseSourceEnv {
			return source
		}
		return ""
	})
	if err == nil {
		t.Fatal("fixture pause must refuse startup without disposable DB marker")
	}
}

func TestOffsetMigrationPauseExecutorOnlyPausesExactSourceAndCancellationIsFailClosed(t *testing.T) {
	inner := &offsetPauseTestExecutor{}
	source := filepath.Join("/tmp/bansoko-harness-library.fixture", "show.mkv")
	paused, err := newOffsetMigrationPauseExecutor(inner, func(key string) string {
		switch key {
		case offsetMigrationPauseSourceEnv:
			return source
		case "BANSOKO_TEST_DB_DISPOSABLE":
			return "1"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paused.Execute(context.Background(), domain.FileOperation{SourcePath: source + ".other"}); err != nil {
		t.Fatalf("unmatched source should reach inner executor: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("unmatched source calls=%d, want 1", inner.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = paused.Execute(ctx, domain.FileOperation{SourcePath: source})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("matched paused source error=%v, want context cancellation", err)
	}
	if inner.calls != 1 {
		t.Fatalf("paused source reached real executor; calls=%d", inner.calls)
	}
}

type offsetPauseReceiptExecutor struct {
	offsetPauseTestExecutor
	maintenance int
}

func (e *offsetPauseReceiptExecutor) ExecuteMaintenance(_ context.Context, operation domain.FileOperation, _ executor.MaintenanceInvocation) (domain.FileOperation, error) {
	e.maintenance++
	return operation, nil
}

func (e *offsetPauseReceiptExecutor) ReadMaintenanceReceipt(context.Context, executor.MaintenanceInvocation) (executor.MaintenanceReceipt, error) {
	return executor.MaintenanceReceipt{}, nil
}

// The pause must not hide the receipt-backed path the production executor
// takes; otherwise the crash fixture exercises a path production never runs.
func TestOffsetMigrationPauseExecutorKeepsReceiptBackedPath(t *testing.T) {
	source := filepath.Join("/tmp/bansoko-harness-library.fixture", "show.mkv")
	lookup := func(key string) string {
		switch key {
		case offsetMigrationPauseSourceEnv:
			return source
		case "BANSOKO_TEST_DB_DISPOSABLE":
			return "1"
		default:
			return ""
		}
	}
	production, err := newOffsetMigrationPauseExecutor(filesystem.OSFileExecutor{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := production.(executor.MaintenanceFileExecutor); !ok {
		t.Fatal("paused production executor lost the receipt-backed interface")
	}
	inner := &offsetPauseReceiptExecutor{}
	paused, err := newOffsetMigrationPauseExecutor(inner, lookup)
	if err != nil {
		t.Fatal(err)
	}
	receipts := paused.(executor.MaintenanceFileExecutor)
	if _, err := receipts.ExecuteMaintenance(context.Background(), domain.FileOperation{SourcePath: source + ".other"}, executor.MaintenanceInvocation{}); err != nil || inner.maintenance != 1 {
		t.Fatalf("unmatched source must reach inner maintenance executor: calls=%d err=%v", inner.maintenance, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := receipts.ExecuteMaintenance(ctx, domain.FileOperation{SourcePath: source}, executor.MaintenanceInvocation{}); !errors.Is(err, context.Canceled) || inner.maintenance != 1 {
		t.Fatalf("paused source reached inner maintenance executor: calls=%d err=%v", inner.maintenance, err)
	}
}
