package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/deployment"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/redact"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// AlignmentState is the operation owner state machine.
type AlignmentState string

const (
	AlignmentPlanned   AlignmentState = "planned"
	AlignmentReady     AlignmentState = "ready"
	AlignmentRunning   AlignmentState = "running"
	AlignmentCompleted AlignmentState = "completed"
	AlignmentBlocked   AlignmentState = "blocked"
	AlignmentFailed    AlignmentState = "failed"
	AlignmentCanceled  AlignmentState = "canceled"
)

// AlignmentOperation is the frozen-input execution truth.
type AlignmentOperation struct {
	ID             string
	IdempotencyKey string
	ManagedAnimeID string
	// CompanionID is set on a companion alignment: the operation moves
	// one registered companion sidecar and commits the companion's current
	// path instead of the placement's.
	CompanionID       string
	ScanGeneration    int64
	ObservationRef    string
	CandidateRef      string
	PolicyVersion     int64
	MetadataVersion   int64
	RulesVersion      int64
	NamingVersion     int64
	SourcePath        string
	SourceFingerprint string
	TargetPath        string
	TargetContract    json.RawMessage
	DryRunResult      json.RawMessage
	State             AlignmentState
	Attempt           json.RawMessage
	FailureCode       string
	Reason            string
	Version           int64
	CreatedAt         time.Time
	// OverwriteRecovery is the durable exchange record of an overwrite
	// move; a restart finishes or restores the exchange from it.
	OverwriteRecovery json.RawMessage
}

// MaxOperationsPerAdmission bounds one replan admission (parent design).
const MaxOperationsPerAdmission = 100

// FileExecutor and FileObserver are compatibility aliases for the dedicated
// filesystem-effect ports.
type FileExecutor = executorpkg.FileExecutor
type FileObserver = executorpkg.FileObserver
type CleanupFileExecutor = executorpkg.CleanupFileExecutor

// AlignmentService owns the operation lifecycle. Execution itself happens
// through alignment_execute work items; this service persists frozen inputs,
// the durable attempt checkpoint, and the typed reconcile port.
type AlignmentService struct {
	db        *sql.DB
	now       func() time.Time
	executor  executorpkg.FileExecutor
	observer  executorpkg.FileObserver
	workItems *workitems.Store
}

// NewAlignmentService wires the operations owner.
func NewAlignmentService(db *sql.DB) *AlignmentService {
	return &AlignmentService{db: db, now: time.Now}
}

// SetFilePorts injects the filesystem executor/observer (composition root).
func (s *AlignmentService) SetFilePorts(executor executorpkg.FileExecutor, observer executorpkg.FileObserver) {
	if s != nil {
		s.executor = executor
		s.observer = observer
	}
}

// SetWorkItems wires the claim store used to checkpoint attempt versions atomically.
func (s *AlignmentService) SetWorkItems(store *workitems.Store) {
	if s != nil {
		s.workItems = store
	}
}

// CreateOperation persists one frozen operation (idempotent by key).
func (s *AlignmentService) CreateOperation(ctx context.Context, op AlignmentOperation) (*AlignmentOperation, error) {
	if op.State == "" {
		op.State = AlignmentPlanned
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = s.now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO alignment_operations
(id, idempotency_key, managed_anime_id, scan_generation, observation_ref, candidate_ref,
 policy_version, metadata_version, rules_version, naming_version,
 source_path, source_fingerprint, target_path, target_contract, dry_run_result,
 state, version, created_at, updated_at, companion_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 1, $17, $17, NULLIF($18, ''))
ON CONFLICT (idempotency_key) DO NOTHING`,
		op.ID, op.IdempotencyKey, op.ManagedAnimeID, op.ScanGeneration, op.ObservationRef, op.CandidateRef,
		op.PolicyVersion, op.MetadataVersion, op.RulesVersion, op.NamingVersion,
		op.SourcePath, op.SourceFingerprint, op.TargetPath, op.TargetContract, op.DryRunResult,
		string(op.State), op.CreatedAt, op.CompanionID)
	if err != nil {
		return nil, fmt.Errorf("create alignment operation: %w", err)
	}
	loaded, err := s.load(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

// CountPlanned returns how many planned operations exist for the anime (for
// the admission cap check).
func (s *AlignmentService) CountPlanned(ctx context.Context, animeID string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM alignment_operations
WHERE managed_anime_id = $1 AND state IN ('planned', 'ready', 'running')`, animeID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// MarkReady moves a planned operation to ready (the execute work item is
// created by the caller with the returned version).
func (s *AlignmentService) MarkReady(ctx context.Context, operationID string, expectedVersion int64) (int64, error) {
	return s.transition(ctx, operationID, expectedVersion, AlignmentReady, "")
}

// BlockPlanned records a terminal planning conflict without admitting a claim.
func (s *AlignmentService) BlockPlanned(ctx context.Context, operationID, code, reason string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE alignment_operations
SET state = 'blocked', failure_code = $2, reason = $3,
    completed_at = CURRENT_TIMESTAMP, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'planned'`, operationID, code, reason)
	return err
}

// PersistAttempt writes the durable effect-preceded checkpoint.
func (s *AlignmentService) PersistAttempt(ctx context.Context, operationID string, expectedVersion int64, attempt json.RawMessage) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE alignment_operations
SET attempt = $2, state = 'running', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $3 AND state IN ('ready', 'running')`,
		operationID, attempt, expectedVersion)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, fmt.Errorf("alignment operation %s changed concurrently (version %d)", operationID, expectedVersion)
	}
	return expectedVersion + 1, nil
}

// Settle completes or fails the operation with CAS.
func (s *AlignmentService) Settle(ctx context.Context, operationID string, expectedVersion int64, state AlignmentState, code, reason string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE alignment_operations
SET state = $2, failure_code = NULLIF($3, ''), reason = NULLIF($4, ''),
    completed_at = CASE WHEN $2 IN ('completed', 'failed', 'blocked', 'canceled') THEN CURRENT_TIMESTAMP ELSE NULL END,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $5`,
		operationID, string(state), code, reason, expectedVersion)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("alignment operation %s changed concurrently on settle (version %d)", operationID, expectedVersion)
	}
	return nil
}

func (s *AlignmentService) transition(ctx context.Context, operationID string, expectedVersion int64, to AlignmentState, reason string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE alignment_operations
SET state = $2, reason = NULLIF($3, ''), version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $4`,
		operationID, string(to), reason, expectedVersion)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, fmt.Errorf("alignment operation %s changed concurrently (version %d)", operationID, expectedVersion)
	}
	return expectedVersion + 1, nil
}

func (s *AlignmentService) load(ctx context.Context, operationID string) (*AlignmentOperation, error) {
	var (
		op       AlignmentOperation
		attempt  []byte
		recovery []byte
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, idempotency_key, managed_anime_id, scan_generation, observation_ref, candidate_ref,
       policy_version, metadata_version, rules_version, naming_version,
       source_path, source_fingerprint, target_path, target_contract, dry_run_result,
       state, attempt, COALESCE(failure_code, ''), COALESCE(reason, ''), version, created_at, COALESCE(companion_id, ''), overwrite_recovery
FROM alignment_operations WHERE id = $1`, operationID).
		Scan(&op.ID, &op.IdempotencyKey, &op.ManagedAnimeID, &op.ScanGeneration, &op.ObservationRef, &op.CandidateRef,
			&op.PolicyVersion, &op.MetadataVersion, &op.RulesVersion, &op.NamingVersion,
			&op.SourcePath, &op.SourceFingerprint, &op.TargetPath, &op.TargetContract, &op.DryRunResult,
			&op.State, &attempt, &op.FailureCode, &op.Reason, &op.Version, &op.CreatedAt, &op.CompanionID, &recovery)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if attempt != nil {
		op.Attempt = attempt
	}
	if recovery != nil {
		op.OverwriteRecovery = recovery
	}
	return &op, nil
}

// ReconcileUnknownEffect is the P2.1 typed reconciler: it reads only this
// owner's frozen attempt and the disk observation, and converges through the
// AlignmentOperation transaction. Missing is never unconditionally blocked:
// an attempt with a matching target observation settles completed; a missing
// target with a frozen attempt is retryable/blocked per evidence.
func (s *AlignmentService) ReconcileUnknownEffect(ctx context.Context, req deployment.ReconcileRequest) (deployment.ReconcileResult, error) {
	if s.observer == nil {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown,
			Reason: "alignment reconcile: observer not wired"}, nil
	}
	// An interrupted overwrite exchange is finished or restored from its
	// persisted record first; the owner settles through its work item's
	// recovery, which reads the same record and the committed exchange.
	if result, done := s.resumeOverwriteExchanges(ctx); done {
		return result, nil
	}
	// Read only this owner's frozen attempts and the disk observation.
	rows, err := s.db.QueryContext(ctx, `
SELECT id, version, source_fingerprint, target_path, state
FROM alignment_operations
WHERE attempt IS NOT NULL AND state IN ('ready', 'running') AND overwrite_recovery IS NULL`)
	if err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
			Reason: safeReason(err)}, nil
	}
	defer rows.Close()
	settled := 0
	var firstIssue *deployment.ReconcileResult
	for rows.Next() {
		var (
			operationID, fingerprint, targetPath, state string
			version                                     int64
		)
		if err := rows.Scan(&operationID, &version, &fingerprint, &targetPath, &state); err != nil {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
				Reason: safeReason(err)}, nil
		}
		observed, err := executorpkg.ObserveLike(ctx, s.observer, targetPath, fingerprint)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
				Reason: safeReason(err)}, nil
		}
		if observed == "" {
			// Target absent with a frozen attempt: the effect never landed;
			// not a silent success, not an unconditional block.
			continue
		}
		if !offsetFingerprintsEqual(observed, fingerprint) {
			result := deployment.ReconcileResult{State: deployment.StateConflict, Effect: deployment.EffectPresent,
				Reason: fmt.Sprintf("alignment operation %s target fingerprint differs from frozen source", operationID)}
			firstIssue = &result
			continue
		}
		if err := s.Settle(ctx, operationID, version, AlignmentCompleted, "", ""); err != nil {
			result := deployment.ReconcileResult{State: deployment.StateConflict, Effect: deployment.EffectPresent,
				Reason: safeReason(err)}
			firstIssue = &result
			continue
		}
		settled++
	}
	if err := rows.Err(); err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
			Reason: safeReason(err)}, nil
	}
	if firstIssue != nil {
		return *firstIssue, nil
	}
	effect := deployment.EffectNone
	if settled > 0 {
		effect = deployment.EffectPresent
	}
	return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: effect,
		Reason: fmt.Sprintf("settled %d alignment operations", settled)}, nil
}

// ExecuteHandler is the alignment_execute work item handler. Execution is
// effect-preceded by a durable attempt checkpoint and settled through the
// owner transaction. The filesystem executor port is wired by composition;
// until it is, operations settle blocked (fail closed) — never a silent
// success.
// errAlignmentPlacementInactive: the checkpoint found no active placement at
// the source; nothing was written.
var errAlignmentPlacementInactive = errors.New("alignment placement is not active")

func (s *AlignmentService) ExecuteHandler() worker.Handler {
	return alignmentRecoveryHandler{service: s, handle: s.alignmentBusinessHandler()}
}
func (s *AlignmentService) alignmentBusinessHandler() worker.Handler {
	return worker.HandlerFunc(func(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
		if claim.Kind != workitems.KindAlignmentExecute {
			return worker.Outcome{}, fmt.Errorf("alignment handler received kind %q", claim.Kind)
		}
		op, err := s.load(ctx, claim.Owner.ID)
		if err != nil {
			return worker.Outcome{}, err
		}
		if op == nil || (op.State != AlignmentReady && op.State != AlignmentRunning) {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "alignment_owner_invalid", Reason: "operation is not ready"}, nil), nil
		}
		if op.Version != claim.ExpectedOwnerVersion {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "alignment_fencing_stale", Reason: "stale fencing token cannot commit alignment progress"}, nil), nil
		}
		// The frozen contract carries the unit's collision mode: overwrite
		// replaces an unmanaged occupant; block and version (whose suffix
		// was chosen at admission) never touch an occupied target.
		collision := frozenAlignmentCollision(op)
		if op.CompanionID != "" && op.State == AlignmentReady {
			// A companion moves only after its video reached the target
			// and never onto a managed file; collisions follow the preset mode.
			outcome, policy, err := s.companionPrecheck(ctx, op)
			if err != nil {
				return worker.Outcome{}, err
			}
			if outcome != nil {
				return *outcome, nil
			}
			collision = policy
		} else if op.CompanionID == "" && op.State == AlignmentReady && s.observer != nil {
			// A video and its companions are one unit: nothing moves while
			// any target in the unit holds a file the mode does not allow
			// replacing.
			sourceFingerprint, err := offsetSourceFingerprintValue(op.SourceFingerprint)
			if err == nil {
				outcome, err := s.videoUnitPrecheck(ctx, op, sourceFingerprint, collision)
				if err != nil {
					return worker.Outcome{}, err
				}
				if outcome != nil {
					return *outcome, nil
				}
			}
		}
		attempt, _ := json.Marshal(map[string]any{
			"operation_id": op.ID, "source_path": op.SourcePath, "source_fingerprint": op.SourceFingerprint,
			"target_path": op.TargetPath, "started_at": s.now().UTC(),
		})
		nextVersion := op.Version + 1
		resumed := false
		if len(op.Attempt) > 0 {
			o, resume, err := (alignmentRecoveryHandler{service: s}).resolveAttempt(ctx, claim, op)
			if err != nil {
				return worker.Outcome{}, err
			}
			if !resume {
				if o.Disposition == workitems.DispositionAmbiguous {
					return worker.Outcome{}, workitems.ErrPublicationUnresolved
				}
				return o, nil
			}
			// The checkpointed attempt never reached its effect: run the same
			// invocation (same attempt, same owner version) instead of parking.
			attempt, nextVersion, resumed = op.Attempt, op.Version, true
		}
		switch {
		case resumed:
		case s.workItems != nil:
			claim, err = s.workItems.MutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
				tx := scope.SQLTx()
				if tx == nil {
					return 0, errors.New("alignment checkpoint transaction is nil")
				}
				if op.CompanionID == "" {
					// A placement released or displaced after admission
					// is no longer managed: its file is never moved.
					var released bool
					if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM placements WHERE managed_anime_id=$1 AND physical_path=$2 AND state IN ('released','displaced'))
 AND NOT EXISTS(SELECT 1 FROM placements WHERE managed_anime_id=$1 AND physical_path=$2 AND state='active')`, op.ManagedAnimeID, op.SourcePath).Scan(&released); err != nil {
						return 0, err
					}
					if released {
						return 0, errAlignmentPlacementInactive
					}
				}
				result, err := tx.ExecContext(ctx, `UPDATE alignment_operations SET attempt=$2,state='running',version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$3 AND state IN ('ready','running')`, op.ID, attempt, op.Version)
				if err != nil {
					return 0, err
				}
				affected, err := result.RowsAffected()
				if err != nil {
					return 0, err
				}
				if affected != 1 {
					return 0, fmt.Errorf("alignment operation %s changed concurrently", op.ID)
				}
				return nextVersion, nil
			})
		default:
			nextVersion, err = s.PersistAttempt(ctx, op.ID, op.Version, attempt)
		}
		if errors.Is(err, errAlignmentPlacementInactive) {
			writer := alignmentSettleWriter(op, op.Version, AlignmentBlocked, "placement_not_active", "the placement is no longer managed", "")
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "placement_not_active", Reason: "the placement is no longer managed"}, writer), nil
		}
		if err != nil {
			return worker.Outcome{}, err
		}
		sourceFingerprint, err := offsetSourceFingerprintValue(op.SourceFingerprint)
		if err != nil {
			writer := alignmentSettleWriter(op, nextVersion, AlignmentFailed, "alignment_fingerprint_invalid", safeReason(err), "")
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "alignment_fingerprint_invalid", Reason: safeReason(err)}, writer), nil
		}

		if s.executor == nil || s.observer == nil {
			writer := alignmentSettleWriter(op, nextVersion, AlignmentBlocked, "alignment_executor_unavailable", "alignment filesystem executor is not wired", "")
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "alignment_executor_unavailable", Reason: "alignment filesystem executor is not wired"}, writer), nil
		}
		// with the frozen fingerprint, the effect already landed (crash between
		// target effect and owner commit, or a manual move to the correct
		// target). Commit the authoritative Placement current path and settle
		// without a second file effect.
		if op.State == AlignmentRunning {
			if observed, err := executorpkg.ObserveLike(ctx, s.observer, op.TargetPath, sourceFingerprint); err == nil && observed != "" && offsetFingerprintsEqual(observed, sourceFingerprint) {
				writer := alignmentSettleWriter(op, nextVersion, AlignmentCompleted, "recovered_aligned", "target already verified after restart", op.TargetPath)
				return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "recovered_aligned", Reason: "target verified after restart"}, writer), nil
			}
			if observed, err := executorpkg.ObserveLike(ctx, s.observer, op.SourcePath, sourceFingerprint); err != nil || !offsetFingerprintsEqual(observed, sourceFingerprint) {
				writer := alignmentSettleWriter(op, nextVersion, AlignmentBlocked, "target_conflict", "neither frozen source nor target matches after restart", "")
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "target_conflict", Reason: "neither frozen source nor target matches after restart"}, writer), nil
			}
		} else if observed, err := executorpkg.ObserveLike(ctx, s.observer, op.TargetPath, sourceFingerprint); err == nil && observed != "" && offsetFingerprintsEqual(observed, sourceFingerprint) {
			writer := alignmentSettleWriter(op, nextVersion, AlignmentCompleted, "recovered_aligned", "target already verified", op.TargetPath)
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "recovered_aligned", Reason: "target already verified"}, writer), nil
		}
		// A naming change can rename the anime or season folder, so the target
		// folder may not exist yet. Create it the way publication does; a
		// created directory stays even when the move then fails.
		if ensurer, ok := s.executor.(parentEnsurer); ok {
			if err := ensurer.EnsurePublicationParent("", op.TargetPath); err != nil {
				writer := alignmentSettleWriter(op, nextVersion, AlignmentFailed, "alignment_execute_failed", safeReason(err), "")
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "alignment_execute_failed", Reason: safeReason(err)}, writer), nil
			}
		}
		// The executor verifies source fingerprint, performs the move, and the
		// observer re-reads the target before the owner settles.
		operation := domain.FileOperation{
			ID:                op.ID,
			Operation:         domain.OperationMove,
			SourcePath:        op.SourcePath,
			TargetPath:        op.TargetPath,
			SourceFingerprint: sourceFingerprint,
			CollisionPolicy:   collision,
		}
		var executed domain.FileOperation
		if collision == domain.CollisionOverwrite {
			overwriteExecutor, ok := s.executor.(executorpkg.MaintenanceOverwriteExecutor)
			if !ok {
				writer := alignmentSettleWriter(op, nextVersion, AlignmentBlocked, "alignment_executor_unavailable", "alignment overwrite executor is not wired", "")
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "alignment_executor_unavailable", Reason: "alignment overwrite executor is not wired"}, writer), nil
			}
			persisted := false
			checkpoint := s.overwriteCheckpoint(op.ID, &persisted)
			executed, err = overwriteExecutor.ExecuteMaintenanceOverwrite(ctx, operation, maintenanceInvocation(claim, attempt, operation.SourcePath, operation.TargetPath, operation.SourceFingerprint), checkpoint)
			if err != nil && !alignmentErrorCode(err, "TARGET_COLLISION_RACE") && !alignmentErrorCode(err, domain.FileErrorCodeTargetOccupied) && persisted {
				// An exchange record is durable: recovery finishes or
				// restores it from that record, never a guess here.
				return worker.Outcome{}, workitems.ErrPublicationUnresolved
			}
			if err != nil && alignmentErrorCode(err, "TARGET_COLLISION_RACE") {
				writer := alignmentSettleWriter(op, nextVersion, AlignmentBlocked, "target_conflict", safeReason(err), "")
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "target_conflict", Reason: safeReason(err)}, writer), nil
			}
		} else if receiptExecutor, ok := s.executor.(executorpkg.MaintenanceFileExecutor); ok {
			executed, err = receiptExecutor.ExecuteMaintenance(ctx, operation, maintenanceInvocation(claim, attempt, operation.SourcePath, operation.TargetPath, operation.SourceFingerprint))
		} else {
			executed, err = s.executor.Execute(ctx, operation)
		}
		if err != nil {
			var operationError executorpkg.ErrorCoder
			if errors.As(err, &operationError) && operationError.ErrorCode() == domain.FileErrorCodeTargetOccupied {
				writer := alignmentSettleWriter(op, nextVersion, AlignmentBlocked, "target_occupied", safeReason(err), "")
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "target_occupied", Reason: safeReason(err)}, writer), nil
			}
			writer := alignmentSettleWriter(op, nextVersion, AlignmentFailed, "alignment_execute_failed", safeReason(err), "")
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "alignment_execute_failed", Reason: safeReason(err)}, writer), nil
		}
		observed, err := executorpkg.ObserveLike(ctx, s.observer, executed.TargetPath, sourceFingerprint)
		if err != nil || observed == "" || !offsetFingerprintsEqual(observed, sourceFingerprint) {
			reason := "target fingerprint mismatch after alignment effect"
			if err != nil {
				reason = fmt.Sprintf("target reread failed: %v", err)
			}
			writer := alignmentSettleWriter(op, nextVersion, AlignmentFailed, "alignment_target_mismatch", reason, "")
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "alignment_target_mismatch", Reason: reason}, writer), nil
		}
		writer := alignmentSettleWriter(op, nextVersion, AlignmentCompleted, "", "", executed.TargetPath)
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "alignment_completed", Reason: "aligned file verified"}, writer), nil
	})
}

// commitPlacementCurrentPath updates the authoritative Placement current path.
func (s *AlignmentService) commitPlacementCurrentPath(ctx context.Context, op *AlignmentOperation, targetPath string) error {
	if s == nil || s.db == nil || op == nil {
		return errors.New("alignment placement commit is not wired")
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE placements
SET physical_path = $1, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $2 AND physical_path = $3
  AND verification_state = 'verified' AND state = 'active'`,
		targetPath, op.ManagedAnimeID, op.SourcePath); err != nil {
		return fmt.Errorf("commit alignment placement current path: %w", err)
	}
	return nil
}

func alignmentSettleWriter(op *AlignmentOperation, expectedVersion int64, state AlignmentState, code, reason, target string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("alignment finish owner transaction is nil")
		}
		if op.CompanionID != "" {
			// A companion alignment commits the companion's own current path
			// (completed) or its typed failure; the video placement is never
			// touched, so a failed companion never rolls back the video.
			var err error
			if target != "" {
				// A companion-only version suffix becomes part of the
				// companion's suffix, so its name stays bound to the video
				// and the next scan finds it aligned.
				var contract companionAlignmentContract
				_ = json.Unmarshal(op.TargetContract, &contract)
				// Another relation at the target was replaced under the
				// overwrite setting (or held the same file): it ends, and its
				// file now belongs to this companion.
				if _, err = tx.ExecContext(ctx, `UPDATE placement_companions SET state='ended',ended_reason='path_taken_by_alignment',version=version+1,updated_at=CURRENT_TIMESTAMP WHERE current_path=$1 AND state='active' AND id<>$2`, target, op.CompanionID); err != nil {
					return 0, err
				}
				// A versioned suffix remembers the suffix it came from; a
				// move that re-derives the name from the video restores it.
				_, err = tx.ExecContext(ctx, `UPDATE placement_companions SET current_path=$2,versioned_from_suffix=CASE WHEN $3::text IS NULL THEN NULL WHEN versioned_from_suffix IS NOT NULL THEN versioned_from_suffix ELSE suffix END,suffix=COALESCE($3,COALESCE(versioned_from_suffix,suffix)),alignment_failure_code=NULL,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND state='active'`, op.CompanionID, target, contract.Suffix)
			} else if state == AlignmentCompleted {
				_, err = tx.ExecContext(ctx, `UPDATE placement_companions SET alignment_failure_code=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, op.CompanionID)
			} else if code != "" {
				_, err = tx.ExecContext(ctx, `UPDATE placement_companions SET alignment_failure_code=$2,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, op.CompanionID, code)
			}
			if err != nil {
				return 0, err
			}
		} else if target != "" {
			rows, err := tx.QueryContext(ctx, `UPDATE placements SET physical_path=$1,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE managed_anime_id=$2 AND physical_path=$3 AND verification_state='verified' AND state='active' RETURNING id, source_evidence_type='file_operation'`, target, op.ManagedAnimeID, op.SourcePath)
			if err != nil {
				return 0, err
			}
			var moved []string
			successor := ""
			for rows.Next() {
				var id string
				var owner bool
				if err := rows.Scan(&id, &owner); err != nil {
					rows.Close()
					return 0, err
				}
				moved = append(moved, id)
				if successor == "" || owner {
					successor = id
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return 0, err
			}
			// One physical path can carry several verified active rows: the
			// owner placement plus the reconciliation index projections of the
			// same file. All of them follow the committed target; only "no row
			// owns this path any more" is the concurrent-change signal.
			if len(moved) == 0 {
				return 0, fmt.Errorf("alignment placement %s changed concurrently", op.ID)
			}
			// A placement still recorded at the target lost its file to this
			// move (replaced under the overwrite setting, or already gone): it
			// is displaced in this commit with the moved placement as its
			// successor.
			if _, err := tx.ExecContext(ctx, `SELECT public.placement_displace($1,$2,true,$3)`, successor, "alignment_operation:"+op.ID, moved); err != nil {
				return 0, err
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE alignment_operations SET state=$2,failure_code=NULLIF($3,''),reason=NULLIF($4,''),completed_at=CASE WHEN $2 IN ('completed','failed','blocked','canceled') THEN CURRENT_TIMESTAMP ELSE NULL END,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$5`, op.ID, string(state), code, reason, expectedVersion)
		if err != nil {
			return 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if affected != 1 {
			return 0, fmt.Errorf("alignment operation %s changed concurrently", op.ID)
		}
		return expectedVersion + 1, nil
	}
}

func safeReason(err error) string {
	if err == nil {
		return ""
	}
	safe, _ := redact.Apply(err.Error(), 2048, redact.Mask, redact.Redacted)
	return safe
}

// ListAlignments returns the alignment operations of one anime (side-effect
// free, newest first) for the per-instance owner truth surface.
func (s *AlignmentService) ListAlignmentsApplication(ctx context.Context, animeID string, limit, offset int) ([]AlignmentOperationView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, managed_anime_id, scan_generation, observation_ref, policy_version,
       source_path, source_fingerprint, target_path,
       state, COALESCE(failure_code, ''), COALESCE(reason, ''), version, created_at, completed_at, updated_at
FROM alignment_operations
WHERE managed_anime_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3`, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := make([]AlignmentOperationView, 0, limit)
	for rows.Next() {
		var (
			view        AlignmentOperationView
			state       string
			completedAt sql.NullTime
			failureCode string
			reason      string
		)
		if err := rows.Scan(&view.ID, &view.ManagedAnimeID, &view.ScanGeneration, &view.ObservationRef, &view.PolicyVersion,
			&view.SourcePath, &view.SourceFingerprint, &view.TargetPath,
			&state, &failureCode, &reason, &view.Version, &view.CreatedAt, &completedAt, &view.UpdatedAt); err != nil {
			return nil, err
		}
		view.State = state
		if completedAt.Valid {
			view.CompletedAt = &completedAt.Time
		}
		if failureCode != "" {
			view.FailureCode = &failureCode
		}
		if reason != "" {
			view.Reason = &reason
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

// resumeOverwriteExchanges runs every interrupted overwrite exchange to its
// end (finished or restored) from the persisted record. done=true reports an
// issue that stops this reconcile pass.
func (s *AlignmentService) resumeOverwriteExchanges(ctx context.Context) (deployment.ReconcileResult, bool) {
	resumer, ok := s.executor.(executorpkg.MaintenanceOverwriteExecutor)
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM alignment_operations WHERE state = 'running' AND overwrite_recovery IS NOT NULL`)
	if err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: safeReason(err)}, true
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: safeReason(err)}, true
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) > 0 && !ok {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, Reason: "alignment reconcile: overwrite executor not wired"}, true
	}
	for _, id := range ids {
		op, err := s.load(ctx, id)
		if err != nil || op == nil {
			continue
		}
		operation, ok := resumeOverwriteOperation(op)
		if !ok {
			continue
		}
		if _, err := resumer.ResumeOverwrite(ctx, operation, s.overwriteCheckpoint(op.ID, nil)); err != nil && !alignmentErrorCode(err, "TARGET_COLLISION_RACE") {
			return deployment.ReconcileResult{State: deployment.StateConflict, Effect: deployment.EffectUnknown,
				Reason: fmt.Sprintf("alignment operation %s overwrite exchange is unresolved: %s", op.ID, safeReason(err))}, true
		}
	}
	return deployment.ReconcileResult{}, false
}
