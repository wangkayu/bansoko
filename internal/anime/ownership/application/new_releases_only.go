package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// NewReleasesOnlyInput is the CAS-guarded new-releases-only change of one pending
// subscription.
type NewReleasesOnlyInput struct {
	ExpectedVersion int64
	Enabled         bool
}

// NewReleasesOnlyLockedError refuses a change of the option after the first
// start: it is frozen then; delete the instance and subscribe again instead.
func NewReleasesOnlyLockedError() error {
	return &apperrors.Business{
		Status:  409,
		Code:    "NEW_RELEASES_ONLY_LOCKED",
		Message: "New releases only cannot be changed after the subscription has started. Delete the subscription and subscribe again to change it.",
	}
}

// SetNewReleasesOnly turns the new-releases-only option on or off while the
// subscription is pending (never started). The baseline stays the instance
// creation time whenever the option is on, even when it is turned on later
// while pending; the database guard refuses any change after start.
func (s *Service) SetNewReleasesOnly(ctx context.Context, managedAnimeID string, input NewReleasesOnlyInput) (Subscription, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, fmt.Errorf("begin new releases only change: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return Subscription{}, err
	}
	var result Subscription
	var state, lifecycleState string
	var originEntryRef, instanceLabel, entrySeason sql.NullString
	var createdAt time.Time
	var retiredAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
SELECT s.id, s.origin, s.origin_entry_ref, s.instance_label, s.entry_season, s.created_at, s.state, s.version,
       ma.lifecycle_state, ma.fence_epoch, ma.retired_at
FROM subscriptions s
JOIN managed_anime ma ON ma.id = s.managed_anime_id
WHERE s.managed_anime_id = $1
FOR UPDATE OF s`, managedAnimeID).Scan(&result.ID, &result.Origin, &originEntryRef, &instanceLabel, &entrySeason, &createdAt, &state, &result.Version,
		&lifecycleState, &result.LifecycleEpoch, &retiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, &apperrors.NotFound{Resource: "subscription", ID: managedAnimeID}
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("lock subscription: %w", err)
	}
	if result.Version != input.ExpectedVersion {
		return Subscription{}, &apperrors.VersionConflict{Owner: "subscription", CurrentVersion: result.Version}
	}
	if lifecycleState == "retired" {
		return Subscription{}, animeapplication.ManagedAnimeRetiredError()
	}
	if state != string(subscriptiondomain.SubscriptionPending) {
		return Subscription{}, NewReleasesOnlyLockedError()
	}
	var baseline sql.NullTime
	if err := tx.QueryRowContext(ctx, `
UPDATE subscriptions
SET new_releases_only = $2,
    start_baseline_at = CASE WHEN $2 THEN created_at END,
    version = version + 1, updated_at = NOW()
WHERE id = $1
RETURNING version, new_releases_only, start_baseline_at`, result.ID, input.Enabled).Scan(&result.Version, &result.NewReleasesOnly, &baseline); err != nil {
		return Subscription{}, fmt.Errorf("save new releases only: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, fmt.Errorf("commit new releases only change: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetCatalog)
	result.ManagedAnimeID = managedAnimeID
	result.State = subscriptiondomain.SubscriptionPending
	result.LifecycleState = lifecycleState
	if baseline.Valid {
		result.StartBaselineAt = &baseline.Time
	}
	if retiredAt.Valid {
		result.RetiredAt = &retiredAt.Time
	}
	if originEntryRef.Valid {
		result.OriginEntryRef = &originEntryRef.String
	}
	if instanceLabel.Valid {
		result.InstanceLabel = &instanceLabel.String
	}
	if entrySeason.Valid {
		result.EntrySeason = &entrySeason.String
	}
	result.CreatedAt = createdAt
	return result, nil
}
