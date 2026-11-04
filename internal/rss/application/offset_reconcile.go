package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"github.com/lib/pq"
)

const offsetRetryKeyPrefix = "offset:"

// ScheduleOffsetReconciliationTx runs only after filemaintenance has completed
// every retention/alignment change in one subscription batch. It schedules the
// ordinary unsubmitted candidates and grants one persisted, automatic retry to
// a completed superseded candidate whose old/new episode is in the batch.
func (s *Service) ScheduleOffsetReconciliationTx(ctx context.Context, scope txscope.Tx, animeID, subscriptionID string, changeIDs []string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("offset reconciliation transaction is nil")
	}
	if s == nil || s.workItems == nil {
		return errors.New("offset reconciliation requires a work item store")
	}
	if len(changeIDs) == 0 {
		return nil
	}
	ordinary, err := offsetReleaseTargets(ctx, tx, `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.id = $1 AND sub.managed_anime_id = $2 AND sub.state = 'active'
  AND `+animeapplication.ReleaseReevaluationScopeSQL+`
ORDER BY r.id
FOR UPDATE OF r`, subscriptionID, animeID)
	if err != nil {
		return err
	}
	for _, target := range ordinary {
		if err := CreateOrMergeReleaseEvaluation(ctx, scope, s.workItems, target.id, target.version, "offset_reconcile"); err != nil {
			return fmt.Errorf("schedule offset candidate %s: %w", target.id, err)
		}
	}

	retryKey := offsetBatchRetryKey(changeIDs)
	superseded, err := offsetReleaseTargets(ctx, tx, `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN download_tasks task ON task.id = r.download_task_id
JOIN acquisitions acq ON acq.id = task.acquisition_id
WHERE sub.id = $1 AND sub.managed_anime_id = $2 AND sub.state = 'active'
  AND task.state = 'completed' AND acq.state = 'succeeded'
  AND r.verdict NOT IN ('adopted_existing', 'acquisition_failed', 'resume_baseline', 'start_baseline')
  AND (COALESCE(r.competition_retry_key, '') = '' OR r.competition_retry_key LIKE 'offset:%')
  AND NOT EXISTS (
      SELECT 1 FROM placements p
      WHERE p.managed_anime_id = sub.managed_anime_id AND p.quality_facts_ref = r.id
        AND p.state IN ('active', 'released', 'displaced'))
  AND NOT EXISTS (
      SELECT 1 FROM download_tasks owned
      JOIN acquisitions running ON running.id = owned.acquisition_id
      WHERE owned.release_id = r.id AND running.state = 'running')
  AND EXISTS (
      SELECT 1 FROM offset_alignment_change_keys k
      JOIN placements old ON old.quality_facts_ref = r.id AND old.state = 'superseded'
      JOIN placement_coverage pc ON pc.placement_id = old.id
      WHERE k.change_id = ANY($3)
        AND pc.observation_key IN (k.old_observation_key, k.new_observation_key))
ORDER BY r.id
FOR UPDATE OF r`, subscriptionID, animeID, pq.Array(changeIDs))
	if err != nil {
		return err
	}
	for _, target := range superseded {
		result, err := tx.ExecContext(ctx, `
UPDATE releases SET competition_retry_key = $2
WHERE id = $1 AND version = $3
  AND (COALESCE(competition_retry_key, '') = '' OR competition_retry_key LIKE 'offset:%')`, target.id, retryKey, target.version)
		if err != nil {
			return fmt.Errorf("grant offset retry to %s: %w", target.id, err)
		}
		marked, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if marked != 1 {
			continue
		}
		if err := CreateOrMergeReleaseEvaluation(ctx, scope, s.workItems, target.id, target.version, "offset_reconcile"); err != nil {
			return fmt.Errorf("schedule offset retry %s: %w", target.id, err)
		}
	}
	return nil
}

// ScheduleOffsetRetryRecoveryTx fills a missing evaluation for a persisted
// automatic qualification after a stop or process restart. A live work item is
// left untouched; awaiting releases belong to the competition and are excluded.
func (s *Service) ScheduleOffsetRetryRecoveryTx(ctx context.Context, scope txscope.Tx, subscriptionID string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("offset retry recovery transaction is nil")
	}
	if s == nil || s.workItems == nil {
		return errors.New("offset retry recovery requires a work item store")
	}
	targets, err := offsetReleaseTargets(ctx, tx, `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.id = $1 AND sub.state = 'active'
  AND r.competition_retry_key LIKE 'offset:%'
  AND r.verdict <> 'awaiting_competition'
  AND NOT EXISTS (
      SELECT 1 FROM work_items wi
      WHERE wi.owner_type = 'release' AND wi.owner_id = r.id
        AND wi.kind = 'release_evaluate' AND wi.state IN ('available', 'leased'))
ORDER BY r.id
FOR UPDATE OF r`, subscriptionID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := CreateOrMergeReleaseEvaluation(ctx, scope, s.workItems, target.id, target.version, "offset_retry_recovery"); err != nil {
			return fmt.Errorf("recover offset retry %s: %w", target.id, err)
		}
	}
	return nil
}

type offsetReleaseTarget struct {
	id      string
	version int64
}

func offsetReleaseTargets(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]offsetReleaseTarget, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read offset reconciliation candidates: %w", err)
	}
	defer rows.Close()
	var targets []offsetReleaseTarget
	for rows.Next() {
		var target offsetReleaseTarget
		if err := rows.Scan(&target.id, &target.version); err != nil {
			return nil, fmt.Errorf("scan offset reconciliation candidate: %w", err)
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func offsetBatchRetryKey(changeIDs []string) string {
	// changeIDs are sorted and locked by the batch barrier. The newest change
	// identifies this dispatch; a later batch replaces only an older offset key.
	return offsetRetryKeyPrefix + strings.TrimPrefix(changeIDs[len(changeIDs)-1], offsetRetryKeyPrefix)
}

// abandonOffsetRetryWriter withdraws an automatic qualification whose
// evaluation ended before it could produce a Release verdict. It never detaches
// a task and never touches an explicit retry key or a newer offset batch's key.
func abandonOffsetRetryWriter(snapshot releaseSnapshot) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("offset retry abandonment transaction is nil")
		}
		var subscriptionState string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE id = $1 FOR UPDATE`, snapshot.subscriptionID).Scan(&subscriptionState); err != nil {
			return 0, fmt.Errorf("lock subscription for offset retry abandonment: %w", err)
		}
		var version int64
		var storedKey string
		if err := tx.QueryRowContext(ctx, `
SELECT version, COALESCE(competition_retry_key, '') FROM releases WHERE id = $1 FOR UPDATE`, snapshot.releaseID).Scan(&version, &storedKey); err != nil {
			return 0, fmt.Errorf("lock release for offset retry abandonment: %w", err)
		}
		if subscriptionState != "active" {
			// A stop freezes an in-flight evaluation just like the main owner
			// writer. Resume decides whether the qualification still applies.
			return version, nil
		}
		return clearOffsetRetryTx(ctx, tx, snapshot.releaseID, snapshot.retryKey, storedKey, version)
	}
}

// clearOffsetRetryTx clears only the qualification this evaluation loaded.
func clearOffsetRetryTx(ctx context.Context, tx *sql.Tx, releaseID, expectedKey, storedKey string, version int64) (int64, error) {
	if !strings.HasPrefix(expectedKey, offsetRetryKeyPrefix) || storedKey != expectedKey {
		return version, nil
	}
	var updated int64
	if err := tx.QueryRowContext(ctx, `
UPDATE releases SET competition_retry_key = NULL, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $2 AND competition_retry_key = $3
RETURNING version`, releaseID, version, expectedKey).Scan(&updated); err != nil {
		return 0, fmt.Errorf("clear offset retry: %w", err)
	}
	return updated, nil
}

// detachOffsetRetriedTaskTx rechecks the automatic retry on the locked Release.
// A stale qualification is withdrawn without a verdict write; an eligible one
// loses only its current-task pointer, leaving the historical task untouched.
func detachOffsetRetriedTaskTx(ctx context.Context, tx *sql.Tx, snapshot releaseSnapshot, expectedVersion, currentVersion int64, currentTask sql.NullString, currentVerdict, storedRetryKey, subscriptionState string) (bool, int64, error) {
	if subscriptionState != "active" && storedRetryKey == snapshot.retryKey && strings.HasPrefix(storedRetryKey, offsetRetryKeyPrefix) {
		// A stop freezes the qualification with the Release. Resume schedules a
		// fresh evaluation; detaching or clearing here would strand the revival.
		return false, currentVersion, nil
	}
	if snapshot.offsetRetry && subscriptionState == "active" && currentVersion != expectedVersion &&
		strings.HasPrefix(snapshot.retryKey, offsetRetryKeyPrefix) && storedRetryKey == snapshot.retryKey {
		// A fenced claim must not withdraw a still-current qualification. The
		// periodic recovery will evaluate the newer owner version instead.
		return false, currentVersion, nil
	}
	eligible := snapshot.offsetRetry && subscriptionState == "active" && currentVersion == expectedVersion &&
		strings.HasPrefix(snapshot.retryKey, offsetRetryKeyPrefix) && storedRetryKey == snapshot.retryKey &&
		!isTerminalReleaseVerdict(currentVerdict) && (currentTask.Valid || currentVerdict == "accepted_pending_download" || currentVerdict == "awaiting_competition" || currentVerdict == "subscription_stopped")
	var taskState, acquisitionState string
	var taskCount int
	var hasCurrentPlacement, hasRunningAcquisition bool
	if eligible {
		if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(task.state, ''), COALESCE(acq.state, ''),
       (SELECT count(*) FROM download_tasks owned WHERE owned.release_id = r.id),
       EXISTS (SELECT 1 FROM placements p
               WHERE p.managed_anime_id = $2 AND p.quality_facts_ref = r.id
                 AND p.state IN ('active', 'released', 'displaced')),
       EXISTS (SELECT 1 FROM download_tasks owned
               JOIN acquisitions running ON running.id = owned.acquisition_id
               WHERE owned.release_id = r.id AND running.state = 'running')
FROM releases r
LEFT JOIN download_tasks task ON task.id = r.download_task_id
LEFT JOIN acquisitions acq ON acq.id = task.acquisition_id
WHERE r.id = $1`, snapshot.releaseID, snapshot.managedAnimeID).Scan(&taskState, &acquisitionState, &taskCount, &hasCurrentPlacement, &hasRunningAcquisition); err != nil {
			return false, 0, fmt.Errorf("read locked offset retry facts: %w", err)
		}
		settledTask := currentTask.Valid && taskState == "completed" && acquisitionState == "succeeded"
		detachedRetry := !currentTask.Valid && (currentVerdict == "accepted_pending_download" || currentVerdict == "awaiting_competition" || currentVerdict == "subscription_stopped") && taskState == "" && acquisitionState == ""
		eligible = (settledTask || detachedRetry) && taskCount == snapshot.taskCount && !hasCurrentPlacement && !hasRunningAcquisition
	}
	if !eligible {
		version, err := clearOffsetRetryTx(ctx, tx, snapshot.releaseID, snapshot.retryKey, storedRetryKey, currentVersion)
		return false, version, err
	}
	if currentTask.Valid {
		if _, err := tx.ExecContext(ctx, `
UPDATE releases SET download_task_id = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $2 AND download_task_id = $3 AND competition_retry_key = $4`,
			snapshot.releaseID, currentVersion, currentTask.String, snapshot.retryKey); err != nil {
			return false, 0, fmt.Errorf("detach offset-retried download task: %w", err)
		}
	}
	return true, currentVersion, nil
}
