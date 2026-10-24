package application

import (
	"context"
	"errors"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// operationExists reports whether the claim's file operation row exists.
// Every row is planned by a committed post-processing plan
// (postprocessing_plan_id is NOT NULL), so an existing row always belongs to
// the typed publication lane.
func (s *Service) operationExists(ctx context.Context, claim workitems.Claim) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrInvalidClaim
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM file_operations WHERE id=$1)`, claim.Owner.ID).Scan(&exists)
	return exists, err
}

// typedExecutionBlocked is the explicit await-recovery outcome for a
// file_execute claim that may not start or settle an effect here (a terminal
// replay, or a handler failure after an unknown effect): the claim stays
// leased and the ticketed recovery lane settles it from persistent evidence.
func typedExecutionBlocked() worker.Outcome {
	outcome := worker.AwaitRecovery(fileExecutionRecoveryPendingCode)
	outcome.OwnerResult = workitems.OwnerResult{State: "blocked", ReasonCode: fileExecutionRecoveryPendingCode, Reason: "file execution is settled by the recovery lane from its publication evidence"}
	return outcome
}

const fileExecutionRecoveryPendingCode = "FILE_CANCEL_FINALIZATION_PENDING"

var errTypedExecutionPending = errors.New(fileExecutionRecoveryPendingCode + ": file execution is settled by the typed recovery lane")
