package workitems

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/persistence/txscope"
	adapter "bansoko/internal/workitems/adapter/persistence"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

// BlockRecovery fences a cancelled/timed-out generation without inventing an
// external disposition or owner result. It deliberately preserves cancel and
// one-shot admission evidence. Blocked work is never selected by ClaimDue.
func (s *Store) BlockRecovery(ctx context.Context, claim Claim, reason string) error {
	if reason != "cancel_requested" && reason != "attempt_exhausted" && reason != "effect_unknown" {
		return fmt.Errorf("%w: recovery reason", ErrInvalidSpec)
	}
	result, err := persistence.New(s.DB).BlockWorkItemRecovery(ctx, persistence.BlockWorkItemRecoveryParams{ID: claim.ID, FencingToken: claim.FencingToken, Reason: reason})
	if err != nil {
		return err
	}
	n := result
	if n != 1 {
		return ErrStaleClaim
	}
	return nil
}

// ResolveBlockedFailure is an explicit owner recovery transaction, NOT an
// automatic business retry. The caller must prove no unresolved external effect
// and supply the authoritative owner writer. One-shot ledger settlement needs
// its marker-first protocol and is intentionally rejected by this ordinary port.
func (s *Store) ResolveBlockedFailure(ctx context.Context, claim Claim, disposition EffectDisposition, writeOwner OwnerWriter) error {
	if claim.OneShot != nil || disposition != DispositionNoExternalEffect || writeOwner == nil {
		return fmt.Errorf("%w: typed no-effect owner recovery required", ErrInvalidSpec)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if claim.ManagedAnimeID != "" {
		if _, err := persistence.New(tx).LockManagedAnimeLifecycleClaim(ctx, persistence.LockManagedAnimeLifecycleClaimParams{ID: claim.ID, ManagedAnimeID: sql.NullString{String: claim.ManagedAnimeID, Valid: true}}); errors.Is(err, sql.ErrNoRows) {
			return ErrOwnerRetired
		} else if err != nil {
			return err
		}
	}
	var version int64
	err = (adapter.Locks{DB: tx}).Recovery(ctx, claim.ID, claim.FencingToken, claim.Owner.Type, claim.Owner.ID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStaleClaim
	}
	if err != nil {
		return err
	}
	if version != claim.ExpectedOwnerVersion {
		return ErrStaleClaim
	}
	ownerVersion, err := writeOwner(ctx, txscope.Wrap(tx), OwnerResult{State: StateFailed, ReasonCode: "WORKER_RECOVERY_REQUIRED", Reason: "cancelled or exhausted worker execution resolved without external effect"})
	if err != nil {
		return err
	}
	if ownerVersion < version {
		return ErrStaleClaim
	}
	err = (adapter.Operations{DB: tx}).FailStaleRecovery(ctx, claim.ID, ownerVersion)
	if err != nil {
		return err
	}
	return tx.Commit()
}
