package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/persistence/txscope"
	adapter "bansoko/internal/workitems/adapter/persistence"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
	"github.com/lib/pq"
)

// ClaimRecovery leases observation only. Attempt/effect key/cancel evidence are
// unchanged. Recovery shares the same WorkItem, fence and runtime lane slots.
func (s *Store) ClaimRecovery(ctx context.Context, kinds []string, lease time.Duration) (Claim, error) {
	if len(kinds) == 0 || lease <= 0 || lease > time.Hour {
		return Claim{}, ErrInvalidSpec
	}
	for _, kind := range kinds {
		if !kindAllowed(kind) {
			return Claim{}, ErrInvalidSpec
		}
	}
	var c Claim
	var raw []byte
	var anime sql.NullString
	var epoch sql.NullInt64
	err := (adapter.Operations{DB: s.DB}).ClaimRecoveryObservation(ctx, pq.Array(kinds), lease.Seconds()).Scan(&c.ID, &c.Owner.Type, &c.Owner.ID, &c.Kind, &c.ExpectedOwnerVersion, &c.FencingToken, &c.Attempt, &c.MaxAttempts, &c.IdempotencyKey, &raw, &c.CancelRequested, &anime, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, ErrNoDueWork
	}
	if err != nil {
		return Claim{}, err
	}
	c.InputSnapshot = json.RawMessage(raw)
	c.ManagedAnimeID = anime.String
	c.OwnerLifecycleEpoch = epoch.Int64
	c.OneShot, err = parseOneShotRef(c.InputSnapshot)
	return c, err
}

func (s *Store) DeferRecovery(ctx context.Context, c Claim, reason string, delay time.Duration) error {
	if len(reason) == 0 || len(reason) > 128 || delay <= 0 || delay > time.Hour {
		return ErrInvalidSpec
	}
	n, err := (adapter.Operations{DB: s.DB}).DeferRecoveryObservation(ctx, c.ID, c.FencingToken, delay.Seconds(), reason)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStaleClaim
	}
	return nil
}

// SettleRecovery is the only observation result writer. One-shot settlement is
// marker-first and consumes the existing lease only with positive disposition.
func (s *Store) SettleRecovery(ctx context.Context, c Claim, state string, result OwnerResult, disposition EffectDisposition, writer OwnerWriter, post func(context.Context, txscope.Tx) error) error {
	if writer == nil || (state != StateSucceeded && state != StateFailed) || !dispositionConsumesLease(disposition) || (disposition == DispositionNoExternalEffect && state != StateFailed) {
		return ErrInvalidSpec
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c.OneShot != nil {
		var markerState string
		var epoch int64
		var count int
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT state,admission_epoch,active_effect_count,active_effect_leases FROM one_shot_acquisitions WHERE id=$1 FOR UPDATE`, c.OneShot.MarkerID).Scan(&markerState, &epoch, &count, &raw); err != nil {
			return err
		}
		if epoch != c.OneShot.AdmissionEpoch || (markerState != OneShotStateRunning && markerState != OneShotStateCleanupBlocked) {
			return ErrOneShotReclaiming
		}
		var entries []oneShotLedgerEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return err
		}
		if err := validateOneShotLedger(int32(count), entries); err != nil {
			return err
		}
		var found *oneShotLedgerEntry
		for i := range entries {
			e := &entries[i]
			if e.WorkItemID == c.ID && e.ClaimedEpoch == epoch {
				if found != nil || e.OwnerType != c.Owner.Type || e.OwnerID != c.Owner.ID {
					return ErrInvalidOneShotLedger
				}
				found = e
			}
		}
		if found == nil && disposition == DispositionConfirmed && KindHasExternalEffect(c.Kind) {
			return fmt.Errorf("%w: confirmed effect lacks admission lease", ErrInvalidOneShotLedger)
		}
		if found != nil {
			marker := oneShotMarkerRow{State: markerState, Epoch: epoch, ActiveEffectCount: int32(count), Leases: entries}
			if _, err := settleOneShotLeaseLockedTx(ctx, tx, c.OneShot.MarkerID, marker, found.AdmissionID); err != nil {
				return err
			}
		}
	} else if c.ManagedAnimeID != "" {
		if _, err := persistence.New(tx).LockManagedAnimeLifecycleClaim(ctx, persistence.LockManagedAnimeLifecycleClaimParams{ID: c.ID, ManagedAnimeID: sql.NullString{String: c.ManagedAnimeID, Valid: true}}); err != nil {
			return err
		}
	}
	var version int64
	err = (adapter.Locks{DB: tx}).RecoveryObservation(ctx, c.ID, c.FencingToken, c.Owner.Type, c.Owner.ID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStaleClaim
	}
	if err != nil {
		return err
	}
	if version != c.ExpectedOwnerVersion {
		return ErrStaleClaim
	}
	next, err := writer(ctx, txscope.Wrap(tx), result)
	if err != nil {
		return err
	}
	if next < version {
		return ErrStaleClaim
	}
	err = (adapter.Operations{DB: tx}).FinishRecoveryObservation(ctx, c.ID, state, next)
	if err != nil {
		return err
	}
	if post != nil {
		if err := post(ctx, txscope.Wrap(tx)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
