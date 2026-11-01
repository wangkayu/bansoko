package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/observability/businesslog"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
)

// displacedPlacement is one managed file another file took over in this
// transaction.
type displacedPlacement struct {
	PlacementID    string
	ManagedAnimeID string
	Version        int64
	PhysicalPath   string
	Cause          string
	ReleaseID      string
}

// displacedSinks are the two places a displacement narrates itself: the
// victim instance's run log and the notification ledger. Either may be
// unwired (nil), and a nil sink is skipped exactly like the other.
type displacedSinks struct {
	activity      observabilityport.ActivityWriter
	notifications plugintargetport.BusinessDeliveryEnqueuer
}

// displacedBySuccessorTx reads the placements the displacement transition just
// took, from the facts it left on them. `placement_displace` stamps every
// victim with the successor that took its place and with the transaction's
// CURRENT_TIMESTAMP, so the successor id plus that stamp name exactly this
// call's victims — an earlier call left an older stamp, and a replay inside
// the same transaction matches nothing new because the rows are no longer
// active.
func displacedBySuccessorTx(ctx context.Context, tx *sql.Tx, successorPlacementID string) ([]displacedPlacement, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT p.id, p.managed_anime_id, p.version, p.physical_path, COALESCE(p.displaced_cause, ''),
       COALESCE(p.quality_facts_ref, '')
FROM placements p
WHERE p.state = 'displaced' AND p.displaced_by_placement_id = $1
  AND p.displaced_at = CURRENT_TIMESTAMP
ORDER BY p.id`, successorPlacementID)
	if err != nil {
		return nil, fmt.Errorf("read displaced placements: %w", err)
	}
	defer rows.Close()
	var victims []displacedPlacement
	for rows.Next() {
		var victim displacedPlacement
		if err := rows.Scan(&victim.PlacementID, &victim.ManagedAnimeID, &victim.Version, &victim.PhysicalPath, &victim.Cause, &victim.ReleaseID); err != nil {
			return nil, fmt.Errorf("scan displaced placement: %w", err)
		}
		victims = append(victims, victim)
	}
	return victims, rows.Err()
}

// appendDisplacedActivityTx records, in the transaction that displaced them,
// that these managed files lost their place to another file. Each victim gets
// one entry on its own instance's stream (a path can be taken across
// instances), named after the transition: the entry is written at most once
// per victim, so a crash-replayed transaction still shows one line.
func appendDisplacedActivityTx(ctx context.Context, scope txscope.Tx, sinks displacedSinks, successorPlacementID string) error {
	tx := scope.SQLTx()
	if tx == nil || successorPlacementID == "" || (sinks.activity == nil && sinks.notifications == nil) {
		return nil
	}
	victims, err := displacedBySuccessorTx(ctx, tx, successorPlacementID)
	if err != nil || len(victims) == 0 {
		return err
	}
	var successorPath, successorReleaseID string
	if err := tx.QueryRowContext(ctx, `
SELECT physical_path, COALESCE(quality_facts_ref, '') FROM placements WHERE id = $1`, successorPlacementID).Scan(&successorPath, &successorReleaseID); err != nil {
		return fmt.Errorf("read displacing placement: %w", err)
	}
	taker, err := releaseRunContextTx(ctx, tx, successorReleaseID)
	if err != nil {
		return err
	}
	replacedBy := map[string]any{"replaced_by_file": businesslog.FileName(successorPath)}
	for _, key := range []struct{ source, suffix string }{{"group_name", "group"}, {"release_title", "title"}} {
		if value, ok := taker[key.source]; ok {
			replacedBy["replaced_by_"+key.suffix] = value
		}
	}
	takerGroup, _ := taker["group_name"].(string)
	takerTitle, _ := taker["release_title"].(string)
	for _, victim := range victims {
		var subscriptionID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM subscriptions WHERE managed_anime_id = $1`, victim.ManagedAnimeID).Scan(&subscriptionID)
		if errors.Is(err, sql.ErrNoRows) {
			// A temporary anime without a subscription has no activity stream
			// and no one to notify.
			continue
		}
		if err != nil {
			return fmt.Errorf("read displaced placement subscription: %w", err)
		}
		version := victim.Version
		if version < 1 {
			version = 1
		}
		if sinks.activity != nil {
			values := map[string]any{
				"file_name": businesslog.FileName(victim.PhysicalPath),
				"cause":     victim.Cause,
			}
			for key, value := range replacedBy {
				values[key] = value
			}
			details, err := businesslog.MarshalDetails(values)
			if err != nil {
				return fmt.Errorf("encode displaced activity details: %w", err)
			}
			if err := sinks.activity.AppendTx(ctx, scope, observabilityport.ActivityRecord{
				SubscriptionID: subscriptionID,
				ManagedAnimeID: victim.ManagedAnimeID,
				Kind:           string(observabilityport.ActivityKindPlacementChanged),
				Outcome:        "info",
				ReasonCode:     string(observabilityport.MessagePlacementDisplaced),
				SubjectType:    "placement",
				SubjectID:      victim.PlacementID,
				SubjectVersion: version,
				DedupeKey:      "placement_displaced:" + victim.PlacementID,
				MessageCode:    observabilityport.MessagePlacementDisplaced,
				ReleaseID:      victim.ReleaseID,
				Details:        details,
			}); err != nil {
				return fmt.Errorf("append displaced activity: %w", err)
			}
		}
		if sinks.notifications != nil {
			title, found, err := managedAnimeTitleTx(ctx, tx, victim.ManagedAnimeID)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			request, err := plugintargetport.FileDisplacedRequest(victim.PlacementID, version, victim.ManagedAnimeID, title,
				businesslog.FileName(victim.PhysicalPath), victim.Cause, businesslog.FileName(successorPath), takerGroup, takerTitle)
			if err != nil {
				return fmt.Errorf("encode displaced notice: %w", err)
			}
			if err := sinks.notifications.EnqueueBusinessDelivery(ctx, scope, request); err != nil {
				return fmt.Errorf("enqueue displaced notice: %w", err)
			}
		}
	}
	return nil
}

// managedAnimeTitleTx reads the display title of one managed anime; found is
// false when the anime record no longer exists (nothing left to notify).
func managedAnimeTitleTx(ctx context.Context, tx *sql.Tx, animeID string) (string, bool, error) {
	var title string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(btrim(title), '') FROM managed_animes WHERE id = $1`, animeID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read displaced anime title: %w", err)
	}
	return title, true, nil
}
