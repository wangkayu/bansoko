package filemaintenance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/deployment"
	"bansoko/internal/fileidentity"
	persistence "bansoko/internal/filemaintenance/adapter/persistence"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// CleanupState is the cleanup lifecycle state machine.
type CleanupState string

const (
	CleanupAdmitted     CleanupState = "admitted"
	CleanupQuarantined  CleanupState = "quarantined"
	CleanupGraceWaiting CleanupState = "grace_waiting"
	CleanupPurged       CleanupState = "purged"
	CleanupBlocked      CleanupState = "blocked"
	CleanupFailed       CleanupState = "failed"
	CleanupCompleted    CleanupState = "completed"
)

// CleanupInput is the frozen admission input.
type CleanupInput struct {
	ID                string
	IdempotencyKey    string
	PlacementID       string
	ManagedAnimeID    string
	Coverage          []string
	ReplacementRef    string
	PolicyVersion     int64
	RetentionMode     string
	SourcePath        string
	SourceFingerprint string
	QuarantinePath    string
	Effect            string // quarantine is the only supported admission effect; purge is an internal second stage
	GraceDeadline     *time.Time
	CorrelationID     string
	CausationType     string
	CausationID       string
}

// CleanupService owns the cleanup lifecycle. Every filesystem effect is
// preceded by a durable attempt checkpoint; crash recovery converges from
// the frozen attempt and disk observation.
type CleanupService struct {
	db         *sql.DB
	repository *persistence.Repository
	now        func() time.Time
	executor   executorpkg.CleanupFileExecutor
	workItems  *workitems.Store
	activity   observabilityport.ActivityWriter
}

// NewCleanupService wires the cleanup owner.
func NewCleanupService(db *sql.DB) *CleanupService {
	return &CleanupService{db: db, repository: persistence.NewRepository(db), now: time.Now}
}

// SetActivityWriter wires the central transaction-local business history
// writer. Unset (nil) keeps cleanup behavior identical without Activity.
func (s *CleanupService) SetActivityWriter(writer observabilityport.ActivityWriter) {
	if s != nil {
		s.activity = writer
	}
}

// SetFileExecutor wires the deployment-owned filesystem executor. Without it
// the quarantine/purge handlers fail closed (blocked), never silently
// succeeding.
func (s *CleanupService) SetFileExecutor(executor executorpkg.CleanupFileExecutor) {
	if s != nil {
		s.executor = executor
	}
}

// SetWorkItems wires the WorkItem store used to schedule quarantine/purge
// items. Without it no cleanup WorkItem is created (handlers stay dormant).
func (s *CleanupService) SetWorkItems(store *workitems.Store) {
	if s != nil {
		s.workItems = store
	}
}

// enqueue schedules one cleanup work item (quarantine or purge) for the
// operation, idempotently. It validates the current owner version in the same
// transaction so a stale caller can never refresh a WorkItem with old evidence.
func (s *CleanupService) enqueue(ctx context.Context, opID string, version int64, kind string) error {
	if s.workItems == nil {
		return errors.New("cleanup work items store is not wired")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var currentVersion int64
	if currentVersion, err = s.repository.LockVersionTx(ctx, txscope.Wrap(tx), opID); err != nil {
		return fmt.Errorf("lock cleanup owner for enqueue: %w", err)
	}
	if currentVersion != version {
		return &apperrors.VersionConflict{Owner: "cleanup " + opID, CurrentVersion: currentVersion}
	}
	snapshot, err := json.Marshal(struct {
		CleanupID string `json:"cleanup_id"`
		Stage     string `json:"stage"`
	}{CleanupID: opID, Stage: kind})
	if err != nil {
		return fmt.Errorf("marshal cleanup work item snapshot: %w", err)
	}
	if _, _, err := s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner: workitems.OwnerRef{Type: workitems.OwnerCleanup, ID: opID},
		Kind:  kind,
		// One stable effect key exists per operation and stage.
		IdempotencyKey:       "cln:" + opID + ":" + kind,
		InputSnapshot:        snapshot,
		AvailableAfter:       0,
		MaxAttempts:          3,
		ExpectedOwnerVersion: version,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// Admit persists a cleanup operation idempotently (fail-closed admission is
// the caller's placement/replacement evidence check; this service freezes).
func (s *CleanupService) Admit(ctx context.Context, input CleanupInput) (*CleanupOperation, error) {
	if input.Effect != "quarantine" {
		return nil, &apperrors.Business{Status: 422, Code: "CLEANUP_EFFECT_UNSUPPORTED", Message: "cleanup admission only accepts quarantine effect"}
	}
	coverage, _ := json.Marshal(SortCoverage(input.Coverage))
	err := s.repository.Insert(ctx, persistence.OperationInput{
		ID:                input.ID,
		IdempotencyKey:    input.IdempotencyKey,
		PlacementID:       input.PlacementID,
		ManagedAnimeID:    input.ManagedAnimeID,
		Coverage:          coverage,
		ReplacementRef:    input.ReplacementRef,
		PolicyVersion:     input.PolicyVersion,
		RetentionMode:     input.RetentionMode,
		SourcePath:        input.SourcePath,
		SourceFingerprint: input.SourceFingerprint,
		QuarantinePath:    input.QuarantinePath,
		Effect:            input.Effect,
		GraceDeadline:     input.GraceDeadline,
		CorrelationID:     input.CorrelationID,
		CausationType:     input.CausationType,
		CausationID:       input.CausationID,
	})
	if err != nil {
		return nil, err
	}
	if s.workItems != nil {
		op, err := s.load(ctx, input.ID)
		if err != nil {
			return nil, err
		}
		if op == nil {
			return nil, errors.New("cleanup operation not found after admission")
		}
		if err := s.enqueue(ctx, input.ID, op.Version, workitems.KindCleanupQuarantine); err != nil {
			return nil, fmt.Errorf("enqueue cleanup quarantine: %w", err)
		}
	}
	return s.load(ctx, input.ID)
}

// persistAttemptTx writes the durable pre-effect checkpoint in the caller's
// transaction. Handler callers must invoke it through MutateClaim.
func (s *CleanupService) persistAttemptTx(ctx context.Context, tx *sql.Tx, cleanupID string, expectedVersion int64, attempt json.RawMessage) (int64, error) {
	return s.repository.PersistAttemptTx(ctx, txscope.Wrap(tx), cleanupID, expectedVersion, attempt)
}

// transitionTx moves the cleanup state machine in the caller's transaction.
// Handler callers must invoke it through MutateClaim.
func (s *CleanupService) transitionTx(ctx context.Context, tx *sql.Tx, cleanupID string, expectedVersion int64, to CleanupState, code, reason string) (int64, error) {
	return s.repository.TransitionTx(ctx, txscope.Wrap(tx), cleanupID, expectedVersion, string(to), code, reason)
}

func (s *CleanupService) quarantineTx(ctx context.Context, tx *sql.Tx, cleanupID string, expectedVersion int64, quarantinePath, code, reason string) (int64, error) {
	return s.repository.QuarantineTx(ctx, txscope.Wrap(tx), cleanupID, expectedVersion, quarantinePath, code, reason)
}

func (s *CleanupService) recordPurgePostObservationTx(ctx context.Context, tx *sql.Tx, cleanupID string, expectedVersion int64) (int64, error) {
	return s.repository.RecordPurgePostObservationTx(ctx, txscope.Wrap(tx), cleanupID, expectedVersion)
}

func (s *CleanupService) PersistAttempt(ctx context.Context, cleanupID string, expectedVersion int64, attempt json.RawMessage) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	version, err := s.persistAttemptTx(ctx, tx, cleanupID, expectedVersion, attempt)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return version, nil
}

// Transition is retained for non-WorkItem callers and tests. Effect handlers
// must use transitionTx through MutateClaim.
func (s *CleanupService) Transition(ctx context.Context, cleanupID string, expectedVersion int64, to CleanupState, code, reason string) (int64, error) {
	return s.transition(ctx, cleanupID, expectedVersion, to, code, reason)
}

// transition is used by the non-WorkItem Restore API. WorkItem handlers must
// use transitionTx through MutateClaim so the owner and claim are fenced.
func (s *CleanupService) transition(ctx context.Context, cleanupID string, expectedVersion int64, to CleanupState, code, reason string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	version, err := s.transitionTx(ctx, tx, cleanupID, expectedVersion, to, code, reason)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *CleanupService) mutateClaim(ctx context.Context, claim workitems.Claim, owner workitems.OwnerWriter) (workitems.Claim, error) {
	if s.workItems == nil {
		return workitems.Claim{}, errors.New("cleanup work items store is not wired")
	}
	return s.workItems.MutateClaim(ctx, claim, owner)
}

// verifyClaimOwner checks the cleanup owner version inside the fenced mutation
// transaction. MutateClaim has already validated the leased state, lease and
// fencing token before this callback runs.
func (s *CleanupService) verifyClaimOwner(claim workitems.Claim) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		version, err := s.repository.LockVersionTx(ctx, scope, claim.Owner.ID)
		if err != nil {
			return 0, fmt.Errorf("lock cleanup owner: %w", err)
		}
		if version != claim.ExpectedOwnerVersion {
			return 0, fmt.Errorf("cleanup owner version conflict: expected %d, got %d", claim.ExpectedOwnerVersion, version)
		}
		return version, nil
	}
}

// syncOwnerVersion is used by the generic WorkItem terminal/retry transition
// after the handler has already committed a fenced cleanup mutation. It does
// not mutate the owner; it only returns the committed owner version so Store
// can synchronize expected_owner_version in the same fenced transaction.
func (s *CleanupService) syncOwnerVersion(claim workitems.Claim) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		version, err := s.repository.LockVersionTx(ctx, scope, claim.Owner.ID)
		if err != nil {
			return 0, fmt.Errorf("read cleanup owner version: %w", err)
		}
		return version, nil
	}
}

func (s *CleanupService) retryOwner(claim workitems.Claim) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		current, err := s.repository.LockTx(ctx, scope, claim.Owner.ID)
		if err != nil {
			return 0, fmt.Errorf("lock cleanup retry owner: %w", err)
		}
		if current.Version != claim.ExpectedOwnerVersion {
			return 0, fmt.Errorf("cleanup owner version conflict: expected %d, got %d", claim.ExpectedOwnerVersion, current.Version)
		}
		state := current.State
		if result.State == workitems.StateFailed {
			state = string(CleanupFailed)
		}
		version, err := s.repository.TransitionTx(ctx, scope, claim.Owner.ID, claim.ExpectedOwnerVersion, state, result.ReasonCode, result.Reason)
		if err != nil {
			return 0, fmt.Errorf("persist cleanup retry owner: %w", err)
		}
		return version, nil
	}
}

// Restore moves a quarantined file back to its original source path with CAS.
// The cleanup operation must be quarantined and the executor verifies the
// frozen observation before any mutation. On success the operation is marked
// completed; a failure leaves the quarantined file untouched.
func (s *CleanupService) Restore(ctx context.Context, cleanupID string, expectedVersion int64) error {
	op, err := s.load(ctx, cleanupID)
	if err != nil {
		return err
	}
	if op == nil {
		return errors.New("cleanup operation not found")
	}
	if op.State != CleanupQuarantined {
		return fmt.Errorf("cleanup %s is %s, only quarantined operations can be restored", cleanupID, op.State)
	}
	if op.Version != expectedVersion {
		return fmt.Errorf("cleanup %s changed concurrently (version %d != %d)", cleanupID, expectedVersion, op.Version)
	}
	if op.QuarantinePath == "" {
		return errors.New("cleanup operation has no quarantine path")
	}
	if s.executor == nil {
		return errors.New("cleanup filesystem executor is not wired")
	}
	expected, err := frozenObservation(op.Attempt, op.SourceFingerprint)
	if err != nil {
		if _, transitionErr := s.Transition(ctx, op.ID, op.Version, CleanupBlocked, "cleanup_restore_evidence_invalid", safeCleanupReason(err)); transitionErr != nil {
			return transitionErr
		}
		return err
	}
	if err := s.executor.RestoreFromQuarantine(ctx, op.QuarantinePath, op.SourcePath, expected); err != nil {
		if _, terr := s.Transition(ctx, op.ID, op.Version, CleanupBlocked, "cleanup_restore_failed", safeCleanupReason(err)); terr != nil {
			return terr
		}
		return err
	}
	if _, err := s.Transition(ctx, op.ID, op.Version, CleanupCompleted, "cleanup_restored", "restored to "+op.SourcePath); err != nil {
		return err
	}
	return nil
}

// ReconcileUnknownEffect is evidence-only recovery. A pending attempt is never
// considered settled merely because its row was read.
func (s *CleanupService) ReconcileUnknownEffect(ctx context.Context, req deployment.ReconcileRequest) (deployment.ReconcileResult, error) {
	if s.executor == nil {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, Reason: "cleanup reconcile: executor not wired"}, nil
	}
	pendingRows, err := s.repository.ListPending(ctx)
	if err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: safeCleanupReason(err)}, nil
	}
	pending := len(pendingRows)
	for _, row := range pendingRows {
		cleanupID, version := row.ID, row.Version
		effect, state := row.Effect, row.State
		sourcePath, sourceFP := row.SourcePath, row.SourceFingerprint
		quarantinePath := persistence.NullString(row.QuarantinePath)
		attemptRaw := row.Attempt

		if effect != "quarantine" {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: unsupported owner effect"}, nil
		}
		if attemptRaw == nil {
			if state == string(CleanupAdmitted) && s.workItems != nil {
				if enqueueErr := s.enqueue(ctx, cleanupID, version, workitems.KindCleanupQuarantine); enqueueErr != nil {
					return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine enqueue failed: " + safeCleanupReason(enqueueErr)}, nil
				}
			}
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: cleanup stage has no attempt"}, nil
		}
		evidence, err := decodeCleanupAttempt(attemptRaw)
		if err != nil {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(err)}, nil
		}
		if err := validateCleanupAttempt(evidence, cleanupID, effect, sourcePath, sourceFP, quarantinePath); err != nil {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(err)}, nil
		}
		expected, err := frozenObservation(attemptRaw, sourceFP)
		if err != nil {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(err)}, nil
		}
		if state == string(CleanupAdmitted) {
			if evidence.Stage != "quarantine" {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: admitted owner requires quarantine attempt stage"}, nil
			}
			qpath, pathErr := cleanupQuarantinePath(evidence, quarantinePath)
			if pathErr != nil {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(pathErr)}, nil
			}
			if _, observeErr := s.executor.Observe(ctx, sourcePath); observeErr == nil {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectNone, EvidenceRef: cleanupID, Reason: "cleanup reconcile: source remains; effect not completed"}, nil
			} else if !isMissingCleanupError(observeErr) {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(observeErr)}, nil
			}
			observed, observeErr := s.executor.Observe(ctx, qpath)
			if observeErr != nil {
				if isMissingCleanupError(observeErr) {
					return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine absence is not sufficient evidence for completion"}, nil
				}
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(observeErr)}, nil
			}
			if !sameFrozenCleanupObservation(observed, expected) {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine evidence conflicts with frozen source"}, nil
			}
			newVersion, convergeErr := s.convergeQuarantine(ctx, cleanupID, version, effect, state, sourcePath, sourceFP, qpath, attemptRaw, expected)
			if convergeErr != nil {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: safeCleanupReason(convergeErr)}, nil
			}
			if enqueueErr := s.enqueue(ctx, cleanupID, newVersion, workitems.KindCleanupPurge); enqueueErr != nil {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine converged but purge enqueue failed: " + safeCleanupReason(enqueueErr)}, nil
			}
			return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine owner converged"}, nil
		}
		if state != string(CleanupQuarantined) && state != string(CleanupGraceWaiting) {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: owner state is not recoverable"}, nil
		}
		if evidence.Stage != "quarantine" && evidence.Stage != "purge" {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantined owner requires quarantine or purge attempt stage"}, nil
		}
		if _, observeErr := s.executor.Observe(ctx, sourcePath); observeErr == nil {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectNone, EvidenceRef: cleanupID, Reason: "cleanup reconcile: source remains; effect is not proven"}, nil
		} else if !isMissingCleanupError(observeErr) {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(observeErr)}, nil
		}
		if !quarantinePath.Valid || quarantinePath.String == "" {
			return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine path evidence is missing"}, nil
		}
		observed, observeErr := s.executor.Observe(ctx, quarantinePath.String)
		if evidence.Stage == "purge" {
			if !evidence.Removed || !evidence.PostObservationMissing {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: purge attempt lacks complete post-effect proof"}, nil
			}
			if observeErr == nil {
				if !sameFrozenCleanupObservation(observed, expected) {
					return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine evidence conflicts with frozen source"}, nil
				}
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: "cleanup reconcile: purge attempt exists but quarantine is still present"}, nil
			}
			if !isMissingCleanupError(observeErr) {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(observeErr)}, nil
			}
			if !evidence.Removed || !evidence.PostObservationMissing {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: purge absence lacks complete post-effect proof"}, nil
			}
			newVersion, convergeErr := s.convergePurge(ctx, cleanupID, version, attemptRaw)
			if convergeErr != nil {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(convergeErr)}, nil
			}
			return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: deployment.EffectNone, EvidenceRef: cleanupID, Reason: fmt.Sprintf("cleanup reconcile: purge owner converged at version %d", newVersion)}, nil
		}
		if observeErr == nil {
			if !sameFrozenCleanupObservation(observed, expected) {
				return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine evidence conflicts with frozen source"}, nil
			}
			if enqueueErr := s.enqueue(ctx, cleanupID, version, workitems.KindCleanupPurge); enqueueErr != nil {
				return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine is committed but purge enqueue failed: " + safeCleanupReason(enqueueErr)}, nil
			}
			return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: deployment.EffectPresent, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine already committed"}, nil
		}
		if !isMissingCleanupError(observeErr) {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: safeCleanupReason(observeErr)}, nil
		}
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, EvidenceRef: cleanupID, Reason: "cleanup reconcile: quarantine absence is not sufficient evidence for completion"}, nil
	}
	if pending > 0 {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown, Reason: "cleanup reconcile: pending attempts lack completion evidence"}, nil
	}
	return deployment.ReconcileResult{State: deployment.StateCompleted, Effect: deployment.EffectNone, Reason: "cleanup reconcile: no pending attempts"}, nil
}

func (s *CleanupService) convergeQuarantine(ctx context.Context, cleanupID string, expectedVersion int64, effect, state, sourcePath, sourceFP, quarantinePath string, attemptRaw []byte, expected domain.FileObservation) (int64, error) {
	if effect != "quarantine" || state != string(CleanupAdmitted) {
		return 0, errors.New("cleanup quarantine convergence requires admitted quarantine owner")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin cleanup quarantine convergence: %w", err)
	}
	defer tx.Rollback()

	// WorkItem handlers always acquire WorkItem -> owner. Recovery must use the
	// same order so a handler and recovery cannot wait on each other's locks.
	workItemID, workItemOwnerType, workItemOwnerID, workItemKind, workItemState, workItemExpectedVersion, err := s.workItems.LockCleanupWorkItemTx(ctx, txscope.Wrap(tx), cleanupID, workitems.KindCleanupQuarantine)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("cleanup quarantine live work item is missing")
		}
		return 0, fmt.Errorf("lock cleanup quarantine work item: %w", err)
	}
	if workItemOwnerType != workitems.OwnerCleanup || workItemOwnerID != cleanupID || workItemKind != workitems.KindCleanupQuarantine {
		return 0, errors.New("cleanup quarantine work item owner or kind changed")
	}
	if workItemState != workitems.StateAvailable && workItemState != workitems.StateLeased {
		return 0, errors.New("cleanup quarantine work item is not live")
	}
	if workItemExpectedVersion < 1 || workItemExpectedVersion > expectedVersion {
		return 0, fmt.Errorf("cleanup quarantine work item owner version %d is outside expected owner version %d", workItemExpectedVersion, expectedVersion)
	}

	var currentEffect, currentState, currentSourcePath, currentSourceFP string
	var currentOwnerID string
	var currentQuarantine sql.NullString
	var currentAttempt []byte
	var currentVersion int64
	if err := tx.QueryRowContext(ctx, `
	SELECT id, effect, state, source_path, source_fingerprint, quarantine_path, attempt, version
	FROM cleanup_operations
	WHERE id = $1
	FOR UPDATE`, cleanupID).
		Scan(&currentOwnerID, &currentEffect, &currentState, &currentSourcePath, &currentSourceFP, &currentQuarantine, &currentAttempt, &currentVersion); err != nil {
		return 0, fmt.Errorf("lock cleanup quarantine owner: %w", err)
	}
	if currentOwnerID != cleanupID || currentVersion != expectedVersion || currentEffect != effect || currentState != state || currentSourcePath != sourcePath || currentSourceFP != sourceFP {
		return 0, errors.New("cleanup quarantine owner changed during evidence reconciliation")
	}
	if !bytes.Equal(currentAttempt, attemptRaw) {
		return 0, errors.New("cleanup quarantine attempt changed during evidence reconciliation")
	}
	currentEvidence, err := decodeCleanupAttempt(currentAttempt)
	if err != nil {
		return 0, fmt.Errorf("recheck cleanup quarantine attempt: %w", err)
	}
	if err := validateCleanupAttempt(currentEvidence, cleanupID, currentEffect, currentSourcePath, currentSourceFP, currentQuarantine); err != nil {
		return 0, fmt.Errorf("recheck cleanup quarantine evidence: %w", err)
	}
	currentFrozen, err := frozenSourceObservation(currentAttempt, currentSourceFP)
	if err != nil {
		return 0, fmt.Errorf("recheck cleanup quarantine frozen source: %w", err)
	}
	if !sameFrozenCleanupObservation(currentFrozen, expected) {
		return 0, errors.New("cleanup quarantine frozen source evidence changed")
	}
	qpath, err := cleanupQuarantinePath(currentEvidence, currentQuarantine)
	if err != nil || qpath != quarantinePath {
		if err == nil {
			err = errors.New("cleanup quarantine path changed during evidence reconciliation")
		}
		return 0, err
	}

	var newVersion int64
	if err := tx.QueryRowContext(ctx, `
	UPDATE cleanup_operations
	SET state = CASE
			WHEN grace_deadline IS NOT NULL AND grace_deadline > CURRENT_TIMESTAMP THEN 'grace_waiting'
			ELSE 'quarantined'
		END, quarantine_path = $2,
	    failure_code = 'cleanup_quarantined', reason = $3,
	    version = version + 1, updated_at = CURRENT_TIMESTAMP
	WHERE id = $1 AND version = $4 AND state = 'admitted' AND effect = 'quarantine'
	RETURNING version`, cleanupID, quarantinePath, "quarantined at "+quarantinePath, currentVersion).Scan(&newVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("cleanup quarantine owner CAS failed")
		}
		return 0, fmt.Errorf("converge cleanup quarantine owner: %w", err)
	}
	affected, err := s.workItems.CompleteCleanupWorkItemTx(ctx, txscope.Wrap(tx), workItemID, cleanupID, workitems.KindCleanupQuarantine, newVersion, currentVersion)
	if err != nil {
		return 0, fmt.Errorf("complete cleanup quarantine work item: %w", err)
	}
	if affected != 1 {
		return 0, errors.New("cleanup quarantine work item CAS failed")
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit cleanup quarantine convergence: %w", err)
	}
	return newVersion, nil
}

func (s *CleanupService) convergePurge(ctx context.Context, cleanupID string, expectedVersion int64, attemptRaw []byte) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin cleanup purge convergence: %w", err)
	}
	defer tx.Rollback()
	workItemID, ownerType, ownerID, kind, workState, workExpectedVersion, err := s.workItems.LockCleanupWorkItemTx(ctx, txscope.Wrap(tx), cleanupID, workitems.KindCleanupPurge)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("cleanup purge live work item is missing")
		}
		return 0, fmt.Errorf("lock cleanup purge work item: %w", err)
	}
	if ownerType != workitems.OwnerCleanup || ownerID != cleanupID || kind != workitems.KindCleanupPurge || (workState != workitems.StateAvailable && workState != workitems.StateLeased) {
		return 0, errors.New("cleanup purge work item identity or state changed")
	}
	if workExpectedVersion < 1 || workExpectedVersion > expectedVersion {
		return 0, fmt.Errorf("cleanup purge work item owner version %d is outside expected owner version %d", workExpectedVersion, expectedVersion)
	}
	var (
		currentEffect, currentState string
		currentAttempt              []byte
		currentVersion              int64
	)
	if err := tx.QueryRowContext(ctx, `
	SELECT effect, state, attempt, version
	FROM cleanup_operations WHERE id = $1 FOR UPDATE`, cleanupID).
		Scan(&currentEffect, &currentState, &currentAttempt, &currentVersion); err != nil {
		return 0, fmt.Errorf("lock cleanup purge owner: %w", err)
	}
	if currentEffect != "quarantine" || (currentState != string(CleanupQuarantined) && currentState != string(CleanupGraceWaiting)) || currentVersion != expectedVersion || !bytes.Equal(currentAttempt, attemptRaw) {
		return 0, errors.New("cleanup purge owner or attempt changed during evidence reconciliation")
	}
	var newVersion int64
	if err := tx.QueryRowContext(ctx, `
	UPDATE cleanup_operations
	SET state = 'purged', failure_code = 'cleanup_purged', reason = 'purged', version = version + 1, updated_at = CURRENT_TIMESTAMP
	WHERE id = $1 AND version = $2 AND effect = 'quarantine'
	RETURNING version`, cleanupID, expectedVersion).Scan(&newVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("cleanup purge owner CAS failed")
		}
		return 0, fmt.Errorf("converge cleanup purge owner: %w", err)
	}
	affected, err := s.workItems.CompleteCleanupWorkItemTx(ctx, txscope.Wrap(tx), workItemID, cleanupID, workitems.KindCleanupPurge, newVersion, expectedVersion)
	if err != nil {
		return 0, fmt.Errorf("complete cleanup purge work item: %w", err)
	}
	if affected != 1 {
		return 0, errors.New("cleanup purge work item CAS failed")
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit cleanup purge convergence: %w", err)
	}
	return newVersion, nil
}

func (s *CleanupService) load(ctx context.Context, cleanupID string) (*CleanupOperation, error) {
	operation, err := s.repository.Get(ctx, cleanupID)
	if errors.Is(err, sql.ErrNoRows) || operation == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return cleanupOperationFromPersistence(*operation), nil
}

func cleanupOperationFromPersistence(value persistence.Operation) *CleanupOperation {
	return &CleanupOperation{
		ID:                value.ID,
		IdempotencyKey:    value.IdempotencyKey,
		PlacementID:       value.PlacementID,
		ManagedAnimeID:    value.ManagedAnimeID,
		Coverage:          value.Coverage,
		ReplacementRef:    value.ReplacementRef,
		PolicyVersion:     value.PolicyVersion,
		RetentionMode:     value.RetentionMode,
		SourcePath:        value.SourcePath,
		SourceFingerprint: value.SourceFingerprint,
		QuarantinePath:    value.QuarantinePath,
		Effect:            value.Effect,
		GraceDeadline:     value.GraceDeadline,
		State:             CleanupState(value.State),
		Attempt:           value.Attempt,
		CorrelationID:     value.CorrelationID,
		Version:           value.Version,
		CreatedAt:         value.CreatedAt,
	}
}

// CleanupOperation is the row model.
type CleanupOperation struct {
	ID                string
	IdempotencyKey    string
	PlacementID       string
	ManagedAnimeID    string
	Coverage          []string
	ReplacementRef    string
	PolicyVersion     int64
	RetentionMode     string
	SourcePath        string
	SourceFingerprint string
	QuarantinePath    string
	Effect            string
	GraceDeadline     *time.Time
	State             CleanupState
	Attempt           json.RawMessage
	CorrelationID     string
	Version           int64
	CreatedAt         time.Time
}

// QuarantineHandler settles admitted cleanups by persisting the pre-effect
// attempt and moving the state machine forward. Every owner mutation is fenced
// by the leased WorkItem claim; a stale claim is rejected before observation or
// filesystem mutation.
func (s *CleanupService) QuarantineHandler() worker.Handler {
	return cleanupRecoveryHandler{service: s, kind: workitems.KindCleanupQuarantine, handle: s.quarantineBusinessHandler()}
}
func (s *CleanupService) quarantineBusinessHandler() worker.Handler {
	return worker.HandlerFunc(func(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
		if claim.Kind != workitems.KindCleanupQuarantine {
			return worker.Outcome{}, fmt.Errorf("cleanup handler received kind %q", claim.Kind)
		}
		if s.workItems == nil {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "cleanup_work_items_unavailable", Reason: "cleanup work items store is not wired"}, nil), nil
		}
		op, err := s.load(ctx, claim.Owner.ID)
		if err != nil {
			return worker.Outcome{}, err
		}
		if op == nil {
			return worker.Outcome{}, errors.New("cleanup operation not found")
		}
		resumed := false
		if op.State == CleanupAdmitted && len(op.Attempt) > 0 {
			o, resume, err := (cleanupRecoveryHandler{service: s, kind: claim.Kind}).resolveAttempt(ctx, claim, op)
			if err != nil {
				return worker.Outcome{}, err
			}
			if !resume {
				if o.Disposition == workitems.DispositionAmbiguous {
					return worker.Outcome{}, workitems.ErrPublicationUnresolved
				}
				return o, nil
			}
			// The checkpointed attempt never reached its move: run the same
			// invocation (same attempt, same owner version) instead of parking.
			resumed = true
		}
		claim, err = s.mutateClaim(ctx, claim, s.verifyClaimOwner(claim))
		if err != nil {
			return worker.Outcome{}, err
		}
		finish := func(result workitems.OwnerResult) (worker.Outcome, error) {
			return worker.Finish(workitems.StateSucceeded, result, s.syncOwnerVersion(claim)), nil
		}
		block := func(code, reason string) (worker.Outcome, error) {
			next, mutateErr := s.mutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
				tx := scope.SQLTx()
				if tx == nil {
					return 0, errors.New("transition cleanup owner transaction is nil")
				}
				return s.transitionTx(ctx, tx, op.ID, claim.ExpectedOwnerVersion, CleanupBlocked, code, reason)
			})
			if mutateErr != nil {
				return worker.Outcome{}, mutateErr
			}
			claim = next
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: code, Reason: reason}, s.syncOwnerVersion(claim)), nil
		}
		if op.State == CleanupQuarantined && op.QuarantinePath != "" {
			// Idempotent replay: the move transition already committed; only the
			// terminal WorkItem transition crashed. Never re-run the move.
			if err := s.enqueue(ctx, op.ID, claim.ExpectedOwnerVersion, workitems.KindCleanupPurge); err != nil {
				return worker.Outcome{}, err
			}
			return finish(workitems.OwnerResult{State: "quarantined", ReasonCode: "cleanup_quarantined", Reason: "quarantined (idempotent replay)"})
		}
		if op.State == CleanupGraceWaiting && op.QuarantinePath != "" {
			if err := s.enqueue(ctx, op.ID, claim.ExpectedOwnerVersion, workitems.KindCleanupPurge); err != nil {
				return worker.Outcome{}, err
			}
			return finish(workitems.OwnerResult{State: "grace_waiting", ReasonCode: "cleanup_grace_waiting", Reason: "quarantine is waiting for grace deadline"})
		}
		if op.State != CleanupAdmitted {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "cleanup_owner_invalid", Reason: "cleanup is not admitted"}, s.syncOwnerVersion(claim)), nil
		}
		if s.executor == nil {
			return block("cleanup_executor_unavailable", "cleanup filesystem executor is not wired")
		}
		quarantine := op.QuarantinePath
		if quarantine == "" {
			quarantine = filepath.Join(filepath.Dir(op.SourcePath), ".bansoko-cleanup-"+op.ID, "object")
		}
		var attempt json.RawMessage
		var expected domain.FileObservation
		if resumed {
			evidence, err := decodeCleanupAttempt(op.Attempt)
			if err != nil {
				return worker.Outcome{}, err
			}
			if expected, err = frozenObservation(op.Attempt, op.SourceFingerprint); err != nil {
				return worker.Outcome{}, err
			}
			attempt, quarantine = op.Attempt, evidence.QuarantinePath
		} else {
			expected, err = s.executor.Observe(ctx, op.SourcePath)
			if err != nil {
				return worker.Retry("cleanup_source_observation_failed", safeCleanupReason(err), 30*time.Second, s.retryOwner(claim)), nil
			}
			if expected.Fingerprint != op.SourceFingerprint || expected.SizeBytes < 0 || !fileidentity.IsPresent(expected.Identity) {
				return block("cleanup_source_changed", "cleanup source observation does not match frozen evidence")
			}
			attempt, err = json.Marshal(map[string]any{
				"cleanup_id": op.ID, "effect": op.Effect, "stage": "quarantine", "source_path": op.SourcePath,
				"source_fingerprint": op.SourceFingerprint, "quarantine_path": quarantine,
				"source_observation": expected, "fencing_token": claim.FencingToken,
			})
			if err != nil {
				return worker.Outcome{}, err
			}
			claim, err = s.mutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
				tx := scope.SQLTx()
				if tx == nil {
					return 0, errors.New("persist cleanup attempt transaction is nil")
				}
				return s.persistAttemptTx(ctx, tx, op.ID, claim.ExpectedOwnerVersion, attempt)
			})
			if err != nil {
				return worker.Outcome{}, err
			}
			if _, err := s.mutateClaim(ctx, claim, s.verifyClaimOwner(claim)); err != nil {
				return worker.Outcome{}, err
			}
		}
		var moved string
		if receiptExecutor, ok := s.executor.(executorpkg.MaintenanceCleanupExecutor); ok {
			moved, err = receiptExecutor.MoveMaintenance(ctx, op.SourcePath, quarantine, expected, maintenanceInvocation(claim, attempt, op.SourcePath, quarantine, expected.Fingerprint))
		} else {
			moved, err = s.executor.MoveToQuarantine(ctx, op.SourcePath, quarantine, expected)
		}
		if err != nil {
			return block("cleanup_quarantine_failed", safeCleanupReason(err))
		}
		if _, err := s.executor.Observe(ctx, op.SourcePath); err == nil {
			return worker.Retry("cleanup_quarantine_source_still_present", "quarantine returned but source remains", 30*time.Second, s.retryOwner(claim)), nil
		} else if !isMissingCleanupError(err) {
			return block("cleanup_quarantine_post_observation_invalid", "quarantine returned without proven source absence")
		}
		quarantineObserved, err := s.executor.Observe(ctx, moved)
		if err != nil {
			return worker.Retry("cleanup_quarantine_post_observation_failed", safeCleanupReason(err), 30*time.Second, s.retryOwner(claim)), nil
		}
		if !sameFrozenCleanupObservation(quarantineObserved, expected) {
			return block("cleanup_quarantine_changed", "quarantine observation does not match frozen source")
		}
		claim, err = s.mutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
			tx := scope.SQLTx()
			if tx == nil {
				return 0, errors.New("quarantine cleanup owner transaction is nil")
			}
			return s.quarantineTx(ctx, tx, op.ID, claim.ExpectedOwnerVersion, moved, "cleanup_quarantined", "quarantined at "+moved)
		})
		if err != nil {
			return worker.Outcome{}, err
		}
		if err := s.enqueue(ctx, op.ID, claim.ExpectedOwnerVersion, workitems.KindCleanupPurge); err != nil {
			return worker.Outcome{}, err
		}
		if s.activity != nil {
			activityOp := *op
			activityOp.Version = claim.ExpectedOwnerVersion
			if err := s.appendCleanupActivity(ctx, &activityOp, "quarantined", "文件已隔离"); err != nil {
				return worker.Outcome{}, err
			}
		}
		state := CleanupQuarantined
		code := "cleanup_quarantined"
		reason := "moved to " + moved
		if updated, loadErr := s.load(ctx, op.ID); loadErr != nil {
			return worker.Outcome{}, loadErr
		} else if updated != nil {
			state = updated.State
			if state == CleanupGraceWaiting {
				code = "cleanup_grace_waiting"
				reason = "quarantine is waiting for grace deadline"
			}
		}
		return finish(workitems.OwnerResult{State: string(state), ReasonCode: code, Reason: reason})
	})
}

// PurgeHandler is the independent purge stage: grace deadline passed +
// quarantined evidence + fingerprint verification, then delete. All owner
// writes and retries are fenced by the active WorkItem claim.
func (s *CleanupService) PurgeHandler() worker.Handler {
	return cleanupRecoveryHandler{service: s, kind: workitems.KindCleanupPurge, handle: s.purgeBusinessHandler()}
}
func (s *CleanupService) purgeBusinessHandler() worker.Handler {
	return worker.HandlerFunc(func(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
		if claim.Kind != workitems.KindCleanupPurge {
			return worker.Outcome{}, fmt.Errorf("cleanup purge handler received kind %q", claim.Kind)
		}
		if s.workItems == nil {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "cleanup_work_items_unavailable", Reason: "cleanup work items store is not wired"}, nil), nil
		}
		op, err := s.load(ctx, claim.Owner.ID)
		if err != nil {
			return worker.Outcome{}, err
		}
		if op == nil {
			return worker.Outcome{}, errors.New("cleanup operation not found")
		}
		if evidence, decodeErr := decodeCleanupAttempt(op.Attempt); decodeErr == nil && evidence.Stage == "purge" && op.State != CleanupPurged {
			o, err := (cleanupRecoveryHandler{service: s, kind: claim.Kind}).recoverReceipt(ctx, claim, op)
			if err == nil && o.Disposition == workitems.DispositionAmbiguous {
				return worker.Outcome{}, workitems.ErrPublicationUnresolved
			}
			return o, err
		}
		claim, err = s.mutateClaim(ctx, claim, s.verifyClaimOwner(claim))
		if err != nil {
			return worker.Outcome{}, err
		}
		block := func(code, reason string) (worker.Outcome, error) {
			next, mutateErr := s.mutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
				tx := scope.SQLTx()
				if tx == nil {
					return 0, errors.New("transition cleanup owner transaction is nil")
				}
				return s.transitionTx(ctx, tx, op.ID, claim.ExpectedOwnerVersion, CleanupBlocked, code, reason)
			})
			if mutateErr != nil {
				return worker.Outcome{}, mutateErr
			}
			claim = next
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: code, Reason: reason}, s.syncOwnerVersion(claim)), nil
		}
		if op.State == CleanupPurged {
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "purged", ReasonCode: "cleanup_purged", Reason: "purged (idempotent replay)"}, s.syncOwnerVersion(claim)), nil
		}
		if op.Effect != "quarantine" {
			return block("cleanup_purge_effect_invalid", "cleanup purge stage requires owner effect quarantine")
		}
		if op.State != CleanupQuarantined && op.State != CleanupGraceWaiting {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "cleanup_purge_invalid", Reason: "cleanup is not quarantined or grace waiting"}, s.syncOwnerVersion(claim)), nil
		}
		if op.GraceDeadline != nil && s.now().Before(*op.GraceDeadline) {
			return worker.Retry("cleanup_grace_not_elapsed", "grace deadline has not elapsed", 30*time.Second, s.retryOwner(claim)), nil
		}
		if s.executor == nil {
			return block("cleanup_executor_unavailable", "cleanup filesystem executor is not wired")
		}
		expected, err := frozenObservation(op.Attempt, op.SourceFingerprint)
		if err != nil {
			return block("cleanup_purge_evidence_invalid", safeCleanupReason(err))
		}
		if op.QuarantinePath == "" {
			return block("cleanup_purge_evidence_invalid", "cleanup quarantine path evidence is missing")
		}
		observed, err := s.executor.Observe(ctx, op.QuarantinePath)
		if err != nil {
			return worker.Retry("cleanup_quarantine_observation_failed", safeCleanupReason(err), 30*time.Second, s.retryOwner(claim)), nil
		}
		if !sameFrozenCleanupObservation(observed, expected) {
			return block("cleanup_quarantine_changed", "cleanup quarantine observation does not match frozen evidence")
		}
		attempt, err := json.Marshal(map[string]any{
			"cleanup_id": op.ID, "effect": op.Effect, "stage": "purge", "quarantine_path": op.QuarantinePath,
			"source_fingerprint": op.SourceFingerprint, "quarantine_observation": observed,
			"fencing_token": claim.FencingToken,
		})
		if err != nil {
			return worker.Outcome{}, err
		}
		claim, err = s.mutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
			tx := scope.SQLTx()
			if tx == nil {
				return 0, errors.New("persist cleanup attempt transaction is nil")
			}
			return s.persistAttemptTx(ctx, tx, op.ID, claim.ExpectedOwnerVersion, attempt)
		})
		if err != nil {
			return worker.Outcome{}, err
		}
		if _, err := s.mutateClaim(ctx, claim, s.verifyClaimOwner(claim)); err != nil {
			return worker.Outcome{}, err
		}
		if receiptExecutor, ok := s.executor.(executorpkg.MaintenanceCleanupExecutor); ok {
			err = receiptExecutor.PurgeMaintenance(ctx, op.QuarantinePath, observed, maintenanceInvocation(claim, attempt, op.QuarantinePath, op.QuarantinePath, observed.Fingerprint))
		} else {
			err = s.executor.RemoveQuarantined(ctx, op.QuarantinePath, observed)
		}
		if err != nil {
			return block("cleanup_purge_failed", safeCleanupReason(err))
		}
		if _, postErr := s.executor.Observe(ctx, op.QuarantinePath); postErr == nil {
			return worker.Retry("cleanup_purge_post_observation_present", "purge returned but quarantine object remains", 30*time.Second, s.retryOwner(claim)), nil
		} else if !isMissingCleanupError(postErr) {
			return block("cleanup_purge_post_observation_invalid", "purge returned without proven quarantine absence")
		}
		claim, err = s.mutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
			tx := scope.SQLTx()
			if tx == nil {
				return 0, errors.New("record cleanup purge observation transaction is nil")
			}
			return s.recordPurgePostObservationTx(ctx, tx, op.ID, claim.ExpectedOwnerVersion)
		})
		if err != nil {
			return worker.Outcome{}, err
		}
		if s.activity != nil {
			activityOp := *op
			activityOp.Version = claim.ExpectedOwnerVersion
			if err := s.appendCleanupActivity(ctx, &activityOp, "purged", "隔离文件已清除"); err != nil {
				return worker.Outcome{}, err
			}
		}
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "purged", ReasonCode: "cleanup_purged", Reason: "purged"}, s.syncOwnerVersion(claim)), nil
	})
}

// safeCleanupReason bounds the reason string for owner rows/logs.
func safeCleanupReason(err error) string {
	if err == nil {
		return "cleanup effect failed"
	}
	text := err.Error()
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

// nullableText converts an empty string to SQL NULL (schema CHECKs usually
// allow NULL but reject empty strings).
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ListCleanups returns the cleanup operations of one anime (side-effect
// free, newest first) for the per-instance owner truth surface.
func (s *CleanupService) ListCleanupsApplication(ctx context.Context, animeID string, limit, offset int) ([]CleanupOperationView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.repository.List(ctx, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	views := make([]CleanupOperationView, 0, len(rows))
	for _, row := range rows {
		view := CleanupOperationView{
			ID:                row.ID,
			PlacementID:       row.PlacementID,
			ManagedAnimeID:    row.ManagedAnimeID,
			ReplacementRef:    row.ReplacementRef,
			PolicyVersion:     int(row.PolicyVersion),
			RetentionMode:     row.RetentionMode,
			SourcePath:        row.SourcePath,
			SourceFingerprint: row.SourceFingerprint,
			Effect:            row.Effect,
			State:             row.State,
			Version:           int(row.Version),
			CreatedAt:         row.CreatedAt,
			UpdatedAt:         &row.UpdatedAt,
			QuarantinePath:    row.QuarantinePath,
			GraceDeadline:     row.GraceDeadline,
			FailureCode:       row.FailureCode,
			Reason:            row.Reason,
		}
		views = append(views, view)
	}
	return views, nil
}

// appendCleanupActivity records one cleanup_completed terminal result. The
// effect transition already committed through its own update; the Activity
// append runs in its own short transaction so a writer failure never rolls
// back the completed cleanup effect.
func (s *CleanupService) appendCleanupActivity(ctx context.Context, op *CleanupOperation, reasonCode, summary string) error {
	if s.activity == nil || op == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	subscriptionID, err := s.repository.SubscriptionIDForAnimeTx(ctx, txscope.Wrap(tx), op.ManagedAnimeID)
	if err != nil {
		return err
	}
	if err := s.activity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
		SubscriptionID:  subscriptionID,
		ManagedAnimeID:  op.ManagedAnimeID,
		Kind:            "cleanup_completed",
		Outcome:         "success",
		ReasonCode:      reasonCode,
		SubjectType:     "cleanup_operation",
		SubjectID:       op.ID,
		SubjectVersion:  op.Version,
		StableResultKey: reasonCode,
		Title:           "清理完成",
		Summary:         summary,
		CorrelationID:   op.CorrelationID,
		CausationType:   "replacement_decision",
		CausationID:     op.ReplacementRef,
	}); err != nil {
		return err
	}
	return tx.Commit()
}
