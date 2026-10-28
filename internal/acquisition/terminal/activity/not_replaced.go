package activity

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"bansoko/internal/observability/businesslog"
	"bansoko/internal/persistence/txscope"
)

// notReplacedScope is what the settlement of a failed or cancelled root needs
// to find the replace decisions its terminal transition staled: the trigger
// (stale_failed_candidate_decisions) stales the admitted, unbound decisions of
// (origin anime, source Release) at the root's terminal time, which lies
// between the root's acceptance and its terminal_at.
type notReplacedScope struct {
	animeID, releaseID     string
	acceptedAt, terminalAt time.Time
	reason                 businesslog.NotReplacedReason
	runContext             map[string]any
}

// newNotReplacedScope is nil for a root without an origin anime or a source
// Release: the trigger stales nothing for it.
func newNotReplacedScope(animeID, releaseID string, acceptedAt, terminalAt time.Time, reason businesslog.NotReplacedReason) *notReplacedScope {
	if animeID == "" || releaseID == "" {
		return nil
	}
	return &notReplacedScope{animeID: animeID, releaseID: releaseID, acceptedAt: acceptedAt, terminalAt: terminalAt, reason: reason}
}

// writeNotReplacedTx writes, in the settlement entry's transaction and after
// it, the not_replaced line of each replace decision the root's failure or
// cancellation staled. The decisions are a plain read and every line names the
// settlement's own subscription and Release, so it takes no lock the
// settlement entry has not already taken. The key is the decision, so a
// replay, or a re-settlement after an import retry, is a no-op.
func (c *Consumer) writeNotReplacedTx(ctx context.Context, tx *sql.Tx, e entry) error {
	scope := e.notReplaced
	if scope.animeID != e.managedAnimeID {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
SELECT d.id, d.version
FROM replacement_decisions d
WHERE d.managed_anime_id = $1 AND d.candidate_release_id = $2
  AND d.verdict = 'replace' AND d.state = 'stale' AND d.stale_reason = 'candidate_acquisition_failed'
  AND d.candidate_placement_id IS NULL
  AND d.updated_at >= $3 AND d.updated_at <= $4
ORDER BY d.id`, scope.animeID, scope.releaseID, scope.acceptedAt, scope.terminalAt)
	if err != nil {
		return fmt.Errorf("read staled replace decisions: %w", err)
	}
	type decision struct {
		id      string
		version int64
	}
	var decisions []decision
	for rows.Next() {
		var d decision
		if err := rows.Scan(&d.id, &d.version); err != nil {
			rows.Close()
			return fmt.Errorf("read staled replace decision: %w", err)
		}
		decisions = append(decisions, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read staled replace decisions: %w", err)
	}
	rows.Close()
	for _, d := range decisions {
		if err := businesslog.WriteReplacementNotReplacedTx(ctx, txscope.Wrap(tx), c.Writer, businesslog.NotReplaced{
			SubscriptionID: e.record.SubscriptionID, ManagedAnimeID: e.managedAnimeID,
			DecisionID: d.id, DecisionVersion: d.version,
			ReleaseID: e.record.ReleaseID, RunContext: scope.runContext,
			Reason: scope.reason, CorrelationID: e.record.CorrelationID,
			CausationType: "acquisition", CausationID: e.record.SubjectID,
			OccurredAt: e.record.OccurredAt,
		}); err != nil {
			return fmt.Errorf("append not-replaced for decision %s: %w", d.id, err)
		}
	}
	return nil
}
