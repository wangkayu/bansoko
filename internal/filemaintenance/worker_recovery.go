package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"strconv"
)

// This narrow adapter preserves the existing business handler and observes only
// durable generation commits. It never calls calibration/planning/trigger logic.
type calibrationRecoveryHandler struct {
	service *CalibrationService
	kind    string
	handle  func(context.Context, workitems.Claim) (worker.Outcome, error)
}

func (h calibrationRecoveryHandler) Handle(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	return h.handle(ctx, c)
}
func (h calibrationRecoveryHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != h.kind || c.Owner.Type != workitems.OwnerFileMaintenance {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	generation, err := claimGeneration(c)
	if err != nil {
		return worker.Outcome{}, err
	}
	consumer := ""
	switch h.kind {
	case workitems.KindReconciliationConsumeAlignment:
		consumer = ConsumerAlignment
	case workitems.KindReconciliationConsumeReplacement:
		consumer = ConsumerReplacement
	}
	check := func(ctx context.Context, tx *sql.Tx) (bool, error) {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT generation FROM reconciliation_scans WHERE managed_anime_id=$1 FOR UPDATE`, c.Owner.ID).Scan(&current); err != nil {
			return false, err
		}
		if current != generation {
			return false, workitems.ErrStaleClaim
		}
		if consumer != "" {
			// The folded checkpoint of this consumer generation (PRUNE-046 S10):
			// a checkpoint stored for another generation is not this generation's
			// outcome, so it reads as pending exactly like the old absent row.
			checkpoint, err := loadConsumerCheckpoint(ctx, tx, c.Owner.ID, generation, consumer)
			if err != nil {
				return false, err
			}
			if checkpoint == nil {
				return false, nil
			}
			return checkpoint.State == ConsumerConsumed || checkpoint.State == ConsumerBlocked, nil
		}
		// Both downstream rows are in the calibration transaction, including the
		// zero-file generation. Facts absence alone is never a completion proof.
		var count int
		err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT kind) FROM work_item_history WHERE owner_type='file_maintenance' AND owner_id=$1 AND kind IN ('reconciliation_consume_alignment','reconciliation_consume_replacement') AND input_snapshot->>'generation'=$2`, c.Owner.ID, strconv.FormatInt(generation, 10)).Scan(&count)
		return count == 2, err
	}
	tx, err := h.service.db.BeginTx(ctx, nil)
	if err != nil {
		return worker.Outcome{}, err
	}
	confirmed, err := check(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		return worker.Outcome{}, err
	}
	if !confirmed {
		return worker.AwaitRecovery(""), nil
	}
	o := worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "generation_recovery_committed"}, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		ok, err := check(ctx, scope.SQLTx())
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, workitems.ErrStaleClaim
		}
		return c.ExpectedOwnerVersion, nil
	})
	o.Disposition = workitems.DispositionConfirmed
	return o, nil
}
