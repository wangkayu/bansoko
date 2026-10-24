//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// consistencyChecks are read-only self-checks of the directly written graph:
// each query counts violations and must return 0. Foreign keys were not enforced
// during the load (replica role), so they are proven here.
var consistencyChecks = []struct{ name, query string }{
	{"subscriptions -> managed_anime", `SELECT count(*) FROM subscriptions s WHERE NOT EXISTS (SELECT 1 FROM managed_anime m WHERE m.id = s.managed_anime_id)`},
	{"rss_sources -> subscriptions", `SELECT count(*) FROM rss_sources r WHERE NOT EXISTS (SELECT 1 FROM subscriptions s WHERE s.id = r.subscription_id)`},
	{"runtime for every source", `SELECT count(*) FROM rss_sources r WHERE NOT EXISTS (SELECT 1 FROM rss_source_runtime x WHERE x.rss_source_id = r.id)`},
	{"releases -> rss_sources", `SELECT count(*) FROM releases r WHERE NOT EXISTS (SELECT 1 FROM rss_sources s WHERE s.id = r.rss_source_id)`},
	{"releases.download_task_id -> download_tasks", `SELECT count(*) FROM releases r WHERE r.download_task_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM download_tasks t WHERE t.id = r.download_task_id AND t.release_id = r.id)`},
	{"submitted release has a task", `SELECT count(*) FROM releases r WHERE r.verdict = 'submitted' AND r.download_task_id IS NULL`},
	{"download_tasks -> releases", `SELECT count(*) FROM download_tasks t WHERE NOT EXISTS (SELECT 1 FROM releases r WHERE r.id = t.release_id)`},
	{"download_tasks -> acquisitions", `SELECT count(*) FROM download_tasks t WHERE NOT EXISTS (SELECT 1 FROM acquisitions a WHERE a.id = t.acquisition_id)`},
	{"every acquisition has a task", `SELECT count(*) FROM acquisitions a WHERE a.execution_branch = 'download' AND NOT EXISTS (SELECT 1 FROM download_tasks t WHERE t.acquisition_id = a.id)`},
	{"every acquisition has input+manifest+vars", `SELECT count(*) FROM acquisitions a WHERE a.input_canonical_utf8 IS NULL OR a.manifest_digest IS NULL
		OR (a.id NOT LIKE 'real-%' AND a.vars_evaluation_canonical_utf8 IS NULL)`},
	{"input digest = root digest", `SELECT count(*) FROM acquisitions a WHERE a.input_schema_version <> 1
		OR a.admission_digest <> 'sha256:'||encode(sha256(convert_to('acquisition-input:v1','UTF8')||decode('00','hex')||convert_to(a.input_canonical_utf8,'UTF8')),'hex')
		OR (a.input->'schema_version') <> to_jsonb(a.input_schema_version)`},
	{"manifest item_count = manifest files", `SELECT count(*) FROM acquisitions a WHERE a.manifest_item_count <> (SELECT count(*) FROM acquisition_files f WHERE f.acquisition_id = a.id)`},
	{"requirements digests chain", `SELECT count(*) FROM acquisitions a WHERE a.requirements_digest IS NOT NULL AND a.requirements_manifest_digest <> a.manifest_digest`},
	{"target set digests chain", `SELECT count(*) FROM acquisitions a WHERE a.target_digest IS NOT NULL AND a.target_requirements_digest <> a.requirements_digest`},
	{"declared target -> requirement file", `SELECT count(*) FROM acquisition_files d WHERE d.spec_digest IS NOT NULL AND d.requirement IS DISTINCT FROM 'required'`},
	{"artifacts -> tasks (same root)", `SELECT count(*) FROM artifacts a WHERE NOT EXISTS (SELECT 1 FROM download_tasks t WHERE t.id = a.download_task_id AND t.acquisition_id = a.acquisition_id)`},
	{"artifact_files -> artifacts (same root)", `SELECT count(*) FROM artifact_files f WHERE NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.id = f.artifact_id AND a.acquisition_id = f.acquisition_id)`},
	{"succeeded/inflight roots have an artifact", `SELECT count(*) FROM acquisitions a JOIN download_tasks t ON t.acquisition_id = a.id WHERE t.state = 'completed' AND NOT EXISTS (SELECT 1 FROM artifacts x WHERE x.download_task_id = t.id)`},
	{"plans -> artifacts", `SELECT count(*) FROM postprocessing_plans p WHERE NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.id = p.artifact_id AND a.acquisition_id = p.acquisition_id)`},
	{"file_operations -> artifact_files+plan", `SELECT count(*) FROM file_operations o WHERE NOT EXISTS (SELECT 1 FROM artifact_files f WHERE f.id = o.artifact_file_id AND f.artifact_id = o.artifact_id AND f.source_path = o.source_path)
		OR NOT EXISTS (SELECT 1 FROM postprocessing_plans p WHERE p.id = o.postprocessing_plan_id AND p.acquisition_id = o.acquisition_id)`},
	{"file_operations -> requirement file", `SELECT count(*) FROM file_operations o WHERE NOT EXISTS (SELECT 1 FROM acquisition_files f WHERE f.acquisition_id = o.acquisition_id AND f.file_ref = o.file_ref AND f.requirement = o.required_tag)`},
	{"delivery results -> operations", `SELECT count(*) FROM acquisition_files d WHERE d.outcome IS NOT NULL AND NOT EXISTS (SELECT 1 FROM file_operations o WHERE o.id = d.operation_id AND o.state = 'completed')`},
	{"succeeded root: every required file delivered", `SELECT count(*) FROM acquisitions a WHERE a.state = 'succeeded' AND a.execution_branch = 'download' AND a.id NOT LIKE 'real-%' AND
		(SELECT count(*) FROM acquisition_files f WHERE f.acquisition_id = a.id AND f.requirement = 'required') <> (SELECT count(*) FROM acquisition_files d WHERE d.acquisition_id = a.id AND d.outcome IS NOT NULL)`},
	{"placements -> managed_anime", `SELECT count(*) FROM placements p WHERE NOT EXISTS (SELECT 1 FROM managed_anime m WHERE m.id = p.managed_anime_id)`},
	{"placements(file_operation) -> completed op", `SELECT count(*) FROM placements p WHERE p.source_evidence_type = 'file_operation' AND NOT EXISTS (SELECT 1 FROM file_operations o WHERE o.id = p.source_owner_ref AND o.state = 'completed' AND o.target_path = p.physical_path)`},
	{"coverage -> placements", `SELECT count(*) FROM placement_coverage c WHERE NOT EXISTS (SELECT 1 FROM placements p WHERE p.id = c.placement_id)`},
	{"download_completions -> succeeded root", `SELECT count(*) FROM download_completions c WHERE NOT EXISTS (SELECT 1 FROM acquisitions a WHERE a.id = c.acquisition_id AND a.state = 'succeeded')`},
	{"media_fact_files -> snapshots", `SELECT count(*) FROM media_fact_files f WHERE NOT EXISTS (SELECT 1 FROM media_fact_snapshots s WHERE s.id = f.snapshot_id)`},
	{"media_fact_snapshots -> releases", `SELECT count(*) FROM media_fact_snapshots s WHERE NOT EXISTS (SELECT 1 FROM releases r WHERE r.id = s.release_id)`},
	{"targets/observations -> releases", `SELECT count(*) FROM acquisition_targets t WHERE NOT EXISTS (SELECT 1 FROM releases r WHERE r.id = t.release_id)
		OR (t.observation_state IS NULL) <> (t.observed_at IS NULL)`},
	{"work_items(rss_refresh) -> source runtime", `SELECT count(*) FROM work_item_history w WHERE w.kind = 'rss_refresh' AND NOT EXISTS (SELECT 1 FROM rss_source_runtime r WHERE r.rss_source_id = w.owner_id)`},
	{"work_items(release_evaluate) -> release", `SELECT count(*) FROM work_item_history w WHERE w.kind = 'release_evaluate' AND NOT EXISTS (SELECT 1 FROM releases r WHERE r.id = w.owner_id)`},
	{"work_items(plugin_deliver) -> delivery", `SELECT count(*) FROM work_item_history w WHERE w.kind = 'plugin_deliver' AND NOT EXISTS (SELECT 1 FROM plugin_deliveries d WHERE d.id = w.owner_id)`},
	{"work_items(qb_*) -> task+acquisition+key", `SELECT count(*) FROM work_item_history w WHERE w.kind IN ('qb_sync', 'qb_submit') AND NOT EXISTS (SELECT 1 FROM download_tasks t WHERE t.id = w.owner_id AND t.acquisition_id = w.acquisition_id AND t.idempotency_key = w.idempotency_key)`},
	{"deliveries -> target", `SELECT count(*) FROM plugin_deliveries d WHERE NOT EXISTS (SELECT 1 FROM plugin_target_configs t WHERE t.id = d.plugin_target_config_id)`},
	{"delivery attempts -> deliveries", `SELECT count(*) FROM plugin_delivery_attempts a WHERE NOT EXISTS (SELECT 1 FROM plugin_deliveries d WHERE d.id = a.plugin_delivery_id)`},
	{"logs -> subscriptions", `SELECT count(*) FROM subscription_log_entries l WHERE NOT EXISTS (SELECT 1 FROM subscriptions s WHERE s.id = l.subscription_id AND s.managed_anime_id = l.managed_anime_id)`},
	{"catalog entries -> snapshots", `SELECT count(*) FROM provider_catalog_entries e WHERE NOT EXISTS (SELECT 1 FROM provider_catalog_snapshots s WHERE s.id = e.snapshot_id)`},
	{"one-shot markers -> release/task", `SELECT count(*) FROM one_shot_acquisitions o WHERE NOT EXISTS (SELECT 1 FROM releases r WHERE r.id = o.release_id AND r.download_task_id = o.download_task_id)`},
	{"live work items are unique per owner/kind", `SELECT count(*) FROM (SELECT 1 FROM work_items WHERE state IN ('available', 'leased') GROUP BY owner_type, owner_id, kind HAVING count(*) > 1) x`},
	{"terminal work items carry retention", `SELECT count(*) FROM work_item_outcomes WHERE state NOT IN ('succeeded', 'failed') OR retention_until IS DISTINCT FROM terminal_at + retention_days * interval '1 day'`},
	{"compact_pinned matches its facts", `SELECT count(*) FROM acquisitions r WHERE r.compact_pinned IS DISTINCT FROM (
	  EXISTS (SELECT 1 FROM download_tasks t JOIN releases rel ON rel.download_task_id = t.id WHERE t.acquisition_id = r.id)
	  OR EXISTS (SELECT 1 FROM file_operations o JOIN placement_obligations po ON po.file_operation_id = o.id WHERE o.acquisition_id = r.id))`},
	{"completion_open matches its facts", `SELECT count(*) FROM acquisitions r WHERE r.completion_open IS DISTINCT FROM (
	  r.state = 'succeeded' AND r.execution_branch = 'download' AND r.record_state = 'full'
	  AND NOT EXISTS (SELECT 1 FROM download_completions c WHERE c.acquisition_id = r.id))`},
}

// runConsistencyChecks proves the loaded graph with the read-only checks above.
func runConsistencyChecks(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	bad := 0
	for _, c := range consistencyChecks {
		var n int64
		if err := db.QueryRowContext(ctx, c.query).Scan(&n); err != nil {
			t.Errorf("self-check %q: %v", c.name, err)
			bad++
			continue
		}
		if n != 0 {
			t.Errorf("self-check %q: %d violations", c.name, n)
			bad++
		}
	}
	if bad == 0 {
		t.Logf("self-check: %d consistency checks passed", len(consistencyChecks))
	}
}

// shapeTables are the tables of the accepted-download graph, with the SQL
// expression naming the acquisition root of a row. The real accept path is the
// reference (ids starting with real-), the generated rows are the rest.
var shapeTables = []struct{ table, root string }{
	{"acquisitions", "id"}, {"acquisition_files", "acquisition_id"},
	{"download_tasks", "acquisition_id"}, {"artifacts", "acquisition_id"},
	{"artifact_files", "acquisition_id"}, {"postprocessing_plans", "acquisition_id"},
	{"file_operations", "acquisition_id"}, {"work_item_history", "acquisition_id"},
}

// shapeStageDependent are columns that only a later stage fills (the real
// sample stops at completed file operations): real NULL and generated non-null
// is expected there.
var shapeStageDependent = map[string]bool{
	"acquisitions.terminal_at": true, "acquisitions.terminal_schema": true, "acquisitions.terminal_receipt": true, "acquisitions.detail_until": true,
}

// compareShapes compares, per table and column, whether the real accept path
// and the generated rows fill the column, and the state-like value sets.
func compareShapes(t *testing.T, db *sql.DB) []string {
	t.Helper()
	ctx := context.Background()
	var diffs []string
	for _, st := range shapeTables {
		cols, err := tableColumns(ctx, db, st.table)
		if err != nil {
			t.Fatal(err)
		}
		var realRows, genRows int64
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FILTER (WHERE %[2]s LIKE 'real-%%'), count(*) FILTER (WHERE %[2]s LIKE 'acq-%%') FROM %[1]s`, st.table, st.root)).Scan(&realRows, &genRows); err != nil {
			t.Fatal(err)
		}
		if realRows == 0 {
			t.Logf("shape %-34s real=0 generated=%d (not produced by the real sample, skipped)", st.table, genRows)
			continue
		}
		if genRows == 0 {
			diffs = append(diffs, fmt.Sprintf("%s: real sample has %d rows, generator wrote none", st.table, realRows))
			continue
		}
		var parts []string
		for _, c := range cols {
			parts = append(parts, fmt.Sprintf(`count(%[1]s) FILTER (WHERE %[2]s LIKE 'real-%%') AS r_%[3]d, count(%[1]s) FILTER (WHERE %[2]s LIKE 'acq-%%') AS g_%[3]d`, quoteIdent(c), st.root, len(parts)/2))
		}
		row := db.QueryRowContext(ctx, `SELECT `+strings.Join(parts, ", ")+` FROM `+st.table)
		dest := make([]any, 2*len(cols))
		vals := make([]int64, 2*len(cols))
		for i := range dest {
			dest[i] = &vals[i]
		}
		if err := row.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		for i, c := range cols {
			realFilled, genFilled := vals[2*i] > 0, vals[2*i+1] > 0
			key := st.table + "." + c
			switch {
			case realFilled && !genFilled:
				diffs = append(diffs, fmt.Sprintf("%s: filled by the real path (%d rows) but never by the generator", key, vals[2*i]))
			case !realFilled && genFilled && !shapeStageDependent[key]:
				t.Logf("shape info: %s filled by the generator only (later stage than the real sample)", key)
			}
		}
		// state-like value sets
		for _, c := range cols {
			if !isStateColumn(c) {
				continue
			}
			rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT %[1]s::text FROM %[2]s WHERE %[3]s LIKE 'real-%%' AND %[1]s IS NOT NULL
				EXCEPT SELECT DISTINCT %[1]s::text FROM %[2]s WHERE %[3]s LIKE 'acq-%%' AND %[1]s IS NOT NULL`, quoteIdent(c), st.table, st.root))
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var v string
				_ = rows.Scan(&v)
				diffs = append(diffs, fmt.Sprintf("%s.%s: real value %q is absent from the generated data", st.table, c, v))
			}
			rows.Close()
		}
		t.Logf("shape %-34s real=%d generated=%d columns=%d compared", st.table, realRows, genRows, len(cols))
	}
	sort.Strings(diffs)
	return diffs
}

func isStateColumn(c string) bool {
	switch c {
	case "state", "kind", "operation", "media_kind", "execution_branch", "admission_kind", "record_state", "intent", "collision_policy",
		"collision", "preparation_mode", "owner_type", "subtitle_binding_state", "requirement", "outcome":
		return true
	}
	return false
}

func tableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
