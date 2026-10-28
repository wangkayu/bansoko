package filemaintenance

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

// Alignment requests are the placements a scan aligns whatever the
// instance's alignment setting:
//
//   - a newly imported file (post-import correction, option a): it is
//     published at its frozen admission target, and once its retention
//     decision is in, a scan routes it to the current render when the two
//     differ (a render failure keeps the L2 reason);
//   - a file an offset change re-keyed: once retention compared its new key,
//     a scan routes it (offset_alignment.go).
//
// The scan scheduler's tick dispatches them: it puts each offset change's
// keys through retention, then submits a scan for an instance whose requests
// are ready, and records each offset change's activity once every request of
// it is resolved. The alignment consumer of a scan resolves the requests
// recorded before that scan started.

// Offset request outcomes (what the routing did with the re-keyed file).
const (
	alignmentOutcomeMoved       = "moved"
	alignmentOutcomeDeleted     = "deleted"
	alignmentOutcomeVersioned   = "versioned"
	alignmentOutcomeOverwritten = "overwritten"
	alignmentOutcomeSkipped     = "skipped"
	alignmentOutcomeUnchanged   = "unchanged"
)

// requestAlignmentTx records that the next scan aligns the placement
// (changeID "" for a new import). A newer request replaces an unresolved one.
func requestAlignmentTx(ctx context.Context, tx *sql.Tx, placementID, changeID string) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO alignment_requests (placement_id, offset_change_id) VALUES ($1, $2)
ON CONFLICT (placement_id) DO UPDATE
SET offset_change_id = COALESCE(EXCLUDED.offset_change_id, alignment_requests.offset_change_id),
    outcome = NULL, requested_at = CURRENT_TIMESTAMP`, placementID, nullableText(changeID)); err != nil {
		return fmt.Errorf("request alignment of placement %s: %w", placementID, err)
	}
	return nil
}

// alignmentRequest is one unresolved request a scan resolves.
type alignmentRequest struct {
	placementID string
	path        string
	state       string
	changeID    string
}

// requestsBeforeScan are the instance's unresolved requests recorded before
// the scan of generation started (a request newer than the scan waits for
// the next one). An offset request is ready once retention compared its
// keys.
func requestsBeforeScan(ctx context.Context, db *sql.DB, animeID string, generation int64) ([]alignmentRequest, error) {
	rows, err := db.QueryContext(ctx, `
SELECT r.placement_id, p.physical_path, p.state, COALESCE(r.offset_change_id, '')
FROM alignment_requests r
JOIN placements p ON p.id = r.placement_id
JOIN reconciliation_scans s ON s.managed_anime_id = p.managed_anime_id
LEFT JOIN offset_alignment_changes ch ON ch.id = r.offset_change_id
WHERE p.managed_anime_id = $1 AND r.outcome IS NULL
  AND s.generation = $2 AND s.started_at IS NOT NULL AND r.requested_at <= s.started_at
  AND (r.offset_change_id IS NULL OR ch.state = 'aligning')
ORDER BY r.placement_id`, animeID, generation)
	if err != nil {
		return nil, fmt.Errorf("read alignment requests: %w", err)
	}
	defer rows.Close()
	var out []alignmentRequest
	for rows.Next() {
		var request alignmentRequest
		if err := rows.Scan(&request.placementID, &request.path, &request.state, &request.changeID); err != nil {
			return nil, err
		}
		out = append(out, request)
	}
	return out, rows.Err()
}

// requestedPaths are the current paths of the requests' active placements.
func requestedPaths(requests []alignmentRequest) map[string]bool {
	paths := map[string]bool{}
	for _, request := range requests {
		if request.state == "active" {
			paths[request.path] = true
		}
	}
	return paths
}

// resolveAlignmentRequests settles the requests the scan covered: a new
// import's request is done; an offset request keeps what the routing did with
// the file (routes by placement id) for its change's record.
func resolveAlignmentRequests(ctx context.Context, db *sql.DB, requests []alignmentRequest, routes map[string]alignmentRoute) error {
	for _, request := range requests {
		if request.changeID == "" {
			if _, err := db.ExecContext(ctx, `DELETE FROM alignment_requests WHERE placement_id = $1 AND offset_change_id IS NULL AND outcome IS NULL`, request.placementID); err != nil {
				return fmt.Errorf("resolve alignment request: %w", err)
			}
			continue
		}
		outcome := alignmentOutcomeUnchanged
		if request.state == "superseded" {
			outcome = alignmentOutcomeDeleted
		} else if route, ok := routes[request.placementID]; ok {
			outcome = routeOutcome(route)
		}
		if _, err := db.ExecContext(ctx, `UPDATE alignment_requests SET outcome = $3 WHERE placement_id = $1 AND offset_change_id = $2 AND outcome IS NULL`, request.placementID, request.changeID, outcome); err != nil {
			return fmt.Errorf("resolve alignment request: %w", err)
		}
	}
	return nil
}

// routeOutcome is the recorded outcome of one admission route.
func routeOutcome(route alignmentRoute) string {
	switch route.Action {
	case routeProceed, routeWait:
		return alignmentOutcomeMoved
	case routeHold:
		if route.Settle {
			return alignmentOutcomeUnchanged
		}
		return alignmentOutcomeMoved
	case routeDrop:
		return alignmentOutcomeDeleted
	case routeVersion:
		return alignmentOutcomeVersioned
	case routeOverwrite:
		return alignmentOutcomeOverwritten
	case routeSkip:
		return alignmentOutcomeSkipped
	}
	return alignmentOutcomeUnchanged
}

// SetAlignmentRequests wires the request dispatch of the tick: the retention
// port that compares an offset change's keys and the activity writer of its
// record (composition root).
func (s *ScanScheduler) SetAlignmentRequests(retention AlignmentRetention, activity observabilityport.ActivityWriter) {
	if s != nil {
		s.retention = retention
		s.activity = activity
	}
}

// dispatchAlignmentRequests is the request half of a tick. Only active
// instances take part: a stopped subscription neither compares nor scans, and
// its requests wait for the resume.
func (s *ScanScheduler) dispatchAlignmentRequests(ctx context.Context) error {
	if err := s.compareOffsetChanges(ctx); err != nil {
		return err
	}
	if err := s.submitRequestedScans(ctx); err != nil {
		return err
	}
	return s.recordOffsetChanges(ctx)
}

// compareOffsetChanges puts every comparing change's re-keyed placements
// through retention (losers are deleted by the retention path) and moves the
// change on to aligning. A failed comparison is retried on the next tick.
func (s *ScanScheduler) compareOffsetChanges(ctx context.Context) error {
	if s.retention == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT ch.id, sub.managed_anime_id
FROM offset_alignment_changes ch
JOIN rss_sources src ON src.id = ch.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE ch.state = 'comparing' AND sub.state = 'active'
ORDER BY ch.created_at, ch.id
LIMIT 50`)
	if err != nil {
		return fmt.Errorf("read offset changes to compare: %w", err)
	}
	type change struct{ id, animeID string }
	var changes []change
	for rows.Next() {
		var item change
		if err := rows.Scan(&item.id, &item.animeID); err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range changes {
		placements, err := changePlacements(ctx, s.db, item.id)
		if err != nil {
			return err
		}
		if len(placements) > 0 {
			if _, err := s.retention.RecheckPlacements(ctx, item.animeID, placements); err != nil {
				s.logger.Warn(ctx, observabilityport.RuntimeEvent{
					Message: "offset change retention comparison failed", Component: "filemaintenance",
					Operation: "offset_compare", Outcome: "failure", ReasonCode: "offset_compare_failed",
					OwnerType: "managed_anime", OwnerID: item.animeID, ErrorClass: "retention",
				})
				continue
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE offset_alignment_changes SET state = 'aligning', updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND state = 'comparing'`, item.id); err != nil {
			return fmt.Errorf("advance offset change: %w", err)
		}
	}
	return nil
}

func changePlacements(ctx context.Context, db *sql.DB, changeID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT placement_id FROM alignment_requests WHERE offset_change_id = $1 AND outcome IS NULL ORDER BY placement_id`, changeID)
	if err != nil {
		return nil, fmt.Errorf("read offset change placements: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// submitRequestedScans submits a scan for every active instance with ready
// requests once no retention evaluation of it is in flight (a new import's
// decision comes first). The request identity is the newest request: a replay
// reuses the scan that already covers them, a running scan is left to finish.
func (s *ScanScheduler) submitRequestedScans(ctx context.Context) error {
	if s.submitter == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT p.managed_anime_id, max(r.requested_at)
FROM alignment_requests r
JOIN placements p ON p.id = r.placement_id
JOIN subscriptions sub ON sub.managed_anime_id = p.managed_anime_id
LEFT JOIN offset_alignment_changes ch ON ch.id = r.offset_change_id
WHERE r.outcome IS NULL AND sub.state = 'active'
  AND (r.offset_change_id IS NULL OR ch.state = 'aligning')
  AND NOT EXISTS (
      SELECT 1 FROM work_items wi
      WHERE wi.idempotency_key = 'replacement_evaluate:' || p.managed_anime_id)
GROUP BY p.managed_anime_id
ORDER BY p.managed_anime_id`)
	if err != nil {
		return fmt.Errorf("read requested scans: %w", err)
	}
	type due struct {
		animeID string
		newest  time.Time
	}
	var dues []due
	for rows.Next() {
		var item due
		if err := rows.Scan(&item.animeID, &item.newest); err != nil {
			rows.Close()
			return err
		}
		dues = append(dues, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range dues {
		treeTop, ok, err := s.treeTop(ctx, item.animeID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		requestID := fmt.Sprintf("alignment:%s:%d", item.animeID, item.newest.UnixNano())
		if err := s.submitter.SubmitScan(ctx, item.animeID, treeTop, requestID); err != nil {
			// A running scan is the usual conflict: the next tick submits again.
			s.logger.Debug(ctx, observabilityport.RuntimeEvent{
				Message: "requested alignment scan not submitted", Component: "filemaintenance",
				Operation: "scan_submit", Outcome: "skipped", ReasonCode: "scan_submit_deferred",
				OwnerType: "managed_anime", OwnerID: item.animeID, ErrorClass: "scan_scheduler",
			})
		}
	}
	return nil
}

// recordOffsetChanges writes the one activity record of every aligning change
// whose requests are all resolved, and drops its requests.
func (s *ScanScheduler) recordOffsetChanges(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT ch.id FROM offset_alignment_changes ch
WHERE ch.state = 'aligning'
  AND NOT EXISTS (SELECT 1 FROM alignment_requests r WHERE r.offset_change_id = ch.id AND r.outcome IS NULL)
ORDER BY ch.created_at, ch.id
LIMIT 50`)
	if err != nil {
		return fmt.Errorf("read offset changes to record: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.recordOffsetChange(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// recordOffsetChange counts the change's outcomes: the routing outcomes of
// its requests, and the retention deletions on the keys its files now cover
// since the change (whichever file lost).
func (s *ScanScheduler) recordOffsetChange(ctx context.Context, changeID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var animeID, subscriptionID, sourceID, groupName string
	var sourceVersion int64
	var rekeyed int
	err = tx.QueryRowContext(ctx, `
SELECT sub.managed_anime_id, sub.id, src.id, src.group_name, ch.source_version, ch.rekeyed_count
FROM offset_alignment_changes ch
JOIN rss_sources src ON src.id = ch.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE ch.id = $1 AND ch.state = 'aligning'
FOR UPDATE OF ch`, changeID).Scan(&animeID, &subscriptionID, &sourceID, &groupName, &sourceVersion, &rekeyed)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock offset change: %w", err)
	}
	counts := map[string]int{}
	outcomes, err := tx.QueryContext(ctx, `SELECT outcome, count(*) FROM alignment_requests WHERE offset_change_id = $1 GROUP BY outcome`, changeID)
	if err != nil {
		return err
	}
	for outcomes.Next() {
		var outcome string
		var count int
		if err := outcomes.Scan(&outcome, &count); err != nil {
			outcomes.Close()
			return err
		}
		counts[outcome] = count
	}
	outcomes.Close()
	var deleted int
	if err := tx.QueryRowContext(ctx, `
SELECT count(DISTINCT d.placement_id)
FROM replacement_deletions d
JOIN offset_alignment_changes ch ON ch.id = $1
WHERE d.managed_anime_id = $2 AND d.state IN ('admitted', 'deleted') AND d.created_at >= ch.created_at
  AND d.coverage_keys ?| ARRAY(
      SELECT k.observation_key FROM alignment_requests r
      JOIN placement_coverage k ON k.placement_id = r.placement_id
      WHERE r.offset_change_id = $1)`, changeID, animeID).Scan(&deleted); err != nil {
		return fmt.Errorf("count offset change deletions: %w", err)
	}
	if s.activity != nil {
		if err := s.activity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
			SubscriptionID:  subscriptionID,
			ManagedAnimeID:  animeID,
			Kind:            string(observabilityport.ActivityKindPlacementChanged),
			Outcome:         "success",
			ReasonCode:      string(observabilityport.MessageOffsetAligned),
			SubjectType:     "rss_source",
			SubjectID:       sourceID,
			SubjectVersion:  sourceVersion,
			StableResultKey: string(observabilityport.MessageOffsetAligned),
			RSSSourceID:     sourceID,
			MessageCode:     observabilityport.MessageOffsetAligned,
			MessageParams: map[string]any{
				observabilityport.ParamGroupName:   groupName,
				observabilityport.ParamRekeyed:     rekeyed,
				observabilityport.ParamDeleted:     deleted,
				observabilityport.ParamVersioned:   counts[alignmentOutcomeVersioned],
				observabilityport.ParamOverwritten: counts[alignmentOutcomeOverwritten],
				observabilityport.ParamSkipped:     counts[alignmentOutcomeSkipped],
			},
		}); err != nil {
			return fmt.Errorf("append offset change activity: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE offset_alignment_changes SET state = 'recorded', deleted_count = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, changeID, deleted); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM alignment_requests WHERE offset_change_id = $1 AND outcome IS NOT NULL`, changeID); err != nil {
		return err
	}
	return tx.Commit()
}
