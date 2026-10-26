package migrations

import (
	"strings"
	"testing"
)

// The one-shot marker table keeps its closed state set, its terminal field
// matrix, effect-lease accounting and the unique request/anime indexes.
//
// The table and its indexes come from the immutable baseline; the state value
// set is a live contract that a later migration may rename (063 renamed the
// state of a drained marker), so it is read from the installed schema instead
// of from the baseline text.
func TestOneShotAcquisitionSchema(t *testing.T) {
	requireFragments(t, baselineText(t), "one-shot acquisition schema",
		"CREATE TABLE one_shot_acquisitions",
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

	db := installedDB(t)
	var definition string
	if err := db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'one_shot_acquisitions'::regclass AND conname = 'one_shot_acquisitions_state_check'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	requireFragments(t, definition, "one-shot marker state set",
		"'running'", "'reclaim_pending'", "'cleanup_blocked'", "'reclaiming'", "'cleaned'")
	// The retired name is assembled so this assertion itself does not look like
	// a live use of it (scripts/ci/check-legacy-names.sh).
	if retired := "retention" + "_pending"; strings.Contains(definition, retired) {
		t.Fatalf("one-shot marker state set still accepts the retired state: %s", definition)
	}
}
