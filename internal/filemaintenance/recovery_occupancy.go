package filemaintenance

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// RecoveryOccupancy is the SQL implementation of the reconciliation scan's
// recovery-probe occupancy port: it reports whether any of the exact paths is
// already owned by ANOTHER active placement or companion relation, or held by
// an unfinished owner (the same non-terminal-owner surface the file manager
// refuses operations on). The candidate's own placement is excluded, so a
// placement sitting at its own rendered target is never a conflict.
type RecoveryOccupancy struct {
	db *sql.DB
}

// NewRecoveryOccupancy wires the probe-side occupancy read over the shared
// pool.
func NewRecoveryOccupancy(db *sql.DB) *RecoveryOccupancy {
	return &RecoveryOccupancy{db: db}
}

// RecoveryTargetsOccupied implements the reconciliation application's
// RecoveryOccupancyChecker port. Any read failure is an error: the probe
// degrades the run instead of freezing a candidate whose ownership surface
// could not be read.
func (o *RecoveryOccupancy) RecoveryTargetsOccupied(ctx context.Context, paths []string, exceptPlacementID string) (bool, error) {
	if o == nil || o.db == nil {
		return true, nil
	}
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return true, nil
	}
	return recoveryPathsOccupied(ctx, o.db, cleaned, exceptPlacementID)
}

// recoveryPathOccupancyQuery is the one occupancy read shared by the scan's
// recovery probe and the confirm's own re-verification: a path is taken when
// another ACTIVE placement or companion relation owns it, or when any
// non-terminal owner still holds it (the same surface the file manager
// refuses new operations on), or when the candidate placement's OWN handoff
// obligation is pending, waiting_retry or failed (a failed handoff can be
// manually re-opened and would admit companions against the operation's
// original target path). Sharing the text keeps the two decisions from
// drifting apart.
const recoveryPathOccupancyQuery = `
SELECT EXISTS(
  SELECT 1 FROM placements p
   WHERE p.physical_path COLLATE "C" = ANY($1::text[])
     AND p.state = 'active' AND p.id <> $2
) OR EXISTS(
  SELECT 1 FROM placement_companions c
   WHERE c.current_path COLLATE "C" = ANY($1::text[])
     AND c.state = 'active' AND c.placement_id <> $2
) OR EXISTS(
  SELECT 1 FROM placements p
  JOIN placement_obligations po ON po.file_operation_id = p.source_owner_ref
   WHERE p.id = $2 AND p.source_evidence_type = 'file_operation'
     AND po.state IN ('pending', 'waiting_retry', 'failed')
) OR EXISTS(SELECT 1 FROM (` + recoveryUnfinishedOwners + `) h
            WHERE h.path COLLATE "C" = ANY($1::text[]))`

// recoveryPathsOccupied runs the shared occupancy read. An empty path list is
// occupied by construction (nothing to re-attach); a read failure is an
// error so both callers fail closed.
func recoveryPathsOccupied(ctx context.Context, q queryer, paths []string, exceptPlacementID string) (bool, error) {
	if q == nil || len(paths) == 0 {
		return true, nil
	}
	var occupied bool
	if err := q.QueryRowContext(ctx, recoveryPathOccupancyQuery, paths, exceptPlacementID).Scan(&occupied); err != nil {
		return false, fmt.Errorf("read recovery target occupancy: %w", err)
	}
	return occupied, nil
}

// recoveryUnfinishedOwners is the non-terminal owner surface the file manager
// holds checks refuse on: unfinished file operations (source and target),
// pending/retrying placement handoffs, live alignments, admitted deletions
// (source and quarantine), admitted/queued replacement decisions and
// unfinished user file operations. The reconciliation
// package cannot import the filemanager store (layering), so the read lives
// here verbatim; a drift is a boundary-check finding.
const recoveryUnfinishedOwners = `
SELECT fo.source_path AS path FROM file_operations fo WHERE fo.state IN ('pending', 'running', 'waiting_retry')
UNION ALL SELECT fo.target_path FROM file_operations fo WHERE fo.state IN ('pending', 'running', 'waiting_retry')
UNION ALL SELECT fo.target_path FROM placement_obligations po JOIN file_operations fo ON fo.id = po.file_operation_id
  WHERE po.state IN ('pending', 'waiting_retry')
UNION ALL SELECT a.source_path FROM alignment_operations a WHERE a.state IN ('planned', 'ready', 'running')
UNION ALL SELECT a.target_path FROM alignment_operations a WHERE a.state IN ('planned', 'ready', 'running')
UNION ALL SELECT c.source_path FROM replacement_deletions c
  WHERE c.state = 'admitted' OR (c.state = 'blocked' AND c.attempt IS NOT NULL)
UNION ALL SELECT c.attempt->>'capture_path' FROM replacement_deletions c
  WHERE (c.state = 'admitted' OR c.state = 'blocked') AND c.attempt ? 'capture_path'
UNION ALL SELECT p.physical_path FROM replacement_decisions d JOIN placements p ON p.id = d.candidate_placement_id
  WHERE d.state IN ('admitted', 'queued')
UNION ALL SELECT p.physical_path FROM replacement_decisions d
  CROSS JOIN LATERAL jsonb_array_elements(d.pools) pool
  CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(pool->'losers') = 'array' THEN pool->'losers' ELSE '[]'::jsonb END) loser
  JOIN placements p ON p.id = loser->>'placement_id'
  WHERE d.state IN ('admitted', 'queued')
UNION ALL SELECT u.source_path FROM file_manager_operations u WHERE u.state IN ('ready', 'running')
UNION ALL SELECT u.target_path FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.target_path IS NOT NULL
UNION ALL SELECT u.result_path FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.result_path IS NOT NULL
UNION ALL SELECT u.attempt->>'quarantine_path' FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.attempt ? 'quarantine_path'`
