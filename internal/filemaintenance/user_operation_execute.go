package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/events"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// User file operations of the file manager. One
// owner row of file_manager_operations is one confirmed user command on a
// detached file (copy also reads a managed one). The file_manager_execute
// WorkItem runs it with the same fenced machinery as alignment and cleanup:
// every pre-effect fact is re-checked in the transaction that writes the
// durable attempt checkpoint, the effect runs through the receipt-bearing
// maintenance executors, and the owner settles in the WorkItem finish
// transaction. A user operation never overwrites: the target is resolved to
// a free path before the checkpoint and executed with the block policy.
const (
	userOperationTable          = "file_manager_operations"
	userOperationQuarantineKind = "file_manager_quarantine"
	userOperationPurgeKind      = "file_manager_purge"
	maxUserOperationVersions    = 1000
)

// CleanupAnchor is the containment anchor of the capture a user file
// operation's delete moves the file into. The file manager has no root layer:
// a path is the absolute path, and the deployment's own boundary — the
// filesystem root — owns where a delete may land.
const CleanupAnchor = string(filepath.Separator)

// Settled failure codes (filemanager.Failure* mirror these).
const (
	userFailureTargetOccupied = "target_occupied"
	userFailureSourceChanged  = "source_changed"
	userFailureSourceMissing  = "source_missing"
	userFailureSourceManaged  = "source_managed"
	userFailureExecuteFailed  = "execute_failed"
	userFailureAnimeGone      = "anime_unavailable"
)

// UserOperationCheck is what the guard re-checks inside the checkpoint
// transaction.
type UserOperationCheck struct {
	OperationID     string
	SourceSpellings []string
	TargetSpellings []string
	RequireDetached bool
	// MatchAnimeID is the anime a match operation moves into; the check fails
	// with anime_unavailable once it no longer takes files.
	MatchAnimeID string
}

// UserOperationGuard answers the file manager's owner facts for a user
// operation: the spellings owner rows may use for a path, and — inside a
// caller transaction — whether the source is still free (and detached when the
// operation needs it) and whether the target is occupied by an owner.
type UserOperationGuard interface {
	Spellings(ctx context.Context, absolute string) (path string, spellings []string, err error)
	CheckTx(ctx context.Context, tx *sql.Tx, check UserOperationCheck) (sourceFailure string, targetOccupied bool, err error)
}

// UserOperationService executes user file operations.
type UserOperationService struct {
	db        *sql.DB
	workItems *workitems.Store
	executor  executorpkg.FileExecutor
	observer  executorpkg.FileObserver
	cleanup   func() executorpkg.CleanupFileExecutor
	guard     UserOperationGuard
	recorder  observabilityport.Recorder
	hub       *events.Hub
	now       func() time.Time
}

// NewUserOperationService wires the executor ports. cleanup builds the
// capture executor a delete uses to move the file into the operation's private
// namespace and remove it.
func NewUserOperationService(db *sql.DB, workItems *workitems.Store, executor executorpkg.FileExecutor, observer executorpkg.FileObserver, cleanup func() executorpkg.CleanupFileExecutor, guard UserOperationGuard) *UserOperationService {
	return &UserOperationService{db: db, workItems: workItems, executor: executor, observer: observer, cleanup: cleanup, guard: guard, now: time.Now}
}

// SetRecorder wires the routine record of each settled operation.
func (s *UserOperationService) SetRecorder(recorder observabilityport.Recorder) {
	if s != nil {
		s.recorder = recorder
	}
}

// SetEventHub wires the realtime refresh after each settle.
func (s *UserOperationService) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.hub = hub
	}
}

// parentEnsurer creates the missing parent directories of a target inside a
// frozen root, walking every component without following a symlink
// (filesystem.OSFileExecutor.EnsurePublicationParent).
type parentEnsurer interface {
	EnsurePublicationParent(root, target string) error
}

type userOperation struct {
	ID           string
	Operation    string
	State        string
	SourcePath   string
	TargetPath   string
	ExpectedSize int64
	Collision    string
	Attempt      json.RawMessage
	ResultPath   string
	Version      int64
	// MatchAnimeID is the anime a match operation moves into.
	MatchAnimeID string
}

type userOperationAttempt struct {
	OperationID       string `json:"operation_id"`
	Operation         string `json:"operation"`
	Stage             string `json:"stage"`
	SourcePath        string `json:"source_path"`
	SourceFingerprint string `json:"source_fingerprint"`
	TargetPath        string `json:"target_path,omitempty"`
	// CreateDirs are the absolute directories a match found missing when it
	// froze its checkpoint, parents first; the effect creates them.
	CreateDirs            []string               `json:"create_dirs,omitempty"`
	QuarantinePath        string                 `json:"quarantine_path,omitempty"`
	SourceObservation     domain.FileObservation `json:"source_observation"`
	QuarantineObservation domain.FileObservation `json:"quarantine_observation"`
	FencingToken          int64                  `json:"fencing_token"`
}

func (s *UserOperationService) load(ctx context.Context, id string) (*userOperation, error) {
	var op userOperation
	var attempt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, operation, state, source_path, COALESCE(target_path,''), expected_size_bytes,
 collision, attempt::text, COALESCE(result_path,''), version, COALESCE(match_anime_id,'')
FROM file_manager_operations WHERE id=$1`, id).Scan(&op.ID, &op.Operation, &op.State, &op.SourcePath, &op.TargetPath,
		&op.ExpectedSize, &op.Collision, &attempt, &op.ResultPath, &op.Version, &op.MatchAnimeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load user operation: %w", err)
	}
	if attempt.Valid {
		op.Attempt = json.RawMessage(attempt.String)
	}
	return &op, nil
}

// ExecuteHandler is the file_manager_execute work item handler.
func (s *UserOperationService) ExecuteHandler() worker.Handler {
	return userOperationRecoveryHandler{service: s}
}

type userOperationRecoveryHandler struct{ service *UserOperationService }

// errUserOperationPrecondition carries a failure found inside the checkpoint
// transaction; nothing is written and the operation settles with it.
type errUserOperationPrecondition struct{ code, reason string }

func (e errUserOperationPrecondition) Error() string { return e.code + ": " + e.reason }

func (h userOperationRecoveryHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	s := h.service
	if claim.Kind != workitems.KindFileManagerExecute || claim.Owner.Type != workitems.OwnerFileManagerOperation {
		return worker.Outcome{}, fmt.Errorf("user operation handler received kind %q", claim.Kind)
	}
	op, err := s.load(ctx, claim.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if op == nil {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "user_operation_owner_invalid", Reason: "operation does not exist"}, nil), nil
	}
	if op.State == "completed" {
		// Settled by deployment recovery from the stage's receipt: the work
		// item follows its owner and nothing runs again.
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "user_operation_settled", Reason: "operation was already settled"},
			settledUserOperationWriter(op.ID, op.State, op.Version)), nil
	}
	if op.State != "ready" && op.State != "running" {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: op.State, ReasonCode: "user_operation_settled", Reason: "operation was already settled"},
			settledUserOperationWriter(op.ID, op.State, op.Version)), nil
	}
	if op.Version != claim.ExpectedOwnerVersion {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "user_operation_fencing_stale", Reason: "stale fencing token cannot commit user operation progress"}, nil), nil
	}
	if len(op.Attempt) > 0 {
		outcome, resume, err := h.resolveAttempt(ctx, claim, op)
		if err != nil {
			return worker.Outcome{}, err
		}
		if !resume {
			if outcome.Disposition == workitems.DispositionAmbiguous {
				return worker.Outcome{}, workitems.ErrPublicationUnresolved
			}
			return outcome, nil
		}
		attempt, err := decodeUserAttempt(op.Attempt)
		if err != nil {
			return worker.Outcome{}, err
		}
		switch attempt.Stage {
		case "purge":
			return h.purge(ctx, claim, op, attempt)
		case "captured":
			return h.advanceToPurge(ctx, claim, op, attempt, attempt.QuarantinePath, attempt.QuarantineObservation)
		}
		return h.effect(ctx, claim, op, attempt)
	}
	return h.start(ctx, claim, op)
}

// fail settles the operation with a typed failure in the finish transaction.
func (h userOperationRecoveryHandler) fail(op *userOperation, version int64, state, code, reason string) worker.Outcome {
	writer := h.service.settleWriter(op, version, state, code, reason, "")
	return h.service.published(worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: state, ReasonCode: code, Reason: reason}, writer))
}

// published refreshes the file manager views after the settle commits.
func (s *UserOperationService) published(outcome worker.Outcome) worker.Outcome {
	outcome.AfterCommit = s.hub.AfterCommit(events.TargetRecords, outcome.AfterCommit)
	return outcome
}

func (h userOperationRecoveryHandler) start(ctx context.Context, claim workitems.Claim, op *userOperation) (worker.Outcome, error) {
	s := h.service
	if s.executor == nil || s.observer == nil || s.guard == nil || s.workItems == nil {
		return h.fail(op, op.Version, "blocked", userFailureExecuteFailed, "file manager executor is not wired"), nil
	}
	_, sourceSpellings, err := s.guard.Spellings(ctx, op.SourcePath)
	if err != nil {
		return h.fail(op, op.Version, "blocked", userFailureSourceChanged, "the file path cannot be read"), nil
	}
	info, err := os.Lstat(op.SourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return h.fail(op, op.Version, "failed", userFailureSourceMissing, "the file no longer exists"), nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() != op.ExpectedSize {
		return h.fail(op, op.Version, "blocked", userFailureSourceChanged, "the file changed since the operation was confirmed"), nil
	}
	fingerprint, err := s.observer.Observe(ctx, op.SourcePath)
	if err != nil || fingerprint == "" {
		return h.fail(op, op.Version, "blocked", userFailureSourceChanged, "the file cannot be read"), nil
	}
	attempt := userOperationAttempt{
		OperationID: op.ID, Operation: op.Operation, Stage: "effect", SourcePath: op.SourcePath,
		SourceFingerprint: fingerprint, FencingToken: claim.FencingToken,
	}
	var targetSpellings []string
	if op.Operation == "delete" {
		capture, ok := s.captureExecutor()
		if !ok {
			return h.fail(op, op.Version, "blocked", userFailureExecuteFailed, "file manager delete executor is not wired"), nil
		}
		observed, err := capture.Observe(ctx, op.SourcePath)
		if err != nil || observed.Fingerprint != fingerprint || observed.SizeBytes != op.ExpectedSize {
			return h.fail(op, op.Version, "blocked", userFailureSourceChanged, "the file changed while it was read"), nil
		}
		attempt.Stage = "quarantine"
		attempt.QuarantinePath = filepath.Join(filepath.Dir(op.SourcePath), ".bansoko-cleanup-"+op.ID, "object")
		attempt.SourceObservation = observed
	} else {
		target, spellings, code, reason, err := h.resolveTarget(ctx, op)
		if err != nil {
			return worker.Outcome{}, err
		}
		if code != "" {
			return h.fail(op, op.Version, "blocked", code, reason), nil
		}
		attempt.TargetPath, targetSpellings = target, spellings
		if op.Operation == "match" {
			if _, ok := s.executor.(parentEnsurer); !ok {
				return h.fail(op, op.Version, "blocked", userFailureExecuteFailed, "file manager executor cannot create directories"), nil
			}
			missing, err := missingDirectories(target)
			if err != nil {
				return h.fail(op, op.Version, "blocked", userFailureExecuteFailed, "the target path is not a plain absolute directory chain"), nil
			}
			attempt.CreateDirs = missing
		}
	}
	raw, err := json.Marshal(attempt)
	if err != nil {
		return worker.Outcome{}, err
	}
	check := UserOperationCheck{OperationID: op.ID, SourceSpellings: sourceSpellings, TargetSpellings: targetSpellings, RequireDetached: op.Operation != "copy", MatchAnimeID: op.MatchAnimeID}
	next := op.Version + 1
	claim, err = s.workItems.MutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("user operation checkpoint transaction is nil")
		}
		sourceFailure, occupied, err := s.guard.CheckTx(ctx, tx, check)
		if err != nil {
			return 0, err
		}
		if sourceFailure == userFailureAnimeGone {
			return 0, errUserOperationPrecondition{sourceFailure, "the anime no longer takes matched files"}
		}
		if sourceFailure != "" {
			return 0, errUserOperationPrecondition{sourceFailure, "the file is held or managed by another owner"}
		}
		if occupied {
			return 0, errUserOperationPrecondition{userFailureTargetOccupied, "the target is occupied"}
		}
		var result any
		if attempt.TargetPath != "" {
			result = attempt.TargetPath
		}
		res, err := tx.ExecContext(ctx, `UPDATE file_manager_operations SET attempt=$2, state='running', result_path=$3,
 version=version+1, updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$4 AND state='ready'`, op.ID, raw, result, op.Version)
		if err != nil {
			return 0, err
		}
		if affected, err := res.RowsAffected(); err != nil || affected != 1 {
			return 0, fmt.Errorf("user operation %s changed concurrently", op.ID)
		}
		return next, nil
	})
	var precondition errUserOperationPrecondition
	if errors.As(err, &precondition) {
		state := "blocked"
		return h.fail(op, op.Version, state, precondition.code, precondition.reason), nil
	}
	if err != nil {
		return worker.Outcome{}, err
	}
	op.Version, op.State, op.Attempt = next, "running", raw
	return h.effect(ctx, claim, op, attempt)
}

// resolveTarget picks the concrete target: the requested path when it is
// free, otherwise (version) the first free " (N)" sibling. Free means absent
// on disk and named by no owner.
func (h userOperationRecoveryHandler) resolveTarget(ctx context.Context, op *userOperation) (string, []string, string, string, error) {
	s := h.service
	for index := 1; index <= maxUserOperationVersions; index++ {
		candidate := op.TargetPath
		if index > 1 {
			if op.Collision != "version" {
				return "", nil, userFailureTargetOccupied, "the target is occupied", nil
			}
			extension := filepath.Ext(op.TargetPath)
			candidate = strings.TrimSuffix(op.TargetPath, extension) + " (" + strconv.Itoa(index) + ")" + extension
		}
		if _, err := os.Lstat(candidate); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, "", "", fmt.Errorf("inspect user operation target: %w", err)
		}
		_, spellings, err := s.guard.Spellings(ctx, candidate)
		if err != nil {
			return "", nil, userFailureExecuteFailed, "the target path cannot be read", nil
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return "", nil, "", "", err
		}
		_, occupied, err := s.guard.CheckTx(ctx, tx, UserOperationCheck{OperationID: op.ID, TargetSpellings: spellings})
		_ = tx.Rollback()
		if err != nil {
			return "", nil, "", "", err
		}
		if !occupied {
			return candidate, spellings, "", "", nil
		}
	}
	return "", nil, userFailureTargetOccupied, "the target is occupied", nil
}

type cleanupObserver interface {
	Observe(context.Context, string) (domain.FileObservation, error)
}

func (s *UserOperationService) captureExecutor() (interface {
	executorpkg.MaintenanceCleanupExecutor
	cleanupObserver
}, bool) {
	if s.cleanup == nil {
		return nil, false
	}
	capture, ok := s.cleanup().(interface {
		executorpkg.MaintenanceCleanupExecutor
		cleanupObserver
	})
	return capture, ok
}

func userInvocation(claim workitems.Claim, attempt json.RawMessage, kind, source, target, fingerprint string) executorpkg.MaintenanceInvocation {
	invocation := maintenanceInvocation(claim, attempt, source, target, fingerprint)
	if kind != "" {
		invocation.Kind = kind
	}
	return invocation
}

// effect runs the checkpointed attempt: a move or copy through the receipt
// executor, or the capture stage of a delete.
func (h userOperationRecoveryHandler) effect(ctx context.Context, claim workitems.Claim, op *userOperation, attempt userOperationAttempt) (worker.Outcome, error) {
	s := h.service
	if op.Operation == "delete" {
		return h.capture(ctx, claim, op, attempt)
	}
	receipts, ok := s.executor.(executorpkg.MaintenanceFileExecutor)
	if !ok {
		return h.fail(op, op.Version, "blocked", userFailureExecuteFailed, "file manager executor has no receipts"), nil
	}
	kind := domain.OperationMove
	if op.Operation == "copy" {
		kind = domain.OperationCopy
	}
	if op.Operation == "match" {
		// The checkpoint froze the directories to create; creating them again
		// after an interrupted attempt is a no-op. A created directory stays
		// even when the move then fails.
		ensurer, ok := s.executor.(parentEnsurer)
		if !ok {
			return h.fail(op, op.Version, "blocked", userFailureExecuteFailed, "file manager executor cannot create directories"), nil
		}
		if err := ensurer.EnsurePublicationParent("", attempt.TargetPath); err != nil {
			return h.fail(op, op.Version, "failed", userFailureExecuteFailed, "the target directories could not be created"), nil
		}
	}
	operation := domain.FileOperation{
		Operation: kind, SourcePath: attempt.SourcePath, TargetPath: attempt.TargetPath,
		SourceFingerprint: attempt.SourceFingerprint, CollisionPolicy: domain.CollisionBlock,
		ExpectedSizeBytes: op.ExpectedSize, ExpectedSizeKnown: true,
	}
	executed, err := receipts.ExecuteMaintenance(ctx, operation, userInvocation(claim, op.Attempt, "", attempt.SourcePath, attempt.TargetPath, attempt.SourceFingerprint))
	if err != nil {
		var coded executorpkg.ErrorCoder
		if errors.As(err, &coded) && coded.ErrorCode() == domain.FileErrorCodeTargetOccupied {
			return h.fail(op, op.Version, "blocked", userFailureTargetOccupied, safeReason(err)), nil
		}
		if errors.As(err, &coded) && coded.ErrorCode() == "SOURCE_CHANGED" {
			return h.fail(op, op.Version, "blocked", userFailureSourceChanged, safeReason(err)), nil
		}
		return h.fail(op, op.Version, "failed", userFailureExecuteFailed, safeReason(err)), nil
	}
	observed, err := s.observer.Observe(ctx, executed.TargetPath)
	if err != nil || !offsetFingerprintsEqual(observed, attempt.SourceFingerprint) {
		return h.fail(op, op.Version, "failed", userFailureExecuteFailed, "the result does not match the file"), nil
	}
	return h.complete(op, op.Version, executed.TargetPath), nil
}

func (h userOperationRecoveryHandler) complete(op *userOperation, version int64, result string) worker.Outcome {
	writer := h.service.settleWriter(op, version, "completed", "", "", result)
	return h.service.published(worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "user_operation_completed", Reason: "user operation completed"}, writer))
}

// capture moves the file into the operation's private namespace, then
// checkpoints the purge stage and removes that exact object.
func (h userOperationRecoveryHandler) capture(ctx context.Context, claim workitems.Claim, op *userOperation, attempt userOperationAttempt) (worker.Outcome, error) {
	s := h.service
	capture, ok := s.captureExecutor()
	if !ok {
		return h.fail(op, op.Version, "blocked", userFailureExecuteFailed, "file manager delete executor is not wired"), nil
	}
	expected := attempt.SourceObservation
	moved, err := capture.MoveMaintenance(ctx, attempt.SourcePath, attempt.QuarantinePath, expected,
		userInvocation(claim, op.Attempt, userOperationQuarantineKind, attempt.SourcePath, attempt.QuarantinePath, expected.Fingerprint))
	if err != nil {
		return h.fail(op, op.Version, "failed", userFailureExecuteFailed, safeReason(err)), nil
	}
	observed, err := capture.Observe(ctx, moved)
	if err != nil || !sameFrozenCleanupObservation(observed, expected) {
		return h.fail(op, op.Version, "failed", userFailureExecuteFailed, "the captured file does not match the file"), nil
	}
	return h.advanceToPurge(ctx, claim, op, attempt, moved, observed)
}

func (h userOperationRecoveryHandler) advanceToPurge(ctx context.Context, claim workitems.Claim, op *userOperation, attempt userOperationAttempt, quarantine string, observed domain.FileObservation) (worker.Outcome, error) {
	s := h.service
	attempt.Stage, attempt.QuarantinePath, attempt.QuarantineObservation, attempt.FencingToken = "purge", quarantine, observed, claim.FencingToken
	raw, err := json.Marshal(attempt)
	if err != nil {
		return worker.Outcome{}, err
	}
	next := op.Version + 1
	claim, err = s.workItems.MutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("user operation purge checkpoint transaction is nil")
		}
		res, err := tx.ExecContext(ctx, `UPDATE file_manager_operations SET attempt=$2, version=version+1, updated_at=CURRENT_TIMESTAMP
 WHERE id=$1 AND version=$3 AND state='running'`, op.ID, raw, op.Version)
		if err != nil {
			return 0, err
		}
		if affected, err := res.RowsAffected(); err != nil || affected != 1 {
			return 0, fmt.Errorf("user operation %s changed concurrently", op.ID)
		}
		return next, nil
	})
	if err != nil {
		return worker.Outcome{}, err
	}
	op.Version, op.Attempt = next, raw
	return h.purge(ctx, claim, op, attempt)
}

func (h userOperationRecoveryHandler) purge(ctx context.Context, claim workitems.Claim, op *userOperation, attempt userOperationAttempt) (worker.Outcome, error) {
	capture, ok := h.service.captureExecutor()
	if !ok {
		return h.fail(op, op.Version, "blocked", userFailureExecuteFailed, "file manager delete executor is not wired"), nil
	}
	observed := attempt.QuarantineObservation
	if err := capture.PurgeMaintenance(ctx, attempt.QuarantinePath, observed,
		userInvocation(claim, op.Attempt, userOperationPurgeKind, attempt.QuarantinePath, attempt.QuarantinePath, observed.Fingerprint)); err != nil {
		return h.fail(op, op.Version, "failed", userFailureExecuteFailed, safeReason(err)), nil
	}
	if _, err := capture.Observe(ctx, attempt.QuarantinePath); err == nil || !isMissingCleanupError(err) {
		return h.fail(op, op.Version, "failed", userFailureExecuteFailed, "the captured file is still present"), nil
	}
	return h.complete(op, op.Version, ""), nil
}

func decodeUserAttempt(raw json.RawMessage) (userOperationAttempt, error) {
	var attempt userOperationAttempt
	if err := json.Unmarshal(raw, &attempt); err != nil || attempt.OperationID == "" || attempt.Stage == "" {
		return attempt, errors.New("user operation attempt is invalid")
	}
	return attempt, nil
}

// ObserveRecovery classifies an interrupted claim.
func (h userOperationRecoveryHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	s := h.service
	if s == nil || c.Kind != workitems.KindFileManagerExecute || c.Owner.Type != workitems.OwnerFileManagerOperation {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	op, err := s.load(ctx, c.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if op != nil && op.State == "completed" {
		// Also reached when deployment recovery settled the owner from the
		// stage's receipt after the claim's checkpoint (version moved on).
		return recoveryCommitted("completed", settledUserOperationWriter(op.ID, "completed", op.Version)), nil
	}
	if op == nil || op.Version != c.ExpectedOwnerVersion {
		return worker.Outcome{}, workitems.ErrStaleClaim
	}
	if op.State == "ready" && len(op.Attempt) == 0 {
		return recoveryNoEffect(maintenanceNoAttemptWriter(userOperationTable, c, "ready",
			s.settleWriter(op, op.Version, "failed", userFailureExecuteFailed, "the operation stopped before it started", ""))), nil
	}
	outcome, _, err := h.resolveAttempt(ctx, c, op)
	return outcome, err
}

// resolveAttempt reads the exact invocation receipt of the checkpointed
// stage once. resume=true means the stage provably never started (or, for a
// capture, it finished and the purge stage is next) and the caller may run
// it unchanged.
func (h userOperationRecoveryHandler) resolveAttempt(ctx context.Context, c workitems.Claim, op *userOperation) (worker.Outcome, bool, error) {
	s := h.service
	ambiguous := worker.AwaitRecovery("")
	attempt, err := decodeUserAttempt(op.Attempt)
	if err != nil {
		return ambiguous, false, nil
	}
	switch attempt.Stage {
	case "effect":
		receipts, ok := s.executor.(executorpkg.MaintenanceFileExecutor)
		if !ok {
			return ambiguous, false, nil
		}
		invocation := userInvocation(c, op.Attempt, "", attempt.SourcePath, attempt.TargetPath, attempt.SourceFingerprint)
		receipt, err := receipts.ReadMaintenanceReceipt(ctx, invocation)
		if err != nil || receipt.Validate(invocation) != nil {
			if op.Operation == "move" || op.Operation == "rename" || op.Operation == "copy" || op.Operation == "match" {
				if maintenanceAttemptNotStarted(ctx, s.observer, err, attempt.SourcePath, attempt.TargetPath, attempt.SourceFingerprint) {
					return worker.Outcome{}, true, nil
				}
			}
			return ambiguous, false, nil
		}
		writer := s.settleWriter(op, op.Version, "completed", "", "", attempt.TargetPath)
		return recoveryCommitted("completed", maintenanceAttemptWriter(userOperationTable, c, op.State, op.Attempt, writer)), false, nil
	case "quarantine":
		capture, ok := s.captureExecutor()
		if !ok {
			return ambiguous, false, nil
		}
		expected := attempt.SourceObservation
		invocation := userInvocation(c, op.Attempt, userOperationQuarantineKind, attempt.SourcePath, attempt.QuarantinePath, expected.Fingerprint)
		receipt, err := capture.ReadMaintenanceReceipt(ctx, invocation)
		if err != nil || receipt.Validate(invocation) != nil || !sameFrozenCleanupObservation(receipt.Observation, expected) {
			if errors.Is(err, executorpkg.ErrMaintenanceNotStarted) {
				observed, observeErr := capture.Observe(ctx, attempt.SourcePath)
				_, targetErr := capture.Observe(ctx, attempt.QuarantinePath)
				if observeErr == nil && sameFrozenCleanupObservation(observed, expected) && isMissingCleanupError(targetErr) {
					return worker.Outcome{}, true, nil
				}
			}
			return ambiguous, false, nil
		}
		// The capture finished: the purge stage is next. Recovery itself only
		// re-queues; the handler checkpoints and runs the purge.
		return worker.Outcome{}, true, h.markCaptured(ctx, c, op, attempt, receipt.Observation)
	case "purge":
		capture, ok := s.captureExecutor()
		if !ok {
			return ambiguous, false, nil
		}
		observed := attempt.QuarantineObservation
		invocation := userInvocation(c, op.Attempt, userOperationPurgeKind, attempt.QuarantinePath, attempt.QuarantinePath, observed.Fingerprint)
		receipt, err := capture.ReadMaintenanceReceipt(ctx, invocation)
		if err != nil || receipt.Validate(invocation) != nil {
			// Purge is irreversible: absence is never a removal receipt.
			return ambiguous, false, nil
		}
		writer := s.settleWriter(op, op.Version, "completed", "", "", "")
		return recoveryCommitted("completed", maintenanceAttemptWriter(userOperationTable, c, op.State, op.Attempt, writer)), false, nil
	}
	return ambiguous, false, nil
}

// markCaptured rewrites the in-memory attempt so a resumed handler runs the
// purge stage next (the purge checkpoint itself is written by advanceToPurge).
func (h userOperationRecoveryHandler) markCaptured(_ context.Context, _ workitems.Claim, op *userOperation, attempt userOperationAttempt, observed domain.FileObservation) error {
	attempt.QuarantineObservation = observed
	attempt.Stage = "captured"
	raw, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	op.Attempt = raw
	return nil
}

func (s *UserOperationService) settleWriter(op *userOperation, expected int64, state, code, reason, result string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("user operation finish transaction is nil")
		}
		var resultPath any
		if result != "" {
			resultPath = result
		}
		var created any
		if op.Operation == "match" {
			raw, _ := json.Marshal(createdDirectories(op.Attempt))
			created = raw
		}
		res, err := tx.ExecContext(ctx, `UPDATE file_manager_operations SET state=$2, failure_code=NULLIF($3,''), reason=NULLIF($4,''),
 result_path=CASE WHEN $2='completed' THEN $5 ELSE result_path END, created_dirs=$7::jsonb, completed_at=CURRENT_TIMESTAMP,
 version=version+1, updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$6 AND state IN ('ready','running')`,
			op.ID, state, code, safeBounded(reason), resultPath, expected, created)
		if err != nil {
			return 0, err
		}
		if affected, err := res.RowsAffected(); err != nil || affected != 1 {
			return 0, fmt.Errorf("user operation %s changed concurrently", op.ID)
		}
		if s.recorder != nil {
			identity := observabilityport.FileManagerOperationRoutine(observabilityport.RoutineOutcomeSuccess, observabilityport.RoutineResultFileManagerCompleted)
			summary := "文件操作已完成"
			if state != "completed" {
				identity = observabilityport.FileManagerOperationRoutine(observabilityport.RoutineOutcomeFailure, observabilityport.RoutineResultFileManagerFailed)
				summary = "文件操作未完成"
			}
			details, _ := json.Marshal(map[string]any{"result_code": string(identity.ResultCode), "item_count": 1})
			if err := s.recorder.UpsertRoutineTx(ctx, scope, observabilityport.RoutineRequest{
				Identity: identity, ScopeID: observabilityport.FileManagerRoutineScopeID, Summary: summary, Details: details,
			}); err != nil {
				return 0, err
			}
		}
		return expected + 1, nil
	}
}

// settledUserOperationWriter finishes a work item against an owner that is
// already terminal: it locks the row, proves it is still in that state at that
// version, writes nothing, and carries the version onto the work item.
func settledUserOperationWriter(id, state string, version int64) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("user operation finish transaction is nil")
		}
		var current string
		var currentVersion int64
		err := tx.QueryRowContext(ctx, `SELECT state, version FROM file_manager_operations WHERE id=$1 FOR UPDATE NOWAIT`, id).Scan(&current, &currentVersion)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (current != state || currentVersion != version) {
			return 0, workitems.ErrStaleClaim
		}
		if err != nil {
			return 0, err
		}
		return version, nil
	}
}

// missingDirectories lists, parents first, the directories between the
// filesystem root and a target's parent that do not exist yet. Every existing
// component must be a real directory: a symlink or a file in the chain refuses
// the target.
func missingDirectories(target string) ([]string, error) {
	dir := filepath.Dir(filepath.Clean(target))
	if !filepath.IsAbs(dir) {
		return nil, errors.New("target must be absolute")
	}
	root := string(filepath.Separator)
	relative, err := filepath.Rel(root, dir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("target must be absolute")
	}
	if relative == "." {
		return nil, nil
	}
	var missing []string
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if len(missing) == 0 {
			info, err := os.Lstat(current)
			switch {
			case err == nil:
				if !info.IsDir() {
					return nil, errors.New("target path crosses a non-directory")
				}
				continue
			case !errors.Is(err, os.ErrNotExist):
				return nil, err
			}
		}
		missing = append(missing, current)
	}
	return missing, nil
}

// createdDirectories reads the directories a settled match created: the ones
// its checkpoint planned that exist now.
func createdDirectories(attempt json.RawMessage) []string {
	created := []string{}
	if len(attempt) == 0 {
		return created
	}
	var decoded userOperationAttempt
	if json.Unmarshal(attempt, &decoded) != nil {
		return created
	}
	for _, dir := range decoded.CreateDirs {
		if info, err := os.Lstat(dir); err == nil && info.IsDir() {
			created = append(created, dir)
		}
	}
	return created
}

func safeBounded(reason string) string {
	if len(reason) > 2048 {
		return reason[:2048]
	}
	return reason
}
