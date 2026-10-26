package filemaintenance

import (
	"context"
	"database/sql"
	"fmt"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
)

// companionCleanupDB is the read/write surface the companion handoff needs;
// both *sql.DB and *sql.Tx satisfy it.
type companionCleanupDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// loserCompanion is one active companion of a superseded loser placement that
// the deletion handoff may take.
type loserCompanion struct {
	ID        string
	Path      string
	SizeBytes int64
}

// loserCompanionsForCleanup returns the loser placement's active companions
// that cleanup may take. A companion is never admitted when its path is owned
// by something else (never delete media): an
// active placement's path, another active companion's path, or the target of
// one of the winner's sidecar operations. Such a companion's relation ends
// instead (path_owned_by_winner), and the file stays.
func loserCompanionsForCleanup(ctx context.Context, db companionCleanupDB, loserPlacementID, winnerPlacementID string) ([]loserCompanion, error) {
	rows, err := db.QueryContext(ctx, `
SELECT c.id, c.current_path, c.size_bytes,
       (EXISTS (SELECT 1 FROM placements p WHERE p.physical_path = c.current_path AND p.state = 'active')
        OR EXISTS (SELECT 1 FROM placement_companions o WHERE o.current_path = c.current_path AND o.state = 'active' AND o.id <> c.id)
        OR EXISTS (SELECT 1 FROM placements w JOIN file_operations s ON s.bound_video_operation_id = w.source_owner_ref
                   WHERE w.id = $2 AND w.source_evidence_type = 'file_operation' AND s.target_path = c.current_path)) AS owned
FROM placement_companions c
WHERE c.placement_id = $1 AND c.state = 'active'
  AND NOT EXISTS (SELECT 1 FROM replacement_deletions co WHERE co.companion_id = c.id)
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
		if err := rows.Scan(&companion.ID, &companion.Path, &companion.SizeBytes, &isOwned); err != nil {
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
	if _, err := db.ExecContext(ctx, `UPDATE replacement_deletions SET companion_id = $2 WHERE id = $1 AND companion_id IS NULL`, cleanupID, companionID); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE placement_companions SET state = 'ended', ended_reason = 'cleanup_admitted', version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND state = 'active'`, companionID)
	return err
}

func companionCleanupID(companionID string) string { return "cln-" + companionID }

// admitCompanionDeletionsTx records, in the caller's transaction, one
// deletion obligation per cleanable companion of a superseded loser. It runs
// for every superseded loser, including a same-path loser whose video is not
// deleted: that loser's companions are deleted unless the winner now owns
// their path. A companion's identity is its recorded size at its path.
func (s *ReplacementDecisionService) admitCompanionDeletionsTx(ctx context.Context, scope txscope.Tx, d *decisionRow, loserPlacementID, winnerPlacementID string, coverage []string) error {
	tx := scope.SQLTx()
	companions, err := loserCompanionsForCleanup(ctx, tx, loserPlacementID, winnerPlacementID)
	if err != nil {
		return err
	}
	for _, companion := range companions {
		id := companionCleanupID(companion.ID)
		if err := s.cleanup.admitTx(ctx, scope, CleanupInput{
			ID:                id,
			IdempotencyKey:    "cln:" + companion.ID + ":" + d.ID,
			PlacementID:       loserPlacementID,
			ManagedAnimeID:    d.AnimeID,
			Coverage:          coverage,
			ReplacementRef:    d.ID,
			AdmissionMode:     string(d.AdmissionMode),
			SourcePath:        companion.Path,
			SourceFingerprint: domain.MetadataFingerprint(companion.SizeBytes, domain.FileIdentity{}),
			CorrelationID:     d.CorrelationID,
			CausationType:     "replacement_decision",
			CausationID:       d.ID,
		}); err != nil {
			return fmt.Errorf("admit companion deletion: %w", err)
		}
		if err := endCompanionForCleanup(ctx, tx, id, companion.ID); err != nil {
			return err
		}
	}
	return nil
}
