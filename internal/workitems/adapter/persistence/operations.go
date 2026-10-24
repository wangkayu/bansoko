package persistence

import (
	"context"
	"database/sql"
)

// Operations calls only fixed owner-held WorkItem functions. SQL predicates
// and writes are owned by migration 145; callers retain transaction ownership.
type Operations struct{ DB DB }

type DB interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (o Operations) UpdateAvailableInput(ctx context.Context, id string, input []byte) error {
	var n int64
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_update_available_input($1,$2::jsonb)`, id, input).Scan(&n)
}
func (o Operations) FailAvailableFileOperation(ctx context.Context, id, code, reason string) error {
	var n int64
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_fail_available_file_operation($1,$2,$3)`, id, code, reason).Scan(&n)
}
func (o Operations) DeleteFileOperation(ctx context.Context, ownerType, id string) error {
	var n int64
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_delete_file_operation($1,$2)`, ownerType, id).Scan(&n)
}
func (o Operations) SynchronizeAvailableReleaseVersion(ctx context.Context, id string, version int64) error {
	var n int64
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_synchronize_available_release_version($1,$2)`, id, version).Scan(&n)
}
func (o Operations) DeleteLocalMaintenanceTree(ctx context.Context, id string) error {
	var n int64
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_delete_local_maintenance_tree($1)`, id).Scan(&n)
}
func (o Operations) LockRetirementSet(ctx context.Context, id string, epoch int64) error {
	var locked string
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_lock_retirement_set($1,$2)::text`, id, epoch).Scan(&locked)
}
func (o Operations) RetireUnadmitted(ctx context.Context, id string, epoch int64) error {
	var n int64
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_retire_unadmitted($1,$2)`, id, epoch).Scan(&n)
}
func (o Operations) FailStaleRecovery(ctx context.Context, id string, version int64) error {
	var n int64
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_fail_stale_recovery($1,$2)`, id, version).Scan(&n)
}
func (o Operations) FinishRecoveryObservation(ctx context.Context, id, state string, version int64) error {
	var n int64
	return o.DB.QueryRowContext(ctx, `SELECT public.work_item_finish_recovery_observation($1,$2,$3)`, id, state, version).Scan(&n)
}
func (o Operations) CheckpointOwnerVersion(ctx context.Context, id string, token, before, after int64) (int64, error) {
	var n int64
	err := o.DB.QueryRowContext(ctx, `SELECT public.work_item_checkpoint_owner_version($1,$2,$3,$4)`, id, token, before, after).Scan(&n)
	return n, err
}
func (o Operations) DeferRecoveryObservation(ctx context.Context, id string, token int64, delay float64, reason string) (int64, error) {
	var n int64
	err := o.DB.QueryRowContext(ctx, `SELECT public.work_item_defer_recovery_observation($1,$2,$3,$4)`, id, token, delay, reason).Scan(&n)
	return n, err
}
func (o Operations) ClaimRecoveryObservation(ctx context.Context, kinds any, lease float64) *sql.Row {
	return o.DB.QueryRowContext(ctx, `SELECT * FROM public.work_item_claim_recovery_observation($1::text[],$2::double precision)`, kinds, lease)
}
