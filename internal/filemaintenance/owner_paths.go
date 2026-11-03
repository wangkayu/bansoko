package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
)

// AutomaticOwnerPaths is the SQL fragment listing every path an unfinished
// automatic owner holds: unfinished file operations (source and target),
// pending or retrying placement handoffs, live alignments, admitted deletions
// (source and quarantine) and admitted or queued replacement decisions
// (candidate and frozen losers). Each row names the owning subsystem's kind
// and, for the intake chain, the acquisition. The file manager's read model,
// its admission checks and the reconciliation recovery probe all consume this
// one text so the occupancy decision cannot drift between them.
const AutomaticOwnerPaths = `
SELECT fo.source_path AS path, 'file_operation' AS kind, COALESCE(fo.acquisition_id, '') AS acquisition_id
  FROM file_operations fo WHERE fo.state IN ('pending', 'running', 'waiting_retry')
UNION ALL
SELECT fo.target_path, 'file_operation', COALESCE(fo.acquisition_id, '')
  FROM file_operations fo WHERE fo.state IN ('pending', 'running', 'waiting_retry')
UNION ALL
SELECT fo.target_path, 'placement_handoff', COALESCE(fo.acquisition_id, '')
  FROM placement_obligations po JOIN file_operations fo ON fo.id = po.file_operation_id
  WHERE po.state IN ('pending', 'waiting_retry')
UNION ALL
SELECT a.source_path, 'alignment', '' FROM alignment_operations a WHERE a.state IN ('planned', 'ready', 'running')
UNION ALL
SELECT a.target_path, 'alignment', '' FROM alignment_operations a WHERE a.state IN ('planned', 'ready', 'running')
UNION ALL
SELECT c.source_path, 'cleanup', '' FROM replacement_deletions c
  WHERE c.state = 'admitted' OR (c.state = 'blocked' AND c.attempt IS NOT NULL)
UNION ALL
SELECT c.attempt->>'capture_path', 'cleanup', '' FROM replacement_deletions c
  WHERE (c.state = 'admitted' OR c.state = 'blocked') AND c.attempt ? 'capture_path'
UNION ALL
SELECT p.physical_path, 'replacement', '' FROM replacement_decisions d JOIN placements p ON p.id = d.candidate_placement_id
  WHERE d.state IN ('admitted', 'queued')
UNION ALL
SELECT p.physical_path, 'replacement', '' FROM replacement_decisions d
  CROSS JOIN LATERAL jsonb_array_elements(d.pools) pool
  CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(pool->'losers') = 'array' THEN pool->'losers' ELSE '[]'::jsonb END) loser
  JOIN placements p ON p.id = loser->>'placement_id'
  WHERE d.state IN ('admitted', 'queued')`

// UserOperationPaths is the unfinished user file operation surface: source,
// requested target, resolved result and delete capture.
const UserOperationPaths = `
UNION ALL
SELECT u.source_path, 'user_operation', '' FROM file_manager_operations u WHERE u.state IN ('ready', 'running')
UNION ALL
SELECT u.target_path, 'user_operation', '' FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.target_path IS NOT NULL
UNION ALL
SELECT u.result_path, 'user_operation', '' FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.result_path IS NOT NULL
UNION ALL
SELECT u.attempt->>'quarantine_path', 'user_operation', '' FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.attempt ? 'quarantine_path'`

// UnfinishedOwnerPaths is the whole non-terminal owner surface: the automatic
// owners plus the unfinished user file operations.
const UnfinishedOwnerPaths = AutomaticOwnerPaths + UserOperationPaths

// OwnerPathsHeld reports whether an unfinished owner holds any of the paths.
func OwnerPathsHeld(ctx context.Context, tx *sql.Tx, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	var held bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM (`+UnfinishedOwnerPaths+`) o WHERE o.path = ANY($1::text[]))`, paths).Scan(&held)
	return held, err
}

// ManagedPaths reports whether an active placement or an active companion of
// an active placement names any of the paths.
func ManagedPaths(ctx context.Context, tx *sql.Tx, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	var managed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM placements WHERE state = 'active' AND physical_path COLLATE "C" = ANY($1::text[]))
 OR EXISTS (SELECT 1 FROM placement_companions c JOIN placements p ON p.id = c.placement_id
   WHERE c.state = 'active' AND p.state = 'active' AND c.current_path = ANY($1::text[]))`, paths).Scan(&managed)
	return managed, err
}

// MatchAnimeTakesFilesTx reports whether an anime still takes matched files:
// its lifecycle is not retired, its subscription is active or stopped, and it
// is no one-shot's temporary anime. With lock the lifecycle row is held
// against a concurrent retirement until the transaction ends.
func MatchAnimeTakesFilesTx(ctx context.Context, tx *sql.Tx, managedAnimeID string, lock bool) (retired, takes bool, err error) {
	query := `SELECT lifecycle_state FROM managed_anime WHERE id = $1`
	if lock {
		query += ` FOR SHARE`
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, query, managedAnimeID).Scan(&lifecycle); errors.Is(err, sql.ErrNoRows) {
		return true, false, nil
	} else if err != nil {
		return false, false, err
	}
	if lifecycle != "active" {
		return true, false, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM subscriptions WHERE managed_anime_id = $1 AND state IN ('active', 'stopped'))
 AND NOT EXISTS (SELECT 1 FROM one_shot_acquisitions WHERE managed_anime_id = $1)`, managedAnimeID).Scan(&takes)
	if err != nil {
		return false, false, err
	}
	return false, takes, nil
}
