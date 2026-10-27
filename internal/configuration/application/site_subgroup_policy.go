package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	automationapp "bansoko/internal/automation/application"
	"bansoko/internal/persistence/txscope"
	selectionapp "bansoko/internal/selection/application"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"
)

// siteSubgroupTarget is one active provider instance whose group selection
// follows the live site policy.
type siteSubgroupTarget struct {
	managedAnimeID string
	subscriptionID string
}

// recomputeSiteSubgroupPolicyTx recomputes the group selection of every active
// provider instance (lifecycle active, subscription pending or active; never a
// retired or stopped one) after a site policy change, inside the Global save's
// transaction, so the recompute reads the new Global value. It reuses the
// exact recompute an instance subgroups save runs (the same policy read,
// ranking and selection write; refresh follows effective_selected as it does
// there). Each instance takes its lifecycle lock first, in a stable order,
// like every per-anime mutation, and is re-checked under that lock.
func recomputeSiteSubgroupPolicyTx(ctx context.Context, db *sql.DB, tx *sql.Tx) error {
	targets, err := activeSiteSubgroupTargets(ctx, tx)
	if err != nil {
		return err
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].managedAnimeID < targets[j].managedAnimeID })
	recomputer := automationapp.Dispatcher{DB: db, PolicyReader: selectionapp.NewService(db)}
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	for _, target := range targets {
		var lifecycle, state string
		err := tx.QueryRowContext(ctx, `
SELECT ma.lifecycle_state, s.state
FROM managed_anime ma
JOIN subscriptions s ON s.managed_anime_id = ma.id
WHERE ma.id = $1 AND s.id = $2
FOR UPDATE OF ma`, target.managedAnimeID, target.subscriptionID).Scan(&lifecycle, &state)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("lock instance %s: %w", target.managedAnimeID, err)
		}
		if lifecycle != "active" || (state != "pending" && state != "active") {
			continue
		}
		if err := recomputer.RecomputeSubscriptionSelections(ctx, txscope.Wrap(tx), target.subscriptionID, target.managedAnimeID); err != nil {
			return fmt.Errorf("recompute instance %s: %w", target.managedAnimeID, err)
		}
		// The new scores and selection re-rank only the releases still
		// waiting for the competition; admitted downloads and placed files
		// are left to the replacement.
		if state == "active" {
			if _, err := competition.EnqueueTx(ctx, tx, store, target.managedAnimeID, competition.TriggerSitePolicy, competition.WhenAwaiting); err != nil {
				return fmt.Errorf("enqueue competition of instance %s: %w", target.managedAnimeID, err)
			}
		}
	}
	return nil
}

func activeSiteSubgroupTargets(ctx context.Context, tx *sql.Tx) ([]siteSubgroupTarget, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT ma.id, s.id
FROM managed_anime ma
JOIN subscriptions s ON s.managed_anime_id = ma.id
WHERE ma.lifecycle_state = 'active'
  AND s.state IN ('pending', 'active')
  AND s.origin IN ('anibt', 'mikan')`)
	if err != nil {
		return nil, fmt.Errorf("list site policy instances: %w", err)
	}
	defer rows.Close()
	var targets []siteSubgroupTarget
	for rows.Next() {
		var target siteSubgroupTarget
		if err := rows.Scan(&target.managedAnimeID, &target.subscriptionID); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}
