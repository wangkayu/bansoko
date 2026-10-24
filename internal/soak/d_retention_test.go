//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	animepersistence "bansoko/internal/anime/adapter/persistence"
	catalogretentionpersistence "bansoko/internal/catalog/adapter/persistence"
	posterpersistence "bansoko/internal/identitymetadata/adapter/persistence"
	observabilitypersistence "bansoko/internal/observability/adapter/persistence"
	"bansoko/internal/persistence/txscope"
	retentionpersistence "bansoko/internal/retention/adapter/persistence"
	retentionapplication "bansoko/internal/retention/application"
)

// expiredCountQueries count, per janitor class, the rows that are past retention
// and waiting for the janitor.
var expiredCountQueries = []struct{ id, query string }{
	{"terminal_work_items", `SELECT count(*) FROM work_item_outcomes WHERE retention_until <= now() AND NOT (input_snapshot ? 'one_shot')`},
	{"observability_subscription_log_entries", `SELECT count(*) FROM subscription_log_entries WHERE retention_until <= now()`},
	{"observability_plugin_deliveries", `SELECT count(*) FROM plugin_deliveries WHERE retention_until <= now()`},
	{"observability_plugin_delivery_attempts", `SELECT count(*) FROM plugin_delivery_attempts WHERE retention_until <= now()`},
	{"observability_automation_dispatch_runs", `SELECT count(*) FROM automation_dispatch_runs WHERE retention_until <= now()`},
	{"observability_diagnostic_episodes", `SELECT count(*) FROM diagnostic_episodes WHERE retention_until <= now() AND succeeded_after_at IS NOT NULL`},
	{"provider_catalog_snapshots", `SELECT count(*) FROM provider_catalog_snapshots WHERE retention_until <= now()`},
	{"provider_group_snapshots", `SELECT count(*) FROM provider_group_snapshots WHERE retention_until <= now()`},
	{"provider_feed_snapshots", `SELECT count(*) FROM provider_feed_snapshots WHERE retention_until <= now()`},
	{"inactive_discovery_releases", `SELECT count(*) FROM releases rel
JOIN rss_sources src ON src.id = rel.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN managed_anime anime ON anime.id = sub.managed_anime_id
WHERE rel.download_task_id IS NULL
  AND ((anime.lifecycle_state = 'retired' AND anime.retired_at <= now() - interval '30 days')
       OR (sub.state = 'stopped' AND sub.updated_at <= now() - interval '30 days'))`},
	{"maintenance_idempotency_records", `SELECT count(*) FROM idempotency_records WHERE created_at <= now() - interval '30 days'`},
	{"maintenance_notification_admissions", `SELECT count(*) FROM notification_admissions WHERE state IN ('resolved', 'admission_failed') AND created_at <= now() - interval '90 days'`},
}

func expiredCounts(ctx context.Context, owner *sql.DB) (map[string]int64, int64) {
	out := map[string]int64{}
	var total int64
	for _, q := range expiredCountQueries {
		var n int64
		if err := owner.QueryRowContext(ctx, q.query).Scan(&n); err != nil {
			continue
		}
		out[q.id] = n
		total += n
	}
	return out, total
}

type cleanerFn struct {
	id  string
	run func(context.Context, txscope.Tx, int) (int64, error)
}

// timedPass runs every cleaner once, each in its own transaction with the production
// batch size, and times them (the janitor's RunPass without the bookkeeping).
func timedPass(ctx context.Context, rt *sql.DB, cleaners []cleanerFn, batch int, cleanerErrors map[string]string) (perCleaner map[string]time.Duration, deleted map[string]int64, total time.Duration, err error) {
	perCleaner, deleted = map[string]time.Duration{}, map[string]int64{}
	start := time.Now()
	for _, c := range cleaners {
		cs := time.Now()
		tx, e := rt.BeginTx(ctx, nil)
		if e != nil {
			return nil, nil, 0, e
		}
		n, e := c.run(ctx, txscope.Wrap(tx), batch)
		if e != nil {
			// A cleaner may fail on a real data condition (a foreign key from a row the
			// work item cleaner has not purged yet): that is a finding, not a harness error.
			_ = tx.Rollback()
			perCleaner[c.id] = time.Since(cs)
			cleanerErrors[c.id] = e.Error()
			continue
		}
		if e := tx.Commit(); e != nil {
			return nil, nil, 0, e
		}
		perCleaner[c.id], deleted[c.id] = time.Since(cs), n
	}
	return perCleaner, deleted, time.Since(start), nil
}

func cleanerList(rt *sql.DB, svc *ownerServices) []cleanerFn {
	obs := observabilitypersistence.NewRepository(rt)
	cat := catalogretentionpersistence.NewRepository(rt)
	return []cleanerFn{
		{"observability_plugin_delivery_attempts", obs.PurgePluginDeliveryAttempts},
		{"observability_plugin_deliveries", obs.PurgePluginDeliveries},
		{"observability_subscription_log_entries", obs.PurgeSubscriptionLogEntries},
		{"observability_automation_dispatch_runs", obs.PurgeAutomationDispatchRuns},
		{"observability_diagnostic_episodes", obs.PurgeDiagnosticEpisodes},
		{"provider_catalog_snapshots", cat.PurgeExpiredProviderCatalogSnapshots},
		{"provider_group_snapshots", cat.PurgeExpiredProviderGroupSnapshots},
		{"provider_feed_snapshots", cat.PurgeExpiredProviderFeedSnapshots},
		{"anime_config_candidates", animepersistence.NewRepository(rt).PurgeEligibleAnimeConfigCandidates},
		{"terminal_work_items", svc.Store.PurgeExpiredTx},
		{"media_poster_cache", posterpersistence.NewRepository(rt).PurgeExpired},
		{"inactive_discovery_releases", retentionpersistence.NewRepository(rt).PurgeExpiredDiscoveryReleases},
		{"maintenance_idempotency_records", retentionpersistence.NewRepository(rt).PurgeExpiredIdempotencyRecords},
		{"maintenance_notification_admissions", retentionpersistence.NewRepository(rt).PurgeExpiredNotificationAdmissions},
		{"maintenance_alignment_operations", retentionpersistence.NewRepository(rt).PurgeExpiredAlignmentOperations},
		{"maintenance_reconciliation_files", retentionpersistence.NewRepository(rt).PurgeSupersededReconciliationFiles},
	}
}

// TestScaleRetention measures the retention janitor: per-cleaner batch time, one
// pass, catch-up of the expired backlog, table bloat afterwards, and seven
// simulated days of production against the production cadence (one pass per 10
// minutes, 500 rows per cleaner).
func TestScaleRetention(t *testing.T) {
	e := getEnv(t)
	if !e.Phases["retention"] {
		t.Skip("phase retention not selected")
	}
	ds := loadDataset(t)
	owner, rt := dbs(t)
	svc := ensureServices(t)
	ctx := context.Background()
	cleaners := cleanerList(rt, svc)
	janitor := newJanitor(t, rt, svc)
	pgssReset(t, owner)
	defer func() { pgssExport(t, owner, "retention"); tablesExport(t, owner, "retention") }()
	const batch = retentionapplication.DefaultBatchSize

	recordUnprunedGrowth(t, owner, ds)
	before, totalBefore := expiredCounts(ctx, owner)
	writeEvidenceFile(t, "retention-backlog-before.txt", fmt.Sprintf("%v\ntotal %d\n", before, totalBefore))

	// ---- one pass: per-cleaner batch times (the first passes carry full batches) ----
	cleanerErrors := map[string]string{}
	var passDur sample
	batchByCleaner := map[string]sample{}
	passesTimed := 3
	for i := 0; i < passesTimed; i++ {
		per, _, total, err := timedPass(ctx, rt, cleaners, batch, cleanerErrors)
		if err != nil {
			t.Fatalf("timed pass: %v", err)
		}
		passDur = append(passDur, total)
		for id, d := range per {
			batchByCleaner[id] = append(batchByCleaner[id], d)
		}
	}
	var worstBatch time.Duration
	worstID := ""
	vals := map[string]float64{}
	for id, s := range batchByCleaner {
		vals["batch_max_ms_"+id] = ms(s.max())
		if s.max() > worstBatch {
			worstBatch, worstID = s.max(), id
		}
	}
	recordResult(t, result{Phase: "retention", ID: "janitor_batch", Desc: "slowest single cleaner batch (500 rows, its own transaction)", Values: vals,
		MaxMs: ms(worstBatch), Target: "<=1s", Verdict: verdictAtMost(ms(worstBatch), 1000), Detail: "worst cleaner: " + worstID})
	recordResult(t, result{Phase: "retention", ID: "janitor_pass", Desc: "one janitor pass (12 cleaners, one batch each)", Runs: len(passDur),
		P50Ms: ms(passDur.percentile(0.5)), MaxMs: ms(passDur.max()), Target: "<=5s", Verdict: verdictAtMost(ms(passDur.max()), 5000)})

	// ---- catch-up with the real RunPass until nothing is deleted ----
	start := time.Now()
	passes, idle, failedPasses := 0, 0, 0
	var deletedTotal int64
	perClass := map[string]int64{}
	// Largest number of terminal work items one RunPass deleted: a demonstrated lower
	// bound of the janitor's per-pass capacity for that class (a pass repeats full
	// batches within its budget, so 500 rows per pass is no longer the ceiling).
	var workItemsPerPassMax int64
	var runDur sample
	for idle < 2 && time.Since(start) < 8*time.Hour {
		ps := time.Now()
		res, err := janitor.RunPass(ctx)
		if err != nil {
			// RunPass isolates a failing cleaner and still runs the others; keep going, remember why.
			failedPasses++
			if len(cleanerErrors) < 12 {
				for _, r := range res.Results {
					if r.Error != nil {
						cleanerErrors[string(r.ID)] = r.Error.Error()
					}
				}
			}
		}
		runDur = append(runDur, time.Since(ps))
		passes++
		var n int64
		for _, r := range res.Results {
			n += r.Count
			perClass[string(r.ID)] += r.Count
			if r.ID == retentionapplication.CleanerWorkItems && r.Count > workItemsPerPassMax {
				workItemsPerPassMax = r.Count
			}
		}
		deletedTotal += n
		if n == 0 {
			idle++
		} else {
			idle = 0
		}
	}
	elapsed := time.Since(start)
	passes += passesTimed
	after, totalAfter := expiredCounts(ctx, owner)
	productionHours := float64(passes-2) * 10.0 / 60.0 // one pass per 10 minutes in the server
	cvals := map[string]float64{"passes_incl_timed": float64(passes), "compute_seconds": elapsed.Seconds(), "deleted_in_runpass": float64(deletedTotal), "passes_with_a_failing_cleaner": float64(failedPasses),
		"backlog_before": float64(totalBefore), "backlog_after": float64(totalAfter), "pass_p95_ms": ms(runDur.percentile(0.95)), "production_cadence_hours": productionHours}
	for k, v := range perClass {
		cvals["deleted_"+k] = float64(v)
	}
	recordResult(t, result{Phase: "retention", ID: "backlog_catch_up", Desc: "expired backlog cleared by consecutive passes; production-cadence time = passes x 10 min", Runs: passes, Values: cvals,
		Target: "<=6h at production cadence", Verdict: verdictAtMost(productionHours, 6), Detail: fmt.Sprintf("after: %v; cleaner errors: %v", after, cleanerErrors)})

	// The slowest DELETE statement of the whole catch-up (the timed passes above only saw the first,
	// cheap batches): a batch that has to check a foreign key per deleted row can take far longer.
	var worstDelete float64
	var worstDeleteQuery string
	if err := owner.QueryRowContext(ctx, `SELECT COALESCE(max_exec_time, 0), COALESCE(left(regexp_replace(query, '\s+', ' ', 'g'), 110), '')
FROM soak_ext.pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND query ~ 'DELETE FROM' AND query !~ 'soak_ext'
ORDER BY max_exec_time DESC LIMIT 1`).Scan(&worstDelete, &worstDeleteQuery); err == nil {
		recordResult(t, result{Phase: "retention", ID: "janitor_batch_catch_up", Desc: "slowest cleaner DELETE statement during the whole catch-up (pg_stat_statements max)", MaxMs: worstDelete,
			Target: "<=1s", Verdict: verdictAtMost(worstDelete, 1000), Detail: worstDeleteQuery})
	}

	// ---- bloat after autovacuum had its chance ----
	waitAutovacuum(t, owner, "work_item_outcomes", 6*time.Minute)
	deadRatio := deadTupleRatio(ctx, owner)
	bloatVals := map[string]float64{}
	worstTable, worst := "", 0.0
	for tbl, ratio := range deadRatio {
		bloatVals["dead_ratio_"+tbl] = ratio
		if ratio > worst {
			worst, worstTable = ratio, tbl
		}
	}
	recordResult(t, result{Phase: "retention", ID: "table_bloat", Desc: "n_dead_tup / (live+dead) of the janitor-managed tables after autovacuum", Values: bloatVals,
		Target: "<20%", Verdict: verdictAtMost(worst*100, 20), Detail: fmt.Sprintf("worst %s %.1f%%", worstTable, worst*100)})

	// ---- seven simulated days (SOAK_SIM_FIRST_DAY continues an earlier run's numbering) ----
	first, days := 1, 7
	if v := envDefault("SOAK_SIM_FIRST_DAY", ""); v != "" {
		fmt.Sscanf(v, "%d", &first)
	}
	if v := envDefault("SOAK_SIM_DAYS", ""); v != "" {
		fmt.Sscanf(v, "%d", &days)
	}
	simulateDays(t, owner, rt, janitor, ds, first, days, workItemsPerPassMax)
	writeEvidenceFile(t, "retention-backlog-after.txt", fmt.Sprintf("%v\n", after))
}

func waitAutovacuum(t *testing.T, owner *sql.DB, table string, max time.Duration) {
	t.Helper()
	var baseline sql.NullTime
	_ = owner.QueryRowContext(context.Background(), `SELECT last_autovacuum FROM pg_stat_user_tables WHERE relname = $1`, table).Scan(&baseline)
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		var cur sql.NullTime
		_ = owner.QueryRowContext(context.Background(), `SELECT last_autovacuum FROM pg_stat_user_tables WHERE relname = $1`, table).Scan(&cur)
		if cur.Valid && (!baseline.Valid || cur.Time.After(baseline.Time)) {
			time.Sleep(5 * time.Second)
			return
		}
		time.Sleep(10 * time.Second)
	}
	t.Logf("autovacuum of %s did not run within %v", table, max)
}

func deadTupleRatio(ctx context.Context, owner *sql.DB) map[string]float64 {
	out := map[string]float64{}
	rows, err := owner.QueryContext(ctx, `SELECT relname, n_live_tup, n_dead_tup FROM pg_stat_user_tables WHERE n_live_tup + n_dead_tup >= 2000 AND relname = ANY($1::text[])`,
		pgArray([]string{"work_items", "work_item_outcomes", "subscription_log_entries", "plugin_deliveries", "plugin_delivery_attempts", "automation_dispatch_runs", "diagnostic_episodes",
			"provider_catalog_snapshots", "provider_catalog_entries", "provider_group_snapshots", "provider_feed_snapshots", "provider_feed_snapshot_seeds", "releases", "media_fact_snapshots"}))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var live, dead int64
		if rows.Scan(&name, &live, &dead) == nil && live+dead > 0 {
			out[name] = float64(dead) / float64(live+dead)
		}
	}
	return out
}

// warpTables age the janitor-managed rows by one simulated day: every timestamp of
// the retention window moves one day back (rows then expire when their simulated
// retention elapses). Done with triggers off, in one statement per table.
var warpStatements = []string{
	`UPDATE work_item_outcomes SET available_at = available_at - interval '1 day', terminal_at = terminal_at - interval '1 day', retention_until = retention_until - interval '1 day', created_at = created_at - interval '1 day', updated_at = updated_at - interval '1 day'`,
	`UPDATE subscription_log_entries SET occurred_at = occurred_at - interval '1 day', retention_until = retention_until - interval '1 day', created_at = created_at - interval '1 day'`,
	`UPDATE plugin_deliveries SET created_at = created_at - interval '1 day', updated_at = updated_at - interval '1 day', retention_until = retention_until - interval '1 day', delivered_at = delivered_at - interval '1 day'`,
	`UPDATE plugin_delivery_attempts SET started_at = started_at - interval '1 day', request_started_at = request_started_at - interval '1 day', response_received_at = response_received_at - interval '1 day', retention_until = retention_until - interval '1 day'`,
	`UPDATE automation_dispatch_runs SET started_at = started_at - interval '1 day', finished_at = finished_at - interval '1 day', created_at = created_at - interval '1 day', updated_at = updated_at - interval '1 day', retention_until = retention_until - interval '1 day'`,
	`UPDATE diagnostic_episodes SET first_failed_at = first_failed_at - interval '1 day', last_failed_at = last_failed_at - interval '1 day', succeeded_after_at = succeeded_after_at - interval '1 day', retention_until = retention_until - interval '1 day'`,
	`UPDATE provider_catalog_snapshots SET observed_at = observed_at - interval '1 day', health_checked_at = health_checked_at - interval '1 day', retention_until = retention_until - interval '1 day'`,
	// The discovery clock is the owner's inactivity, not the release timestamp:
	// age a stopped subscription and a retired managed anime with the day.
	`UPDATE subscriptions SET updated_at = updated_at - interval '1 day' WHERE state = 'stopped'`,
	`UPDATE managed_anime SET retired_at = retired_at - interval '1 day' WHERE lifecycle_state = 'retired'`,
}

// dailyProduction inserts one simulated day of production (Profile M volumes scaled
// by the profile), aged within the last 24 hours.
func dailyProduction(t *testing.T, owner *sql.DB, p Profile, day int) {
	t.Helper()
	scale := float64(p.Sources) / 800.0
	n := func(v float64) int { return maxInt(int(v*scale), 1) }
	rss, re, pd, qs := n(76800), n(1500), n(700), n(600)
	logs, deliveries, attempts, runs := n(7500), n(700), n(1200), n(96)
	tag := fmt.Sprintf("sim%d", day)
	replicaExec(t, owner, fmt.Sprintf(`
INSERT INTO work_item_outcomes(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts, input_snapshot,
                       expected_owner_version, terminal_at, retention_days, retention_until, created_at, updated_at)
SELECT '%[1]s-w-' || w, 'rss_source_runtime', s.id, 'rss_refresh', 'succeeded', t - interval '1 minute', 1, 'rss_refresh:' || s.id || ':%[1]s:' || w, 1, 5,
       '{"trigger":"scheduled"}'::jsonb, 1, t, 7, t + (7::double precision * interval '1 day'), t - interval '2 minutes', t
FROM (SELECT w, now() - make_interval(secs => soak_ext.rnd('%[1]s', w) * 86000) AS t, soak_ext.pick('%[1]ssrc', w, (SELECT count(*) FROM rss_sources WHERE id LIKE 'r%%')) AS pick FROM generate_series(1, %[2]d) w) q
JOIN (SELECT row_number() OVER (ORDER BY id) AS rn, id FROM rss_sources WHERE id LIKE 'r%%') s ON s.rn = q.pick`, tag, rss+re+pd+qs))
	replicaExec(t, owner, fmt.Sprintf(`
INSERT INTO subscription_log_entries(id, subscription_id, managed_anime_id, kind, outcome, occurred_at, title, summary, details, dedupe_key, retention_until, created_at, reason_code, subject_type, subject_id, subject_version)
SELECT '%[1]s-l-' || w, s.id, s.managed_anime_id, 'rss_new_content', 'info', t, 'Soak', 'simulated day event', '{}'::jsonb, '%[1]s:' || w, t + interval '30 days', t, 'rss_new_content', 'subscription', s.id, 1
FROM (SELECT w, now() - make_interval(secs => soak_ext.rnd('%[1]sl', w) * 86000) AS t, soak_ext.pick('%[1]sls', w, (SELECT count(*) FROM subscriptions)) AS pick FROM generate_series(1, %[2]d) w) q
JOIN (SELECT row_number() OVER (ORDER BY id) AS rn, id, managed_anime_id FROM subscriptions) s ON s.rn = q.pick`, tag, logs))
	replicaExec(t, owner, fmt.Sprintf(`
INSERT INTO plugin_deliveries(id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot, secret_ref, delivery_kind, trigger_kind, source_owner_type,
                              source_owner_id, source_owner_version, source_result_key, state, version, idempotency_key, payload_schema_version, payload, payload_digest,
                              template_language_version, template_catalog_version, title_template_snapshot, body_template_snapshot, target_display, attempt_count, delivered_at,
                              created_at, updated_at, retention_until)
SELECT '%[1]s-d-' || w, 't1', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'secret-plugin-1', 'business', 'release_discovered', 'release', '%[1]s-r' || w, 1, 'k' || w,
       'delivered', 1, '%[1]s-key-' || w, 1, '{}'::jsonb, soak_ext.dg('sim', w::text), 'bansoko-template-1', 'notification-catalog-v1', '{{t}}', '{{b}}', 'Soak webhook', 1, t, t, t,
       t + interval '30 days'
FROM (SELECT w, now() - make_interval(secs => soak_ext.rnd('%[1]sd', w) * 86000) AS t FROM generate_series(1, %[2]d) w) q`, tag, deliveries))
	replicaExec(t, owner, fmt.Sprintf(`
INSERT INTO plugin_delivery_attempts(id, plugin_delivery_id, attempt_number, started_at, target_display, payload_digest, retention_until)
SELECT '%[1]s-a-' || w, '%[1]s-d-' || (1 + (w %% %[3]d)), 1 + (w / %[3]d), t, 'Soak webhook', soak_ext.dg('sim', w::text), t + interval '14 days'
FROM (SELECT w, now() - make_interval(secs => soak_ext.rnd('%[1]sa', w) * 86000) AS t FROM generate_series(1, %[2]d) w) q`, tag, attempts, deliveries))
	replicaExec(t, owner, fmt.Sprintf(`
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, target_count, enqueued_count, started_at, finished_at, retention_until, created_at, updated_at)
SELECT '%[1]s-r-' || w, 'rss_refresh', 'scheduled', 'completed', 800, 800, t, t + interval '2 seconds', t + interval '2 seconds' + interval '30 days', t, t
FROM (SELECT w, now() - make_interval(secs => soak_ext.rnd('%[1]sr', w) * 86000) AS t FROM generate_series(1, %[2]d) w) q`, tag, runs))
}

// simulateDays runs `days` simulated days: age the data one day, insert the day's
// production, run the janitor 144 times (one pass per 10 minutes) and record the
// expired backlog and the database size. The size is compared between the start
// and the end after a table rewrite, so ageing updates do not count as growth.
func simulateDays(t *testing.T, owner, rt *sql.DB, janitor *retentionapplication.Service, ds *dataset, first, days int, workItemsPerPassMax int64) {
	t.Helper()
	ctx := context.Background()
	conn, err := owner.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	managed := []string{"work_items", "work_item_outcomes", "subscription_log_entries", "plugin_deliveries", "plugin_delivery_attempts", "automation_dispatch_runs", "diagnostic_episodes", "provider_catalog_snapshots", "releases", "media_fact_snapshots", "media_fact_files"}
	compact := func() int64 {
		for _, tbl := range managed {
			if _, err := owner.ExecContext(ctx, `VACUUM (FULL, ANALYZE) public.`+quoteIdent(tbl)); err != nil {
				t.Logf("vacuum full %s: %v", tbl, err)
			}
		}
		var size int64
		_ = owner.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&size)
		return size
	}
	size0 := compact()
	passesPerDay := 144
	if v := envDefault("SOAK_SIM_PASSES", ""); v != "" {
		fmt.Sscanf(v, "%d", &passesPerDay)
	}
	var backlogByDay []int64
	var deletedByDay []int64
	for day := first; day < first+days; day++ {
		for _, q := range warpStatements {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("age day %d: %v (%s)", day, err, q[:40])
			}
		}
		dailyProduction(t, owner, ds.Profile, day)
		var deleted int64
		for i := 0; i < passesPerDay; i++ {
			res, err := janitor.RunPass(ctx)
			if err != nil {
				t.Fatalf("simulated day %d pass %d: %v", day, i, err)
			}
			for _, r := range res.Results {
				deleted += r.Count
				if r.ID == retentionapplication.CleanerWorkItems && r.Count > workItemsPerPassMax {
					workItemsPerPassMax = r.Count
				}
			}
		}
		counts, _ := expiredCounts(ctx, owner)
		// Rows the cleaners keep by design (deliveries still retrying, open episodes, ...) are not janitor
		// capacity: follow the classes the janitor can always purge.
		var backlog int64
		for _, id := range []string{"terminal_work_items", "observability_subscription_log_entries", "observability_automation_dispatch_runs"} {
			backlog += counts[id]
		}
		backlogByDay = append(backlogByDay, backlog)
		deletedByDay = append(deletedByDay, deleted)
		t.Logf("simulated day %d: deleted %d, expired purgeable backlog %d (all classes %v)", day, deleted, backlog, counts)
	}
	size1 := compact()
	growthMB := float64(size1-size0) / float64(days) / (1 << 20)
	vals := map[string]float64{"size_start_bytes": float64(size0), "size_end_bytes": float64(size1), "growth_mb_per_day": growthMB}
	for i := range backlogByDay {
		vals[fmt.Sprintf("backlog_day%d", first+i)] = float64(backlogByDay[i])
		vals[fmt.Sprintf("deleted_day%d", first+i)] = float64(deletedByDay[i])
	}
	growing := backlogByDay[len(backlogByDay)-1] > backlogByDay[0]+int64(days)*10
	verdict := "pass"
	if growing {
		verdict = "miss"
	}
	scale := float64(ds.Profile.Sources) / 800.0
	produced := int(80000 * scale)
	// Capacity per class per day at the production cadence: the larger of one 500-row
	// batch per pass and the most terminal work items one pass deleted in the catch-up
	// above (or in the simulated days), times 144 passes.
	perPass := int64(500)
	if workItemsPerPassMax > perPass {
		perPass = workItemsPerPassMax
	}
	capacity := int(perPass) * 144
	vals["modelled_work_items_per_day"] = float64(produced)
	vals["janitor_capacity_per_class_per_day"] = float64(capacity)
	vals["capacity_over_production_work_items"] = float64(capacity) / float64(maxInt(produced, 1))
	// The target is a capacity ratio as well as "no growth": a class whose daily production the
	// janitor cannot keep up with only shows the growth once its retention window has elapsed.
	if float64(capacity) < 1.2*float64(produced) {
		verdict = "miss"
	}
	recordResult(t, result{Phase: "retention", ID: fmt.Sprintf("steady_state_backlog_days_%d_%d", first, first+days-1), Desc: "expired backlog per simulated day of production (144 passes/day)", Values: vals,
		Target: "backlog not growing; capacity >= 1.2x daily production", Verdict: verdict,
		Detail: fmt.Sprintf("modelled work items/day %d vs capacity %d/class/day at %dx144 (ratio %.2f)", produced, capacity, perPass, float64(capacity)/float64(maxInt(produced, 1)))})
	growthVerdict := verdictAtMost(growthMB, 150)
	recordResult(t, result{Phase: "retention", ID: fmt.Sprintf("steady_state_growth_days_%d_%d", first, first+days-1), Desc: "database growth per simulated day (after table rewrite)",
		Values: map[string]float64{"growth_mb_per_day": growthMB}, Target: "<=150MB/day", Verdict: growthVerdict,
		Detail: strings.TrimSpace(fmt.Sprintf("start %d MB, end %d MB; non-janitor classes (releases, graph) are not modelled here", size0>>20, size1>>20))})
}

// recordUnprunedGrowth records (no limit) how fast the classes no janitor prunes
// grow: bytes per release and per accepted download across their tables, and the
// resulting growth per owner per month at the dataset's own release rate.
func recordUnprunedGrowth(t *testing.T, owner *sql.DB, ds *dataset) {
	t.Helper()
	ctx := context.Background()
	sizeOf := func(tables ...string) (int64, int64) {
		var bytes, rows int64
		for _, tbl := range tables {
			var b, r int64
			if err := owner.QueryRowContext(ctx, `SELECT pg_total_relation_size($1::regclass), (SELECT count(*) FROM public.`+quoteIdent(tbl)+`)`, "public."+quoteIdent(tbl)).Scan(&b, &r); err == nil {
				bytes += b
				rows += r
			}
		}
		return bytes, rows
	}
	// S8 folded target_observations into acquisition_targets; S4 folded the
	// acquisition children into acquisitions.
	releaseTables := []string{"releases", "media_fact_snapshots", "media_fact_files", "acquisition_targets"}
	graphTables := []string{"acquisitions", "acquisition_files",
		"download_tasks", "artifacts", "artifact_files",
		"postprocessing_plans", "file_operations", "download_completions", "placements", "placement_coverage",
		"placement_obligations"}
	relBytes, relRows := sizeOf("releases")
	allRelBytes, _ := sizeOf(releaseTables...)
	graphBytes, _ := sizeOf(graphTables...)
	_, acqRows := sizeOf("acquisitions")
	var oldest, newest time.Time
	var owners int64
	_ = owner.QueryRowContext(ctx, `SELECT min(discovered_at), max(discovered_at), (SELECT count(*) FROM subscriptions WHERE origin <> 'oneshot') FROM releases`).Scan(&oldest, &newest, &owners)
	spanDays := newest.Sub(oldest).Hours() / 24
	if spanDays < 1 {
		spanDays = 1
	}
	perRelease := float64(allRelBytes) / float64(maxInt(int(relRows), 1))
	perAcq := float64(graphBytes) / float64(maxInt(int(acqRows), 1))
	releasesPerOwnerMonth := float64(relRows) / float64(maxInt(int(owners), 1)) / spanDays * 30
	acqPerOwnerMonth := float64(acqRows) / float64(maxInt(int(owners), 1)) / spanDays * 30
	recordResult(t, result{Phase: "retention", ID: "unpruned_growth", Desc: "growth of classes no janitor prunes (releases and the download graph), per owner per month", Verdict: "info",
		Values: map[string]float64{"bytes_per_release_all_tables": perRelease, "bytes_per_accepted_download_all_tables": perAcq, "releases_table_bytes": float64(relBytes),
			"releases_per_owner_per_month": releasesPerOwnerMonth, "acquisitions_per_owner_per_month": acqPerOwnerMonth,
			"mb_per_owner_per_month": (perRelease*releasesPerOwnerMonth + perAcq*acqPerOwnerMonth) / (1 << 20), "span_days": spanDays}})
	_ = ds
}
