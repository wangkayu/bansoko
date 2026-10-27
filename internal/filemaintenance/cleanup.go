package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"bansoko/internal/fileidentity"
	persistence "bansoko/internal/filemaintenance/adapter/persistence"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// CleanupState is the deletion-obligation state machine of a replacement
// loser: admitted, then exactly one terminal state.
type CleanupState string

const (
	CleanupAdmitted CleanupState = "admitted"
	CleanupDeleted  CleanupState = "deleted"
	CleanupSkipped  CleanupState = "skipped"
	CleanupBlocked  CleanupState = "blocked"
	CleanupFailed   CleanupState = "failed"
)

// Typed reasons of a skipped deletion. Every skip has zero filesystem effect:
// the file the obligation names is left where it is.
const (
	CleanupSkipLoserMissing         = "loser_missing"
	CleanupSkipLoserNotRegular      = "loser_not_regular"
	CleanupSkipLoserIdentityChanged = "loser_identity_changed"
	CleanupSkipLoserIdentityUnknown = "loser_identity_unknown"
	CleanupSkipLoserPathOwned       = "loser_path_owned"
)

// Attempt stages of one replacement_delete work item. The capture stage moves the
// file into the owner's private namespace next to it; the purge stage removes
// that exact captured object. Both run inside one claim; the namespace is not
// a quarantine area (no grace period, nothing can be restored).
const (
	cleanupStageCapture = "capture"
	cleanupStagePurge   = "purge"
	// Invocation kinds of the two stages in the maintenance receipt namespace.
	cleanupCaptureInvocation = "cleanup_capture"
	cleanupPurgeInvocation   = "cleanup_purge"
	// unknownCleanupIdentity is the frozen identity of an obligation admitted
	// as skipped because the loser has no recorded size.
	unknownCleanupIdentity = "unknown"
)

// CleanupInput is the frozen admission input of one deletion obligation.
// SourceFingerprint is the loser's recorded path-level identity
// (domain.MetadataFingerprint: size, plus device/inode when recorded); empty
// means no size was recorded and the obligation is admitted as skipped.
type CleanupInput struct {
	ID                string
	IdempotencyKey    string
	PlacementID       string
	ManagedAnimeID    string
	Coverage          []string
	ReplacementRef    string
	AdmissionMode     string
	SourcePath        string
	SourceFingerprint string
	CorrelationID     string
	CausationType     string
	CausationID       string
}

// CleanupService owns the deletion obligations of replacement losers. Every
// filesystem effect is preceded by a durable attempt checkpoint; an effect
// whose outcome is uncertain is blocked and converged from the exact
// invocation receipts.
type CleanupService struct {
	db         *sql.DB
	repository *persistence.Repository
	now        func() time.Time
	executor   executorpkg.CleanupFileExecutor
	workItems  *workitems.Store
	activity   observabilityport.ActivityWriter
	// notifications admits the replacement_deletion_failed notice; nil
	// admits none.
	notifications plugintargetport.BusinessDeliveryEnqueuer
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
// the delete handler blocks, never silently succeeding.
func (s *CleanupService) SetFileExecutor(executor executorpkg.CleanupFileExecutor) {
	if s != nil {
		s.executor = executor
	}
}

// SetWorkItems wires the WorkItem store used to schedule replacement_delete
// items. Without it no deletion obligation can be admitted.
func (s *CleanupService) SetWorkItems(store *workitems.Store) {
	if s != nil {
		s.workItems = store
	}
}

// Admit persists one deletion obligation and its replacement_delete work item in
// one transaction. Replacement settlement uses admitTx inside its own
// transaction; Admit is the standalone form.
func (s *CleanupService) Admit(ctx context.Context, input CleanupInput) (*ReplacementDeletion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.admitTx(ctx, txscope.Wrap(tx), input); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.load(ctx, input.ID)
}

// admitTx records one deletion obligation in the caller's transaction. A
// loser without a recorded size is admitted directly as
// skipped/loser_identity_unknown: deletion fails closed. An admitted
// obligation gets its single replacement_delete work item in the same
// transaction. Admission is idempotent per idempotency key.
func (s *CleanupService) admitTx(ctx context.Context, scope txscope.Tx, input CleanupInput) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("cleanup admission transaction is nil")
	}
	coverage, err := json.Marshal(SortCoverage(input.Coverage))
	if err != nil {
		return err
	}
	row := persistence.OperationInput{
		ID:                input.ID,
		IdempotencyKey:    input.IdempotencyKey,
		PlacementID:       input.PlacementID,
		ManagedAnimeID:    input.ManagedAnimeID,
		Coverage:          coverage,
		ReplacementRef:    input.ReplacementRef,
		AdmissionMode:     input.AdmissionMode,
		SourcePath:        input.SourcePath,
		SourceFingerprint: input.SourceFingerprint,
		State:             string(CleanupAdmitted),
		CorrelationID:     input.CorrelationID,
		CausationType:     input.CausationType,
		CausationID:       input.CausationID,
	}
	if _, ok := domain.ParseMetadataFingerprint(input.SourceFingerprint); !ok {
		row.SourceFingerprint = unknownCleanupIdentity
		row.State = string(CleanupSkipped)
		row.FailureCode = CleanupSkipLoserIdentityUnknown
		row.Reason = "the replaced file has no recorded size"
	}
	if err := s.repository.InsertTx(ctx, scope, row); err != nil {
		return err
	}
	op, err := s.repository.LockTx(ctx, scope, input.ID)
	if err != nil {
		return fmt.Errorf("lock admitted cleanup: %w", err)
	}
	switch CleanupState(op.State) {
	case CleanupAdmitted:
		return s.enqueueTx(ctx, tx, op.ID, op.Version)
	case CleanupSkipped:
		if op.Version == 1 && row.State == string(CleanupSkipped) {
			return s.appendCleanupActivityTx(ctx, scope, replacementDeletionFromPersistence(*op), "skipped", CleanupSkipLoserIdentityUnknown)
		}
	}
	return nil
}

// enqueueTx creates (or merges into) the single replacement_delete work item of
// an admitted obligation at its current owner version.
func (s *CleanupService) enqueueTx(ctx context.Context, tx *sql.Tx, cleanupID string, version int64) error {
	return s.enqueueNextTx(ctx, tx, cleanupID, version, time.Time{}, 0)
}

// enqueueNextTx creates the work item of an obligation, due after delay. A
// non-zero busySince continues a busy chain (see cleanupWorkInput).
func (s *CleanupService) enqueueNextTx(ctx context.Context, tx *sql.Tx, cleanupID string, version int64, busySince time.Time, delay time.Duration) error {
	if s.workItems == nil {
		return errors.New("cleanup work items store is not wired")
	}
	input := cleanupWorkInput{CleanupID: cleanupID}
	if !busySince.IsZero() {
		since := busySince.UTC()
		input.BusySince = &since
	}
	snapshot, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, _, err = s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerReplacementDeletion, ID: cleanupID},
		Kind:                 workitems.KindReplacementDelete,
		IdempotencyKey:       "cln:" + cleanupID + ":" + workitems.KindReplacementDelete,
		InputSnapshot:        snapshot,
		AvailableAfter:       delay,
		MaxAttempts:          3,
		ExpectedOwnerVersion: version,
	})
	return err
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

// syncOwnerVersion returns the committed owner version so the Store can
// synchronize expected_owner_version; it never mutates the owner.
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

// settleWriter moves the owner to one terminal state at the expected version
// in the WorkItem's own terminal transaction and records the Activity of a
// deleted or skipped obligation there too; a deletion that stayed busy for the
// whole retry window also admits its notice.
func (s *CleanupService) settleWriter(op *ReplacementDeletion, expected int64, to CleanupState, code, reason string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		version, err := s.repository.TransitionTx(ctx, scope, op.ID, expected, string(to), code, reason)
		if err != nil {
			return 0, fmt.Errorf("settle cleanup owner: %w", err)
		}
		switch to {
		case CleanupDeleted:
			settled := *op
			settled.Version = version
			if err := s.appendCleanupActivityTx(ctx, scope, &settled, "deleted", "cleanup_deleted"); err != nil {
				return 0, err
			}
		case CleanupSkipped:
			settled := *op
			settled.Version = version
			if err := s.appendCleanupActivityTx(ctx, scope, &settled, "skipped", code); err != nil {
				return 0, err
			}
		case CleanupFailed:
			if cleanupGaveUp(code) {
				settled := *op
				settled.Version = version
				if err := s.appendCleanupFailureTx(ctx, scope, &settled, code); err != nil {
					return 0, err
				}
			}
		}
		return version, nil
	}
}

func (s *CleanupService) load(ctx context.Context, cleanupID string) (*ReplacementDeletion, error) {
	operation, err := s.repository.Get(ctx, cleanupID)
	if errors.Is(err, sql.ErrNoRows) || operation == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return replacementDeletionFromPersistence(*operation), nil
}

func replacementDeletionFromPersistence(value persistence.Operation) *ReplacementDeletion {
	return &ReplacementDeletion{
		ID:                value.ID,
		IdempotencyKey:    value.IdempotencyKey,
		PlacementID:       value.PlacementID,
		ManagedAnimeID:    value.ManagedAnimeID,
		Coverage:          value.Coverage,
		ReplacementRef:    value.ReplacementRef,
		AdmissionMode:     value.AdmissionMode,
		SourcePath:        value.SourcePath,
		SourceFingerprint: value.SourceFingerprint,
		State:             CleanupState(value.State),
		Attempt:           value.Attempt,
		FailureCode:       value.FailureCode,
		Reason:            value.Reason,
		CorrelationID:     value.CorrelationID,
		Version:           value.Version,
		CreatedAt:         value.CreatedAt,
	}
}

// ReplacementDeletion is the row model.
type ReplacementDeletion struct {
	ID                string
	IdempotencyKey    string
	PlacementID       string
	ManagedAnimeID    string
	Coverage          []string
	ReplacementRef    string
	AdmissionMode     string
	SourcePath        string
	SourceFingerprint string
	State             CleanupState
	Attempt           json.RawMessage
	FailureCode       string
	Reason            string
	CorrelationID     string
	Version           int64
	CreatedAt         time.Time
}

// cleanupCapturePath is the private namespace object a deletion captures the
// loser into: next to it, bound to the owner id.
func cleanupCapturePath(op *ReplacementDeletion) string {
	return filepath.Join(filepath.Dir(op.SourcePath), ".bansoko-cleanup-"+op.ID, "object")
}

// DeleteHandler is the replacement_delete work item handler: it verifies the
// loser's path-level identity, then captures and purges it inside one claim.
func (s *CleanupService) DeleteHandler() worker.Handler {
	return cleanupRecoveryHandler{service: s, handle: worker.HandlerFunc(s.handleDelete)}
}

func (s *CleanupService) handleDelete(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Kind != workitems.KindReplacementDelete {
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
	if op.State != CleanupAdmitted {
		// Idempotent replay: the owner already settled; only the WorkItem
		// transition is left.
		terminal := workitems.StateSucceeded
		if op.State == CleanupBlocked || op.State == CleanupFailed {
			terminal = workitems.StateFailed
		}
		return worker.Finish(terminal, workitems.OwnerResult{State: string(op.State), ReasonCode: op.FailureCode, Reason: op.Reason}, s.syncOwnerVersion(claim)), nil
	}
	claim, err = s.mutateClaim(ctx, claim, s.verifyClaimOwner(claim))
	if err != nil {
		return worker.Outcome{}, err
	}
	run := cleanupRun{service: s, op: op, claim: claim}
	if s.executor == nil {
		return run.block("cleanup_executor_unavailable", "cleanup filesystem executor is not wired"), nil
	}
	if len(op.Attempt) > 0 {
		return run.resume(ctx)
	}
	return run.start(ctx)
}

// cleanupRun carries one claim's deletion of one obligation.
type cleanupRun struct {
	service *CleanupService
	op      *ReplacementDeletion
	claim   workitems.Claim
}

func (r *cleanupRun) settle(terminal string, to CleanupState, code, reason string) worker.Outcome {
	return worker.Finish(terminal, workitems.OwnerResult{State: string(to), ReasonCode: code, Reason: reason},
		r.service.settleWriter(r.op, r.claim.ExpectedOwnerVersion, to, code, reason))
}

func (r *cleanupRun) skip(code, reason string) worker.Outcome {
	return r.settle(workitems.StateSucceeded, CleanupSkipped, code, reason)
}

// block settles an uncertain effect: the attempt stays on the owner and
// deployment recovery converges it from the exact invocation receipts.
func (r *cleanupRun) block(code, reason string) worker.Outcome {
	return r.settle(workitems.StateFailed, CleanupBlocked, code, reason)
}

func (r *cleanupRun) deleted(reason string) worker.Outcome {
	return r.settle(workitems.StateSucceeded, CleanupDeleted, "cleanup_deleted", reason)
}

// start verifies the loser's identity at metadata level (never reading the
// content) and runs both stages. Every mismatch is a zero-effect skip.
func (r *cleanupRun) start(ctx context.Context) (worker.Outcome, error) {
	s, op := r.service, r.op
	recorded, ok := domain.ParseMetadataFingerprint(op.SourceFingerprint)
	if !ok {
		return r.skip(CleanupSkipLoserIdentityUnknown, "the replaced file has no recorded size"), nil
	}
	observed, err := observeCleanupFile(ctx, s.executor, op.SourcePath, op.SourceFingerprint)
	switch {
	case isMissingCleanupError(err):
		return r.skip(CleanupSkipLoserMissing, "the replaced file is no longer at its path"), nil
	case errors.Is(err, domain.ErrNotRegularFile):
		return r.skip(CleanupSkipLoserNotRegular, "the path no longer holds a regular file"), nil
	case domain.FileBusy(err):
		return r.observationBusy(err), nil
	case err != nil:
		return worker.Retry("cleanup_source_observation_failed", safeCleanupReason(err), 30*time.Second, s.retryOwner(r.claim)), nil
	}
	actual, ok := domain.ParseMetadataFingerprint(observed.Fingerprint)
	if !ok || observed.SizeBytes < 0 || !fileidentity.IsPresent(observed.Identity) || !domain.MetadataFactsMatch(actual, recorded) {
		return r.skip(CleanupSkipLoserIdentityChanged, "the file at the path is not the replaced file"), nil
	}
	owned, err := s.pathOwned(ctx, op.SourcePath)
	if err != nil {
		return worker.Outcome{}, err
	}
	if owned {
		return r.skip(CleanupSkipLoserPathOwned, "another managed file now owns the path"), nil
	}
	attempt := cleanupAttemptEvidence{
		CleanupID:         op.ID,
		Stage:             cleanupStageCapture,
		WorkItemID:        r.claim.ID,
		OwnerVersion:      r.claim.ExpectedOwnerVersion + 1,
		SourcePath:        op.SourcePath,
		SourceFingerprint: op.SourceFingerprint,
		CapturePath:       cleanupCapturePath(op),
		SourceObservation: observed,
		FencingToken:      r.claim.FencingToken,
	}
	if err := r.checkpoint(ctx, attempt); err != nil {
		return worker.Outcome{}, err
	}
	return r.capture(ctx, attempt)
}

// checkpoint persists the attempt before its effect; the owner version it
// produces is the one the invocation receipt binds.
func (r *cleanupRun) checkpoint(ctx context.Context, attempt cleanupAttemptEvidence) error {
	raw, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	next, err := r.service.mutateClaim(ctx, r.claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		version, err := r.service.repository.PersistAttemptTx(ctx, scope, r.op.ID, r.claim.ExpectedOwnerVersion, raw)
		if err != nil {
			return 0, err
		}
		if version != attempt.OwnerVersion {
			return 0, fmt.Errorf("cleanup attempt version %d does not match checkpoint %d", version, attempt.OwnerVersion)
		}
		return version, nil
	})
	if err != nil {
		return err
	}
	r.claim = next
	r.op.Attempt, r.op.Version = raw, attempt.OwnerVersion
	return nil
}

func (r *cleanupRun) receiptExecutor() (executorpkg.MaintenanceCleanupExecutor, bool) {
	e, ok := r.service.executor.(executorpkg.MaintenanceCleanupExecutor)
	return e, ok
}

// capture moves the loser into the private namespace, proves the source
// absent and the captured object identical, then runs the purge stage.
func (r *cleanupRun) capture(ctx context.Context, attempt cleanupAttemptEvidence) (worker.Outcome, error) {
	s, op := r.service, r.op
	e, ok := r.receiptExecutor()
	if !ok {
		return r.block("cleanup_executor_unavailable", "cleanup executor has no receipts"), nil
	}
	expected := attempt.SourceObservation
	moved, err := e.MoveMaintenance(ctx, attempt.SourcePath, attempt.CapturePath, expected, cleanupInvocation(op, attempt, cleanupCaptureInvocation))
	if err != nil {
		return r.refused(ctx, attempt, "cleanup_capture_failed", err), nil
	}
	if _, err := observeCleanupFile(ctx, s.executor, attempt.SourcePath, op.SourceFingerprint); !isMissingCleanupError(err) {
		return r.block("cleanup_capture_source_present", "the capture returned without proven source absence"), nil
	}
	captured, err := observeCleanupFile(ctx, s.executor, moved, op.SourceFingerprint)
	if err != nil || !sameFrozenCleanupObservation(captured, expected) {
		return r.block("cleanup_capture_changed", "the captured object does not match the replaced file"), nil
	}
	return r.advanceToPurge(ctx, attempt, moved, captured)
}

func (r *cleanupRun) advanceToPurge(ctx context.Context, attempt cleanupAttemptEvidence, captured string, observed domain.FileObservation) (worker.Outcome, error) {
	attempt.Stage = cleanupStagePurge
	attempt.CapturePath = captured
	attempt.CaptureObservation = observed
	attempt.WorkItemID = r.claim.ID
	attempt.OwnerVersion = r.claim.ExpectedOwnerVersion + 1
	attempt.FencingToken = r.claim.FencingToken
	if err := r.checkpoint(ctx, attempt); err != nil {
		return worker.Outcome{}, err
	}
	return r.purge(ctx, attempt)
}

// purge removes the exact captured object and proves its absence.
func (r *cleanupRun) purge(ctx context.Context, attempt cleanupAttemptEvidence) (worker.Outcome, error) {
	s, op := r.service, r.op
	e, ok := r.receiptExecutor()
	if !ok {
		return r.block("cleanup_executor_unavailable", "cleanup executor has no receipts"), nil
	}
	if err := e.PurgeMaintenance(ctx, attempt.CapturePath, attempt.CaptureObservation, cleanupInvocation(op, attempt, cleanupPurgeInvocation)); err != nil {
		return r.refused(ctx, attempt, "cleanup_purge_failed", err), nil
	}
	if _, err := observeCleanupFile(ctx, s.executor, attempt.CapturePath, op.SourceFingerprint); !isMissingCleanupError(err) {
		return r.block("cleanup_purge_object_present", "the purge returned without proven absence"), nil
	}
	return r.deleted("replaced file deleted"), nil
}

// resume continues an obligation whose attempt a deployment recovery
// re-admitted: a finished capture continues with its purge, a provably
// unstarted stage runs again, anything else blocks.
func (r *cleanupRun) resume(ctx context.Context) (worker.Outcome, error) {
	attempt, err := decodeCleanupAttempt(r.op.Attempt)
	if err != nil {
		return r.block("cleanup_attempt_invalid", safeCleanupReason(err)), nil
	}
	e, ok := r.receiptExecutor()
	if !ok {
		return r.block("cleanup_executor_unavailable", "cleanup executor has no receipts"), nil
	}
	switch resolveCleanupAttempt(ctx, r.service.executor, e, r.op, attempt) {
	case cleanupResolutionPurged:
		return r.deleted("exact purge invocation returned"), nil
	case cleanupResolutionCaptured:
		captured, err := observeCleanupFile(ctx, r.service.executor, attempt.CapturePath, r.op.SourceFingerprint)
		if err != nil || !sameFrozenCleanupObservation(captured, attempt.SourceObservation) {
			return r.block("cleanup_capture_changed", "the captured object does not match the replaced file"), nil
		}
		return r.advanceToPurge(ctx, attempt, attempt.CapturePath, captured)
	case cleanupResolutionCaptureNotStarted:
		return r.start(ctx)
	case cleanupResolutionPurgeNotStarted:
		next := attempt
		next.WorkItemID = r.claim.ID
		next.OwnerVersion = r.claim.ExpectedOwnerVersion + 1
		next.FencingToken = r.claim.FencingToken
		if err := r.checkpoint(ctx, next); err != nil {
			return worker.Outcome{}, err
		}
		return r.purge(ctx, next)
	}
	return r.block("cleanup_effect_uncertain", "the interrupted deletion has no receipt"), nil
}

func (s *CleanupService) pathOwned(ctx context.Context, path string) (bool, error) {
	var owned bool
	err := s.repository.WithTx(ctx, func(scope txscope.Tx) error {
		var err error
		owned, err = s.repository.PathOwnedTx(ctx, scope, path)
		return err
	})
	return owned, err
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

// ListReplacementDeletionsApplication returns the cleanup operations of one anime
// (side-effect free, newest first) for the per-instance owner truth surface.
func (s *CleanupService) ListReplacementDeletionsApplication(ctx context.Context, animeID string, limit, offset int) ([]ReplacementDeletionView, error) {
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
	views := make([]ReplacementDeletionView, 0, len(rows))
	for _, row := range rows {
		views = append(views, ReplacementDeletionView{
			ID:                row.ID,
			PlacementID:       row.PlacementID,
			ManagedAnimeID:    row.ManagedAnimeID,
			ReplacementRef:    row.ReplacementRef,
			AdmissionMode:     row.AdmissionMode,
			SourcePath:        row.SourcePath,
			SourceFingerprint: row.SourceFingerprint,
			State:             row.State,
			Version:           int(row.Version),
			CreatedAt:         row.CreatedAt,
			UpdatedAt:         &row.UpdatedAt,
			FailureCode:       row.FailureCode,
			Reason:            row.Reason,
		})
	}
	return views, nil
}

// appendCleanupActivityTx records one cleanup_completed result (deleted or
// skipped) in the caller's transaction.
func (s *CleanupService) appendCleanupActivityTx(ctx context.Context, scope txscope.Tx, op *ReplacementDeletion, outcome, reasonCode string) error {
	if s.activity == nil || op == nil {
		return nil
	}
	subscriptionID, err := s.repository.SubscriptionIDForAnimeTx(ctx, scope, op.ManagedAnimeID)
	if errors.Is(err, sql.ErrNoRows) {
		// The subscription was removed meanwhile: the result is still
		// settled, it just has no subscription log to append to.
		return nil
	}
	if err != nil {
		return err
	}
	summary := "旧版本已删除"
	activityOutcome := "success"
	if outcome == "skipped" {
		summary = "旧版本未删除：" + cleanupSkipSummary(reasonCode)
		activityOutcome = "info"
	}
	return s.activity.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID:  subscriptionID,
		ManagedAnimeID:  op.ManagedAnimeID,
		Kind:            "cleanup_completed",
		Outcome:         activityOutcome,
		ReasonCode:      reasonCode,
		SubjectType:     "replacement_deletion",
		SubjectID:       op.ID,
		SubjectVersion:  op.Version,
		StableResultKey: reasonCode,
		Title:           "清理完成",
		Summary:         summary,
		CorrelationID:   op.CorrelationID,
		CausationType:   "replacement_decision",
		CausationID:     op.ReplacementRef,
	})
}

func cleanupSkipSummary(code string) string {
	switch code {
	case CleanupSkipLoserMissing:
		return "文件已不在原路径"
	case CleanupSkipLoserNotRegular:
		return "原路径不再是普通文件"
	case CleanupSkipLoserIdentityChanged:
		return "原路径上的文件已变化"
	case CleanupSkipLoserIdentityUnknown:
		return "没有记录文件大小"
	case CleanupSkipLoserPathOwned:
		return "路径已归其他受管文件"
	}
	return "身份核对未通过"
}
