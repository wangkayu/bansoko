package migrations

import (
	"context"
	"testing"
)

// 098 retires the offset executor: an offset plan that never applied, of a
// generation still current, carries forward as a re-key of the placement's
// coverage plus an alignment request; every plan and generation row is then
// removed and the runtime keeps no write privilege on the two tables.
func TestOffsetAlignmentRequestsCarryUnappliedPlansForward(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "096_seasonless_key_rewrite.sql")

	seedRewriteInstance(t, db, "o1", "", "")
	for _, placement := range []struct{ id, key string }{
		{"o1-open", "tv:e1"}, {"o1-occupied", "tv:e3"}, {"o1-noop", "tv:e5"},
		{"o1-applied", "tv:e7"}, {"o1-old-generation", "tv:e9"}, {"o1-ineligible", "tv:e11"},
	} {
		seedRewritePlacement(t, db, "o1", placement.id, "active", placement.key)
	}
	rewriteExec(t, db,
		`UPDATE placements SET replacement_eligible = false WHERE id = 'o1-ineligible'`,
		`INSERT INTO offset_migration_generations(id, managed_anime_id, scope_kind, scope_owner_id, policy_version, old_offset, new_offset, state, completed_at)
		 VALUES ('gen-old', 'o1', 'rss_source', 'src-o1', 1, 0, 1, 'superseded', CURRENT_TIMESTAMP - interval '1 minute'),
		        ('gen-current', 'o1', 'rss_source', 'src-o1', 2, 0, 1, 'running', NULL)`,
		`INSERT INTO offset_migration_plans(id, managed_anime_id, placement_id, policy_version, source_fingerprint, old_target_path, state, generation_id, old_coverage_key, new_coverage_key, reason_code)
		 VALUES ('plan-open', 'o1', 'o1-open', 2, 'fp-open', '/library/o1-open.mkv', 'ready', 'gen-current', 'tv:e1', 'tv:e2', NULL),
		        ('plan-occupied', 'o1', 'o1-occupied', 2, 'fp-occupied', '/library/o1-occupied.mkv', 'blocked', 'gen-current', 'tv:e3', 'tv:e4', 'target_occupied'),
		        ('plan-noop', 'o1', 'o1-noop', 2, 'fp-noop', '/library/o1-noop.mkv', 'blocked', 'gen-current', 'tv:e5', 'tv:e6', 'movie_noop'),
		        ('plan-applied', 'o1', 'o1-applied', 2, 'fp-applied', '/library/o1-applied.mkv', 'applied', 'gen-current', 'tv:e6', 'tv:e7', NULL),
		        ('plan-old-generation', 'o1', 'o1-old-generation', 1, 'fp-old', '/library/o1-old-generation.mkv', 'planned', 'gen-old', 'tv:e9', 'tv:e10', NULL),
		        ('plan-ineligible', 'o1', 'o1-ineligible', 2, 'fp-ineligible', '/library/o1-ineligible.mkv', 'planned', 'gen-current', 'tv:e11', 'tv:e12', NULL)`,
	)

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	// The two plans that never applied are re-keyed and requested.
	assertRewritePlacement(t, db, "upgrade", "o1-open", "active", true, 2, "tv:e2")
	assertRewritePlacement(t, db, "upgrade", "o1-occupied", "active", true, 2, "tv:e4")
	// A typed no-op, an applied plan, a superseded generation and an excluded
	// placement keep their keys.
	assertRewritePlacement(t, db, "upgrade", "o1-noop", "active", true, 1, "tv:e5")
	assertRewritePlacement(t, db, "upgrade", "o1-applied", "active", true, 1, "tv:e7")
	assertRewritePlacement(t, db, "upgrade", "o1-old-generation", "active", true, 1, "tv:e9")
	assertRewritePlacement(t, db, "upgrade", "o1-ineligible", "active", false, 1, "tv:e11")
	rewriteExpect(t, db, "upgrade", "the re-keyed placements are requested for alignment, with no offset change",
		`SELECT array_agg(placement_id ORDER BY placement_id) = ARRAY['o1-occupied', 'o1-open']
		        AND bool_and(offset_change_id IS NULL AND outcome IS NULL)
		 FROM alignment_requests`)
	rewriteExpect(t, db, "upgrade", "every plan and generation is removed",
		`SELECT (SELECT count(*) FROM offset_migration_plans) = 0 AND (SELECT count(*) FROM offset_migration_generations) = 0
		        AND (SELECT count(*) FROM offset_alignment_changes) = 0`)
	rewriteExpect(t, db, "upgrade", "the runtime cannot write the retired tables and writes the new ones",
		`SELECT NOT has_table_privilege('bansoko_runtime', 'offset_migration_plans', 'INSERT')
		        AND NOT has_table_privilege('bansoko_runtime', 'offset_migration_generations', 'UPDATE')
		        AND has_table_privilege('bansoko_runtime', 'offset_migration_plans', 'SELECT')
		        AND has_table_privilege('bansoko_runtime', 'alignment_requests', 'INSERT')
		        AND has_table_privilege('bansoko_runtime', 'offset_alignment_changes', 'UPDATE')`)
	rewriteExpect(t, db, "upgrade", "the work item kind is gone from the owner/kind constraint",
		`SELECT pg_get_constraintdef(oid) NOT LIKE '%offset_migration%'
		 FROM pg_constraint WHERE conname = 'work_items_owner_kind_check'`)
}
