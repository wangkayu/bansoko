package persistence

import (
	"context"
	"database/sql"
)

// Locks calls fixed owner-held statements; the caller owns the transaction.
type Locks struct{ DB *sql.Tx }

func (l Locks) LiveIdentity(ctx context.Context, id string) *sql.Row {
	return l.DB.QueryRowContext(ctx, `SELECT id FROM public.work_item_lock_live_identity($1)`, id)
}
func (l Locks) Recovery(ctx context.Context, id string, token int64, ownerType, ownerID string) *sql.Row {
	return l.DB.QueryRowContext(ctx, `SELECT expected_owner_version FROM public.work_item_lock_recovery($1,$2,$3,$4)`, id, token, ownerType, ownerID)
}
func (l Locks) RecoveryObservation(ctx context.Context, id string, token int64, ownerType, ownerID string) *sql.Row {
	return l.DB.QueryRowContext(ctx, `SELECT expected_owner_version FROM public.work_item_lock_recovery_observation($1,$2,$3,$4)`, id, token, ownerType, ownerID)
}
func (l Locks) MetadataEvaluations(ctx context.Context, id string) (*sql.Rows, error) {
	return l.DB.QueryContext(ctx, `SELECT id FROM public.work_item_lock_metadata_evaluations($1)`, id)
}
func (l Locks) PlacementRecovery(ctx context.Context, id string, token int64, owner string) *sql.Row {
	return l.DB.QueryRowContext(ctx, `SELECT expected_owner_version FROM public.work_item_lock_placement_recovery($1,$2,$3)`, id, token, owner)
}
func (l Locks) LatestFileExecute(ctx context.Context, ownerType, ownerID, kind string) *sql.Row {
	return l.DB.QueryRowContext(ctx, `SELECT id,state,kind,input_snapshot FROM public.work_item_lock_latest_file_execute($1,$2,$3)`, ownerType, ownerID, kind)
}
func (l Locks) FileOperationInputs(ctx context.Context, ownerType, ownerID string) (*sql.Rows, error) {
	return l.DB.QueryContext(ctx, `SELECT input_snapshot FROM public.work_item_lock_file_operation_inputs($1,$2)`, ownerType, ownerID)
}
func (l Locks) PreparedPlan(ctx context.Context, id, owner string, token int64) *sql.Row {
	return l.DB.QueryRowContext(ctx, `SELECT expected_owner_version FROM public.work_item_lock_prepared_plan($1,$2,$3)`, id, owner, token)
}
func (l Locks) ReleaseEvaluation(ctx context.Context, ownerType, ownerID, kind string) *sql.Row {
	return l.DB.QueryRowContext(ctx, `SELECT id,state,expected_owner_version FROM public.work_item_lock_release_evaluation($1,$2,$3)`, ownerType, ownerID, kind)
}
func (l Locks) PlacementTrigger(ctx context.Context, anime, release string) *sql.Row {
	return l.DB.QueryRowContext(ctx, `SELECT id FROM public.work_item_lock_placement_trigger($1,$2)`, anime, release)
}
