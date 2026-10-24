package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"bansoko/internal/deployment"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

func isTargetOccupied(err error) bool {
	var operationError executorpkg.ErrorCoder
	return errors.As(err, &operationError) && operationError.ErrorCode() == executorpkg.CodeTargetOccupied
}

// OffsetMigrationExecuteService owns the durable execution of one ready
// offset migration operation: fenced attempt checkpoints, verified move
// effects, and the atomic placement/coverage switch. Success is only
// reported after the target fingerprint is re-read and matches; an uncertain
// effect is parked as effect_unknown for typed recovery, never guessed as
// failed. The old placement stays authoritative until the new target is
// verified and the placement row is switched in one transaction.
type OffsetMigrationExecuteService struct {
	db        *sql.DB
	executor  executorpkg.FileExecutor
	observer  executorpkg.FileObserver
	workItems *workitems.Store
	now       func() time.Time
}

// NewOffsetMigrationExecuteService wires the execute owner. File ports are
// injected by the composition root; work-item admission for ready operations
// happens inside the planner transaction (SetWorkItems on the planner).
func NewOffsetMigrationExecuteService(db *sql.DB) *OffsetMigrationExecuteService {
	return &OffsetMigrationExecuteService{db: db, now: time.Now}
}

// SetFilePorts injects the verified filesystem executor/observer (same ports
// as the alignment owner). Without them execution fails closed.
func (s *OffsetMigrationExecuteService) SetFilePorts(executor executorpkg.FileExecutor, observer executorpkg.FileObserver) {
	if s != nil {
		s.executor = executor
		s.observer = observer
	}
}

// SetWorkItems wires the claim store used to checkpoint attempt versions atomically.
func (s *OffsetMigrationExecuteService) SetWorkItems(store *workitems.Store) {
	if s != nil {
		s.workItems = store
	}
}

// ExecuteHandler returns the worker handler for one frozen offset migration
// operation. The claim owner id is the operation id; fencing ties the claim
// to the operation version captured when the work item was admitted.
func (s *OffsetMigrationExecuteService) ExecuteHandler() worker.Handler {
	return offsetRecoveryHandler{service: s, handle: s.offsetBusinessHandler()}
}
func (s *OffsetMigrationExecuteService) offsetBusinessHandler() worker.Handler {
	return worker.HandlerFunc(func(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
		if claim.Kind != workitems.KindOffsetMigrationExecute {
			return worker.Outcome{}, fmt.Errorf("offset migration handler received kind %q", claim.Kind)
		}
		op, err := loadOffsetMigrationOperation(ctx, s.db, claim.Owner.ID)
		if err != nil {
			return worker.Outcome{}, err
		}
		switch op.State {
		case OffsetMigrationOperationReady,
			OffsetMigrationOperationWaitingRetry,
			OffsetMigrationOperationRunning:
			// executable: ready is a fresh plan, waiting_retry is a re-authorized
			// retry, running is a previously fenced claim continuing.
		default:
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{
				State: string(op.State), ReasonCode: "offset_owner_invalid",
				Reason: "operation is not executable"}, nil), nil
		}
		if op.Version != claim.ExpectedOwnerVersion {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{
				State: "failed", ReasonCode: "offset_fencing_stale",
				Reason: "stale fencing token cannot commit offset migration progress"}, nil), nil
		}
		attempt, _ := json.Marshal(map[string]any{
			"operation_id": op.ID, "source_path": op.OldTargetPath,
			"source_fingerprint": op.SourceFingerprint, "target_path": op.NewTargetPath,
			"started_at": s.now().UTC(),
		})
		nextVersion := op.Version + 1
		resumed := false
		if len(op.Attempt) > 0 {
			o, resume, err := (offsetRecoveryHandler{service: s}).resolveAttempt(ctx, claim, op)
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
					return 0, errors.New("offset migration checkpoint transaction is nil")
				}
				// A placement released or displaced after planning is no
				// longer managed: its file is never moved.
				var released bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM placements WHERE id=$1 AND state IN ('released','displaced'))`, op.PlacementID).Scan(&released); err != nil {
					return 0, err
				}
				if released {
					return 0, errOffsetPlacementInactive
				}
				result, err := tx.ExecContext(ctx, `UPDATE offset_migration_plans SET attempt=$2,state='running',version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$3 AND state IN ('ready','running','waiting_retry')`, op.ID, attempt, op.Version)
				if err != nil {
					return 0, err
				}
				affected, err := result.RowsAffected()
				if err != nil {
					return 0, err
				}
				if affected != 1 {
					return 0, fmt.Errorf("offset migration operation %s changed concurrently", op.ID)
				}
				return nextVersion, nil
			})
		default:
			nextVersion, err = s.persistAttempt(ctx, op.ID, op.Version, attempt)
		}
		if errors.Is(err, errOffsetPlacementInactive) {
			writer := offsetMigrationTerminalWriter(op.ID, op.Version, OffsetMigrationOperationBlocked, "placement_not_active", "the placement is no longer managed")
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "placement_not_active", Reason: "the placement is no longer managed"}, writer), nil
		}
		if err != nil {
			return worker.Outcome{}, err
		}
		// Fail closed: without a proven filesystem effect the operation must
		// never report success.
		if s.executor == nil || s.observer == nil {
			writer := offsetMigrationTerminalWriter(op.ID, nextVersion, OffsetMigrationOperationBlocked, "offset_executor_unavailable", "offset migration filesystem executor is not wired")
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "offset_executor_unavailable", Reason: "offset migration filesystem executor is not wired"}, writer), nil
		}
		operation := domain.FileOperation{
			Operation: domain.OperationMove, SourcePath: op.OldTargetPath, TargetPath: op.NewTargetPath,
			SourceFingerprint: op.SourceFingerprint, CollisionPolicy: domain.CollisionBlock,
		}
		sourceFingerprint, err := offsetSourceFingerprintHex(op.SourceFingerprint)
		if err != nil {
			writer := offsetMigrationTerminalWriter(op.ID, nextVersion, OffsetMigrationOperationFailed, "offset_fingerprint_invalid", safeReason(err))
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "offset_fingerprint_invalid", Reason: safeReason(err)}, writer), nil
		}
		operation.SourceFingerprint = sourceFingerprint
		if op.State == OffsetMigrationOperationRunning {
			targetPath := filepath.Clean(op.NewTargetPath)
			if observed, err := executorpkg.ObserveLike(ctx, s.observer, targetPath, sourceFingerprint); err == nil && offsetFingerprintsEqual(observed, sourceFingerprint) {
				committedOp := *op
				committedOp.NewTargetPath = targetPath
				writeOwner := func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
					return s.commitAppliedTx(ctx, scope.SQLTx(), &committedOp, nextVersion)
				}
				return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: string(OffsetMigrationOperationApplied), ReasonCode: "offset_applied", Reason: "target verified after restart"}, writeOwner), nil
			}
			if observed, err := executorpkg.ObserveLike(ctx, s.observer, op.OldTargetPath, sourceFingerprint); err != nil || !offsetFingerprintsEqual(observed, sourceFingerprint) {
				writer := offsetMigrationTerminalWriter(op.ID, nextVersion, OffsetMigrationOperationBlocked, "target_conflict", "neither frozen source nor target matches after restart")
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "target_conflict", Reason: "neither frozen source nor target matches after restart"}, writer), nil
			}
		}
		// The new target may sit in a season folder that does not exist yet.
		if ensurer, ok := s.executor.(parentEnsurer); ok {
			if err := ensurer.EnsurePublicationParent("", filepath.Clean(operation.TargetPath)); err != nil {
				writeOwner := offsetMigrationRetryWriter(s.db, op.ID, nextVersion)
				return worker.Retry("offset_execute_failed", safeReason(err), time.Minute, writeOwner), nil
			}
		}
		var executed domain.FileOperation
		if receiptExecutor, ok := s.executor.(executorpkg.MaintenanceFileExecutor); ok {
			operation.TargetPath = filepath.Clean(operation.TargetPath)
			executed, err = receiptExecutor.ExecuteMaintenance(ctx, operation, maintenanceInvocation(claim, attempt, operation.SourcePath, operation.TargetPath, operation.SourceFingerprint))
		} else {
			executed, err = s.executor.Execute(ctx, operation)
		}
		if err != nil {
			if isTargetOccupied(err) {
				writer := offsetMigrationTerminalWriter(op.ID, nextVersion, OffsetMigrationOperationBlocked, "target_occupied", safeReason(err))
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "blocked", ReasonCode: "target_occupied", Reason: safeReason(err)}, writer), nil
			}
			writeOwner := offsetMigrationRetryWriter(s.db, op.ID, nextVersion)
			return worker.Retry("offset_execute_failed", safeReason(err), time.Minute, writeOwner), nil
		}
		observed, err := executorpkg.ObserveLike(ctx, s.observer, executed.TargetPath, sourceFingerprint)
		if err != nil || observed == "" || !offsetFingerprintsEqual(observed, sourceFingerprint) {
			reason := "target fingerprint mismatch after offset migration effect"
			if err != nil {
				reason = fmt.Sprintf("target reread failed: %v", err)
			}
			writeOwner := offsetMigrationTerminalWriter(op.ID, nextVersion, OffsetMigrationOperationEffectUnknown, "offset_effect_unknown", reason)
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: string(OffsetMigrationOperationEffectUnknown), ReasonCode: "offset_effect_unknown", Reason: reason}, writeOwner), nil
		}
		// Verified effect: switch placement + coverage and mark applied in the
		// same transaction as the work-item finish. The placement stores the
		// path the executor actually wrote — a version collision appends a
		// suffix — never the planned target.
		committedTarget := executed.TargetPath
		writeOwner := func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
			tx := scope.SQLTx()
			if tx == nil {
				return 0, errors.New("offset migration applied owner transaction is nil")
			}
			op.NewTargetPath = committedTarget
			return s.commitAppliedTx(ctx, tx, op, nextVersion)
		}
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{
			State: string(OffsetMigrationOperationApplied), ReasonCode: "offset_applied",
			Reason: "file moved and verified"}, writeOwner), nil
	})
}

// admitOffsetMigrationWorkItemTx creates one durable execute work item inside
// the planner transaction. It is idempotent (create-or-merge) and optional:
// planners without a wired work store (unit tests) skip admission. The
// operation is ready at admission time, so its version is still 1 and the
// expected owner version stays correct across planner replays.
func (s *OffsetMigrationService) admitOffsetMigrationWorkItemTx(ctx context.Context, tx *sql.Tx, operation plannedOffsetOperation) error {
	if s.work == nil {
		return nil
	}
	snapshot := operation.targetContract
	if len(snapshot) == 0 {
		snapshot = json.RawMessage(`{}`)
	}
	_, _, err := s.work.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerOffsetMigration, ID: operation.id},
		Kind:                 workitems.KindOffsetMigrationExecute,
		IdempotencyKey:       "offmig:" + operation.id,
		InputSnapshot:        snapshot,
		AvailableAfter:       0,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	return err
}

// offsetSourceFingerprintHex extracts the pure sha256 hex value from the
// frozen placement fingerprint. The placement fingerprint is persisted as a
// JSONB document ({algorithm, value[, size_bytes]}), while the filesystem
// executor and observer compare plain hex only. Non-JSON input is passed
// through untouched (legacy plain fingerprints).
// errOffsetPlacementInactive: the checkpoint found the plan's placement no
// longer active; nothing was written.
var errOffsetPlacementInactive = errors.New("offset migration placement is not active")

func offsetSourceFingerprintHex(frozen string) (string, error) {
	trimmed := strings.TrimSpace(frozen)
	if !strings.HasPrefix(trimmed, "{") {
		return trimmed, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil || fields == nil {
		return "", fmt.Errorf("decode offset source fingerprint: %w", err)
	}
	var algorithm, value string
	if err := json.Unmarshal(fields["algorithm"], &algorithm); err != nil || algorithm != "sha256" {
		return "", errors.New("offset source fingerprint algorithm must be sha256")
	}
	if err := json.Unmarshal(fields["value"], &value); err != nil || !isHexFingerprint(value) {
		return "", errors.New("offset source fingerprint value is invalid")
	}
	return value, nil
}

func isHexFingerprint(value string) bool {
	if len(value) != 64 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// resolveOffsetTargetPath returns the frozen target for execution and
// observation. Naming templates render absolute targets, so the path is used
// as-is; a relative frozen target is never anchored to a global root and the
// executor rejects it fail-closed.
func resolveOffsetTargetPath(target string) string {
	return filepath.Clean(target)
}

// offsetFingerprintsEqual compares two fingerprint representations (plain
// hex or {algorithm,value} JSON) by their hex value.
func offsetFingerprintsEqual(left, right string) bool {
	if domain.IsMetadataFingerprint(left) || domain.IsMetadataFingerprint(right) {
		// Path-level identity (size, inode when both know it); a metadata
		// fingerprint never equals a content digest.
		return domain.MetadataFingerprintsMatch(left, right)
	}
	leftHex, leftErr := offsetSourceFingerprintHex(left)
	rightHex, rightErr := offsetSourceFingerprintHex(right)
	if leftErr != nil || rightErr != nil {
		return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
	}
	return strings.EqualFold(leftHex, rightHex)
}

// persistAttempt freezes the durable attempt checkpoint and moves the
// operation to running with CAS on the expected version.
func (s *OffsetMigrationExecuteService) persistAttempt(ctx context.Context, operationID string, expectedVersion int64, attempt json.RawMessage) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE offset_migration_plans
SET attempt = $2, state = 'running', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $3 AND state IN ('ready', 'running', 'waiting_retry')`,
		operationID, attempt, expectedVersion)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, fmt.Errorf("offset migration operation %s changed concurrently (version %d)", operationID, expectedVersion)
	}
	return expectedVersion + 1, nil
}

// settle transitions one operation with CAS and returns the next version.
func (s *OffsetMigrationExecuteService) settle(ctx context.Context, operationID string, expectedVersion int64, state OffsetMigrationOperationState, code, reason string) error {
	_, err := settleOffsetMigrationOperation(ctx, s.db, operationID, expectedVersion, state, code, reason)
	return err
}

// commitAppliedTx atomically applies one verified offset migration inside an
// existing transaction: the placement row is switched with CAS on its frozen
// version, the coverage key is replaced, and the operation becomes applied.
// When the placement changed concurrently the operation is failed, never
// applied: the file effect is verified, but this placement no longer owns it.
func (s *OffsetMigrationExecuteService) commitAppliedTx(ctx context.Context, tx *sql.Tx, op *OffsetMigrationOperation, expectedVersion int64) (int64, error) {
	result, err := tx.ExecContext(ctx, `
UPDATE placements
SET physical_path = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'active' AND version = $3`,
		op.PlacementID, op.NewTargetPath, op.PlacementVersion)
	if err != nil {
		return 0, fmt.Errorf("switch offset placement: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return settleOffsetMigrationOperationTx(ctx, tx, op.ID, expectedVersion,
			OffsetMigrationOperationFailed, "placement_stale",
			"placement changed concurrently before offset commit")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM placement_coverage WHERE placement_id = $1`, op.PlacementID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, 0, 'offset-migration', $3)
ON CONFLICT (placement_id, observation_key) DO NOTHING`,
		op.PlacementID, op.NewCoverageKey, op.PlacementID); err != nil {
		return 0, err
	}
	return settleOffsetMigrationOperationTx(ctx, tx, op.ID, expectedVersion,
		OffsetMigrationOperationApplied, "", "")
}

// offsetMigrationRetryWriter parks the owner as waiting_retry (or failed once
// the attempt budget is exhausted, decided by the work-item store).
func offsetMigrationRetryWriter(db *sql.DB, operationID string, expectedVersion int64) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("offset migration retry owner transaction is nil")
		}
		state := OffsetMigrationOperationWaitingRetry
		if result.State == workitems.StateFailed {
			state = OffsetMigrationOperationFailed
		}
		return settleOffsetMigrationOperationTx(ctx, tx, operationID, expectedVersion, state, "offset_execute_failed", result.Reason)
	}
}

func offsetMigrationTerminalWriter(operationID string, expectedVersion int64, state OffsetMigrationOperationState, code, reason string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("offset migration finish owner transaction is nil")
		}
		return settleOffsetMigrationOperationTx(ctx, tx, operationID, expectedVersion, state, code, reason)
	}
}

// settleOffsetMigrationOperationTx transitions one operation with CAS inside
// an existing transaction and returns the next owner version.
func settleOffsetMigrationOperationTx(ctx context.Context, tx *sql.Tx, operationID string, expectedVersion int64, state OffsetMigrationOperationState, code, reason string) (int64, error) {
	result, err := tx.ExecContext(ctx, `
UPDATE offset_migration_plans
SET state = $2, reason_code = NULLIF($3, ''), failure_reason = NULLIF($4, ''),
    completed_at = CASE WHEN $2 IN ('applied', 'blocked', 'failed', 'superseded') THEN CURRENT_TIMESTAMP ELSE NULL END,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $5`,
		operationID, string(state), code, reason, expectedVersion)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, fmt.Errorf("offset migration operation %s changed concurrently on settle (version %d)", operationID, expectedVersion)
	}
	return expectedVersion + 1, nil
}

// settleOffsetMigrationOperation is the non-transaction CAS variant.
func settleOffsetMigrationOperation(ctx context.Context, db *sql.DB, operationID string, expectedVersion int64, state OffsetMigrationOperationState, code, reason string) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	version, err := settleOffsetMigrationOperationTx(ctx, tx, operationID, expectedVersion, state, code, reason)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return version, nil
}

// loadOffsetMigrationOperation reads one frozen operation by id.
func loadOffsetMigrationOperation(ctx context.Context, db *sql.DB, operationID string) (*OffsetMigrationOperation, error) {
	var (
		op            OffsetMigrationOperation
		sourceOffset  sql.NullInt64
		season        sql.NullInt64
		rawEpisode    sql.NullString
		newEpisode    sql.NullString
		newTarget     sql.NullString
		oldCoverage   sql.NullString
		newCoverage   sql.NullString
		attempt       sql.NullString
		reasonCode    sql.NullString
		failureReason sql.NullString
		completedAt   sql.NullTime
	)
	err := db.QueryRowContext(ctx, `
SELECT id, generation_id, managed_anime_id, scope_kind, scope_owner_id,
       placement_id, placement_version, policy_version, source_path,
       source_fingerprint, fingerprint, media_fact_snapshot_id,
       media_fact_file_ref, rss_source_id, source_episode_offset, media_kind,
       bound_season, raw_episode, new_episode, old_target_path, new_target_path,
       old_identity, new_identity, old_coverage_key, new_coverage_key,
       target_contract, state, attempt, reason_code, failure_reason, version,
       created_at, updated_at, completed_at
FROM offset_migration_plans WHERE id = $1`, operationID).Scan(
		&op.ID, &op.GenerationID, &op.ManagedAnimeID, &op.ScopeKind, &op.ScopeOwnerID,
		&op.PlacementID, &op.PlacementVersion, &op.PolicyVersion, &op.SourcePath,
		&op.SourceFingerprint, &op.Fingerprint, &op.MediaFactSnapshotID,
		&op.MediaFactFileRef, &op.RSSSourceID, &sourceOffset, &op.MediaKind,
		&season, &rawEpisode, &newEpisode, &op.OldTargetPath, &newTarget,
		&op.OldIdentity, &op.NewIdentity, &oldCoverage, &newCoverage,
		&op.TargetContract, &op.State, &attempt, &reasonCode, &failureReason,
		&op.Version, &op.CreatedAt, &op.UpdatedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("offset migration operation %s not found", operationID)
	}
	if err != nil {
		return nil, fmt.Errorf("read offset migration operation %s: %w", operationID, err)
	}
	if sourceOffset.Valid {
		value := int(sourceOffset.Int64)
		op.SourceEpisodeOffset = &value
	}
	if season.Valid {
		value := int(season.Int64)
		op.BoundSeason = &value
	}
	op.NewTargetPath = nullablePath(newTarget)
	op.OldCoverageKey = nullableCoverage(oldCoverage)
	op.NewCoverageKey = nullableCoverage(newCoverage)
	if attempt.Valid {
		op.Attempt = json.RawMessage(attempt.String)
	}
	op.ReasonCode = nullableReason(reasonCode)
	op.FailureReason = nullableReason(failureReason)
	if completedAt.Valid {
		value := completedAt.Time
		op.CompletedAt = &value
	}
	return &op, nil
}

func nullablePath(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullableCoverage(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullableReason(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// ReconcileUnknownEffect settles operations whose move outcome is uncertain
// after a crash: the frozen attempt is re-checked against the actual target.
// A verified target commits placement/coverage/applied; a missing target is
// left for a retryable execution; a mismatched target is a conflict.
func (s *OffsetMigrationExecuteService) ReconcileUnknownEffect(ctx context.Context, req deployment.ReconcileRequest) (deployment.ReconcileResult, error) {
	if s.observer == nil {
		return deployment.ReconcileResult{State: deployment.StateBlocked, Effect: deployment.EffectUnknown,
			Reason: "offset migration reconcile: observer not wired"}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, version, source_fingerprint, new_target_path, placement_id, placement_version, new_coverage_key
FROM offset_migration_plans
WHERE attempt IS NOT NULL AND state IN ('running', 'effect_unknown')`)
	if err != nil {
		return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
			Reason: safeReason(err)}, nil
	}
	defer rows.Close()
	settled := 0
	var firstIssue *deployment.ReconcileResult
	for rows.Next() {
		var (
			operationID, fingerprint, targetPath, placementID, coverageKey string
			version, placementVersion                                      int64
		)
		if err := rows.Scan(&operationID, &version, &fingerprint, &targetPath, &placementID, &placementVersion, &coverageKey); err != nil {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
				Reason: safeReason(err)}, nil
		}
		// The persisted target is absolute; normalize it exactly like the
		// executor does before observing and committing.
		targetPath = filepath.Clean(targetPath)
		observed, err := executorpkg.ObserveLike(ctx, s.observer, targetPath, fingerprint)
		if err != nil {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
				Reason: safeReason(err)}, nil
		}
		if observed == "" {
			// Target absent with a frozen attempt: the effect never landed.
			// Not a silent success, not an unconditional block.
			continue
		}
		if !offsetFingerprintsEqual(observed, fingerprint) {
			result := deployment.ReconcileResult{State: deployment.StateConflict, Effect: deployment.EffectPresent,
				Reason: fmt.Sprintf("offset migration operation %s target fingerprint differs from frozen source", operationID)}
			firstIssue = &result
			continue
		}
		op := &OffsetMigrationOperation{
			ID: operationID, PlacementID: placementID, PlacementVersion: placementVersion,
			NewTargetPath: targetPath, NewCoverageKey: coverageKey, Version: version,
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
				Reason: safeReason(err)}, nil
		}
		if _, err := s.commitAppliedTx(ctx, tx, op, version); err != nil {
			_ = tx.Rollback()
			result := deployment.ReconcileResult{State: deployment.StateConflict, Effect: deployment.EffectPresent,
				Reason: safeReason(err)}
			firstIssue = &result
			continue
		}
		if err := tx.Commit(); err != nil {
			return deployment.ReconcileResult{State: deployment.StateRetryable, Effect: deployment.EffectUnknown,
				Reason: safeReason(err)}, nil
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
		Reason:      fmt.Sprintf("settled %d offset migration operation(s)", settled),
		EvidenceRef: "offset_migration_plans"}, nil
}
