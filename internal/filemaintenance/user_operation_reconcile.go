package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/deployment"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

var _ deployment.ReconcileHandler = (*UserOperationService)(nil)

// ReconcileUnknownEffect is the deployment recovery reconciler of the file
// manager's user operations (owner file_manager_operation). It reads the
// exact invocation receipt of every running operation's checkpointed stage
// through the same evidence the worker's recovery uses:
//   - a valid receipt settles the operation completed in the owner's own
//     transaction (the CAS on version and attempt keeps it exact); the
//     WorkItem then finishes against the settled owner when it is claimed;
//   - a stage that provably never started (or a finished capture whose purge
//     is next) is left for the WorkItem to resume;
//   - anything else is an unknown effect and blocks the gate (fail closed):
//     a move, copy or purge is never assumed done or undone without proof.
//
// Operations still `ready` have no attempt and no effect.
func (s *UserOperationService) ReconcileUnknownEffect(ctx context.Context, _ deployment.ReconcileRequest) (deployment.ReconcileResult, error) {
	retryable := func(err error) (deployment.ReconcileResult, error) {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: safeReason(err)}, nil
	}
	if s == nil || s.db == nil || s.executor == nil || s.observer == nil {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown,
			Reason: "user operation reconcile: file ports are not wired"}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM file_manager_operations WHERE state = 'running' ORDER BY created_at, id`)
	if err != nil {
		return retryable(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return retryable(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return retryable(err)
	}
	handler := userOperationRecoveryHandler{service: s}
	settled, resumable := 0, 0
	for _, id := range ids {
		op, err := s.load(ctx, id)
		if err != nil {
			return retryable(err)
		}
		if op == nil || op.State != "running" {
			continue
		}
		if len(op.Attempt) == 0 {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: op.ID,
				Reason: "user operation reconcile: running operation has no attempt"}, nil
		}
		claim, found, err := s.reconcileClaim(ctx, op)
		if err != nil {
			return retryable(err)
		}
		if !found {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: op.ID,
				Reason: "user operation reconcile: the operation's work item is missing"}, nil
		}
		outcome, resume, err := handler.resolveAttempt(ctx, claim, op)
		if err != nil {
			return retryable(err)
		}
		if resume {
			resumable++
			continue
		}
		if outcome.WriteOwner == nil || outcome.Disposition != workitems.DispositionConfirmed {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: op.ID,
				Reason: "user operation reconcile: the effect of the checkpointed stage is unknown"}, nil
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return retryable(err)
		}
		if _, err := outcome.WriteOwner(ctx, txscope.Wrap(tx), outcome.OwnerResult); err != nil {
			_ = tx.Rollback()
			if errors.Is(err, workitems.ErrStaleClaim) {
				// The owner moved on concurrently; a later pass reads it again.
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectPresent, EvidenceRef: op.ID,
					Reason: "user operation reconcile: the operation changed while it was settled"}, nil
			}
			return retryable(err)
		}
		if err := tx.Commit(); err != nil {
			return retryable(err)
		}
		settled++
	}
	effect := deployment.EffectNone
	if settled > 0 {
		effect = deployment.EffectPresent
	}
	return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: effect, EvidenceRef: userOperationTable,
		Reason: fmt.Sprintf("settled %d user operation(s); %d resume through their work item", settled, resumable)}, nil
}

// reconcileClaim rebuilds the identity the checkpointed stage's invocation was
// bound to: the operation's single work item at the owner version the
// checkpoint wrote.
func (s *UserOperationService) reconcileClaim(ctx context.Context, op *userOperation) (workitems.Claim, bool, error) {
	var id, kind string
	err := s.db.QueryRowContext(ctx, `SELECT id, kind FROM work_item_history
WHERE owner_type = $1 AND owner_id = $2 AND kind = $3 ORDER BY created_at DESC, id DESC LIMIT 1`,
		workitems.OwnerFileManagerOperation, op.ID, workitems.KindFileManagerExecute).Scan(&id, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return workitems.Claim{}, false, nil
	}
	if err != nil {
		return workitems.Claim{}, false, err
	}
	return workitems.Claim{
		ID: id, Kind: kind, ExpectedOwnerVersion: op.Version,
		Owner: workitems.OwnerRef{Type: workitems.OwnerFileManagerOperation, ID: op.ID},
	}, true, nil
}
