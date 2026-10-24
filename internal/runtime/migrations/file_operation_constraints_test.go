package migrations

import (
	"strings"
	"testing"
)

// The overwrite execution snapshot is required for, and only allowed with, the
// overwrite collision policy, and must carry the operation identity and a plan
// digest.
func TestFileOperationOverwriteSnapshotConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	// The probe table copies CONSTRAINTS and DEFAULTS but not triggers, so the
	// BEFORE INSERT guard that would populate budget_clock_at never runs here.
	// The budget columns are therefore supplied explicitly below; without them
	// file_attempt_budget_shape rejects every row, the valid one included.
	p.accept("probe table", `CREATE TEMP TABLE file_operation_constraint_probe (LIKE file_operations INCLUDING CONSTRAINTS INCLUDING DEFAULTS)`)
	insertProbe := `INSERT INTO file_operation_constraint_probe(
		id, artifact_id, artifact_version, artifact_file_id, operation, source_path, target_path,
		source_fingerprint, collision_policy, frozen_render_snapshot, frozen_config_snapshot,
		overwrite_execution_snapshot, intent, state, idempotency_key, version, created_at, updated_at,
		acquisition_id,file_ref,postprocessing_plan_id,file_ready_at,file_wall_until,file_max_attempts,file_attempt_timeout_seconds,
		budget_clock_at,file_attempts_started,file_attempt_index,file_attempt_ticket
	) VALUES ($1, 'artifact', 1, 'file', 'copy', '/source', '/target', '{}', $2, '{}', '{}', $3, 'ensure_present', 'pending', $4, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP,
	'probe-root','probe-file','probe-plan',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP+INTERVAL '1 day',3,60,
	CURRENT_TIMESTAMP,0,'[]'::jsonb,NULL)`
	validSnapshot := `{"protocol_version":"overwrite-execution-v1","operation_id":"operation-valid","plan_digest":"` + strings.Repeat("a", 64) + `","recovery":{}}`
	p.accept("valid overwrite execution snapshot", insertProbe, "operation-valid", "overwrite", validSnapshot, "probe-valid")
	p.reject("overwrite snapshot operation identity", insertProbe,
		"operation-wrong-id", "overwrite", validSnapshot, "probe-wrong-id")
	p.reject("overwrite snapshot plan digest", insertProbe,
		"operation-bad-digest", "overwrite", `{"protocol_version":"overwrite-execution-v1","operation_id":"operation-bad-digest","plan_digest":"bad","recovery":{}}`, "probe-bad-digest")
	p.reject("non-overwrite execution snapshot", insertProbe,
		"operation-block", "block", `{"protocol_version":"overwrite-execution-v1","operation_id":"operation-block","plan_digest":"`+strings.Repeat("b", 64)+`","recovery":{}}`, "probe-block")
}
