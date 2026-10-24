package migrations

import "testing"

// Offset migration generations are scoped to RSS sources only, are unique per
// scope and policy version, and plans carry the recovery index. Work items for
// an offset migration operation are owned by that operation.
func TestOffsetMigrationSchema(t *testing.T) {
	text := baselineText(t)
	requireFragments(t, text, "offset migration schema",
		"CREATE TABLE offset_migration_generations",
		"CONSTRAINT offset_migration_generations_scope_kind_check CHECK (scope_kind = 'rss_source')",
		"CONSTRAINT offset_migration_plans_scope_kind_check CHECK (scope_kind IS NULL OR scope_kind = 'rss_source')",
		"'completed_with_blocks', 'failed'",
		"UNIQUE (scope_kind, scope_owner_id, policy_version)",
		"generation_id TEXT",
		"media_fact_file_fk",
		"offset_migration_plans_recovery_idx",
		"owner_type = 'offset_migration_operation' AND kind = 'offset_migration_execute'",
	)
}
