package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
)

// TransientResidueDirectories lists the distinct directories bansoko has
// recorded paths in: placed files, file operation targets, and admission
// targets. They are the directories where a transient file can be left behind,
// most recently updated first, over the newest limit recorded paths.
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
