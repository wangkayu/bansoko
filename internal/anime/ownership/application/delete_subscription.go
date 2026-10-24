package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/workitems"
)

// DeleteAnimeSubscription is the reviewed compatibility name for explicit
// ManagedAnime retirement. Retirement is irreversible and separate from the
// Subscription active/stopped state: it keeps the complete local owner graph
// inspectable, fences stale writers, and terminates only work that has not been
// admitted to an external effect. Physical qBittorrent data and media files are
// never touched by this transaction.
//
// Lock order: [one-shot marker] -> lifecycle -> the complete unadmitted
// WorkItem set NOWAIT -> subscription -> candidate -> fixed work termination.
// A set conflict rolls back this entire prefix before a bounded full retry.
// Lifecycle fences and qualification SHARE locks remain unchanged.
func (s *Service) DeleteAnimeSubscription(ctx context.Context, managedAnimeID string, input SubscriptionDeleteInput) error {
	if s == nil || s.db == nil {
		return errors.New("anime service database is nil")
	}
	if s.oneShotMarkers == nil {
		return errors.New("one-shot marker repository is not configured")
	}
	for attempt := 0; attempt < 5; attempt++ {
		err := s.deleteAnimeSubscriptionAttempt(ctx, managedAnimeID, input)
		if errors.Is(err, workitems.ErrRetirementContention) && attempt == 4 {
			return &apperrors.Business{Status: 409, Code: "MANAGED_ANIME_RETIREMENT_BUSY", Message: "Retirement is busy; retry the request."}
		}
		if !errors.Is(err, workitems.ErrRetirementContention) {
			return err
		}
		timer := time.NewTimer((20 * time.Millisecond) << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable retirement attempt limit")
}

// Each return runs Rollback before the caller considers a complete retry.
func (s *Service) deleteAnimeSubscriptionAttempt(ctx context.Context, managedAnimeID string, input SubscriptionDeleteInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed anime retirement: %w", err)
	}
	defer tx.Rollback()

	oneShotState, markerFound, err := s.oneShotMarkers.LockStateByManagedAnimeID(ctx, txscope.Wrap(tx), managedAnimeID)
	if err != nil {
		return err
	}

	var lifecycleState string
	var fenceEpoch int64
	err = tx.QueryRowContext(ctx, `
SELECT lifecycle_state, fence_epoch
FROM managed_anime
WHERE id = $1
FOR UPDATE`, managedAnimeID).Scan(&lifecycleState, &fenceEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	if err != nil {
		return fmt.Errorf("lock lifecycle for retirement: %w", err)
	}

	if lifecycleState == "active" {
		if err := workitems.LockRetirementSetTx(ctx, tx, managedAnimeID, fenceEpoch); err != nil {
			return fmt.Errorf("prelock schedulable work for retirement: %w", err)
		}
	}

	var subscriptionID string
	var subscriptionVersion int64
	err = tx.QueryRowContext(ctx, `
SELECT s.id, s.version
FROM subscriptions s
WHERE s.managed_anime_id = $1
FOR UPDATE`, managedAnimeID).Scan(&subscriptionID, &subscriptionVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return s.subscriptionMissing(ctx, managedAnimeID)
	}
	if err != nil {
		return fmt.Errorf("lock subscription for retirement: %w", err)
	}
	if subscriptionVersion != input.ExpectedVersion {
		return &apperrors.VersionConflict{Owner: "subscription", CurrentVersion: subscriptionVersion}
	}
	if lifecycleState == "retired" {
		if err := archiveRetiredProviderCandidate(ctx, tx, managedAnimeID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if markerFound && oneShotState != "cleaned" {
		return &apperrors.Business{
			Status:  409,
			Code:    "RETIREMENT_ONE_SHOT_ACTIVE",
			Message: "The anime is owned by an unfinished one-shot acquisition and must be reclaimed through its marker lifecycle.",
		}
	}

	if err := archiveRetiredProviderCandidate(ctx, tx, managedAnimeID); err != nil {
		return err
	}
	// Terminate only work that never reached an external effect. This runs while
	// the lifecycle is still active at the locked epoch: the WorkItem fence
	// accepts the write for the current owner, and the epoch advance below makes
	// every later Finish/Retry/Checkpoint of these rows stale.
	if err := workitems.RetireUnadmittedTx(ctx, tx, managedAnimeID, fenceEpoch); err != nil {
		return fmt.Errorf("isolate schedulable work for retirement: %w", err)
	}
	if s.notifications != nil {
		// The notice row names no managed anime (retirement fences new rows
		// that name it); the anime travels in the payload.
		var animeTitle string
		if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(title_override, ''), title) FROM managed_anime WHERE id = $1`, managedAnimeID).Scan(&animeTitle); err != nil {
			return fmt.Errorf("read retired anime title: %w", err)
		}
		if err := s.admitSubscriptionChangeTx(ctx, tx, subscriptionID, subscriptionVersion, managedAnimeID, animeTitle, plugintargetport.SubscriptionRetired); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state = 'retired',
    fence_epoch = $2 + 1,
    retired_at = CURRENT_TIMESTAMP,
    retired_subscription_version = $3,
    lifecycle_version = lifecycle_version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND lifecycle_state = 'active' AND fence_epoch = $2`, managedAnimeID, fenceEpoch, subscriptionVersion); err != nil {
		return fmt.Errorf("write managed anime retirement fact: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit managed anime retirement: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetCatalog, events.TargetRSS)
	return nil
}

func archiveRetiredProviderCandidate(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_config_candidates
SET activated_managed_anime_id = NULL,
    status = 'archived',
    cleanup_eligible_at = CURRENT_TIMESTAMP,
    cleanup_reason = 'provider_owner_retired',
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE activated_managed_anime_id = $1
  AND origin IN ('anibt', 'mikan')
  AND status = 'dormant'`, managedAnimeID); err != nil {
		return fmt.Errorf("archive retired provider candidate: %w", err)
	}
	return nil
}

func (s *Service) subscriptionMissing(ctx context.Context, managedAnimeID string) error {
	var dummy string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM managed_anime WHERE id = $1`, managedAnimeID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return &apperrors.NotFound{Resource: "managed_anime", ID: managedAnimeID}
	}
	if err != nil {
		return fmt.Errorf("read managed anime for missing subscription: %w", err)
	}
	return &apperrors.NotFound{Resource: "subscription", ID: managedAnimeID}
}
