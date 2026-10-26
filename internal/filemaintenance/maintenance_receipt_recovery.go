package filemaintenance

import (
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// maintenanceAttemptNotStarted reports whether a persisted move attempt provably
// never reached its effect, so the same invocation may run again: the executor
// found no start marker for it under the invocation lock, the source still
// carries the frozen fingerprint, and nothing exists at the target. Any other
// state (partial marker, drifted source, occupied target, unreadable path)
// stays unresolved.
func maintenanceAttemptNotStarted(ctx context.Context, observer executorpkg.FileObserver, readErr error, source, target, fingerprint string) bool {
	if observer == nil || !errors.Is(readErr, executorpkg.ErrMaintenanceNotStarted) {
		return false
	}
	observed, err := executorpkg.ObserveLike(ctx, observer, source, fingerprint)
	if err != nil || !offsetFingerprintsEqual(observed, fingerprint) {
		return false
	}
	observed, err = executorpkg.ObserveLike(ctx, observer, target, fingerprint)
	return err != nil && errors.Is(err, os.ErrNotExist) || err == nil && observed == ""
}

func (h alignmentRecoveryHandler) recoverReceipt(ctx context.Context, c workitems.Claim, op *AlignmentOperation) (worker.Outcome, error) {
	o, _, err := h.resolveAttempt(ctx, c, op)
	return o, err
}

// resolveAttempt reads the exact invocation receipt once. resume=true means the
// attempt provably never started and the caller may execute it unchanged.
func (h alignmentRecoveryHandler) resolveAttempt(ctx context.Context, c workitems.Claim, op *AlignmentOperation) (worker.Outcome, bool, error) {
	e, ok := h.service.executor.(executorpkg.MaintenanceFileExecutor)
	if !ok {
		return worker.AwaitRecovery(""), false, nil
	}
	fp, err := offsetSourceFingerprintValue(op.SourceFingerprint)
	if err != nil {
		return worker.Outcome{}, false, err
	}
	i := maintenanceInvocation(c, op.Attempt, op.SourcePath, op.TargetPath, fp)
	if operation, ok := resumeOverwriteOperation(op); ok {
		return h.resumeOverwrite(ctx, c, op, operation, i)
	}
	r, err := e.ReadMaintenanceReceipt(ctx, i)
	if err != nil || r.Validate(i) != nil {
		if maintenanceAttemptNotStarted(ctx, h.service.observer, err, op.SourcePath, op.TargetPath, fp) {
			return worker.Outcome{}, true, nil
		}
		return worker.AwaitRecovery(""), false, nil
	}
	writer := alignmentSettleWriter(op, op.Version, AlignmentCompleted, "maintenance_receipt_recovered", "exact invocation final receipt", op.TargetPath)
	return recoveryCommitted("completed", maintenanceAttemptWriter("alignment_operations", c, string(op.State), op.Attempt, writer)), false, nil
}

// resumeOverwrite finishes or restores an interrupted overwrite exchange from
// the operation's persisted record, under the same invocation. A finished
// exchange settles completed; a restored one (the occupant raced back)
// settles blocked; anything else stays unresolved for review.
func (h alignmentRecoveryHandler) resumeOverwrite(ctx context.Context, c workitems.Claim, op *AlignmentOperation, operation domain.FileOperation, i executorpkg.MaintenanceInvocation) (worker.Outcome, bool, error) {
	e, ok := h.service.executor.(executorpkg.MaintenanceOverwriteExecutor)
	if !ok {
		return worker.AwaitRecovery(""), false, nil
	}
	executed, err := e.ExecuteMaintenanceOverwrite(ctx, operation, i, h.service.overwriteCheckpoint(op.ID, nil))
	if err != nil {
		if alignmentErrorCode(err, "TARGET_COLLISION_RACE") {
			writer := alignmentSettleWriter(op, op.Version, AlignmentBlocked, "target_conflict", safeReason(err), "")
			o := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "target_conflict", Reason: safeReason(err)}, maintenanceAttemptWriter("alignment_operations", c, string(op.State), op.Attempt, writer))
			o.Disposition = workitems.DispositionConfirmed
			return o, false, nil
		}
		return worker.AwaitRecovery(""), false, nil
	}
	writer := alignmentSettleWriter(op, op.Version, AlignmentCompleted, "overwrite_recovered", "exchange finished from its persisted record", executed.TargetPath)
	return recoveryCommitted("completed", maintenanceAttemptWriter("alignment_operations", c, string(op.State), op.Attempt, writer)), false, nil
}

func (h offsetRecoveryHandler) recoverReceipt(ctx context.Context, c workitems.Claim, op *OffsetMigrationOperation) (worker.Outcome, error) {
	o, _, err := h.resolveAttempt(ctx, c, op)
	return o, err
}

// resolveAttempt reads the exact invocation receipt once. resume=true means the
// attempt provably never started and the caller may execute it unchanged.
func (h offsetRecoveryHandler) resolveAttempt(ctx context.Context, c workitems.Claim, op *OffsetMigrationOperation) (worker.Outcome, bool, error) {
	e, ok := h.service.executor.(executorpkg.MaintenanceFileExecutor)
	if !ok {
		return worker.AwaitRecovery(""), false, nil
	}
	fp, err := offsetSourceFingerprintValue(op.SourceFingerprint)
	if err != nil {
		return worker.Outcome{}, false, err
	}
	target := filepath.Clean(op.NewTargetPath)
	i := maintenanceInvocation(c, op.Attempt, op.OldTargetPath, target, fp)
	r, err := e.ReadMaintenanceReceipt(ctx, i)
	if err != nil || r.Validate(i) != nil {
		if maintenanceAttemptNotStarted(ctx, h.service.observer, err, op.OldTargetPath, target, fp) {
			return worker.Outcome{}, true, nil
		}
		return worker.AwaitRecovery(""), false, nil
	}
	committed := *op
	committed.NewTargetPath = target
	writer := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		return h.service.commitAppliedTx(ctx, scope.SQLTx(), &committed, op.Version)
	}
	return recoveryCommitted("applied", maintenanceAttemptWriter("offset_migration_plans", c, string(op.State), op.Attempt, writer)), false, nil
}
func (h cleanupRecoveryHandler) recoverReceipt(ctx context.Context, c workitems.Claim, op *CleanupOperation) (worker.Outcome, error) {
	o, _, err := h.resolveAttempt(ctx, c, op)
	return o, err
}

// cleanupQuarantineNotStarted reports whether a persisted quarantine attempt
// provably never reached its move: no start marker under the invocation lock,
// the source still matches the frozen observation, and nothing exists at the
// quarantine target. Purge is never resumed here: its receipt covers only the
// exact removal call, so every pending purge stays unresolved.
func (h cleanupRecoveryHandler) cleanupQuarantineNotStarted(ctx context.Context, readErr error, source, target string, expected domain.FileObservation) bool {
	if h.kind != workitems.KindCleanupQuarantine || !errors.Is(readErr, executorpkg.ErrMaintenanceNotStarted) {
		return false
	}
	observed, err := observeCleanupFile(ctx, h.service.executor, source, expected.Fingerprint)
	if err != nil || !sameFrozenCleanupObservation(observed, expected) {
		return false
	}
	_, err = observeCleanupFile(ctx, h.service.executor, target, expected.Fingerprint)
	return isMissingCleanupError(err)
}

// resolveAttempt reads the exact invocation receipt once. resume=true means the
// quarantine attempt provably never started and the caller may execute it
// unchanged.
func (h cleanupRecoveryHandler) resolveAttempt(ctx context.Context, c workitems.Claim, op *CleanupOperation) (worker.Outcome, bool, error) {
	e, ok := h.service.executor.(executorpkg.MaintenanceCleanupExecutor)
	if !ok {
		return worker.AwaitRecovery(""), false, nil
	}
	evidence, err := decodeCleanupAttempt(op.Attempt)
	if err != nil {
		return worker.AwaitRecovery(""), false, nil
	}
	expected, err := frozenObservation(op.Attempt, op.SourceFingerprint)
	if err != nil {
		return worker.AwaitRecovery(""), false, nil
	}
	source, target := op.SourcePath, evidence.QuarantinePath
	if h.kind == workitems.KindCleanupPurge {
		if evidence.Stage != "purge" {
			return worker.AwaitRecovery(""), false, nil
		}
		source, target = op.QuarantinePath, op.QuarantinePath
	} else if evidence.Stage != "quarantine" {
		return worker.AwaitRecovery(""), false, nil
	}
	i := maintenanceInvocation(c, op.Attempt, source, target, expected.Fingerprint)
	r, err := e.ReadMaintenanceReceipt(ctx, i)
	if err != nil || r.Validate(i) != nil || !sameFrozenCleanupObservation(r.Observation, expected) {
		if h.cleanupQuarantineNotStarted(ctx, err, source, target, expected) {
			return worker.Outcome{}, true, nil
		}
		return worker.AwaitRecovery(""), false, nil
	}
	writer := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		if h.kind == workitems.KindCleanupPurge {
			return h.service.transitionTx(ctx, scope.SQLTx(), op.ID, op.Version, CleanupPurged, "maintenance_receipt_recovered", "exact purge invocation returned")
		}
		v, err := h.service.quarantineTx(ctx, scope.SQLTx(), op.ID, op.Version, target, "maintenance_receipt_recovered", "exact quarantine invocation returned")
		if err != nil {
			return 0, err
		}
		snapshot, _ := json.Marshal(map[string]any{"cleanup_id": op.ID, "stage": workitems.KindCleanupPurge})
		_, _, err = h.service.workItems.CreateOrMerge(ctx, scope.SQLTx(), workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerCleanup, ID: op.ID}, Kind: workitems.KindCleanupPurge, IdempotencyKey: "cln:" + op.ID + ":" + workitems.KindCleanupPurge, InputSnapshot: snapshot, MaxAttempts: 3, ExpectedOwnerVersion: v})
		return v, err
	}
	return recoveryCommitted("completed", maintenanceAttemptWriter("cleanup_operations", c, string(op.State), op.Attempt, writer)), false, nil
}
func maintenanceAttemptWriter(table string, c workitems.Claim, state string, attempt json.RawMessage, next workitems.OwnerWriter) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		if _, err := lockedMaintenanceEvidence(table, c, state, c.ExpectedOwnerVersion)(ctx, scope, result); err != nil {
			return 0, err
		}
		var current []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT attempt FROM `+table+` WHERE id=$1`, c.Owner.ID).Scan(&current); err != nil {
			return 0, err
		}
		var a, b any
		if json.Unmarshal(current, &a) != nil || json.Unmarshal(attempt, &b) != nil {
			return 0, workitems.ErrStaleClaim
		}
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if string(x) != string(y) {
			return 0, workitems.ErrStaleClaim
		}
		return next(ctx, scope, result)
	}
}
