package application

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"errors"
)

// ObserveRecovery performs no feed fetch, release admission or file planning.
// RSS refresh changes only DB facts; the fenced owner writer atomically records
// exhaustion/cancellation. Existing product successes are not guessed from logs.
func (h RSSRefreshHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindRSSRefresh || c.Owner.Type != workitems.OwnerRSSSourceRuntime {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	snapshot, err := h.service.loadSourceSnapshot(ctx, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	o := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: workitems.StateFailed, ReasonCode: "WORKER_EXECUTION_STOPPED", Reason: "worker execution ended without an external effect"}, h.service.refreshOwnerWriter(c, snapshot, "failed", "WORKER_EXECUTION_STOPPED", "worker execution ended without an external effect", nil))
	o.Disposition = workitems.DispositionNoExternalEffect
	return o, nil
}

func (h ReleaseEvaluateHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindReleaseEvaluate || c.Owner.Type != workitems.OwnerRelease {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	var version int64
	var verdict string
	var task sql.NullString
	if err := h.service.db.QueryRowContext(ctx, `SELECT version,verdict,download_task_id FROM releases WHERE id=$1`, c.Owner.ID).Scan(&version, &verdict, &task); err != nil {
		return worker.Outcome{}, err
	}
	// Acquisition committed in the previous generation must never be replaced
	// by a manufactured parse failure or a second admission.
	if task.Valid || isTerminalReleaseVerdict(verdict) {
		o := worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: verdict, ReasonCode: "release_recovery_committed"}, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
			var current int64
			var currentVerdict string
			var currentTask sql.NullString
			err := scope.SQLTx().QueryRowContext(ctx, `SELECT version,verdict,download_task_id FROM releases WHERE id=$1 FOR UPDATE`, c.Owner.ID).Scan(&current, &currentVerdict, &currentTask)
			if err != nil {
				return 0, err
			}
			if current != version || currentVerdict != verdict || currentTask != task {
				return 0, workitems.ErrStaleClaim
			}
			return current, nil
		})
		o.Disposition = workitems.DispositionConfirmed
		return o, nil
	}
	if version != c.ExpectedOwnerVersion {
		return worker.Outcome{}, workitems.ErrStaleClaim
	}
	o := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "acquisition_failed", ReasonCode: "WORKER_EXECUTION_STOPPED"}, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		var next int64
		err := scope.SQLTx().QueryRowContext(ctx, `UPDATE releases SET verdict='acquisition_failed',reason_code='WORKER_EXECUTION_STOPPED',reason_detail='evaluation stopped before acquisition commit',version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$2 AND download_task_id IS NULL RETURNING version`, c.Owner.ID, c.ExpectedOwnerVersion).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, workitems.ErrStaleClaim
		}
		return next, err
	})
	o.Disposition = workitems.DispositionNoExternalEffect
	return o, nil
}
