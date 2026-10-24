package application

import (
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
	"context"
)

// The validator must lock the candidate row in this transaction, check its
// expected version/current revision and verify the exact frozen source scope.
// The repository must run it before INSERT and retain the lock through commit.
type CustomSnapshotAtomicRevisionReader interface {
	ValidateCustomSnapshotSourceTx(context.Context, txscope.Tx, string, int64, int64, FrozenCustomSnapshotSource) error
}
type CustomSnapshotAtomicStore interface {
	PersistCustomFeedSnapshot(context.Context, catalogdomain.ProviderFeedSnapshotWrite, func(context.Context, txscope.Tx) error) (catalogdomain.ProviderFeedSnapshot, error)
}
