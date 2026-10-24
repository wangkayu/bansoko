package workitems

import (
	adapter "bansoko/internal/workitems/adapter/persistence"
	"context"
	"database/sql"
)

// These fixed lock operations retain locks in the caller's transaction.
func LockMetadataEvaluationsTx(ctx context.Context, tx *sql.Tx, id string) (*sql.Rows, error) {
	return (adapter.Locks{DB: tx}).MetadataEvaluations(ctx, id)
}
func LockPlacementRecoveryTx(ctx context.Context, tx *sql.Tx, id string, token int64, owner string) *sql.Row {
	return (adapter.Locks{DB: tx}).PlacementRecovery(ctx, id, token, owner)
}
func LockLatestFileExecuteTx(ctx context.Context, tx *sql.Tx, ownerType, ownerID, kind string) *sql.Row {
	return (adapter.Locks{DB: tx}).LatestFileExecute(ctx, ownerType, ownerID, kind)
}
func LockFileOperationInputsTx(ctx context.Context, tx *sql.Tx, ownerType, ownerID string) (*sql.Rows, error) {
	return (adapter.Locks{DB: tx}).FileOperationInputs(ctx, ownerType, ownerID)
}
func LockReleaseEvaluationTx(ctx context.Context, tx *sql.Tx, ownerType, ownerID, kind string) *sql.Row {
	return (adapter.Locks{DB: tx}).ReleaseEvaluation(ctx, ownerType, ownerID, kind)
}
func LockPlacementTriggerTx(ctx context.Context, tx *sql.Tx, anime, release string) *sql.Row {
	return (adapter.Locks{DB: tx}).PlacementTrigger(ctx, anime, release)
}
