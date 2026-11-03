package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"bansoko/internal/workitems"
)

// TransientResidueDirectories lists the distinct directories bansoko has
// recorded paths in: placed files, file operation targets, admission targets,
// and the sources a delete captured from. They are the directories where a
// transient file or an emptied delete namespace can be left behind, most
// recently updated first, over the newest limit recorded paths.
func TransientResidueDirectories(ctx context.Context, db *sql.DB, limit int) ([]string, error) {
	if db == nil {
		return nil, errors.New("transient residue directories require a database")
	}
	if limit <= 0 {
		return nil, errors.New("transient residue directory limit must be positive")
	}
	rows, err := db.QueryContext(ctx, `
SELECT path FROM (
  SELECT physical_path AS path, updated_at FROM placements
  UNION ALL
  SELECT target_path AS path, updated_at FROM file_operations
  UNION ALL
  SELECT target_path AS path, updated_at FROM acquisition_targets
  UNION ALL
  SELECT source_path AS path, updated_at FROM file_manager_operations
  UNION ALL
  SELECT source_path AS path, updated_at FROM replacement_deletions
) recorded
ORDER BY updated_at DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]struct{}, limit)
	dirs := make([]string, 0, limit)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		dir := filepath.Dir(path)
		if path == "" || dir == "." || dir == string(filepath.Separator) {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return dirs, nil
}

// TransientResidueProtectedOwners lists the delete owners whose work item is
// still available or leased. A delete stage's start proof freezes the identity
// of its private namespace directory, so the startup sweep must not remove one
// while its owner may still read that receipt.
func TransientResidueProtectedOwners(ctx context.Context, db *sql.DB, limit int) (map[string]struct{}, error) {
	if db == nil {
		return nil, errors.New("transient residue protected owners require a database")
	}
	if limit <= 0 {
		return nil, errors.New("transient residue protected owner limit must be positive")
	}
	rows, err := db.QueryContext(ctx, `
SELECT DISTINCT owner_id FROM work_items
WHERE state IN ('available','leased')
  AND owner_type IN ($1,$2)
LIMIT $3`, workitems.OwnerReplacementDeletion, workitems.OwnerFileManagerOperation, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	protected := make(map[string]struct{})
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		protected[owner] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return protected, nil
}
