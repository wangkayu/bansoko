package filemaintenance

import (
	"context"
	"fmt"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

// configScanBatch bounds the requests one tick dispatches; the rest wait for
// the next tick.
const configScanBatch = 100

// Config scan requests are written by the configuration writes that can change
// a rendered path (anime/application.RequestConfigScanTx): one pending row per
// anime. The tick submits the scan that re-renders the anime's files under the
// current configuration; the scan's alignment consumer keeps honouring the
// anime's alignment setting, so the scan moves nothing when auto-align is off.
//
// The submitted request identity carries the row's revision and request time,
// which differ for every request, so the scan is always a new generation
// instead of a replay of the previous completed one. The row is cleared only
// at the revision read before the submission: a write made while the scan runs
// stays pending and is dispatched after it.

// dispatchConfigScanRequests is the config half of a tick. Only active
// instances take part: a stopped subscription is not scanned and its request
// waits for the resume.
func (s *ScanScheduler) dispatchConfigScanRequests(ctx context.Context) error {
	if s.submitter == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT r.managed_anime_id, r.revision, r.requested_at
FROM config_scan_requests r
JOIN subscriptions sub ON sub.managed_anime_id = r.managed_anime_id
WHERE sub.state = 'active'
ORDER BY r.requested_at, r.managed_anime_id
LIMIT $1`, configScanBatch)
	if err != nil {
		return fmt.Errorf("read config scan requests: %w", err)
	}
	type due struct {
		animeID     string
		revision    int64
		requestedAt time.Time
	}
	var dues []due
	for rows.Next() {
		var item due
		if err := rows.Scan(&item.animeID, &item.revision, &item.requestedAt); err != nil {
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
		if ok {
			requestID := fmt.Sprintf("config:%s:%d:%d", item.animeID, item.revision, item.requestedAt.UnixNano())
			if err := s.submitter.SubmitScan(ctx, item.animeID, treeTop, requestID); err != nil {
				// A running scan is the usual conflict: the next tick submits again.
				s.logger.Debug(ctx, observabilityport.RuntimeEvent{
					Message: "config scan not submitted", Component: "filemaintenance",
					Operation: "scan_submit", Outcome: "skipped", ReasonCode: "scan_submit_deferred",
					OwnerType: "managed_anime", OwnerID: item.animeID, ErrorClass: "scan_scheduler",
				})
				continue
			}
		}
		// An anime that placed nothing has no file to re-render: the request is
		// done, and an import renders the current configuration itself.
		if _, err := s.db.ExecContext(ctx, `DELETE FROM config_scan_requests WHERE managed_anime_id = $1 AND revision = $2`, item.animeID, item.revision); err != nil {
			return fmt.Errorf("clear config scan request: %w", err)
		}
	}
	return nil
}
