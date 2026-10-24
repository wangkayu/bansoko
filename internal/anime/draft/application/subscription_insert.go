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
func insertSubscriptionTx(ctx context.Context, tx *sql.Tx, subscriptionID, animeID, origin string, originEntryRef, instanceLabel any, state string, newReleasesOnly bool) (*time.Time, error) {
	var baseline sql.NullTime
	if err := tx.QueryRowContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state, new_releases_only, created_at, start_baseline_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, CURRENT_TIMESTAMP, CASE WHEN $7 THEN CURRENT_TIMESTAMP END)
RETURNING start_baseline_at`, subscriptionID, animeID, origin, originEntryRef, instanceLabel, state, newReleasesOnly).Scan(&baseline); err != nil {
		return nil, fmt.Errorf("create subscription: %w", err)
	}
	if !baseline.Valid {
		return nil, nil
	}
	value := baseline.Time
	return &value, nil
}
