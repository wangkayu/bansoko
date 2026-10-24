package application

import (
	"context"
	"database/sql"
)

// OverwriteTargetManaged reports whether an owner manages the file at path:
// an active or superseded placement, or an active companion. Under the
// overwrite collision setting acceptance asks it whether an approved quality
// replacement may apply; without one the file is replaced directly and its
// placement is displaced when the successor activates. The predicate lives
// in the database (overwrite_target_managed).
func OverwriteTargetManaged(ctx context.Context, db *sql.DB, path string) (bool, error) {
	var managed bool
	err := db.QueryRowContext(ctx, `SELECT public.overwrite_target_managed($1)`, path).Scan(&managed)
	return managed, err
}
