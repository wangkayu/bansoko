package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// ReleaseVerdictStartBaseline marks a Release of a subscription with
// new-releases-only option that was published
// before the instance was created, or has no publish time. Automatic paths
// treat it as terminal: it is not evaluated against the rules and a rule
// change does not re-evaluate it. Unlike resume_baseline, an explicit retry
// evaluates it normally through every rule; only this gate is skipped then.
// It is a quiet verdict: no failure and no discovery notice.
const ReleaseVerdictStartBaseline = "start_baseline"

var errStartBaselineMissing = errors.New("new releases only is on but the subscription has no frozen start baseline")

// startBaselineApplies reports whether the Release takes start_baseline. The
// baseline is the instance's frozen creation time; a Release published at or
// after it is new. A Release without a publish time cannot be shown to be new
// and takes the baseline verdict. An explicit retry never applies the gate.
// The option without its baseline fails closed.
func startBaselineApplies(snapshot releaseSnapshot, published time.Time) (bool, error) {
	if snapshot.manualRetry || !snapshot.newReleasesOnly {
		return false, nil
	}
	if !snapshot.startBaselineAt.Valid {
		return false, errStartBaselineMissing
	}
	return StartBaselineCovers(snapshot.startBaselineAt.Time, published), nil
}

// StartBaselineCovers is the one baseline comparison: a zero publish time or
// one strictly before the baseline is covered. The pending test bench uses it
// to mark the rows a start would record as start_baseline.
func StartBaselineCovers(baseline, published time.Time) bool {
	return published.IsZero() || published.Before(baseline)
}

// startBaselineWriter stores the start_baseline verdict. It writes no
// activity entry and drops a pending discovery notice: the Release is history
// from before the subscription, not a discovery and not a failure.
func (s *Service) startBaselineWriter(claim workitems.Claim, snapshot releaseSnapshot) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("start baseline owner writer transaction is nil")
		}
		var currentVersion int64
		var currentTask sql.NullString
		var currentVerdict string
		if err := tx.QueryRowContext(ctx, `
SELECT version, download_task_id, verdict
FROM releases
WHERE id = $1
FOR UPDATE`, snapshot.releaseID).Scan(&currentVersion, &currentTask, &currentVerdict); err != nil {
			return 0, fmt.Errorf("lock release for start baseline: %w", err)
		}
		if currentTask.Valid || isTerminalReleaseVerdict(currentVerdict) {
			return currentVersion, nil
		}
		if currentVersion != claim.ExpectedOwnerVersion {
			return 0, fmt.Errorf("%w: expected %d, got %d", errReleaseVersionConflict, claim.ExpectedOwnerVersion, currentVersion)
		}
		var version int64
		if err := tx.QueryRowContext(ctx, `
UPDATE releases
SET verdict = $2, reason_code = $2, reason_detail = NULL,
    evaluation_version = $3, evaluated_rules_version = $4,
    discovery_notice_due = FALSE,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $5 AND download_task_id IS NULL
RETURNING version`, snapshot.releaseID, ReleaseVerdictStartBaseline, evaluationVersionString(), snapshot.rulesVersion, claim.ExpectedOwnerVersion).Scan(&version); err != nil {
			return 0, fmt.Errorf("persist start baseline release: %w", err)
		}
		return version, nil
	}
}
