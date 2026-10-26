package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/deployment"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

type cleanupRecoveryHandler struct {
	service *CleanupService
	handle  worker.Handler
}

func (h cleanupRecoveryHandler) Handle(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	return h.handle.Handle(ctx, c)
}

// ObserveRecovery classifies an interrupted replacement_delete claim from the
// owner and the exact invocation receipts only; it never runs an effect.
func (h cleanupRecoveryHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindReplacementDelete || c.Owner.Type != workitems.OwnerReplacementDeletion {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	s := h.service
	op, err := s.load(ctx, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if op == nil || op.Version != c.ExpectedOwnerVersion {
		return worker.Outcome{}, workitems.ErrStaleClaim
	}
	switch op.State {
	case CleanupDeleted, CleanupSkipped:
		return recoveryCommitted(string(op.State), lockedMaintenanceEvidence("replacement_deletions", c, string(op.State), op.Version)), nil
	case CleanupBlocked, CleanupFailed:
		o := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: string(op.State), ReasonCode: op.FailureCode, Reason: op.Reason},
			lockedMaintenanceEvidence("replacement_deletions", c, string(op.State), op.Version))
		o.Disposition = workitems.DispositionConfirmed
		return o, nil
	}
	if len(op.Attempt) == 0 {
		writer := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
			return s.repository.TransitionTx(ctx, scope, op.ID, op.Version, string(CleanupFailed), "WORKER_EXECUTION_STOPPED", "the deletion stopped before it started")
		}
		return recoveryNoEffect(maintenanceNoAttemptWriter("replacement_deletions", c, "admitted", writer)), nil
	}
	attempt, err := decodeCleanupAttempt(op.Attempt)
	if err != nil {
		return worker.AwaitRecovery(""), nil
	}
	receipts, ok := s.executor.(executorpkg.MaintenanceCleanupExecutor)
	if !ok {
		return worker.AwaitRecovery(""), nil
	}
	switch resolveCleanupAttempt(ctx, s.executor, receipts, op, attempt) {
	case cleanupResolutionPurged:
		writer := s.settleWriter(op, op.Version, CleanupDeleted, "maintenance_receipt_recovered", "exact purge invocation returned")
		return recoveryCommitted("deleted", maintenanceAttemptWriter("replacement_deletions", c, string(op.State), op.Attempt, writer)), nil
	case cleanupResolutionCaptureNotStarted:
		writer := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
			return s.repository.TransitionTx(ctx, scope, op.ID, op.Version, string(CleanupFailed), "WORKER_EXECUTION_STOPPED", "the deletion stopped before its capture started")
		}
		return recoveryNoEffect(maintenanceAttemptWriter("replacement_deletions", c, string(op.State), op.Attempt, writer)), nil
	case cleanupResolutionCaptured, cleanupResolutionPurgeNotStarted:
		// The file sits captured in the private namespace. Recovery never
		// purges; the owner blocks with its attempt and deployment recovery
		// re-admits it so a fresh claim finishes the purge.
		writer := s.settleWriter(op, op.Version, CleanupBlocked, "cleanup_capture_unpurged", "the replaced file was captured but not yet purged")
		o := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: string(CleanupBlocked), ReasonCode: "cleanup_capture_unpurged"},
			maintenanceAttemptWriter("replacement_deletions", c, string(op.State), op.Attempt, writer))
		o.Disposition = workitems.DispositionConfirmed
		return o, nil
	}
	return worker.AwaitRecovery(""), nil
}

// ReconcileUnknownEffect is the deployment-owned convergence of deletion
// obligations. An admitted obligation without an attempt gets its work item;
// a blocked obligation with an attempt is settled from its exact invocation
// receipts: a returned purge is deleted, a finished capture or a provably
// unstarted stage is re-admitted for a fresh claim, anything else stays
// blocked as an unknown effect.
func (s *CleanupService) ReconcileUnknownEffect(ctx context.Context, _ deployment.ReconcileRequest) (deployment.ReconcileResult, error) {
	if s.executor == nil {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, Reason: "cleanup reconcile: executor not wired"}, nil
	}
	receipts, ok := s.executor.(executorpkg.MaintenanceCleanupExecutor)
	if !ok {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, Reason: "cleanup reconcile: executor has no receipts"}, nil
	}
	rows, err := s.repository.ListPending(ctx)
	if err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: safeCleanupReason(err)}, nil
	}
	unknown := ""
	for _, row := range rows {
		op := replacementDeletionFromPersistence(row)
		if op.State == CleanupAdmitted {
			if len(op.Attempt) > 0 {
				// An interrupted claim: its work item's recovery lane owns it.
				continue
			}
			if err := s.readmit(ctx, op, false); err != nil {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: op.ID, Reason: "cleanup reconcile: enqueue failed: " + safeCleanupReason(err)}, nil
			}
			continue
		}
		attempt, err := decodeCleanupAttempt(op.Attempt)
		if err != nil {
			unknown = op.ID
			continue
		}
		switch resolveCleanupAttempt(ctx, s.executor, receipts, op, attempt) {
		case cleanupResolutionPurged:
			if err := s.settleRecovered(ctx, op); err != nil {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: op.ID, Reason: safeCleanupReason(err)}, nil
			}
		case cleanupResolutionCaptured, cleanupResolutionCaptureNotStarted, cleanupResolutionPurgeNotStarted:
			if err := s.readmit(ctx, op, true); err != nil {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: op.ID, Reason: "cleanup reconcile: re-admission failed: " + safeCleanupReason(err)}, nil
			}
		default:
			unknown = op.ID
		}
	}
	if unknown != "" {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: unknown, Reason: "cleanup reconcile: a blocked deletion has no receipt"}, nil
	}
	return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: deployment.EffectNone, Reason: "cleanup reconcile: no unresolved deletion"}, nil
}

// readmit (re)creates the work item of an obligation. A blocked obligation is
// moved back to admitted with its attempt kept, so the next claim resumes it.
func (s *CleanupService) readmit(ctx context.Context, op *ReplacementDeletion, fromBlocked bool) error {
	return s.repository.WithTx(ctx, func(scope txscope.Tx) error {
		current, err := s.repository.LockTx(ctx, scope, op.ID)
		if err != nil {
			return err
		}
		if current.Version != op.Version || current.State != string(op.State) {
			return errors.New("cleanup owner changed during reconciliation")
		}
		version := current.Version
		if fromBlocked {
			if version, err = s.repository.TransitionTx(ctx, scope, op.ID, op.Version, string(CleanupAdmitted), "cleanup_readmitted", "re-admitted from its receipts"); err != nil {
				return err
			}
		}
		return s.enqueueTx(ctx, scope.SQLTx(), op.ID, version)
	})
}

// settleRecovered settles a blocked obligation whose purge receipt proves
// the deletion.
func (s *CleanupService) settleRecovered(ctx context.Context, op *ReplacementDeletion) error {
	return s.repository.WithTx(ctx, func(scope txscope.Tx) error {
		current, err := s.repository.LockTx(ctx, scope, op.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("cleanup %s disappeared during reconciliation", op.ID)
		}
		if err != nil {
			return err
		}
		if current.Version != op.Version || current.State != string(CleanupBlocked) {
			return errors.New("cleanup owner changed during reconciliation")
		}
		_, err = s.settleWriter(op, op.Version, CleanupDeleted, "maintenance_receipt_recovered", "exact purge invocation returned")(ctx, scope, workitems.OwnerResult{})
		return err
	})
}
