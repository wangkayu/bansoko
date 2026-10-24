package workitems

import (
	"context"
	"fmt"

	"bansoko/internal/persistence/txscope"
)

// WithTypedStageTransaction locks the optional one-shot marker before the
// caller's root/task/work gates. The callback owns the WorkItem transition;
// this coordinator settles its admission in the same transaction.
func (s *Store) WithTypedStageTransaction(ctx context.Context, claim Claim, apply func(txscope.Tx) (EffectDisposition, error)) error {
	return s.withTypedStageTransaction(ctx, claim, apply, false)
}

// WithFileFinalizationTransaction additionally requires the original admission
// to exist and be consumed when a one-shot file result is committed.
func (s *Store) WithFileFinalizationTransaction(ctx context.Context, claim Claim, apply func(txscope.Tx) (EffectDisposition, error)) error {
	if claim.Kind != KindFileExecute || claim.Owner.Type != OwnerFileOperation {
		return ErrInvalidSpec
	}
	return s.withTypedStageTransaction(ctx, claim, apply, true)
}

func (s *Store) withTypedStageTransaction(ctx context.Context, claim Claim, apply func(txscope.Tx) (EffectDisposition, error), requireFileAdmission bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin typed stage transaction: %w", err)
	}
	defer tx.Rollback()
	var marker oneShotMarkerRow
	if claim.OneShot != nil {
		if err := validateOneShotClaimContext(claim); err != nil {
			return err
		}
		marker, err = lockOneShotMarkerTx(ctx, tx, claim.OneShot.MarkerID, claim.OneShot.AdmissionEpoch)
		if err != nil {
			return err
		}
		if err := validateOneShotMarkerLedger(marker); err != nil {
			return err
		}
	}
	disposition, err := apply(txscope.Wrap(tx))
	if err != nil {
		return err
	}
	// The typed callback has acquired root/task/work locks. Read the current
	// reference under that work lock before consuming the marker admission.
	var persistedInput []byte
	if err := tx.QueryRowContext(ctx, `SELECT input_snapshot FROM work_item_history WHERE id=$1`, claim.ID).Scan(&persistedInput); err != nil {
		return err
	}
	persistedRef, err := parseOneShotRef(persistedInput)
	if err != nil {
		return err
	}
	if (persistedRef == nil) != (claim.OneShot == nil) || (persistedRef != nil && *persistedRef != *claim.OneShot) {
		return ErrOneShotReclaiming
	}
	if claim.OneShot != nil && dispositionConsumesLease(disposition) {
		// Recovery may retain an admission from the original token. The ledger
		// identity belongs to the WorkItem/owner/epoch, not its current token.
		consumed := false
		for _, entry := range marker.Leases {
			if entry.WorkItemID != claim.ID {
				continue
			}
			lease, err := leaseEntryToAdmissionLease(claim.OneShot.MarkerID, entry)
			if err != nil {
				return err
			}
			if err := validatePersistedOneShotLease(claim, lease, entry); err != nil {
				return err
			}
			consumed, err = settleOneShotLeaseLockedTx(ctx, tx, claim.OneShot.MarkerID, marker, entry.AdmissionID)
			if err != nil {
				return err
			}
			break
		}
		if requireFileAdmission && !consumed {
			return ErrInvalidOneShotLedger
		}
	}
	return tx.Commit()
}
