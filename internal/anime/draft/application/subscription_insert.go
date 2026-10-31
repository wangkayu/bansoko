package application

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// insertSubscriptionTx creates the instance's Subscription row. The
// new-releases-only option is frozen here: when it is on, the baseline is the row's
// own creation time (start_baseline_at = created_at, both the transaction
// timestamp), never the later first start. The returned baseline is nil when
// the option is off.
func insertSubscriptionTx(ctx context.Context, tx *sql.Tx, subscriptionID, animeID, origin string, originEntryRef, entrySeason, instanceLabel any, state string, newReleasesOnly bool) (*time.Time, time.Time, error) {
	var baseline sql.NullTime
	var createdAt time.Time
	if err := tx.QueryRowContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, entry_season, instance_label, state, new_releases_only, created_at, start_baseline_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_TIMESTAMP, CASE WHEN $8 THEN CURRENT_TIMESTAMP END)
RETURNING start_baseline_at, created_at`, subscriptionID, animeID, origin, originEntryRef, entrySeason, instanceLabel, state, newReleasesOnly).Scan(&baseline, &createdAt); err != nil {
		return nil, time.Time{}, fmt.Errorf("create subscription: %w", err)
	}
	if !baseline.Valid {
		return nil, createdAt, nil
	}
	value := baseline.Time
	return &value, createdAt, nil
}
