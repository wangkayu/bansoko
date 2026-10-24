package application

import (
	"context"
	"errors"
	"fmt"

	"bansoko/internal/persistence/txscope"
)

// ScheduleUnsubmittedReleaseReevaluationTx enqueues a release_evaluate WorkItem
// for every live, unsubmitted Release of one instance, inside the caller's
// transaction.
//
// This is the parser-policy-change hook: switching the instance parser kind or
// its effective profile must schedule reevaluation immediately instead of
// waiting for the next feed refresh to notice. Only Releases without a
// DownloadTask are eligible — a Release that already produced a download is
// never re-evaluated, and terminal verdicts are skipped by the shared
// create-or-merge helper, which also keeps this idempotent (an existing live
// evaluation WorkItem is merged, not duplicated).
func (s *Service) ScheduleUnsubmittedReleaseReevaluationTx(ctx context.Context, scope txscope.Tx, managedAnimeID string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("release reevaluation transaction is nil")
	}
	if s == nil || s.workItems == nil {
		return errors.New("release reevaluation requires a work item store")
	}
	// Collect before enqueuing: the enclosing transaction cannot run new
	// statements while a result set is still open.
	rows, err := tx.QueryContext(ctx, `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources rs ON rs.id = r.rss_source_id
JOIN subscriptions s ON s.id = rs.subscription_id
WHERE s.managed_anime_id = $1
  AND r.download_task_id IS NULL
ORDER BY r.id`, managedAnimeID)
	if err != nil {
		return fmt.Errorf("read unsubmitted releases for reevaluation: %w", err)
	}
	type releaseTarget struct {
		id      string
		version int64
	}
	var targets []releaseTarget
	for rows.Next() {
		var target releaseTarget
		if err := rows.Scan(&target.id, &target.version); err != nil {
			rows.Close()
			return fmt.Errorf("scan unsubmitted release for reevaluation: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate unsubmitted releases for reevaluation: %w", err)
	}
	rows.Close()

	for _, target := range targets {
		if err := CreateOrMergeReleaseEvaluation(ctx, scope, s.workItems, target.id, target.version, "parser_policy_change"); err != nil {
			return fmt.Errorf("schedule release reevaluation for %s: %w", target.id, err)
		}
	}
	return nil
}
