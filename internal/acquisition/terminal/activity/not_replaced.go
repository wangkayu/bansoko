package activity

import (
	"context"
	"database/sql"
	"fmt"

	"bansoko/internal/observability/businesslog"
	"bansoko/internal/persistence/txscope"
)

// notReplacedScope is what the settlement of a failed or cancelled root needs
// to write the not_replaced lines of the replace decisions its terminal
// transition staled: the trigger (stale_failed_candidate_decisions) names the
// root in each decision it stales (stale_acquisition_id), so the decisions are
// found by that link alone.
type notReplacedScope struct {
	reason     businesslog.NotReplacedReason
	runContext map[string]any
}

// writeNotReplacedTx writes, in the settlement entry's transaction and after
// it, the not_replaced line of each replace decision the root's failure or
// cancellation staled. The decisions are a plain read and every line names the
// settlement's own subscription and Release, so it takes no lock the
// settlement entry has not already taken; a decision of an anime other than the
// entry's is not written under it. The key is the decision, so a replay is a
// no-op, and after an import retry the re-settlement finds the first run's
// decisions again (no-ops) beside the ones its own failure staled.
func (c *Consumer) writeNotReplacedTx(ctx context.Context, tx *sql.Tx, e entry) error {
	scope := e.notReplaced
	rows, err := tx.QueryContext(ctx, `
SELECT d.id, d.version
FROM replacement_decisions d
WHERE d.stale_acquisition_id = $1
  AND d.verdict = 'replace' AND d.stale_reason = 'candidate_acquisition_failed'
  AND d.managed_anime_id = $2
ORDER BY d.id`, e.record.SubjectID, e.managedAnimeID)
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
