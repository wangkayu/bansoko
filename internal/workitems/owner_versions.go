package workitems

import (
	"context"
	"errors"
	"strings"

	"bansoko/internal/persistence/txscope"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

// LockRSSSourceRuntimeVersionTx locks and reads the RSS source runtime owner
// version inside the caller-owned transaction scope.
func (s *Store) LockRSSSourceRuntimeVersionTx(ctx context.Context, scope txscope.Tx, ownerID string) (int64, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return 0, errors.New("read owner version transaction is nil")
	}
	if strings.TrimSpace(ownerID) == "" {
		return 0, errors.New("owner ID is required")
	}
	return persistence.New(tx).LockRSSSourceRuntimeVersion(ctx, ownerID)
}

// LockDownloadTaskVersionTx locks and reads the download task owner version
// inside the caller-owned transaction scope.
func (s *Store) LockDownloadTaskVersionTx(ctx context.Context, scope txscope.Tx, ownerID string) (int64, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return 0, errors.New("read owner version transaction is nil")
	}
	if strings.TrimSpace(ownerID) == "" {
		return 0, errors.New("owner ID is required")
	}
	return persistence.New(tx).LockDownloadTaskVersion(ctx, ownerID)
}

// LockArtifactVersionTx locks and reads the artifact owner version inside the
// caller-owned transaction scope.
func (s *Store) LockArtifactVersionTx(ctx context.Context, scope txscope.Tx, ownerID string) (int64, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return 0, errors.New("read owner version transaction is nil")
	}
	if strings.TrimSpace(ownerID) == "" {
		return 0, errors.New("owner ID is required")
	}
	return persistence.New(tx).LockArtifactVersion(ctx, ownerID)
}
