package migrations

import "testing"

// The one-shot marker table keeps its closed state set, its terminal field
// matrix, effect-lease accounting and the unique request/anime indexes.
func TestOneShotAcquisitionSchema(t *testing.T) {
	requireFragments(t, baselineText(t), "one-shot acquisition schema",
		"CREATE TABLE one_shot_acquisitions",
		"state IN ('running', 'reclaim_pending', 'cleanup_blocked', 'reclaiming', 'cleaned')",
		"terminal_outcome IN ('succeeded', 'failed')",
		"one_shot_acquisitions_terminal_field_matrix_check",
		"active_effect_count = jsonb_array_length(active_effect_leases)",
		"jsonb_array_length(active_effect_leases) = 0 OR state IN ('running', 'cleanup_blocked')",
		"request_hash ~ '^sha256:[0-9a-f]{64}$'",
		"CREATE UNIQUE INDEX one_shot_acquisitions_request_key_uq",
		"CREATE UNIQUE INDEX one_shot_acquisitions_managed_anime_uq",
		"CREATE INDEX one_shot_acquisitions_retention_idx",
		"jsonb_typeof(input_snapshot) = 'object'",
		"pg_column_size(input_snapshot) <= 262144",
		"one_shot_acquisitions_adopted_terminal_check",
	)
}
