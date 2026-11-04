package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/workitems"
)

// scheduleWaitingMetadataReleasesTx re-enqueues every release held as
// waiting_metadata, so turning the release-without-metadata switch on admits
// the releases that were already waiting instead of only the next ones. The
// predicate is the metadata-completion path's (release in re-evaluation scope
// with no live release_evaluate item), with the per-anime scope removed. The
// shared idempotency key makes the scheduling idempotent: saving the switch on
// again, or racing a completion-triggered schedule, adds no second live item.
func (s *Service) scheduleWaitingMetadataReleasesTx(ctx context.Context, tx *sql.Tx) error {
	if s.workItems == nil {
		return errors.New("metadata_hold save requires the work item store")
	}
	rows, err := tx.QueryContext(ctx, `
SELECT r.id, r.version
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE r.verdict = 'waiting_metadata'
  AND `+animeapplication.ReleaseReevaluationScopeSQL+`
  AND NOT EXISTS (
    SELECT 1 FROM work_items wi
    WHERE wi.owner_type = 'release' AND wi.owner_id = r.id
      AND wi.kind = 'release_evaluate' AND wi.state IN ('available', 'leased')
  )
FOR UPDATE OF r`)
	if err != nil {
		return fmt.Errorf("list held releases: %w", err)
	}
	type heldRelease struct {
		id      string
		version int64
	}
	var held []heldRelease
	for rows.Next() {
		var release heldRelease
		if err := rows.Scan(&release.id, &release.version); err != nil {
			rows.Close()
			return fmt.Errorf("scan held release: %w", err)
		}
		held = append(held, release)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate held releases: %w", err)
	}
	rows.Close()
	for _, release := range held {
		if _, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerRelease, ID: release.id},
			Kind:                 workitems.KindReleaseEvaluate,
			IdempotencyKey:       "release_evaluate:" + release.id,
			InputSnapshot:        json.RawMessage(`{"trigger":"metadata_release_allowed"}`),
			MaxAttempts:          animeapplication.DefaultMaxAttempts,
			ExpectedOwnerVersion: release.version,
		}); err != nil && !errors.Is(err, workitems.ErrLiveItemConflict) {
			return fmt.Errorf("schedule release re-evaluation: %w", err)
		}
	}
	return nil
}
