package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"

	"bansoko/internal/workitems"
)

// NamingRecoveryChange is the set of facts one metadata write supplied. Only
// the facts a naming template can read belong here.
type NamingRecoveryChange struct {
	Title     bool
	MediaKind bool
	Year      bool
	Season    bool
}

// ReasonCodes maps the supplied facts to the structured missing-fact failures
// they can repair. This is deliberately not a general acquisition-failure
// retry: an unrelated metadata write must not rerun collisions, unsafe
// templates, parser failures, or other terminal outcomes.
func (c NamingRecoveryChange) ReasonCodes() []string {
	var codes []string
	if c.Title {
		codes = append(codes, "naming_fact_missing_anime_title", "naming_fact_missing_anime_title_override")
	}
	if c.MediaKind {
		codes = append(codes, "naming_fact_missing_anime_kind")
	}
	if c.Year {
		codes = append(codes, "naming_fact_missing_anime_year")
	}
	if c.Season {
		codes = append(codes, "naming_fact_missing_anime_season", "naming_fact_missing_anime_season_folder")
	}
	return codes
}

// namingRecoverySQL selects the failed acquisitions a change of the supplied
// facts can repair. The generic release scope still excludes
// acquisition_failed; this separate scope also requires no download task and
// relies on the existing work-item merge for idempotency.
const namingRecoverySQL = `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = $1
  AND r.verdict = 'acquisition_failed'
  AND r.download_task_id IS NULL
  AND r.reason_code = ANY($2::text[])
FOR UPDATE OF r`

// ScheduleNamingRecoveryTx reopens and re-evaluates only failed acquisitions
// whose persisted reason names a fact supplied by this metadata write. Every
// metadata write path (owner match, owner patch, scheduled match) goes through
// here so none of them silently drops the recovery. The reopen and the work
// item share the caller's transaction; unrelated terminal failures stay put.
func ScheduleNamingRecoveryTx(ctx context.Context, tx *sql.Tx, store *workitems.Store, managedAnimeID string, change NamingRecoveryChange, trigger string) (int, error) {
	codes := change.ReasonCodes()
	if len(codes) == 0 {
		return 0, nil
	}
	rows, err := tx.QueryContext(ctx, namingRecoverySQL, managedAnimeID, pq.Array(codes))
	if err != nil {
		return 0, fmt.Errorf("list naming recovery releases: %w", err)
	}
	type releaseTarget struct {
		id      string
		version int64
	}
	var releases []releaseTarget
	for rows.Next() {
		var release releaseTarget
		if err := rows.Scan(&release.id, &release.version); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan naming recovery release: %w", err)
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate naming recovery releases: %w", err)
	}
	rows.Close()
	snapshot, err := json.Marshal(map[string]string{"trigger": trigger})
	if err != nil {
		return 0, fmt.Errorf("encode release evaluation trigger: %w", err)
	}
	scheduled := 0
	for _, release := range releases {
		// A failed acquisition is terminal to the ordinary evaluator. Reopen
		// exactly the selected row before enqueueing it: no download was
		// submitted, so the next evaluation starts again at normal automatic
		// acceptance instead of needing a terminal-verdict exception in every
		// worker and owner writer.
		var reopenedVersion int64
		if err := tx.QueryRowContext(ctx, `
UPDATE releases
SET verdict = 'accepted_pending_download', reason_code = 'accepted', reason_detail = NULL,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $2
  AND verdict = 'acquisition_failed'
  AND download_task_id IS NULL
  AND reason_code = ANY($3::text[])
RETURNING version`, release.id, release.version, pq.Array(codes)).Scan(&reopenedVersion); err != nil {
			return 0, fmt.Errorf("reopen naming recovery release: %w", err)
		}
		_, created, err := store.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerRelease, ID: release.id},
			Kind:                 workitems.KindReleaseEvaluate,
			IdempotencyKey:       "release_evaluate:" + release.id,
			InputSnapshot:        snapshot,
			MaxAttempts:          DefaultMaxAttempts,
			ExpectedOwnerVersion: reopenedVersion,
		})
		if err != nil {
			return 0, fmt.Errorf("schedule naming recovery evaluation: %w", err)
		}
		if created {
			scheduled++
		}
	}
	return scheduled, nil
}
