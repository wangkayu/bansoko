package application

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lib/pq"
)

// namingRecoveryReasonCodes maps an actual owner-input change to the structured
// missing-fact failures that the change can repair. This is deliberately not a
// general acquisition-failure retry: an unrelated metadata write must not rerun
// collisions, unsafe templates, parser failures, or other terminal outcomes.
func (c metadataChange) namingRecoveryReasonCodes() []string {
	var codes []string
	if c.title {
		codes = append(codes, "naming_fact_missing_anime_title", "naming_fact_missing_anime_title_override")
	}
	if c.mediaKind {
		codes = append(codes, "naming_fact_missing_anime_kind")
	}
	if c.year {
		codes = append(codes, "naming_fact_missing_anime_year")
	}
	if c.season {
		codes = append(codes, "naming_fact_missing_anime_season", "naming_fact_missing_anime_season_folder")
	}
	return codes
}

// scheduleNamingRecoveryReleaseEvaluation re-evaluates only failed acquisitions
// whose persisted reason names a fact supplied by this metadata change. The
// generic release scope still excludes acquisition_failed; this separate scope
// also requires no download task and relies on the existing work-item merge for
// idempotency.
func (s *Service) scheduleNamingRecoveryReleaseEvaluation(ctx context.Context, tx *sql.Tx, managedAnimeID string, change metadataChange) (int, error) {
	codes := change.namingRecoveryReasonCodes()
	if len(codes) == 0 {
		return 0, nil
	}
	scheduled, err := s.scheduleUnsubmittedReleaseEvaluationCount(ctx, tx, `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE sub.managed_anime_id = $1
  AND r.verdict = 'acquisition_failed'
  AND r.download_task_id IS NULL
  AND r.reason_code = ANY($2::text[])
FOR UPDATE OF r`, managedAnimeID, "metadata_naming_recovery", pq.Array(codes))
	if err != nil {
		return 0, fmt.Errorf("schedule naming-fact recovery: %w", err)
	}
	return scheduled, nil
}
