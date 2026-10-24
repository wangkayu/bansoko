package workitems

import (
	adapter "bansoko/internal/workitems/adapter/persistence"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func ownerOperations(tx *sql.Tx) (adapter.Operations, error) {
	if tx == nil {
		return adapter.Operations{}, errors.New("work item operation requires caller transaction")
	}
	return adapter.Operations{DB: tx}, nil
}
func UpdateAvailableInputTx(ctx context.Context, tx *sql.Tx, id string, input []byte) error {
	o, e := ownerOperations(tx)
	if e != nil {
		return e
	}
	return o.UpdateAvailableInput(ctx, id, input)
}
func FailAvailableFileOperationTx(ctx context.Context, tx *sql.Tx, id, code, reason string) error {
	o, e := ownerOperations(tx)
	if e != nil {
		return e
	}
	return o.FailAvailableFileOperation(ctx, id, code, reason)
}
func DeleteFileOperationWorkItemsTx(ctx context.Context, tx *sql.Tx, ownerType, id string) error {
	o, e := ownerOperations(tx)
	if e != nil {
		return e
	}
	return o.DeleteFileOperation(ctx, ownerType, id)
}
func SynchronizeAvailableReleaseVersionTx(ctx context.Context, tx *sql.Tx, id string, version int64) error {
	o, e := ownerOperations(tx)
	if e != nil {
		return e
	}
	return o.SynchronizeAvailableReleaseVersion(ctx, id, version)
}
func DeleteLocalMaintenanceTreeTx(ctx context.Context, tx *sql.Tx, id string) error {
	o, e := ownerOperations(tx)
	if e != nil {
		return e
	}
	return o.DeleteLocalMaintenanceTree(ctx, id)
}

// ErrRetirementContention marks only conflicts from the fixed NOWAIT set.
var ErrRetirementContention = errors.New("retirement work set contention")

func retirementOperationError(err error) error {
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "55P03" {
		return fmt.Errorf("%w: %w", ErrRetirementContention, err)
	}
	return err
}

func LockRetirementSetTx(ctx context.Context, tx *sql.Tx, id string, epoch int64) error {
	o, err := ownerOperations(tx)
	if err != nil {
		return err
	}
	return retirementOperationError(o.LockRetirementSet(ctx, id, epoch))
}

func RetireUnadmittedTx(ctx context.Context, tx *sql.Tx, id string, epoch int64) error {
	o, e := ownerOperations(tx)
	if e != nil {
		return e
	}
	return retirementOperationError(o.RetireUnadmitted(ctx, id, epoch))
}
