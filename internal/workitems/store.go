package workitems

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

const (
	DefaultRetentionDays      = 7
	DefaultRetentionBatchSize = 500
	MaxRetentionDays          = 30
)

// Store owns WorkItem claim/lease/retry/fencing/retention mechanics. Typed
// owner writes always flow through the handler-supplied OwnerWriter inside
// the same transaction.
type Store struct {
	DB            *sql.DB
	RetentionDays int
	Logger        observabilityport.RuntimeLogger
}

func NewStore(db *sql.DB, retentionDays int, logger observabilityport.RuntimeLogger) (*Store, error) {
	if db == nil {
		return nil, errors.New("work item store database is nil")
	}
	if retentionDays == 0 {
		retentionDays = DefaultRetentionDays
	}
	if retentionDays < 1 || retentionDays > MaxRetentionDays {
		return nil, ErrRetentionInvalid
	}
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	return &Store{DB: db, RetentionDays: retentionDays, Logger: logger}, nil
}

// HasLiveFileMaintenanceWorkItemsTx checks for live WorkItems attached to the
// managed anime through one of the four file-maintenance owner families. It
// only uses the caller-owned transaction scope and never controls its lifecycle.
func (s *Store) HasLiveFileMaintenanceWorkItemsTx(ctx context.Context, scope txscope.Tx, managedAnimeID string) (bool, error) {
	if s == nil {
		return false, errors.New("work item store is nil")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return false, errors.New("file maintenance work item check requires a transaction")
	}
	if strings.TrimSpace(managedAnimeID) == "" {
		return false, errors.New("managed anime ID is required")
	}
	result, err := persistence.New(tx).HasLiveFileMaintenanceWorkItems(ctx, managedAnimeID)
	if err != nil {
		return false, fmt.Errorf("check live file maintenance work items: %w", err)
	}
	if !result.Valid {
		return false, errors.New("check live file maintenance work items returned an invalid result")
	}
	return result.Bool, nil
}

// DeleteFileMaintenanceWorkItemsTx deletes WorkItems attached to the managed
// anime through one of the four file-maintenance owner families, regardless of
// WorkItem state. The caller owns the transaction lifecycle.
func (s *Store) DeleteFileMaintenanceWorkItemsTx(ctx context.Context, scope txscope.Tx, managedAnimeID string) (int64, error) {
	if s == nil {
		return 0, errors.New("work item store is nil")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return 0, errors.New("file maintenance work item deletion requires a transaction")
	}
	if strings.TrimSpace(managedAnimeID) == "" {
		return 0, errors.New("managed anime ID is required")
	}
	result, err := persistence.New(tx).DeleteFileMaintenanceWorkItems(ctx, managedAnimeID)
	if err != nil {
		return 0, fmt.Errorf("delete file maintenance work items: %w", err)
	}
	affected := result
	return affected, nil
}

// LockCleanupWorkItemTx locks the first live cleanup WorkItem in the
// caller-owned transaction. The generated row type remains private to this
// WorkItem package and only the recovery facts cross the Store boundary.
func (s *Store) LockCleanupWorkItemTx(ctx context.Context, scope txscope.Tx, cleanupID, kind string) (id, ownerType, ownerID, lockedKind, state string, expectedOwnerVersion int64, err error) {
	if s == nil {
		return "", "", "", "", "", 0, errors.New("work item store is nil")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return "", "", "", "", "", 0, errors.New("lock cleanup work item requires a transaction")
	}
	if strings.TrimSpace(cleanupID) == "" || strings.TrimSpace(kind) == "" {
		return "", "", "", "", "", 0, errors.New("lock cleanup work item requires cleanup ID and kind")
	}
	row, err := persistence.New(tx).LockCleanupWorkItem(ctx, persistence.LockCleanupWorkItemParams{
		OwnerType: OwnerReplacementDeletion,
		OwnerID:   cleanupID,
		Kind:      kind,
	})
	if err != nil {
		return "", "", "", "", "", 0, err
	}
	return row.ID, row.OwnerType, row.OwnerID, row.Kind, row.State, row.ExpectedOwnerVersion, nil
}

// CompleteCleanupWorkItemTx completes one live cleanup WorkItem using the
// exact owner identity, live-state, and expected-version upper-bound CAS.
// The caller retains transaction ownership; the affected-row count is returned
// for the recovery layer's exact-one validation.
func (s *Store) CompleteCleanupWorkItemTx(ctx context.Context, scope txscope.Tx, workItemID, cleanupID, kind string, newVersion, maxExpectedVersion int64) (int64, error) {
	if s == nil {
		return 0, errors.New("work item store is nil")
	}
	tx := scope.SQLTx()
	if tx == nil {
		return 0, errors.New("complete cleanup work item requires a transaction")
	}
	if strings.TrimSpace(workItemID) == "" || strings.TrimSpace(cleanupID) == "" || strings.TrimSpace(kind) == "" {
		return 0, errors.New("complete cleanup work item requires work item ID, cleanup ID, and kind")
	}
	result, err := persistence.New(tx).CompleteCleanupWorkItem(ctx, persistence.CompleteCleanupWorkItemParams{
		NewOwnerVersion:    newVersion,
		WorkItemID:         workItemID,
		OwnerType:          OwnerReplacementDeletion,
		OwnerID:            cleanupID,
		Kind:               kind,
		MaxExpectedVersion: maxExpectedVersion,
	})
	if err != nil {
		return 0, err
	}
	return result, nil
}

// WithTx owns the lifecycle of a WorkItem transaction. The callback runs with
// the transaction opened by the store; a callback error rolls the transaction
// back and a successful callback is committed. Callers must not commit or roll
// back the transaction themselves.
func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if fn == nil {
		return errors.New("work item transaction callback is nil")
	}
	if s == nil || s.DB == nil {
		return errors.New("work item store database is nil")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin work item transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit work item transaction: %w", err)
	}
	return nil
}

// Create inserts one live WorkItem inside the caller's transaction. A second
func (s *Store) Create(ctx context.Context, tx *sql.Tx, spec WorkItemSpec) (string, error) {
	if tx == nil {
		return "", errors.New("work item create requires a transaction")
	}
	if err := spec.validate(); err != nil {
		return "", err
	}
	snapshot := spec.InputSnapshot
	if len(snapshot) == 0 {
		snapshot = json.RawMessage(`{}`)
	}
	if spec.OneShot != nil {
		merged, err := mergeOneShotSnapshot(snapshot, spec.OneShot)
		if err != nil {
			return "", err
		}
		snapshot = merged
	}
	id := uuid.NewString()
	returnedID, err := persistence.New(tx).CreateWorkItem(ctx, persistence.CreateWorkItemParams(workItemParams(id, spec, snapshot, s.RetentionDays)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrLiveItemConflict
		}
		return "", fmt.Errorf("create work item: %w", err)
	}
	s.logEvent(spec.Owner, returnedID, "create", "work_item_created", "idempotency_key_hash="+keyHash(spec.IdempotencyKey), 0)
	return returnedID, nil
}

// workItemCreateRow is the one parameter shape of work_item_create/merge.
type workItemCreateRow struct {
	ID                    string
	OwnerType             string
	OwnerID               string
	Kind                  string
	IdempotencyKey        string
	MaxAttempts           int32
	InputSnapshot         json.RawMessage
	ExpectedOwnerVersion  int64
	AvailableAfterSeconds float64
	RetentionDays         int16
}

func workItemParams(id string, spec WorkItemSpec, snapshot json.RawMessage, retentionDays int) workItemCreateRow {
	return workItemCreateRow{
		ID:                    id,
		OwnerType:             spec.Owner.Type,
		OwnerID:               spec.Owner.ID,
		Kind:                  spec.Kind,
		IdempotencyKey:        spec.IdempotencyKey,
		MaxAttempts:           int32(spec.MaxAttempts),
		InputSnapshot:         snapshot,
		ExpectedOwnerVersion:  spec.ExpectedOwnerVersion,
		AvailableAfterSeconds: spec.AvailableAfter.Seconds(),
		RetentionDays:         int16(retentionDays),
	}
}

// CreateOrMerge inserts a live WorkItem or refreshes the matching live effect
// inside the caller's transaction. Available items become immediately due with
// the latest owner version/input. A leased item has its lease expired so the
// old claim is fenced out and the next reclaim observes the refreshed inputs.
// The idempotency key must match: a different effect never merges by owner.
func (s *Store) CreateOrMerge(ctx context.Context, tx *sql.Tx, spec WorkItemSpec) (id string, created bool, err error) {
	if tx == nil {
		return "", false, errors.New("work item create-or-merge requires a transaction")
	}
	if err := spec.validate(); err != nil {
		return "", false, err
	}
	snapshot := spec.InputSnapshot
	if len(snapshot) == 0 {
		snapshot = json.RawMessage(`{}`)
	}
	if spec.OneShot != nil {
		merged, err := mergeOneShotSnapshot(snapshot, spec.OneShot)
		if err != nil {
			return "", false, err
		}
		snapshot = merged
	}
	candidateID := uuid.NewString()
	returnedID, err := persistence.New(tx).MergeWorkItem(ctx, persistence.MergeWorkItemParams(workItemParams(candidateID, spec, snapshot, s.RetentionDays)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, ErrLiveItemConflict
		}
		if isUniqueViolation(err) {
			return "", false, ErrLiveItemConflict
		}
		return "", false, fmt.Errorf("create or merge work item: %w", err)
	}
	created = returnedID == candidateID
	operation := "merge"
	if created {
		operation = "create"
	}
	s.logEvent(spec.Owner, returnedID, operation, "work_item_"+operation+"d", "idempotency_key_hash="+keyHash(spec.IdempotencyKey), 0)
	return returnedID, created, nil
}

// ClaimDue leases only runnable business work. Every claim increments attempt
// and fencing exactly once. Cancelled or exhausted expired rows are quarantined
// for typed recovery, never reclaimed into another business execution.
func (s *Store) ClaimDue(ctx context.Context, workerID string, kinds []string, lease time.Duration) (Claim, error) {
	if len(kinds) == 0 {
		return Claim{}, fmt.Errorf("%w: allowed kinds are required", ErrInvalidSpec)
	}
	for _, kind := range kinds {
		if !kindAllowed(kind) {
			return Claim{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidSpec, kind)
		}
	}
	if lease <= 0 || lease > time.Hour {
		return Claim{}, fmt.Errorf("%w: lease duration is outside the allowed bounds", ErrInvalidSpec)
	}
	if err := persistence.New(s.DB).BlockUnrunnableWorkItems(ctx, kinds); err != nil {
		return Claim{}, fmt.Errorf("isolate unrunnable work items: %w", err)
	}
	row, err := persistence.New(s.DB).ClaimDue(ctx, persistence.ClaimDueParams{
		Kinds:        kinds,
		LeaseSeconds: lease.Seconds(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, ErrNoDueWork
	}
	if err != nil {
		return Claim{}, fmt.Errorf("claim due work item: %w", err)
	}
	claim := Claim{
		ID:                   row.ID,
		Owner:                OwnerRef{Type: row.OwnerType, ID: row.OwnerID},
		Kind:                 row.Kind,
		ExpectedOwnerVersion: row.ExpectedOwnerVersion,
		FencingToken:         row.FencingToken,
		Attempt:              int(row.Attempt),
		MaxAttempts:          int(row.MaxAttempts),
		IdempotencyKey:       row.IdempotencyKey,
		InputSnapshot:        json.RawMessage(row.InputSnapshot),
		CancelRequested:      row.CancelRequested,
		ManagedAnimeID:       row.ManagedAnimeID,
		OwnerLifecycleEpoch:  row.OwnerLifecycleEpoch,
	}
	// Structural gate precondition: a snapshot declaring a one_shot object
	// must carry the complete marker reference. A malformed reference fails
	// the claim closed (the item stays leased until normal expiry) instead of
	// dispatching an ungated handler.
	ref, err := parseOneShotRef(claim.InputSnapshot)
	if err != nil {
		s.logEvent(claim.Owner, claim.ID, "claim", "one_shot_claim_invalid", "malformed one_shot input snapshot; claim left for lease expiry", claim.Attempt)
		return Claim{}, err
	}
	claim.OneShot = ref
	claimOperation := "claim"
	if row.StateBefore == StateLeased {
		claimOperation = "reclaim"
	}
	s.logEvent(claim.Owner, claim.ID, claimOperation, "work_item_claimed", fmt.Sprintf("state_before=%s attempt=%d max_attempts=%d fencing_token=%d worker_id=%s idempotency_key_hash=%s", row.StateBefore, claim.Attempt, claim.MaxAttempts, claim.FencingToken, workerID, keyHash(claim.IdempotencyKey)), claim.Attempt)
	return claim, nil
}

// AdmitManagedAnimeEffect is the persistent-owner effect gate. It serializes
// with retirement on the lifecycle row and records that the current fenced
// claim may already have produced an external effect. A retired/stale owner is
// rejected before its handler runs.
func (s *Store) AdmitManagedAnimeEffect(ctx context.Context, claim Claim) error {
	if strings.TrimSpace(claim.ManagedAnimeID) == "" {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := lockClaim(ctx, tx, claim); err != nil {
			return err
		}
		return tx.Commit()
	}
	_, err := persistence.New(s.DB).AdmitManagedAnimeEffect(ctx, persistence.AdmitManagedAnimeEffectParams{
		EffectBearing: KindHasExternalEffect(claim.Kind),
		WorkItemID:    claim.ID,
		FencingToken:  claim.FencingToken,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOwnerRetired
	}
	if err != nil {
		return fmt.Errorf("admit managed anime effect: %w", err)
	}
	return nil
}

// Finish commits the owner result and terminal WorkItem state in one fenced
// transaction. Periodic scheduling is owned by fixed automation dispatch, so a
// handler never creates another future WorkItem. A stale fencing token changes
// nothing.
func (s *Store) Finish(ctx context.Context, claim Claim, terminalState string, writeOwner OwnerWriter) error {
	return s.FinishWithResult(ctx, claim, terminalState, OwnerResult{State: terminalState}, writeOwner)
}

// FinishWithResult is the typed-result variant used by handlers that need to
// persist owner reason fields together with the terminal WorkItem transition.
// The WorkItem terminal state is separate from the owner state because product
// owners have their own allowlisted state sets.
func (s *Store) FinishWithResult(ctx context.Context, claim Claim, terminalState string, ownerResult OwnerResult, writeOwner OwnerWriter) error {
	return s.finishWithResult(ctx, claim, terminalState, ownerResult, writeOwner, nil)
}

// FinishWithResultPostTransition finishes the claim exactly like
// FinishWithResult and then runs postTransition inside the SAME fencing
// transaction, after the WorkItem terminal transition and before commit.
// This is the only point where a handler may durably create a follow-up row
// that shares the (owner, kind) live-item uniqueness space: by the time
// postTransition runs, the current item is already terminal, so the typed
// continuation insert cannot collide with the still-leased claim row.
func (s *Store) FinishWithResultPostTransition(ctx context.Context, claim Claim, terminalState string, ownerResult OwnerResult, writeOwner OwnerWriter, postTransition func(context.Context, txscope.Tx) error) error {
	if postTransition == nil {
		return fmt.Errorf("%w: post-transition hook is required", ErrInvalidSpec)
	}
	return s.finishWithResult(ctx, claim, terminalState, ownerResult, writeOwner, postTransition)
}

func (s *Store) finishWithResult(ctx context.Context, claim Claim, terminalState string, ownerResult OwnerResult, writeOwner OwnerWriter, postTransition func(context.Context, txscope.Tx) error) error {
	if terminalState != StateSucceeded && terminalState != StateFailed {
		return fmt.Errorf("%w: terminal state must be succeeded or failed", ErrInvalidSpec)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finish transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockClaim(ctx, tx, claim); err != nil {
		return err
	}
	ownerVersion := claim.ExpectedOwnerVersion
	if writeOwner != nil {
		ownerVersion, err = writeOwner(ctx, txscope.Wrap(tx), ownerResult)
		if err != nil {
			return fmt.Errorf("write owner result: %w", err)
		}
	}
	if err := persistence.New(tx).FinishWorkItem(ctx, persistence.FinishWorkItemParams{
		ID:                   claim.ID,
		State:                terminalState,
		ExpectedOwnerVersion: ownerVersion,
	}); err != nil {
		return fmt.Errorf("finish work item: %w", err)
	}
	if postTransition != nil {
		if err := postTransition(ctx, txscope.Wrap(tx)); err != nil {
			return fmt.Errorf("run finish post-transition: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit finish transaction: %w", err)
	}
	s.logEvent(claim.Owner, claim.ID, "finish", "work_item_finished", fmt.Sprintf("state_after=%s attempt=%d fencing_token=%d idempotency_key_hash=%s", terminalState, claim.Attempt, claim.FencingToken, keyHash(claim.IdempotencyKey)), claim.Attempt)
	return nil
}

// LockClaimTx locks and validates the leased claim inside a transaction the
// caller owns, for a handler that must take its own owner locks before the
// work item (the subscription competition locks the subscription row first,
// the order of every producer that merges into it). The caller ends the claim
// with FinishLockedTx in the same transaction and reports the commit as a
// worker.Committed outcome.
func (s *Store) LockClaimTx(ctx context.Context, tx *sql.Tx, claim Claim) error {
	if tx == nil {
		return errors.New("work item claim lock requires a transaction")
	}
	return lockClaim(ctx, tx, claim)
}

// FinishLockedTx records the terminal transition of a claim the caller locked
// with LockClaimTx in the same transaction.
func (s *Store) FinishLockedTx(ctx context.Context, tx *sql.Tx, claim Claim, terminalState string, ownerVersion int64) error {
	if terminalState != StateSucceeded && terminalState != StateFailed {
		return fmt.Errorf("%w: terminal state must be succeeded or failed", ErrInvalidSpec)
	}
	if err := persistence.New(tx).FinishWorkItem(ctx, persistence.FinishWorkItemParams{
		ID:                   claim.ID,
		State:                terminalState,
		ExpectedOwnerVersion: ownerVersion,
	}); err != nil {
		return fmt.Errorf("finish work item: %w", err)
	}
	s.logEvent(claim.Owner, claim.ID, "finish", "work_item_finished", fmt.Sprintf("state_after=%s attempt=%d fencing_token=%d idempotency_key_hash=%s", terminalState, claim.Attempt, claim.FencingToken, keyHash(claim.IdempotencyKey)), claim.Attempt)
	return nil
}

// MutateClaim runs one narrow owner mutation in its own fenced transaction.
// It locks the ManagedAnime lifecycle before the leased WorkItem, invokes the typed
// owner writer, synchronizes expected_owner_version (including when the version is
// unchanged), commits, and returns the claim snapshot subsequent phases must use.
func (s *Store) MutateClaim(ctx context.Context, claim Claim, writeOwner OwnerWriter) (Claim, error) {
	if writeOwner == nil {
		return Claim{}, errors.New("fenced work item mutation requires an owner writer")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, fmt.Errorf("begin fenced mutation transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockClaim(ctx, tx, claim); err != nil {
		return Claim{}, err
	}
	ownerVersion, err := writeOwner(ctx, txscope.Wrap(tx), OwnerResult{})
	if err != nil {
		return Claim{}, fmt.Errorf("write fenced owner mutation: %w", err)
	}
	if ownerVersion < 1 {
		return Claim{}, fmt.Errorf("%w: owner version must be positive", ErrInvalidSpec)
	}
	result, err := persistence.New(tx).SynchronizeWorkItemOwnerVersion(ctx, persistence.SynchronizeWorkItemOwnerVersionParams{
		OwnerVersion:                ownerVersion,
		ID:                          claim.ID,
		FencingToken:                claim.FencingToken,
		CurrentExpectedOwnerVersion: claim.ExpectedOwnerVersion,
	})
	if err != nil {
		return Claim{}, fmt.Errorf("synchronize work item owner version: %w", err)
	}
	rows := result
	if rows != 1 {
		return Claim{}, ErrStaleClaim
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, fmt.Errorf("commit fenced mutation transaction: %w", err)
	}
	claim.ExpectedOwnerVersion = ownerVersion
	return claim, nil
}

// AdvanceClaimOwnerVersion updates the leased WorkItem's expected owner
// version while retaining the same claim and fencing token. Owner code uses
// this when it durably freezes an input snapshot before the final effect.
// It applies the same lifecycle-then-WorkItem lock order as every other fenced
// mutation, so a checkpoint cannot write a retired owner's claim: the caller
// sees ErrOwnerRetired instead of a half-applied checkpoint.
func (s *Store) AdvanceClaimOwnerVersion(ctx context.Context, scope txscope.Tx, claim Claim, ownerVersion int64) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("advance work item owner version requires a transaction")
	}
	if ownerVersion < 1 {
		return fmt.Errorf("%w: owner version must be positive", ErrInvalidSpec)
	}
	if err := lockClaim(ctx, tx, claim); err != nil {
		return err
	}
	result, err := persistence.New(tx).SynchronizeWorkItemOwnerVersion(ctx, persistence.SynchronizeWorkItemOwnerVersionParams{
		OwnerVersion:                ownerVersion,
		ID:                          claim.ID,
		FencingToken:                claim.FencingToken,
		CurrentExpectedOwnerVersion: claim.ExpectedOwnerVersion,
	})
	if err != nil {
		return fmt.Errorf("advance work item owner version: %w", err)
	}
	rows := result
	if rows != 1 {
		return ErrStaleClaim
	}
	return nil
}

// RetryOutcome reports whether the retry returned the item to available or
// exhausted its attempt budget and terminally failed it.
type RetryOutcome struct {
	Terminal bool
}

// RetryWithoutAttemptBudget preserves unresolved effect identity. At the business
// budget it quarantines rather than terminally failing or re-running Handle.
// Typed effect reconciliation must resolve the preserved evidence explicitly.
func (s *Store) RetryWithoutAttemptBudget(ctx context.Context, claim Claim, reasonCode, reason string, delay time.Duration, writeOwner OwnerWriter) (RetryOutcome, error) {
	if claim.Attempt >= claim.MaxAttempts {
		return RetryOutcome{}, s.BlockRecovery(ctx, claim, "effect_unknown")
	}
	return s.retry(ctx, claim, reasonCode, reason, delay, writeOwner, false)
}

// Retry handles a transient handler failure in one fenced transaction: the
// owner's visible retry state/reason/version advances, the same item returns
// to available with a database-time future available_at, and the stable
// effect key is preserved. Attempt is never incremented here; the next
// successful claim increments it. At the attempt budget the same transaction
// marks the item failed and writes the owner's terminal result.
func (s *Store) Retry(ctx context.Context, claim Claim, reasonCode, reason string, delay time.Duration, writeOwner OwnerWriter) (RetryOutcome, error) {
	return s.retry(ctx, claim, reasonCode, reason, delay, writeOwner, true)
}

func (s *Store) retry(ctx context.Context, claim Claim, reasonCode, reason string, delay time.Duration, writeOwner OwnerWriter, enforceAttemptBudget bool) (RetryOutcome, error) {
	if strings.TrimSpace(reasonCode) == "" || len(reasonCode) > 128 {
		return RetryOutcome{}, fmt.Errorf("%w: reason code is required and bounded", ErrInvalidSpec)
	}
	if len(reason) > 2048 {
		return RetryOutcome{}, fmt.Errorf("%w: reason is outside the allowed bounds", ErrInvalidSpec)
	}
	if delay < 0 || delay > 24*time.Hour {
		return RetryOutcome{}, fmt.Errorf("%w: retry delay is outside the allowed bounds", ErrInvalidSpec)
	}
	terminal := enforceAttemptBudget && claim.Attempt >= claim.MaxAttempts
	ownerState := "waiting_retry"
	if terminal {
		ownerState = StateFailed
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RetryOutcome{}, fmt.Errorf("begin retry transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockClaim(ctx, tx, claim); err != nil {
		return RetryOutcome{}, err
	}
	// A handler may durably advance its owner's version in a fenced mutation
	// before it returns the final retry outcome (for example, qB freezes the
	// path snapshot before attempting an upload). In that case the owner
	// writer captures the advanced claim, while the worker still carries the
	// original claim value. Keep the fencing-token check above, but use the
	// row's current expected version for the final WorkItem CAS when an owner
	// writer is present. A nil writer remains strict: an old claim must never
	// transition a WorkItem whose owner version has advanced.
	var currentExpectedVersion int64
	if currentExpectedVersion, err = persistence.New(tx).ReadCurrentWorkItemOwnerVersion(ctx, claim.ID); err != nil {
		return RetryOutcome{}, fmt.Errorf("read current work item owner version: %w", err)
	}
	if writeOwner == nil && currentExpectedVersion != claim.ExpectedOwnerVersion {
		return RetryOutcome{}, ErrStaleClaim
	}
	ownerVersion := claim.ExpectedOwnerVersion
	if writeOwner != nil {
		ownerVersion, err = writeOwner(ctx, txscope.Wrap(tx), OwnerResult{State: ownerState, ReasonCode: reasonCode, Reason: reason})
		if err != nil {
			return RetryOutcome{}, fmt.Errorf("write owner retry result: %w", err)
		}
		if ownerVersion < currentExpectedVersion {
			return RetryOutcome{}, ErrStaleClaim
		}
	}
	if terminal {
		result, err := persistence.New(tx).FailWorkItemAtAttemptBudget(ctx, persistence.FailWorkItemAtAttemptBudgetParams{
			OwnerVersion:           ownerVersion,
			ReasonCode:             reasonCode,
			Reason:                 reason,
			ID:                     claim.ID,
			CurrentExpectedVersion: currentExpectedVersion,
		})
		if err != nil {
			return RetryOutcome{}, fmt.Errorf("fail work item at attempt budget: %w", err)
		}
		affected := result
		if affected != 1 {
			return RetryOutcome{}, ErrStaleClaim
		}
	} else {
		result, err := persistence.New(tx).ReturnWorkItemToAvailable(ctx, persistence.ReturnWorkItemToAvailableParams{
			DelaySeconds:           delay.Seconds(),
			OwnerVersion:           ownerVersion,
			ReasonCode:             reasonCode,
			Reason:                 reason,
			ID:                     claim.ID,
			CurrentExpectedVersion: currentExpectedVersion,
		})
		if err != nil {
			return RetryOutcome{}, fmt.Errorf("return work item to available: %w", err)
		}
		affected := result
		if affected != 1 {
			return RetryOutcome{}, ErrStaleClaim
		}
	}
	if err := tx.Commit(); err != nil {
		return RetryOutcome{}, fmt.Errorf("commit retry transaction: %w", err)
	}
	stateAfter := StateAvailable
	if terminal {
		stateAfter = StateFailed
	}
	retryOperation := "retry"
	if terminal {
		retryOperation = "retry_exhausted"
	}
	s.logEvent(claim.Owner, claim.ID, retryOperation, "work_item_retried", fmt.Sprintf("state_after=%s reason_code=%s attempt=%d max_attempts=%d fencing_token=%d retryable=%t idempotency_key_hash=%s", stateAfter, reasonCode, claim.Attempt, claim.MaxAttempts, claim.FencingToken, !terminal, keyHash(claim.IdempotencyKey)), claim.Attempt)
	return RetryOutcome{Terminal: terminal}, nil
}

const maxRetentionBatchSize = 500

// PurgeExpiredTx deletes only expired terminal ordinary WorkItems in the
// caller-owned transaction. One-shot-marked snapshots remain under the
// independent OneShotReclaimService, and live rows/product owners are never
// touched.
func (s *Store) PurgeExpiredTx(ctx context.Context, scope txscope.Tx, batchSize int) (int64, error) {
	if s == nil {
		return 0, errors.New("work item store is nil")
	}
	if batchSize < 1 || batchSize > maxRetentionBatchSize {
		return 0, fmt.Errorf("work item retention batch size must be between 1 and %d", maxRetentionBatchSize)
	}
	tx := scope.SQLTx()
	if tx == nil {
		return 0, errors.New("purge expired work items requires a transaction")
	}
	result, err := persistence.New(tx).PurgeExpiredWorkItems(ctx, int32(batchSize))
	if err != nil {
		return 0, fmt.Errorf("purge expired work items: %w", err)
	}
	purged := result
	return purged, nil
}

// PurgeExpired is retained as a compatibility wrapper for callers outside the
// generic janitor. Production scheduling uses PurgeExpiredTx through the
// code-owned retention registry.
func (s *Store) PurgeExpired(ctx context.Context) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, errors.New("work item store database is nil")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin work item retention purge: %w", err)
	}
	defer tx.Rollback()
	purged, err := s.PurgeExpiredTx(ctx, txscope.Wrap(tx), DefaultRetentionBatchSize)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit work item retention purge: %w", err)
	}
	return purged, nil
}

// RenewLease extends the claim lease and heartbeat while the claim is still
// active, the fencing token matches, the lease is unexpired and no cancel was
// requested. Any violation fails closed with ErrStaleClaim (the handler must
// tear down; the item becomes reclaimable through the normal claim path).
func (s *Store) RenewLease(ctx context.Context, claim Claim, extendTo time.Time) error {
	result, err := persistence.New(s.DB).RenewWorkItemLease(ctx, persistence.RenewWorkItemLeaseParams{
		ID:           claim.ID,
		ExtendTo:     extendTo,
		FencingToken: claim.FencingToken,
	})
	if err != nil {
		return fmt.Errorf("renew work item lease: %w", err)
	}
	affected := result
	if affected == 0 {
		return fmt.Errorf("%w: claim %s lost lease, expired, cancelled, or token mismatch", ErrStaleClaim, claim.ID)
	}
	s.logEvent(claim.Owner, claim.ID, "heartbeat", "lease_heartbeat_renewed", fmt.Sprintf("fencing_token=%d lease_until=%s idempotency_key_hash=%s", claim.FencingToken, extendTo.UTC().Format(time.RFC3339), keyHash(claim.IdempotencyKey)), 0)
	return nil
}

// RequestCancel marks a single leased claim as cancelled so its heartbeat
// fails closed and the handler context is torn down.
func (s *Store) RequestCancel(ctx context.Context, claim Claim, reason string) error {
	result, err := persistence.New(s.DB).RequestWorkItemCancel(ctx, persistence.RequestWorkItemCancelParams{
		ID:           claim.ID,
		FencingToken: claim.FencingToken,
	})
	if err != nil {
		return fmt.Errorf("request work item cancel: %w", err)
	}
	affected := result
	if affected == 0 {
		return fmt.Errorf("%w: claim %s not active or token mismatch", ErrStaleClaim, claim.ID)
	}
	s.logEvent(claim.Owner, claim.ID, "cancel", "work_item_cancel_requested", "reason="+reason, 0)
	return nil
}

// CancelRequests marks every available/leased item of the given kinds as
// cancelled (maintenance/drain entry). Returns the number of items marked.
func (s *Store) CancelRequests(ctx context.Context, kinds []string, reason string) (int64, error) {
	if len(kinds) == 0 {
		return 0, nil
	}
	result, err := persistence.New(s.DB).CancelWorkItemRequests(ctx, kinds)
	if err != nil {
		return 0, fmt.Errorf("cancel work item requests: %w", err)
	}
	count := result
	s.logEvent(OwnerRef{Type: "batch", ID: "kinds"}, "", "cancel_requests", "work_items_cancel_requested", fmt.Sprintf("kinds=%d count=%d reason=%s", len(kinds), count, reason), 0)
	return count, nil
}

// lockClaim locks and validates the current leased row using PostgreSQL's
// current timestamp. State, fencing token, and an unexpired lease are one
// condition, so an expired claim cannot write owner or WorkItem state.
func lockClaim(ctx context.Context, tx *sql.Tx, claim Claim) error {
	queries := persistence.New(tx)
	if strings.TrimSpace(claim.ManagedAnimeID) != "" {
		if _, err := queries.LockManagedAnimeLifecycleClaim(ctx, persistence.LockManagedAnimeLifecycleClaimParams{
			ID:             claim.ID,
			ManagedAnimeID: sql.NullString{String: claim.ManagedAnimeID, Valid: true},
		}); errors.Is(err, sql.ErrNoRows) {
			return ErrOwnerRetired
		} else if err != nil {
			return fmt.Errorf("lock managed anime lifecycle claim: %w", err)
		}
	}
	_, err := queries.LockWorkItemClaim(ctx, persistence.LockWorkItemClaimParams{
		ID:           claim.ID,
		FencingToken: claim.FencingToken,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStaleClaim
	}
	if err != nil {
		return fmt.Errorf("lock work item claim: %w", err)
	}
	return nil
}

func kindAllowed(kind string) bool {
	for _, shape := range ownerKinds {
		for _, candidate := range shape.kinds {
			if candidate == kind {
				return true
			}
		}
	}
	return false
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func keyHash(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:6])
}

func (s *Store) logEvent(owner OwnerRef, workItemID, operation, reasonCode, summary string, attempt int) {
	if s == nil || s.Logger == nil {
		return
	}
	event := observabilityport.RuntimeEvent{
		Message:    "work item " + operation,
		Component:  "workitems",
		Operation:  operation,
		Outcome:    "info",
		ReasonCode: reasonCode,
		OwnerType:  owner.Type,
		OwnerID:    owner.ID,
		WorkItemID: workItemID,
		Attempt:    attempt,
	}
	if summary != "" {
		event.Message = "work item " + operation + " " + summary
	}
	s.Logger.Info(context.Background(), event)
}
