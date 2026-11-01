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

// IgnoreMultiVideoInput is the CAS-guarded ignore-multi-video change of one
// subscription, changeable in any subscription state.
type IgnoreMultiVideoInput struct {
	ExpectedVersion int64
	Enabled         bool
}

// multiVideoAwaitingSweepSQL settles the undecided releases the option being
// turned on now ignores: awaiting competition without a download task, with a
// stored manifest carrying more than two video files. The direct write mirrors
// migration 103's deselect sweep (the evaluation writer is not rerun for
// releases whose only change is this option). The video count is a scalar
// subquery: an UPDATE's FROM clause cannot reference the target alias, so the
// count cannot be a lateral join here.
const multiVideoAwaitingSweepSQL = `
UPDATE releases r
SET verdict = 'filtered', reason_code = 'multi_video_files',
    reason_detail = 'video files: ' || (
        SELECT count(*)
        FROM jsonb_array_elements(COALESCE(r.internal_facts -> 'release_files', '[]'::jsonb)) f
        WHERE f ->> 'media_kind' = 'video'
    ),
    discovery_notice_due = FALSE, competition_retry_key = NULL,
    version = r.version + 1, updated_at = CURRENT_TIMESTAMP
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = $1
  AND r.rss_source_id = src.id
  AND r.verdict = 'awaiting_competition' AND r.download_task_id IS NULL
  AND (
      SELECT count(*)
      FROM jsonb_array_elements(COALESCE(r.internal_facts -> 'release_files', '[]'::jsonb)) f
      WHERE f ->> 'media_kind' = 'video'
  ) > 2`

// multiVideoFilteredReleasesSQL re-enqueues the releases only this option
// filtered when it is turned off, so the next evaluation writes the true
// verdict. The re-evaluation scope predicate keeps the single definition of
// which releases a change of evaluation input re-evaluates.
const multiVideoFilteredReleasesSQL = `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = $1
  AND r.verdict = 'filtered' AND r.reason_code = 'multi_video_files'
  AND ` + animeapplication.ReleaseReevaluationScopeSQL + `
FOR UPDATE OF r`

// SetIgnoreMultiVideoReleases turns the ignore-multi-video option on or off in
// any subscription state. Turning it on settles undecided multi-video releases
// directly (filtered/multi_video_files); turning it off schedules a normal
// re-evaluation of the releases the option filtered. Releases with a download
// task and terminal releases are never touched, and the evaluation writer
// aborts on an option change mid-evaluation.
func (s *Service) SetIgnoreMultiVideoReleases(ctx context.Context, managedAnimeID string, input IgnoreMultiVideoInput) (Subscription, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, fmt.Errorf("begin ignore multi video change: %w", err)
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
	var baseline sql.NullTime
	err = tx.QueryRowContext(ctx, `
SELECT s.id, s.origin, s.origin_entry_ref, s.instance_label, s.entry_season, s.created_at, s.state, s.version,
       s.new_releases_only, s.start_baseline_at, s.ignore_multi_video_releases,
       ma.lifecycle_state, ma.fence_epoch, ma.retired_at
FROM subscriptions s
JOIN managed_anime ma ON ma.id = s.managed_anime_id
WHERE s.managed_anime_id = $1
FOR UPDATE OF s`, managedAnimeID).Scan(&result.ID, &result.Origin, &originEntryRef, &instanceLabel, &entrySeason, &createdAt, &state, &result.Version,
		&result.NewReleasesOnly, &baseline, &result.IgnoreMultiVideoReleases,
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
	if result.IgnoreMultiVideoReleases != input.Enabled {
		if err := tx.QueryRowContext(ctx, `
UPDATE subscriptions
SET ignore_multi_video_releases = $2,
    version = version + 1, updated_at = NOW()
WHERE id = $1
RETURNING version, ignore_multi_video_releases`, result.ID, input.Enabled).Scan(&result.Version, &result.IgnoreMultiVideoReleases); err != nil {
			return Subscription{}, fmt.Errorf("save ignore multi video: %w", err)
		}
		if input.Enabled {
			if _, err := tx.ExecContext(ctx, multiVideoAwaitingSweepSQL, managedAnimeID); err != nil {
				return Subscription{}, fmt.Errorf("sweep multi video releases: %w", err)
			}
		} else if _, err := s.scheduleUnsubmittedReleaseEvaluationCount(ctx, tx, multiVideoFilteredReleasesSQL, managedAnimeID, "ignore_multi_video_off"); err != nil {
			return Subscription{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, fmt.Errorf("commit ignore multi video change: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetRSS)
	result.ManagedAnimeID = managedAnimeID
	result.State = subscriptiondomain.SubscriptionState(state)
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
