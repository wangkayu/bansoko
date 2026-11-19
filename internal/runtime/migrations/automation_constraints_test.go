package migrations

import "testing"

// Dispatch run counters must agree with the run state.
func TestAutomationDispatchRunConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("partial dispatch", `
		INSERT INTO automation_dispatch_runs(
			id, kind, trigger, state, target_count, enqueued_count, dispatch_error_count,
			started_at, finished_at
		) VALUES ('dispatch-partial', 'rss_refresh', 'scheduled', 'partial', 2, 1, 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	p.accept("partial dispatch with inline completion but no queued work", `
		INSERT INTO automation_dispatch_runs(
			id, kind, trigger, state, target_count, completed_count, dispatch_error_count,
			started_at, finished_at
		) VALUES ('dispatch-partial-inline', 'catalog_refresh', 'scheduled', 'partial', 2, 1, 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	p.reject("failed dispatch with confirmed progress",
		`INSERT INTO automation_dispatch_runs(
			id, kind, trigger, state, target_count, completed_count, dispatch_error_count,
			started_at, finished_at
		) VALUES ('dispatch-failed-inline', 'catalog_refresh', 'manual', 'failed', 2, 1, 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	p.reject("live dispatch with a terminal count",
		`INSERT INTO automation_dispatch_runs(id, kind, trigger, state, completed_count)
		 VALUES ('dispatch-queued-inline', 'catalog_refresh', 'manual', 'queued', 0)`)
	p.reject("negative completion count",
		`UPDATE automation_dispatch_runs SET completed_count = -1 WHERE id = 'dispatch-partial-inline'`)
	p.reject("completed dispatch with errors",
		`INSERT INTO automation_dispatch_runs(
			id, kind, trigger, state, target_count, enqueued_count, dispatch_error_count,
			started_at, finished_at
		) VALUES ('dispatch-completed-errors', 'group_discovery', 'manual', 'completed', 1, 1, 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	p.reject("partial dispatch without accepted target",
		`INSERT INTO automation_dispatch_runs(
			id, kind, trigger, state, target_count, enqueued_count, dispatch_error_count,
			started_at, finished_at
		) VALUES ('dispatch-partial-empty', 'metadata_match', 'manual', 'partial', 1, 0, 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
}

// The global scope uses the 'global' sentinel id; other scopes never do.
func TestRoutineActivityScopeConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("global routine activity", `
		INSERT INTO routine_activities(
			id, kind, scope_kind, scope_id, managed_anime_id, outcome,
			last_executed_at, last_result_code, summary
		) VALUES ('routine-global', 'rss_refresh', 'global', 'global', NULL, 'success',
			CURRENT_TIMESTAMP, 'ok', 'RSS refresh completed')`)
	p.reject("global routine activity with non-sentinel scope",
		`INSERT INTO routine_activities(
			id, kind, scope_kind, scope_id, outcome, last_executed_at, last_result_code, summary
		) VALUES ('routine-global-invalid', 'rss_refresh', 'global', 'instance', 'success',
			CURRENT_TIMESTAMP, 'ok', 'RSS refresh completed')`)
	p.reject("non-global routine activity with global sentinel",
		`INSERT INTO routine_activities(
			id, kind, scope_kind, scope_id, outcome, last_executed_at, last_result_code, summary
		) VALUES ('routine-provider-invalid', 'provider_refresh', 'provider', 'global', 'success',
			CURRENT_TIMESTAMP, 'ok', 'Provider refresh completed')`)
}

// Only one open diagnostic episode exists per target; closing it lets a new
// episode on the same target open.
func TestDiagnosticEpisodeOpenIdentity(t *testing.T) {
	p := newConstraintProbe(t)
	const insert = `
		INSERT INTO diagnostic_episodes(
			id, kind, variant, severity, target_id, affected_ids, recent,
			first_failed_at, last_failed_at, occurrence_count
		) VALUES ($1, 'metadata', 'unreachable', 'warning', 'tmdb', '{}', '[]'::jsonb,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1)`
	p.accept("open episode", insert, "diagnostic-1")
	p.reject("second open episode on the same target", insert, "diagnostic-2")
	p.accept("close episode", `UPDATE diagnostic_episodes SET succeeded_after_at = CURRENT_TIMESTAMP WHERE id = 'diagnostic-1'`)
	p.accept("new episode after the success boundary", insert, "diagnostic-2")
}
