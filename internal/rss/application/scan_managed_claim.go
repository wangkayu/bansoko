package application

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"bansoko/internal/acquisition/target"
	"bansoko/internal/observability/businesslog"
	"bansoko/internal/persistence/txscope"
)

// A scan-managed file (a placement the library scan admitted, source evidence
// "reconciliation") is a managed file without a release: it has no group and
// no comparison values until a release claims it. A release claims it when
// the release passes evaluation and its rendered target is that file's exact
// path with the exact entry size — the same evidence that adopts an existing
// target (adopted_existing). The claim gives the placement the release as its
// quality facts: from then on it pools by the release's group and compares
// with the values the release froze.

// staleReasonClaimedExisting marks the pre-acquisition replace decisions of a
// release that claimed the scan-managed files at its targets instead of
// downloading: the release never places a file of its own, so those
// decisions never bind.
const staleReasonClaimedExisting = "candidate_claimed_existing"

// scanManagedUnclaimedAt reports whether path holds an active scan-managed
// placement of the anime that no release has claimed. A placement kept out of
// the instance's identity (not replacement-eligible) is never claimable, so a
// claim never makes it eligible again.
func scanManagedUnclaimedAt(ctx context.Context, q queryer, managedAnimeID, path string) (bool, error) {
	var claimable bool
	if err := q.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM placements p
    WHERE p.managed_anime_id = $1 AND p.physical_path = $2
      AND p.source_evidence_type = 'reconciliation' AND p.quality_facts_ref IS NULL
      AND p.state = 'active' AND p.verification_state = 'verified' AND p.replacement_eligible)`, managedAnimeID, path).Scan(&claimable); err != nil {
		return false, fmt.Errorf("read scan-managed file at target: %w", err)
	}
	return claimable, nil
}

// staledReplaceDecision is one admitted replace decision a claim staled: it
// will never take effect, so the release writes its not_replaced line.
type staledReplaceDecision struct {
	id      string
	version int64
}

// claimScanManagedTargetsTx attaches the release to every unclaimed
// scan-managed placement of the anime at one of the adopted targets, inside
// the release owner transaction: quality_facts_ref = release and
// replacement_eligible. The release's pre-acquisition decisions go stale
// (they never bind: the release places no file of its own). It returns the
// number of claimed placements and the replace decisions it staled.
func claimScanManagedTargetsTx(ctx context.Context, tx *sql.Tx, managedAnimeID, releaseID string, set target.Set) (int64, []staledReplaceDecision, error) {
	paths := make([]string, 0, len(set.Targets))
	for _, spec := range set.Targets {
		paths = append(paths, spec.TargetPath)
	}
	if len(paths) == 0 {
		return 0, nil, nil
	}
	result, err := tx.ExecContext(ctx, `
UPDATE placements p
SET quality_facts_ref = $2, replacement_eligible = true,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE p.managed_anime_id = $1 AND p.physical_path = ANY($3)
  AND p.source_evidence_type = 'reconciliation' AND p.quality_facts_ref IS NULL
  AND p.state = 'active' AND p.verification_state = 'verified' AND p.replacement_eligible`, managedAnimeID, releaseID, paths)
	if err != nil {
		return 0, nil, fmt.Errorf("claim scan-managed placements: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return 0, nil, err
	}
	if claimed == 0 {
		return 0, nil, nil
	}
	rows, err := tx.QueryContext(ctx, `
UPDATE replacement_decisions
SET state = 'stale', stale_reason = $3, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND candidate_release_id = $2
  AND candidate_placement_id IS NULL AND state = 'admitted'
RETURNING id, version, verdict`, managedAnimeID, releaseID, staleReasonClaimedExisting)
	if err != nil {
		return 0, nil, fmt.Errorf("stale claimed release decisions: %w", err)
	}
	defer rows.Close()
	var staled []staledReplaceDecision
	for rows.Next() {
		var decision staledReplaceDecision
		var verdict string
		if err := rows.Scan(&decision.id, &decision.version, &verdict); err != nil {
			return 0, nil, fmt.Errorf("read stale claimed release decision: %w", err)
		}
		if verdict == "replace" {
			staled = append(staled, decision)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("stale claimed release decisions: %w", err)
	}
	sort.Slice(staled, func(i, j int) bool { return staled[i].id < staled[j].id })
	return claimed, staled, nil
}

// writeClaimNotReplacedTx writes the not_replaced line (superseded) of every
// replace decision the release's claim staled, after the release's own entry,
// so it takes no row lock that entry has not already taken.
func (s *Service) writeClaimNotReplacedTx(ctx context.Context, tx *sql.Tx, snapshot releaseSnapshot, staled []staledReplaceDecision, correlationID string) error {
	for _, decision := range staled {
		if err := businesslog.WriteReplacementNotReplacedTx(ctx, txscope.Wrap(tx), s.activityWriter, businesslog.NotReplaced{
			SubscriptionID: snapshot.subscriptionID, ManagedAnimeID: snapshot.managedAnimeID,
			DecisionID: decision.id, DecisionVersion: decision.version,
			ReleaseID: snapshot.releaseID, RunContext: snapshot.activityRunContext(),
			Reason: businesslog.NotReplacedSuperseded, CorrelationID: correlationID,
			CausationType: "release", CausationID: snapshot.releaseID,
		}); err != nil {
			return fmt.Errorf("write claimed release not-replaced activity: %w", err)
		}
	}
	return nil
}
