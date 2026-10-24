package application

import (
	"context"
	"database/sql"
	"fmt"

	"bansoko/internal/workitems"
)

// Read-only recovery preparation preserves original execution identity. It does
// not bypass cancel/lease/wall rules or authorize a Finish; the finish must lock and
// revalidate the current recovery claim before committing business bookkeeping.
func (s *Service) prepareRecoveredSuccessfulFileResult(ctx context.Context, claim workitems.Claim, permit string) (preparedFileResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return preparedFileResult{}, err
	}
	defer tx.Rollback()
	p, err := captureRecoveredSuccessfulFileResultTx(ctx, tx, claim, permit)
	if err != nil {
		return preparedFileResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return preparedFileResult{}, err
	}
	return p, nil
}

func captureRecoveredSuccessfulFileResultTx(ctx context.Context, tx *sql.Tx, claim workitems.Claim, permit string) (preparedFileResult, error) {
	if tx == nil || claim.Kind != workitems.KindFileExecute || claim.Owner.Type != workitems.OwnerFileOperation {
		return preparedFileResult{}, ErrInvalidClaim
	}
	var originalWork, operation string
	var token, version int64
	if err := tx.QueryRowContext(ctx, `SELECT publication_work_item_id,id,publication_fencing_token,publication_owner_version FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&originalWork, &operation, &token, &version); err != nil {
		return preparedFileResult{}, err
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.work_items w JOIN public.file_operations o ON o.id=w.owner_id JOIN public.file_operations e ON e.publication_permit_id=$5 AND e.id=o.id AND e.acquisition_id=o.acquisition_id AND e.publication_work_item_id=w.id WHERE w.id=$1 AND w.acquisition_id=o.acquisition_id AND w.kind='file_execute' AND w.owner_type='file_operation' AND w.owner_id=$2 AND w.state='leased' AND (w.recovery_blocked OR NOT w.cancel_requested) AND w.cancel_requested=$6 AND w.fencing_token=$3 AND w.expected_owner_version=$4 AND o.version=$4 AND w.lease_until>clock_timestamp())`, claim.ID, claim.Owner.ID, claim.FencingToken, claim.ExpectedOwnerVersion, permit, claim.CancelRequested).Scan(&valid); err != nil {
		return preparedFileResult{}, err
	}
	if !valid || originalWork != claim.ID || operation != claim.Owner.ID || claim.FencingToken <= token {
		return preparedFileResult{}, fmt.Errorf("FILE_RESULT_RECOVERY_CLAIM_MISMATCH")
	}
	original := claim
	original.ID = originalWork
	original.FencingToken = token
	original.ExpectedOwnerVersion = version
	p, err := captureSuccessfulFileResultTx(ctx, tx, original, permit)
	if err != nil {
		return preparedFileResult{}, err
	}
	p.claimWork, p.claimToken, p.claimOwnerVersion = claim.ID, claim.FencingToken, claim.ExpectedOwnerVersion
	p.recoveryClaim = true
	return p, nil
}
