package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
)

// Settlement increments each frozen loser's version once. Recovery never
// supersedes it again and rejects later edits instead of cleaning a new object.
func revalidateSettledRecoveryTx(ctx context.Context, scope txscope.Tx, d *decisionRow) error {
	tx := scope.SQLTx()
	var subState string
	var subVersion, policyVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT state,version FROM subscriptions WHERE managed_anime_id=$1 FOR UPDATE NOWAIT`, d.AnimeID).Scan(&subState, &subVersion); err != nil {
		return err
	}
	if subState != "active" || subVersion != d.SubscriptionVersion {
		return workitems.ErrStaleClaim
	}
	if err := tx.QueryRowContext(ctx, `SELECT replacement_version FROM anime_business_configs WHERE managed_anime_id=$1 AND replacement_version IS NOT NULL FOR UPDATE NOWAIT`, d.AnimeID).Scan(&policyVersion); err != nil {
		return err
	}
	if policyVersion != d.PolicyVersion {
		return workitems.ErrStaleClaim
	}
	for _, ref := range d.placementRefs() {
		var state, verification, fp string
		var v int64
		if err := tx.QueryRowContext(ctx, `SELECT state,verification_state,target_fingerprint,version FROM placements WHERE id=$1 AND managed_anime_id=$2 FOR UPDATE NOWAIT`, ref.PlacementID, d.AnimeID).Scan(&state, &verification, &fp, &v); err != nil {
			return err
		}
		expected := ref.Version
		want := "active"
		if ref.PlacementID != d.Winner.PlacementID {
			expected++
			want = "superseded"
		}
		if v != expected || state != want || verification != "verified" || fp != ref.Fingerprint {
			return workitems.ErrStaleClaim
		}
	}
	return nil
}
