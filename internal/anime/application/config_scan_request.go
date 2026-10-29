package application

import (
	"context"
	"database/sql"
	"fmt"
)

// RequestConfigScanTx durably requests one reconciliation scan of the anime
// inside the caller's configuration write, which commits the request with
// itself: a crash after the commit cannot lose it. Subscription files render
// with the current configuration, so a write that can change a rendered path
// asks for the scan that re-renders them (the scan moves them under the
// anime's alignment setting, never re-downloads).
//
// Requests coalesce into the anime's one pending row; each write advances its
// revision, which is how the scheduler tells a request made after it read the
// row (the scan it submits may have started before the write) from the one it
// served.
func RequestConfigScanTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO config_scan_requests (managed_anime_id) VALUES ($1)
ON CONFLICT (managed_anime_id) DO UPDATE
SET revision = config_scan_requests.revision + 1, requested_at = CURRENT_TIMESTAMP`, managedAnimeID); err != nil {
		return fmt.Errorf("request configuration scan: %w", err)
	}
	return nil
}
