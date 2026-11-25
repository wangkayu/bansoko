package varspreflight

import (
	"context"
	"database/sql"
	"fmt"
)

const Migration = "163_retire_naming_vars.sql"

func Check(ctx context.Context, db Queryer) error {
	hits, err := Scan(ctx, db)
	if err != nil {
		return err
	}
	if len(hits) != 0 {
		return &Blocked{Hits: hits}
	}
	return nil
}

// CheckPending does not scan a release already migrated before a consume replay.
func CheckPending(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applied bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version=$1)`, Migration).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return nil
	}
	return Check(ctx, tx)
}

func CheckBeforeMigration(ctx context.Context, tx *sql.Tx) error {
	// Freeze the scanned inputs until the schema change commits. Readers may
	// continue during the scan, but a concurrent writer cannot invalidate it.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE public.global_config_sections,
public.naming_template_library, public.anime_config_overrides, public.anime_business_configs,
public.anime_config_candidates, public.anime_config_candidate_revisions,
public.plugin_target_configs, public.acquisitions IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock naming Vars scan inputs: %w", err)
	}
	return Check(ctx, tx)
}
