package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type alignmentRecoveryHandler struct {
	service *AlignmentService
	handle  worker.Handler
}

func (h alignmentRecoveryHandler) Handle(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	return h.handle.Handle(ctx, c)
}
func (h alignmentRecoveryHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindAlignmentExecute || c.Owner.Type != workitems.OwnerAlignment {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	op, err := h.service.load(ctx, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if op == nil {
		return worker.Outcome{}, workitems.ErrStaleClaim
	}
	if op.Version != c.ExpectedOwnerVersion {
		return worker.Outcome{}, workitems.ErrStaleClaim
	}
	if op.State == AlignmentCompleted {
		return recoveryCommitted("completed", lockedMaintenanceEvidence("alignment_operations", c, "completed", op.Version)), nil
	}
	if op.State == AlignmentReady && len(op.Attempt) == 0 {
		return recoveryNoEffect(maintenanceNoAttemptWriter("alignment_operations", c, "ready", alignmentSettleWriter(op, op.Version, AlignmentFailed, "WORKER_EXECUTION_STOPPED", "no attempt admitted", ""))), nil
	}
	// An attempt/target match alone lacks executor quiescence proof. Preserve it.
	return h.recoverReceipt(ctx, c, op)
}

type offsetRecoveryHandler struct {
	service *OffsetMigrationExecuteService
	handle  worker.Handler
}

func (h offsetRecoveryHandler) Handle(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	return h.handle.Handle(ctx, c)
}
func (h offsetRecoveryHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindOffsetMigrationExecute || c.Owner.Type != workitems.OwnerOffsetMigration {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	op, err := loadOffsetMigrationOperation(ctx, h.service.db, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if op.Version != c.ExpectedOwnerVersion {
		return worker.Outcome{}, workitems.ErrStaleClaim
	}
	if op.State == OffsetMigrationOperationApplied {
		return recoveryCommitted("applied", lockedMaintenanceEvidence("offset_migration_plans", c, "applied", op.Version)), nil
	}
	if op.State == OffsetMigrationOperationReady && len(op.Attempt) == 0 {
		return recoveryNoEffect(maintenanceNoAttemptWriter("offset_migration_plans", c, "ready", offsetMigrationTerminalWriter(op.ID, op.Version, OffsetMigrationOperationFailed, "WORKER_EXECUTION_STOPPED", "no attempt admitted"))), nil
	}
	return h.recoverReceipt(ctx, c, op)
}

type replacementRecoveryHandler struct {
	service *ReplacementDecisionService
	handle  worker.Handler
}

func (h replacementRecoveryHandler) Handle(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	return h.handle.Handle(ctx, c)
}
func (h replacementRecoveryHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindReplacementEvaluate || c.Owner.Type != workitems.OwnerFileMaintenance {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	var input struct {
		CandidateReleaseID string `json:"candidate_release_id"`
	}
	if json.Unmarshal(c.InputSnapshot, &input) != nil || input.CandidateReleaseID == "" {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	check := func(ctx context.Context, q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}) (bool, error) {
		var exists bool
		// Committed: the release has a live decision at the claimed version
		// and none of its key decisions is still pending — no admitted
		// replace (bound, or unbound for a key the release already placed)
		// and no settled replace whose loser lacks its cleanup owner (a loser
		// shared by two key decisions has one cleanup, owned by either).
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM replacement_decisions d WHERE managed_anime_id=$1 AND candidate_release_id=$2 AND subscription_version=$3 AND state<>'stale') AND NOT EXISTS(SELECT 1 FROM replacement_decisions d WHERE managed_anime_id=$1 AND candidate_release_id=$2 AND subscription_version=$3 AND verdict='replace' AND ((state='admitted' AND (candidate_placement_id IS NOT NULL OR EXISTS(SELECT 1 FROM placements p JOIN placement_coverage pc ON pc.placement_id=p.id WHERE p.managed_anime_id=d.managed_anime_id AND p.quality_facts_ref=d.candidate_release_id AND p.source_evidence_type='file_operation' AND p.state='active' AND p.verification_state='verified' AND pc.observation_key=d.coverage_key))) OR (state='settled' AND EXISTS(SELECT 1 FROM jsonb_array_elements(d.pools) pool CROSS JOIN LATERAL jsonb_array_elements(COALESCE(pool->'losers','[]'::jsonb)) loser WHERE NOT EXISTS(SELECT 1 FROM cleanup_operations co WHERE co.placement_id=loser->>'placement_id' AND co.companion_id IS NULL)))))`, c.Owner.ID, input.CandidateReleaseID, c.ExpectedOwnerVersion).Scan(&exists)
		return exists, err
	}
	exists, err := check(ctx, h.service.db)
	if err != nil {
		return worker.Outcome{}, err
	}
	if !exists {
		return h.service.adoptReplacementReservation(ctx, c, input.CandidateReleaseID)
	}
	writer := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		var version int64
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT version FROM subscriptions WHERE managed_anime_id=$1 FOR UPDATE`, c.Owner.ID).Scan(&version); err != nil {
			return 0, err
		}
		if version != c.ExpectedOwnerVersion {
			return 0, workitems.ErrStaleClaim
		}
		ok, err := check(ctx, scope.SQLTx())
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, workitems.ErrStaleClaim
		}
		return version, nil
	}
	return recoveryCommitted("admitted", writer), nil
}
func recoveryCommitted(state string, w workitems.OwnerWriter) worker.Outcome {
	o := worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: state, ReasonCode: "owner_recovery_committed"}, w)
	o.Disposition = workitems.DispositionConfirmed
	return o
}
func recoveryNoEffect(w workitems.OwnerWriter) worker.Outcome {
	o := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "WORKER_EXECUTION_STOPPED"}, w)
	o.Disposition = workitems.DispositionNoExternalEffect
	return o
}

// table is exclusively one of the three literal owner tables above.
func maintenanceNoAttemptWriter(table string, c workitems.Claim, state string, next workitems.OwnerWriter) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		if _, err := lockedMaintenanceEvidence(table, c, state, c.ExpectedOwnerVersion)(ctx, scope, result); err != nil {
			return 0, err
		}
		var empty bool
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT attempt IS NULL FROM `+table+` WHERE id=$1`, c.Owner.ID).Scan(&empty); err != nil {
			return 0, err
		}
		if !empty {
			return 0, workitems.ErrStaleClaim
		}
		return next(ctx, scope, result)
	}
}

func lockedMaintenanceEvidence(table string, c workitems.Claim, state string, expected int64) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		var version int64
		var current string
		err := scope.SQLTx().QueryRowContext(ctx, `SELECT version,state FROM `+table+` WHERE id=$1 FOR UPDATE NOWAIT`, c.Owner.ID).Scan(&version, &current)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, workitems.ErrStaleClaim
		}
		if err != nil {
			return 0, err
		}
		if version != expected || version != c.ExpectedOwnerVersion || current != state {
			return 0, workitems.ErrStaleClaim
		}
		return version, nil
	}
}
