package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/library/domain"
)

func TestOSCleanupExecutorPurgeCapturedMissingParentIsRecoveryUnsafe(t *testing.T) {
	root := t.TempDir()
	executor := NewOSCleanupExecutor(root)
	expected := domain.FileObservation{
		Fingerprint: "frozen",
		SizeBytes:   1,
		Identity:    domain.FileIdentity{Device: 1, Inode: 1},
	}

	err := executor.PurgeCaptured(context.Background(), filepath.Join(root, "missing", "entry"), expected)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("missing quarantine parent was not recovery-unsafe: %v", err)
	}
}

func TestOSCleanupExecutorPurgeCapturedMissingEntryIsRecoveryUnsafe(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, ".quarantine")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	executor := NewOSCleanupExecutor(root)
	expected := domain.FileObservation{
		Fingerprint: "frozen",
		SizeBytes:   1,
		Identity:    domain.FileIdentity{Device: 1, Inode: 1},
	}

	err := executor.PurgeCaptured(context.Background(), filepath.Join(parent, "missing"), expected)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != CodeRecoveryUnsafe {
		t.Fatalf("missing quarantine entry was not recovery-unsafe: %v", err)
	}
}
