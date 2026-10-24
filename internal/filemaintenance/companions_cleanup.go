package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	persistence "bansoko/internal/filemaintenance/adapter/persistence"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// companionCleanupDB is the read/write surface the companion handoff needs;
// both *sql.DB and *sql.Tx satisfy it.
type companionCleanupDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// loserCompanion is one active companion of a superseded loser placement that
// the cleanup handoff may quarantine.
type loserCompanion struct {
	ID          string
	Path        string
	Fingerprint string
}

// loserCompanionsForCleanup returns the loser placement's active companions
// that cleanup may take. A companion is never admitted when its path is owned
// by something else (never delete media): an
// active placement's path, another active companion's path, or the target of
// one of the winner's sidecar operations. Such a companion's relation ends
// instead (path_owned_by_winner), and the file stays.
func loserCompanionsForCleanup(ctx context.Context, db companionCleanupDB, loserPlacementID, winnerPlacementID string) ([]loserCompanion, error) {
	rows, err := db.QueryContext(ctx, `
SELECT c.id, c.current_path, c.fingerprint,
       (EXISTS (SELECT 1 FROM placements p WHERE p.physical_path = c.current_path AND p.state = 'active')
        OR EXISTS (SELECT 1 FROM placement_companions o WHERE o.current_path = c.current_path AND o.state = 'active' AND o.id <> c.id)
        OR EXISTS (SELECT 1 FROM placements w JOIN file_operations s ON s.bound_video_operation_id = w.source_owner_ref
                   WHERE w.id = $2 AND w.source_evidence_type = 'file_operation' AND s.target_path = c.current_path)) AS owned
FROM placement_companions c
WHERE c.placement_id = $1 AND c.state = 'active'
  AND NOT EXISTS (SELECT 1 FROM cleanup_operations co WHERE co.companion_id = c.id)
ORDER BY c.id`, loserPlacementID, winnerPlacementID)
	if err != nil {
		return nil, fmt.Errorf("load loser companions: %w", err)
	}
	defer rows.Close()
	var admit []loserCompanion
	var owned []string
	for rows.Next() {
		var companion loserCompanion
		var isOwned bool
		if err := rows.Scan(&companion.ID, &companion.Path, &companion.Fingerprint, &isOwned); err != nil {
			return nil, err
		}
		if isOwned {
			owned = append(owned, companion.ID)
			continue
		}
		admit = append(admit, companion)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, id := range owned {
		if _, err := db.ExecContext(ctx, `UPDATE placement_companions SET state = 'ended', ended_reason = 'path_owned_by_winner', version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND state = 'active'`, id); err != nil {
			return nil, err
		}
	}
	return admit, nil
}

// endCompanionForCleanup closes the companion relation once its cleanup
// record owns the file.
func endCompanionForCleanup(ctx context.Context, db companionCleanupDB, cleanupID, companionID string) error {
	if _, err := db.ExecContext(ctx, `UPDATE cleanup_operations SET companion_id = $2 WHERE id = $1 AND companion_id IS NULL`, cleanupID, companionID); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE placement_companions SET state = 'ended', ended_reason = 'cleanup_admitted', version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND state = 'active'`, companionID)
	return err
}

func companionCleanupID(companionID string) string { return "cln-" + companionID }

// handoffCompanionsToCleanup admits one cleanup record per cleanable
// companion of a loser placement under the decision's cleanup authorization.
// It runs for every loser, including a same-path loser whose video is not
// cleaned: that loser's companions are cleaned unless the winner now owns
// their path.
func (s *ReplacementDecisionService) handoffCompanionsToCleanup(ctx context.Context, decision *decisionRow, loser PlacementRef, coverage []string) error {
	winnerID := ""
	if decision.Winner != nil {
		winnerID = decision.Winner.PlacementID
	}
	companions, err := loserCompanionsForCleanup(ctx, s.db, loser.PlacementID, winnerID)
	if err != nil || len(companions) == 0 {
		return err
	}
	var effect string
	var graceDeadline time.Time
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(NULLIF(cleanup_policy->>'effect', ''), 'quarantine'),
		       CURRENT_TIMESTAMP + make_interval(secs => COALESCE(NULLIF(cleanup_policy->>'grace_seconds', '')::bigint, 0))
		FROM anime_business_configs WHERE managed_anime_id = $1 AND retention_version = $2 AND cleanup_policy IS NOT NULL`, decision.AnimeID, decision.PolicyVersion).
		Scan(&effect, &graceDeadline); err != nil {
		return fmt.Errorf("read companion cleanup policy: %w", err)
	}
	for _, companion := range companions {
		id := companionCleanupID(companion.ID)
		if _, err := s.cleanup.Admit(ctx, CleanupInput{
			ID:                id,
			IdempotencyKey:    "cln:" + companion.ID + ":" + decision.ID,
			PlacementID:       loser.PlacementID,
			ManagedAnimeID:    decision.AnimeID,
			Coverage:          coverage,
			ReplacementRef:    decision.ID,
			PolicyVersion:     decision.PolicyVersion,
			RetentionMode:     string(decision.AdmissionMode),
			SourcePath:        companion.Path,
			SourceFingerprint: companion.Fingerprint,
			Effect:            effect,
			GraceDeadline:     &graceDeadline,
			CorrelationID:     decision.CorrelationID,
			CausationType:     "replacement_decision",
			CausationID:       decision.ID,
		}); err != nil {
			return fmt.Errorf("admit companion cleanup: %w", err)
		}
		if err := endCompanionForCleanup(ctx, s.db, id, companion.ID); err != nil {
			return err
		}
	}
	return nil
}

// handoffRecoveryCompanionsTx is the recovered-decision variant: it persists
// the companion cleanup obligations in the caller's transaction.
func (s *ReplacementDecisionService) handoffRecoveryCompanionsTx(ctx context.Context, scope txscope.Tx, d *decisionRow, loserPlacementID string, coverage []byte, effect string, deadline time.Time) error {
	tx := scope.SQLTx()
	winnerID := ""
	if d.Winner != nil {
		winnerID = d.Winner.PlacementID
	}
	companions, err := loserCompanionsForCleanup(ctx, tx, loserPlacementID, winnerID)
	if err != nil {
		return err
	}
	for _, companion := range companions {
		id := companionCleanupID(companion.ID)
		if err := s.cleanup.repository.InsertTx(ctx, scope, persistence.OperationInput{ID: id, IdempotencyKey: "cln:" + companion.ID + ":" + d.ID, PlacementID: loserPlacementID, ManagedAnimeID: d.AnimeID, Coverage: coverage, ReplacementRef: d.ID, PolicyVersion: d.PolicyVersion, RetentionMode: string(d.AdmissionMode), SourcePath: companion.Path, SourceFingerprint: companion.Fingerprint, Effect: effect, GraceDeadline: &deadline, CorrelationID: d.CorrelationID, CausationType: "replacement_decision", CausationID: d.ID}); err != nil {
			return err
		}
		if err := endCompanionForCleanup(ctx, tx, id, companion.ID); err != nil {
			return err
		}
		var version int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM cleanup_operations WHERE id=$1 AND replacement_ref=$2 FOR UPDATE`, id, d.ID).Scan(&version); err != nil {
			return err
		}
		snapshot, _ := json.Marshal(map[string]any{"cleanup_id": id, "stage": workitems.KindCleanupQuarantine})
		if _, _, err := s.cleanup.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerCleanup, ID: id}, Kind: workitems.KindCleanupQuarantine, IdempotencyKey: "cln:" + id + ":" + workitems.KindCleanupQuarantine, InputSnapshot: snapshot, MaxAttempts: 3, ExpectedOwnerVersion: version}); err != nil {
			return err
		}
	}
	return nil
}
