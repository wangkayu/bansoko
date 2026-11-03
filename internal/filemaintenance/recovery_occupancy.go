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
// refuses new operations on, defined once in UnfinishedOwnerPaths), or when
// the candidate placement's OWN handoff obligation is pending, waiting_retry
// or failed (a failed handoff can be manually re-opened and would admit
// companions against the operation's original target path). Sharing the text
// keeps the two decisions from drifting apart.
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
) OR EXISTS(SELECT 1 FROM (` + UnfinishedOwnerPaths + `) h
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
