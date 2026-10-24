package application

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
)

type scanRecoveryHandler struct{ service *Service }

func (h scanRecoveryHandler) Handle(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	return h.service.handleScan(ctx, c)
}
func (h scanRecoveryHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindReconciliationScan || c.Owner.Type != workitems.OwnerFileMaintenance {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	owner, err := loadScanOwner(ctx, h.service.db, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if owner == nil {
		return worker.AwaitRecovery(""), nil
	}
	// A newer scan owner is not evidence about this frozen work generation.
	if owner.Version != c.ExpectedOwnerVersion {
		return worker.AwaitRecovery(""), nil
	}
	state := workitems.StateFailed
	disposition := workitems.DispositionNoExternalEffect
	if owner.State == ScanCompleted {
		state = workitems.StateSucceeded
		disposition = workitems.DispositionConfirmed
	}
	o := worker.Finish(state, workitems.OwnerResult{State: state, ReasonCode: "scan_recovery_observed"}, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		current, err := readScanOwner(ctx, scope.SQLTx(), c.Owner.ID, true)
		if err != nil {
			return 0, err
		}
		if current == nil || current.Generation != owner.Generation || current.Version != owner.Version || current.State != owner.State {
			return 0, workitems.ErrStaleClaim
		}
		if current.State != ScanRunning {
			return current.Version, nil
		}
		if current.Version != c.ExpectedOwnerVersion {
			return 0, workitems.ErrStaleClaim
		}
		var next int64
		err = scope.SQLTx().QueryRowContext(ctx, `UPDATE reconciliation_scans SET state='failed',failure_code='WORKER_EXECUTION_STOPPED',failure_reason='read-only scan stopped',version=version+1,updated_at=CURRENT_TIMESTAMP WHERE managed_anime_id=$1 AND generation=$2 AND version=$3 RETURNING version`, c.Owner.ID, current.Generation, current.Version).Scan(&next)
		return next, err
	})
	o.Disposition = disposition
	// Completed scan and calibration admission commit together in the normal
	// path. Do not enumerate/hash or manufacture a replacement generation here.
	return o, nil
}
