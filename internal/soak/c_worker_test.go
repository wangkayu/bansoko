//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	acquisitionactivity "bansoko/internal/acquisition/terminal/activity"
	acquisitionsettlement "bansoko/internal/acquisition/terminal/settlement"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	automationapplication "bansoko/internal/automation/application"
	catalogadapter "bansoko/internal/catalog/adapter"
	catalogrecordretentionpersistence "bansoko/internal/catalog/adapter/persistence"
	downloadapplication "bansoko/internal/download/application"
	downloaddomain "bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/downloadcompletion"
	posterpersistence "bansoko/internal/identitymetadata/adapter/persistence"
	observabilitypersistence "bansoko/internal/observability/adapter/persistence"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	retentionpersistence "bansoko/internal/recordretention/adapter/persistence"
	retentionapplication "bansoko/internal/recordretention/application"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	rssapplication "bansoko/internal/rss/application"
	runtimeworker "bansoko/internal/runtime/worker"
	selectionapplication "bansoko/internal/selection/application"
	"bansoko/internal/workitems"
)

func workerSeconds() time.Duration {
	if v := envDefault("SOAK_WORKER_SECONDS", "90"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 90 * time.Second
}

// Harness constants of the worker_under_reads measurement (W8). The PRUNE-046
// final soak reported a 100% drop (with-reads 0 items/min) because both halves
// shared the dataset's live release_evaluate pool: the first half drained it,
// so the second half started from an empty queue. Both constants below are
// named so the measurement no longer depends on what earlier subtests left.
const (
	// workerUnderReadsBacklog is the fresh release_evaluate backlog each half
	// builds for itself before its window. Sized to outlast the default 90 s
	// window with wide margin: SCALE-034 measured ~3,247 items/min without
	// reads (Profile M, 4 cores) and the accepted with-reads floor is
	// ~1,483/min, so 90 s consumes at most ~4,900 items; 20,000 is the size of
	// the dataset's own live release_evaluate backlog (Profile M
	// WorkAvailable). A profile with fewer candidate releases simply builds
	// less; the built count is logged and recorded.
	workerUnderReadsBacklog = 20000

	// underReadsCadence caps each of the 4 readers at one request per interval,
	// matching the read load of the accepted scale run:
	// the 54.3% drop (3,247 -> 1,483 items/min, Profile M, 4 cores) was
	// measured with four closed-loop readers whose pace was set by read
	// latency - 878 ListAnimeApplication calls inside the 90 s with-reads
	// window (~10 requests/s). 100 ms per reader reproduces that load and
	// keeps a faster read path or machine from silently weighing more than
	// the accepted baseline; readers slower than the cadence (workspace,
	// records) are latency-bound and unaffected.
	underReadsCadence = 100 * time.Millisecond
)

// verdictAtMost / verdictAtLeast turn a scalar into a pass/miss against a locked limit.
func verdictAtMost(v, limit float64) string {
	if v <= limit {
		return "pass"
	}
	return "miss"
}

func verdictAtLeast(v, limit float64) string {
	if v >= limit {
		return "pass"
	}
	return "miss"
}

// TestScaleWorker measures the claim/settle path with no-op handlers, the idle
// cost of every background loop, the rss_refresh / release_evaluate /
// plugin_deliver chains over local fixtures, and worker throughput while reads run.
func TestScaleWorker(t *testing.T) {
	e := getEnv(t)
	if !e.Phases["worker"] {
		t.Skip("phase worker not selected")
	}
	ds := loadDataset(t)
	owner, rt := dbs(t)
	svc := ensureServices(t)
	store := svc.Store
	pgssReset(t, owner)
	defer func() { pgssExport(t, owner, "worker"); tablesExport(t, owner, "worker") }()
	ctx := context.Background()
	lightKinds := laneKinds(t, runtimeworker.LaneLight)
	heavyKinds := laneKinds(t, runtimeworker.LaneHeavy)

	// W1: one claim, with the backlog the dataset carries (20k available, 150k terminal).
	t.Run("claim_latency", func(t *testing.T) {
		var dueSample, recSample, emptySample sample
		for _, kind := range []string{workitems.KindReleaseEvaluate, workitems.KindPluginDeliver, workitems.KindRSSRefresh} {
			for i := 0; i < 150; i++ {
				start := time.Now()
				claim, err := store.ClaimDue(ctx, "soak-claim", []string{kind}, time.Minute)
				d := time.Since(start)
				if errors.Is(err, workitems.ErrNoDueWork) {
					emptySample = append(emptySample, d)
					continue
				}
				if err != nil {
					t.Fatalf("claim %s: %v", kind, err)
				}
				dueSample = append(dueSample, d)
				if err := store.Finish(ctx, claim, workitems.StateSucceeded, nil); err != nil {
					t.Fatalf("finish: %v", err)
				}
			}
		}
		for _, kind := range []string{workitems.KindReleaseEvaluate, workitems.KindQBSubmit, workitems.KindFileExecute} {
			for i := 0; i < 100; i++ {
				start := time.Now()
				_, err := store.ClaimRecovery(ctx, []string{kind}, time.Minute)
				recSample = append(recSample, time.Since(start))
				if err != nil && !errors.Is(err, workitems.ErrNoDueWork) {
					t.Fatalf("claim recovery: %v", err)
				}
			}
		}
		// A tick is ClaimRecovery (half of the ticks first) + ClaimDue (block_unrunnable + claim_due) + ClaimRecovery.
		tick := ms(dueSample.percentile(0.95)) + 2*ms(recSample.percentile(0.95))
		recordResult(t, result{Phase: "worker", ID: "claim_p95", Desc: "one claim tick: block_unrunnable + claim_due + claim_recovery (p95)",
			Runs: len(dueSample), P50Ms: ms(dueSample.percentile(0.5)), P95Ms: ms(dueSample.percentile(0.95)), P99Ms: ms(dueSample.percentile(0.99)),
			Values: map[string]float64{"claim_due_p95_ms": ms(dueSample.percentile(0.95)), "claim_recovery_p95_ms": ms(recSample.percentile(0.95)),
				"claim_nothing_due_p95_ms": ms(emptySample.percentile(0.95)), "tick_p95_ms": tick},
			Target: "tick p95<=15ms", Verdict: verdictAtMost(tick, 15), Detail: fmt.Sprintf("due=%d recovery=%d nothing_due=%d samples", len(dueSample), len(recSample), len(emptySample))})
	})

	// W3: single kind with a backlog, no-op handlers: processed per minute.
	throughput := func(t *testing.T, id, desc string, kind string, lane []string, floor float64, backlog func()) {
		deferBacklog(t, owner)
		backlog()
		c := newCounters()
		stop := startRuntime(t, store, c, nil)
		dur := workerSeconds()
		time.Sleep(dur)
		stop()
		handled := c.get(kind)
		perMin := float64(handled) / dur.Minutes()
		recordResult(t, result{Phase: "worker", ID: id, Desc: desc, Runs: int(handled),
			Values: map[string]float64{"per_minute": perMin, "window_s": dur.Seconds(), "lane_total": float64(c.sum(lane))},
			Target: fmt.Sprintf(">=%.0f/min", floor), Verdict: verdictAtLeast(perMin, floor)})
	}
	t.Run("throughput_light_single_kind", func(t *testing.T) {
		throughput(t, "throughput_light", "no-op release_evaluate, single backlogged kind (light lane)", workitems.KindReleaseEvaluate, lightKinds, 22,
			func() { makeDue(t, owner, workitems.KindReleaseEvaluate) })
	})
	t.Run("throughput_heavy_single_kind", func(t *testing.T) {
		throughput(t, "throughput_heavy", "no-op qb_submit, single backlogged kind (heavy lane)", workitems.KindQBSubmit, heavyKinds, 19, func() {
			replicaExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wb-qsub-' || t.id, 'download_task', t.id, t.acquisition_id, 'qb_submit', 'available', now() - interval '1 minute', 0, t.idempotency_key, 0, 5,
       '{}'::jsonb, 1, 7, now(), now(), a.origin_managed_anime_ref, 1
FROM download_tasks t JOIN acquisitions a ON a.id = t.acquisition_id WHERE t.state = 'completed' AND a.id LIKE 'acq-%' ORDER BY t.id LIMIT 3000 ON CONFLICT DO NOTHING`)
		})
	})

	// W4: every light kind has a backlog; throughput of the light lane in items per second.
	t.Run("throughput_light_all_kinds", func(t *testing.T) {
		deferBacklog(t, owner)
		per := 600
		if per > e.Profile.Owners {
			per = e.Profile.Owners
		}
		// reconciliation kinds are owned by file_maintenance rows named after the managed anime.
		replicaExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wb-' || k.kind || '-' || m.id, 'file_maintenance', m.id, k.kind, 'available', now() - interval '1 minute', 0, k.kind || ':' || m.id, 0, 3,
       '{}'::jsonb, 1, 7, now(), now(), m.id, 1
FROM unnest(ARRAY['reconciliation_calibrate', 'reconciliation_consume_alignment', 'reconciliation_consume_replacement']) k(kind),
     (SELECT id FROM managed_anime WHERE id LIKE 'a%' ORDER BY id LIMIT $1) m`, per)
		replicaExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wb-pp-' || p.id, 'postprocessing_plan', p.id, p.acquisition_id, 'postprocess_plan', 'available', now() - interval '1 minute', 0, 'plan-backlog:' || p.id,
       0, 3, '{}'::jsonb, 1, 7, now(), now(), a.origin_managed_anime_ref, 1
FROM postprocessing_plans p JOIN acquisitions a ON a.id = p.acquisition_id WHERE p.id LIKE 'pp-%' ORDER BY p.id LIMIT $1`, per)
		replicaExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at)
SELECT 'wb-ce-' || c.id, 'download_completion', c.id, 'completion_execute', 'available', now() - interval '1 minute', 0, 'completion-backlog:' || c.id,
       0, 3, '{}'::jsonb, 1, 7, now(), now() FROM download_completions c WHERE c.id LIKE 'dc-%' ORDER BY c.id LIMIT $1`, per)
		replicaExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, acquisition_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wb-qsync-' || t.id, 'download_task', t.id, t.acquisition_id, 'qb_sync', 'available', now() - interval '1 minute', 0, t.idempotency_key, 0, 5, '{}'::jsonb, 1, 7, now(), now(),
       a.origin_managed_anime_ref, 1
FROM download_tasks t JOIN acquisitions a ON a.id = t.acquisition_id
WHERE t.state = 'completed' AND a.id LIKE 'acq-%' AND NOT EXISTS (SELECT 1 FROM work_items w WHERE w.owner_id = t.id AND w.kind = 'qb_sync' AND w.state IN ('available', 'leased'))
ORDER BY t.id LIMIT $1 ON CONFLICT DO NOTHING`, per)
		for _, k := range []string{workitems.KindRSSRefresh, workitems.KindReleaseEvaluate, workitems.KindPluginDeliver} {
			makeDue(t, owner, k)
		}
		c := newCounters()
		stop := startRuntime(t, store, c, nil)
		dur := workerSeconds()
		time.Sleep(dur)
		stop()
		perSec := float64(c.sum(lightKinds)) / dur.Seconds()
		vals := map[string]float64{"per_second": perSec, "window_s": dur.Seconds()}
		for _, k := range lightKinds {
			vals["kind_"+k] = float64(c.get(k))
		}
		recordResult(t, result{Phase: "worker", ID: "throughput_light_all_kinds", Desc: "no-op handlers, every light-lane kind backlogged", Runs: int(c.sum(lightKinds)),
			Values: vals, Target: ">=3/s", Verdict: verdictAtLeast(perSec, 3)})
	})

	// W2: idle. Nothing is due; every background loop of the server runs for the idle window.
	t.Run("idle_background_loops", func(t *testing.T) {
		deferBacklog(t, owner)
		if _, err := owner.ExecContext(ctx, `UPDATE automation_schedules SET next_run_at = now() + interval '2 days'`); err != nil {
			t.Fatal(err)
		}
		// A freshly loaded dataset carries activity the consumer has not logged yet. That one-time
		// catch-up is not idle cost: drain it first, so the window shows the steady passes.
		drainActivity(t, rt, svc)
		stopLoops, loopNames, loopPasses := startBackgroundLoops(t, rt, svc)
		c := newCounters()
		stopRuntime := startRuntime(t, store, c, nil)
		pgssReset(t, owner)
		window := e.Idle
		start := time.Now()
		time.Sleep(window)
		elapsed := time.Since(start)
		stopRuntime()
		stopLoops()
		workItemsMs, _ := pgssTotals(owner, `work_items|work_item_`)
		allMs := pgssAllTotals(owner)
		pgssExport(t, owner, "idle")
		core := elapsed.Seconds() * 1000
		wiShare, allShare := workItemsMs/core*100, allMs/core*100
		recordResult(t, result{Phase: "worker", ID: "idle_work_items", Desc: "idle window: work_items statements, % of one core", Runs: int(c.total()),
			Values: map[string]float64{"percent_of_core": wiShare, "work_items_ms": workItemsMs, "window_s": elapsed.Seconds()}, Target: "<=5% core", Verdict: verdictAtMost(wiShare, 5),
			Detail: fmt.Sprintf("loops: %v", loopNames)})
		recordResult(t, result{Phase: "worker", ID: "idle_all_loops", Desc: "idle window: all background statements, % of one core",
			Values: map[string]float64{"percent_of_core": allShare, "total_ms": allMs, "window_s": elapsed.Seconds()}, Target: "<=10% core", Verdict: verdictAtMost(allShare, 10)})
		for _, name := range loopNames {
			recordResult(t, result{Phase: "worker", ID: "idle_loop_" + strings.NewReplacer("/", "_").Replace(name), Desc: "idle window: wall time of one " + name + " pass",
				Values: loopPasses.values(name), Target: "record", Verdict: "info"})
		}
		explainLoopPasses(t, owner, svc)
	})

	// W5: 800 sources due, real rss_refresh handler over local fixture feeds.
	t.Run("rss_refresh_cycle", func(t *testing.T) {
		deferBacklog(t, owner)
		feed := newFeedServer(10)
		defer feed.Close()
		fetcher := &fixtureFetcher{base: feed.URL, client: &http.Client{Timeout: 10 * time.Second}}
		rssSvc, err := rssapplication.NewService(rt, store, fetcher)
		if err != nil {
			t.Fatal(err)
		}
		wireRSS(rssSvc, rt, svc)
		ensureLiveRefresh(t, owner)
		due := makeDue(t, owner, workitems.KindRSSRefresh)
		// the template owner's source (a UUID id) has no fixture feed: keep it out of the cycle
		if res, err := owner.ExecContext(ctx, `UPDATE work_items SET available_at = now() + interval '2 days' WHERE kind = 'rss_refresh' AND state = 'available' AND owner_id NOT LIKE 'r%'`); err == nil {
			n, _ := res.RowsAffected()
			due -= n
		}
		diagnoseFinish(t, store, rssSvc.RSSRefreshHandler(), workitems.KindRSSRefresh) // pre-flight: one item through claim, handler and finish
		due--
		c := newCounters()
		errs := newErrorLog(rssSvc.RSSRefreshHandler())
		defer errs.report(t, "rss_refresh")
		stop := startRuntime(t, store, c, map[string]runtimeworker.Handler{workitems.KindRSSRefresh: errs})
		cap := chainCap(20 * time.Minute)
		start := time.Now()
		deadline := start.Add(cap)
		for time.Now().Before(deadline) && liveCount(t, owner, workitems.KindRSSRefresh) > 0 {
			time.Sleep(2 * time.Second)
		}
		elapsed := time.Since(start)
		remaining := liveCount(t, owner, workitems.KindRSSRefresh)
		stop()
		done := due - remaining
		rate := float64(done) / elapsed.Minutes()
		projected := elapsed
		if remaining > 0 && done > 0 {
			projected = time.Duration(float64(due) / float64(done) * float64(elapsed))
		}
		verdict := "pass"
		if projected > 15*time.Minute {
			verdict = "miss"
		}
		if done == 0 {
			verdict = "error"
		}
		recordResult(t, result{Phase: "worker", ID: "rss_refresh_cycle", Desc: "all due sources refreshed (real handler, local feed fixtures)", Runs: int(done),
			Values: map[string]float64{"sources_due": float64(due), "processed": float64(done), "elapsed_s": elapsed.Seconds(), "per_minute": rate, "projected_full_cycle_s": projected.Seconds(),
				"remaining": float64(remaining), "feed_calls": float64(fetcher.calls.Load())},
			Target: "<=15min per cycle", Verdict: verdict})
	})

	// W6: a new subscription backfills 300 releases through the real release_evaluate handler.
	t.Run("release_evaluate_backfill", func(t *testing.T) {
		deferBacklog(t, owner)
		feed := newFeedServer(1)
		defer feed.Close()
		rssSvc, err := rssapplication.NewService(rt, store, &fixtureFetcher{base: feed.URL, client: http.DefaultClient})
		if err != nil {
			t.Fatal(err)
		}
		wireRSS(rssSvc, rt, svc)
		const n = 300
		prefix := fmt.Sprintf("bf%d-", time.Now().Unix())
		var sourceID string
		if err := owner.QueryRowContext(ctx, `SELECT r.id FROM rss_sources r JOIN subscriptions s ON s.id = r.subscription_id WHERE s.managed_anime_id = $1 ORDER BY r.id LIMIT 1`, ds.TypicalOwner).Scan(&sourceID); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, internal_facts, verdict, reason_code)
SELECT $4::text || n, $1, 'guid:' || $4::text || n,
       jsonb_build_object('title', '[Backfill] Soak Anime - ' || lpad(n::text, 3, '0') || ' [WebRip 1080p]', 'enclosure_type', 'application/x-bittorrent'),
       jsonb_build_object('identity_kind', 'guid', 'identity_value', $4::text || n),
       jsonb_build_object('guid', $4::text || n, 'title', '[Backfill] Soak Anime - ' || lpad(n::text, 3, '0') || ' [WebRip 1080p]', 'download_url', $3::text || '/dl/' || $4::text || n || '.torrent'),
       'discovered', 'discovered' FROM generate_series(1, $2) n`, sourceID, n, feed.URL, prefix); err != nil {
			t.Fatalf("backfill releases: %v", err)
		}
		tx, err := store.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= n; i++ {
			if _, err := store.Create(ctx, tx, workitems.WorkItemSpec{
				Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: prefix + strconv.Itoa(i)}, Kind: workitems.KindReleaseEvaluate,
				IdempotencyKey: "release_evaluate:" + prefix + strconv.Itoa(i), MaxAttempts: 5, ExpectedOwnerVersion: 1,
			}); err != nil {
				t.Fatalf("enqueue backfill %d: %v", i, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		diagnoseFinish(t, store, rssSvc.ReleaseEvaluateHandler(), workitems.KindReleaseEvaluate)
		c := newCounters()
		evalErrs := newErrorLog(rssSvc.ReleaseEvaluateHandler())
		defer evalErrs.report(t, "release_evaluate")
		stop := startRuntime(t, store, c, map[string]runtimeworker.Handler{workitems.KindReleaseEvaluate: evalErrs})
		start := time.Now()
		deadline := start.Add(chainCap(25 * time.Minute))
		for time.Now().Before(deadline) && liveCount(t, owner, workitems.KindReleaseEvaluate) > 0 {
			time.Sleep(2 * time.Second)
		}
		elapsed := time.Since(start)
		remaining := liveCount(t, owner, workitems.KindReleaseEvaluate)
		stop()
		done := int64(n) - remaining
		projected := elapsed
		if remaining > 0 && done > 0 {
			projected = time.Duration(float64(n) / float64(done) * float64(elapsed))
		}
		verdict := "pass"
		if projected > 5*time.Minute {
			verdict = "miss"
		}
		if done == 0 {
			verdict = "error"
		}
		recordResult(t, result{Phase: "worker", ID: "release_evaluate_backfill", Desc: "300 releases of a new subscription evaluated (real handler)", Runs: int(done),
			Values: map[string]float64{"elapsed_s": elapsed.Seconds(), "processed": float64(done), "remaining": float64(remaining), "projected_s": projected.Seconds()},
			Target: "<=5min", Verdict: verdict})
	})

	// W7: notification deliveries to a local webhook sink through the real delivery handler.
	t.Run("plugin_deliver_sink", func(t *testing.T) {
		deferBacklog(t, owner)
		sink := newWebhookSink()
		defer sink.srv.Close()
		ensurePluginBacklog(t, owner, 20000)
		makeDue(t, owner, workitems.KindPluginDeliver)
		backlog := liveCount(t, owner, workitems.KindPluginDeliver)
		handler := plugintargetapplication.NewPluginDeliverHandler(rt, staticSecrets{value: `{"endpoint_url":"` + sink.endpoint() + `"}`}, store, sink.client(), fixedRenderer{})
		c := newCounters()
		stop := startRuntime(t, store, c, map[string]runtimeworker.Handler{workitems.KindPluginDeliver: handler})
		dur := workerSeconds() * 2
		time.Sleep(dur)
		stop()
		delivered := sink.calls.Load()
		perMin := float64(delivered) / dur.Minutes()
		clearMin := 0.0
		if perMin > 0 {
			clearMin = 20000 / perMin
		}
		recordResult(t, result{Phase: "worker", ID: "plugin_deliver_20k", Desc: "20,000 deliveries cleared into the local sink (projected from the measured rate)", Runs: int(delivered),
			Values: map[string]float64{"sink_calls": float64(delivered), "per_minute": perMin, "backlog_at_start": float64(backlog), "projected_clear_minutes_for_20000": clearMin, "window_s": dur.Seconds(), "handled": float64(c.get(workitems.KindPluginDeliver))},
			Target: "<=60min for 20000", Verdict: func() string {
				if delivered == 0 {
					return "error"
				}
				return verdictAtMost(clearMin, 60)
			}(), Detail: "projection = 20000 / measured per-minute rate; 0 sink calls reads as miss"})
		if delivered == 0 {
			rows, err := owner.QueryContext(ctx, `SELECT state, COALESCE(last_error_code, ''), left(COALESCE(last_error, ''), 160), count(*) FROM plugin_deliveries WHERE updated_at > now() - interval '5 minutes' AND state <> 'pending' GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT 5`)
			if err == nil {
				for rows.Next() {
					var st, code, msg string
					var n int
					_ = rows.Scan(&st, &code, &msg, &n)
					t.Logf("delivery outcome: %s %s %q x%d", st, code, msg, n)
				}
				rows.Close()
			}
		}
	})

	// W8: worker throughput with and without concurrent reads. Each half
	// measures against its own fresh release_evaluate backlog, so the
	// with-reads rate no longer depends on what this subtest's first half or
	// the earlier subtests left in the dataset's live pool (the PRUNE-046
	// final soak read a 100% drop at 0 items/min from exactly that reuse).
	t.Run("worker_under_reads", func(t *testing.T) {
		rs := newReadSet(t)
		rate := func(withReads bool) (float64, int64) {
			deferBacklog(t, owner)
			run := fmt.Sprintf("udr%d", time.Now().UnixNano())
			built := buildEvaluateBacklog(t, owner, workerUnderReadsBacklog, run)
			c := newCounters()
			stop := startRuntime(t, store, c, nil)
			var wg sync.WaitGroup
			cctx, cancel := context.WithCancel(ctx)
			if withReads {
				for i := 0; i < 4; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						tick := time.NewTicker(underReadsCadence)
						defer tick.Stop()
						for cctx.Err() == nil {
							select {
							case <-cctx.Done():
								return
							case <-tick.C:
							}
							switch i % 4 {
							case 0:
								_, _ = rs.svc.Readmode.ListAnimeApplication(cctx)
							case 1:
								_, _ = rs.svc.Readmode.GetAnimeWorkspaceApplication(cctx, ds.TypicalOwner)
							case 2:
								lim := 50
								_, _ = rs.obs.ListRecords(cctx, recordQuery(&lim))
							default:
								_, _ = rs.svc.Readmode.ListAcquisitionRecords(cctx, recordsFirstPage())
							}
						}
					}(i)
				}
			}
			dur := workerSeconds()
			time.Sleep(dur)
			cancel()
			wg.Wait()
			stop()
			return float64(c.get(workitems.KindReleaseEvaluate)) / dur.Minutes(), built
		}
		alone, builtAlone := rate(false)
		with, builtWith := rate(true)
		drop := 0.0
		if alone > 0 {
			drop = (alone - with) / alone * 100
		}
		recordResult(t, result{Phase: "worker", ID: "worker_drop_under_reads", Desc: "worker throughput drop while 4 concurrent readers run",
			Values: map[string]float64{"alone_per_min": alone, "with_reads_per_min": with, "drop_percent": drop}, Target: "<=20% drop", Verdict: verdictAtMost(drop, 20),
			Detail: fmt.Sprintf("fresh backlog per half: %d (alone) / %d (with reads) of %d requested", builtAlone, builtWith, workerUnderReadsBacklog)})
	})
}

// bgLoop is one background loop of the server: its cadence and one pass.
type bgLoop struct {
	name  string
	every time.Duration
	pass  func(context.Context)
}

// backgroundLoops builds, over db, the loops the server runs besides the claim
// loops: acquisition settlement (5 s), download completion (5 s), acquisition
// activity (5 s), automation dispatcher (30 s), qB sync dispatcher (60 s), the
// retention janitor and one-shot retention (10 min).
func backgroundLoops(t *testing.T, rt *sql.DB, svc *ownerServices) []bgLoop {
	t.Helper()
	var loops []bgLoop
	add := func(name string, every time.Duration, pass func(context.Context)) {
		loops = append(loops, bgLoop{name: name, every: every, pass: pass})
	}
	settle, err := acquisitionsettlement.NewSettlementService(rt)
	if err != nil {
		t.Fatal(err)
	}
	add("settlement/5s", 5*time.Second, func(ctx context.Context) { _, _ = settle.RunPass(ctx) })

	consumer := &acquisitionactivity.Consumer{DB: rt, Writer: svc.Activity, Diagnostics: svc.Activity}
	add("activity/5s", 5*time.Second, func(ctx context.Context) { _, _ = consumer.ConsumeOnce(ctx) })

	mediaStore := facts.NewStoreWithRepository(mediapersistence.NewRepository(rt))
	completion, err := downloadcompletion.NewService(&downloadcompletion.Store{DB: rt, Work: svc.Store}, mediaStore, downloadcompletion.TorrentAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	add("completion/5s", 5*time.Second, func(ctx context.Context) { _, _ = completion.RunPass(ctx) })

	dispatcher := &automationapplication.Dispatcher{DB: rt, WorkItems: svc.Store, PolicyReader: selectionapplication.NewService(rt),
		GroupReaders: map[string]automationapplication.GroupReader{}, Connections: catalogadapter.DBConnectionSource{DB: rt}, Recorder: svc.Activity, ActivityWriter: svc.Activity}
	add("dispatcher/30s", 30*time.Second, func(ctx context.Context) {
		_ = dispatcher.ReapStaleRuns(ctx, 10*time.Minute)
		_, _ = dispatcher.DispatchDue(ctx)
		_, _ = dispatcher.DispatchManual(ctx)
	})

	dl, err := downloadapplication.NewService(rt, svc.Store, stubProvider{}, stubQBConfig{})
	if err != nil {
		t.Fatal(err)
	}
	add("qb_sync_dispatch/60s", time.Minute, func(ctx context.Context) { _, _ = dl.DispatchQBSync(ctx) })

	janitor := newJanitor(t, rt, svc)
	oneShot, err := workitems.NewOneShotReclaimService(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	add("retention/10m", 10*time.Minute, func(ctx context.Context) { _, _ = janitor.RunPass(ctx); _, _ = oneShot.RunPass(ctx) })
	return loops
}

// loopTimes collects the wall time of every pass of every background loop.
type loopTimes struct {
	mu     sync.Mutex
	passes map[string][]time.Duration
}

func (l *loopTimes) add(name string, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.passes[name] = append(l.passes[name], d)
}

// values returns passes, mean, p95, max and total in milliseconds.
func (l *loopTimes) values(name string) map[string]float64 {
	l.mu.Lock()
	ds := append([]time.Duration(nil), l.passes[name]...)
	l.mu.Unlock()
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	out := map[string]float64{"passes": float64(len(ds))}
	if len(ds) == 0 {
		return out
	}
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	out["mean_ms"] = ms(total / time.Duration(len(ds)))
	out["p95_ms"] = ms(ds[(len(ds)*95+99)/100-1])
	out["max_ms"] = ms(ds[len(ds)-1])
	out["total_ms"] = ms(total)
	return out
}

// startBackgroundLoops runs the background loops on their production cadences
// (the first pass immediately) and times every pass.
func startBackgroundLoops(t *testing.T, rt *sql.DB, svc *ownerServices) (stop func(), names []string, times *loopTimes) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	times = &loopTimes{passes: map[string][]time.Duration{}}
	for _, l := range backgroundLoops(t, rt, svc) {
		l := l
		names = append(names, l.name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			run := func() {
				start := time.Now()
				l.pass(ctx)
				if ctx.Err() == nil {
					times.add(l.name, time.Since(start))
				}
			}
			run()
			tk := time.NewTicker(l.every)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
					run()
				}
			}
		}()
	}
	return func() { cancel(); wg.Wait() }, names, times
}

// explainLoopPasses runs one pass of every background loop on a fresh runtime
// pool whose role logs every statement plan (auto_explain, analyze + buffers,
// nested statements), so the container log holds the plan of each loop query.
// The role setting is reset afterwards; the idle measurement is already done.
func explainLoopPasses(t *testing.T, owner *sql.DB, svc *ownerServices) {
	t.Helper()
	ctx := context.Background()
	e := getEnv(t)
	probe, err := sql.Open("pgx", e.RuntimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	var role string
	if err := probe.QueryRowContext(ctx, `SELECT current_user`).Scan(&role); err != nil {
		probe.Close()
		t.Fatal(err)
	}
	probe.Close()
	ident := `"` + strings.ReplaceAll(role, `"`, `""`) + `"`
	if _, err := owner.ExecContext(ctx, `ALTER ROLE `+ident+` SET auto_explain.log_min_duration = 0`); err != nil {
		t.Logf("loop plans skipped (auto_explain role setting): %v", err)
		return
	}
	defer func() {
		if _, err := owner.ExecContext(ctx, `ALTER ROLE `+ident+` RESET auto_explain.log_min_duration`); err != nil {
			t.Errorf("reset auto_explain role setting: %v", err)
		}
	}()
	pool, err := sql.Open("pgx", e.RuntimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(2)
	for _, l := range backgroundLoops(t, pool, svc) {
		if strings.HasPrefix(l.name, "retention/") {
			continue // its DELETE batches are not a scan of open rows
		}
		if _, err := pool.ExecContext(ctx, `SELECT 'soak-loop-plan:`+l.name+`'`); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		l.pass(ctx)
		t.Logf("loop plan pass %s: %v", l.name, time.Since(start).Round(time.Millisecond))
	}
}

// newJanitor builds the production retention service (same dependency set as the
// composition root) over the runtime database.
func newJanitor(t *testing.T, rt *sql.DB, svc *ownerServices) *retentionapplication.Service {
	t.Helper()
	registry, err := retentionapplication.NewRegistry(retentionapplication.Dependencies{
		Observability:   observabilitypersistence.NewRepository(rt),
		Catalog:         catalogrecordretentionpersistence.NewRepository(rt),
		AnimeCandidates: animepersistence.NewRepository(rt),
		WorkItems:       svc.Store,
		MediaPosters:    posterpersistence.NewRepository(rt),
		Discovery:       retentionpersistence.NewRepository(rt),
		Maintenance:     retentionpersistence.NewRepository(rt),
	})
	if err != nil {
		t.Fatal(err)
	}
	j, err := retentionapplication.NewService(rt, registry, nil, svc.Activity)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// stubProvider / stubQBConfig let the qB sync dispatcher (a database scan that
// enqueues sync items) run without a qBittorrent.
type stubProvider struct{}

func (stubProvider) LookupByOperationKey(context.Context, string) (downloaddomain.ProviderTask, bool, error) {
	return downloaddomain.ProviderTask{}, false, nil
}
func (stubProvider) Sync(context.Context, string) (downloaddomain.ProviderTask, error) {
	return downloaddomain.ProviderTask{}, errors.New("soak: no qBittorrent")
}
func (stubProvider) ClassifyProviderError(error) downloaddomain.ProviderErrorClass {
	return downloaddomain.ProviderErrorClass("")
}

type stubQBConfig struct{}

func (stubQBConfig) GetQBittorrentRuntimeConfig(context.Context) (qbittorrent.RuntimeConfig, error) {
	return qbittorrent.RuntimeConfig{}, errors.New("soak: no qBittorrent")
}

// drainActivity runs the acquisition activity consumer until it writes nothing for
// three consecutive passes.
func drainActivity(t *testing.T, rt *sql.DB, svc *ownerServices) {
	t.Helper()
	consumer := &acquisitionactivity.Consumer{DB: rt, Writer: svc.Activity, Diagnostics: svc.Activity}
	quiet, passes := 0, 0
	var written int
	start := time.Now()
	for quiet < 3 && passes < 2000 {
		res, err := consumer.ConsumeOnce(context.Background())
		if err != nil {
			t.Logf("activity drain pass %d: %v", passes, err)
		}
		passes++
		written += res.Written
		if res.Written == 0 {
			quiet++
		} else {
			quiet = 0
		}
	}
	t.Logf("activity consumer drained: %d passes, %d entries written, %v", passes, written, time.Since(start).Round(time.Second))
	settle, err := acquisitionsettlement.NewSettlementService(rt)
	if err == nil {
		for i := 0; i < 20; i++ {
			r, err := settle.RunPass(context.Background())
			if err != nil || r.Settled+r.Disposed+r.Compacted == 0 {
				break
			}
		}
	}
}

// chainCap bounds a chain measurement; SOAK_CHAIN_CAP_MINUTES shortens it for the smoke profile.
func chainCap(def time.Duration) time.Duration {
	if v := envDefault("SOAK_CHAIN_CAP_MINUTES", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return def
}

// ensureLiveRefresh gives every generated source one live rss_refresh item again
// (earlier measurements may have consumed them), shaped like the ones the
// automation dispatcher creates.
func ensureLiveRefresh(t *testing.T, owner *sql.DB) {
	t.Helper()
	n := replicaExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts, input_snapshot,
                       expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wi-rssx-' || s.id || '-' || extract(epoch FROM clock_timestamp())::bigint, 'rss_source_runtime', s.id, 'rss_refresh', 'available', now() - interval '1 minute', 0,
       'rss_refresh:' || s.id, 0, 5, '{"trigger":"scheduled"}'::jsonb, 1, 7, now(), now(), sub.managed_anime_id, 1
FROM rss_sources s JOIN subscriptions sub ON sub.id = s.subscription_id
WHERE s.id LIKE 'r%' AND sub.origin <> 'oneshot'
  AND NOT EXISTS (SELECT 1 FROM work_items w WHERE w.kind = 'rss_refresh' AND w.owner_id = s.id AND w.state IN ('available', 'leased'))`)
	t.Logf("live rss_refresh items re-created for %d sources", n)
}

// ensurePluginBacklog tops the pending notification deliveries (and their live
// plugin_deliver items) up to `target`, copying a delivered row, so the sink
// measurement always starts from the PRD's 20,000 backlog.
func ensurePluginBacklog(t *testing.T, owner *sql.DB, target int) {
	t.Helper()
	var pending int
	if err := owner.QueryRowContext(context.Background(), `SELECT count(*) FROM plugin_deliveries d WHERE d.state = 'pending' AND EXISTS (SELECT 1 FROM work_items w WHERE w.owner_id = d.id AND w.kind = 'plugin_deliver' AND w.state IN ('available', 'leased'))`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	missing := target - pending
	if missing <= 0 {
		return
	}
	replicaExec(t, owner, `
INSERT INTO plugin_deliveries(id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot, secret_ref, delivery_kind, trigger_kind, source_owner_type,
                              source_owner_id, source_owner_version, source_result_key, managed_anime_id, state, version, idempotency_key, payload_schema_version, payload, payload_digest,
                              template_language_version, template_catalog_version, title_template_snapshot, body_template_snapshot, target_display, attempt_count, created_at, updated_at, retention_until)
SELECT 'pdx-' || n || '-' || extract(epoch FROM clock_timestamp())::bigint, t.plugin_target_config_id, 1, 'webhook', t.adapter_config_snapshot, t.secret_ref, 'business', t.trigger_kind, t.source_owner_type,
       t.source_owner_id, 1, 'bk-' || n || '-' || extract(epoch FROM clock_timestamp())::bigint, t.managed_anime_id, 'pending', 1, 'pdkx-' || n || '-' || extract(epoch FROM clock_timestamp())::bigint, 1, t.payload,
       t.payload_digest, t.template_language_version, t.template_catalog_version, t.title_template_snapshot, t.body_template_snapshot, t.target_display, 0, now(), now(), now() + interval '30 days'
FROM generate_series(1, $1) n CROSS JOIN LATERAL (SELECT * FROM plugin_deliveries WHERE state = 'delivered' ORDER BY id LIMIT 1) t`, missing)
	replicaExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts, input_snapshot,
                       expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wi-pdx-' || d.id, 'plugin_delivery', d.id, 'plugin_deliver', 'available', now() - interval '1 minute', 0, 'plugin_deliver:' || d.id, 0, 5, '{}'::jsonb, 1, 7, now(), now(), d.managed_anime_id, 1
FROM plugin_deliveries d WHERE d.id LIKE 'pdx-%' AND d.state = 'pending' AND NOT EXISTS (SELECT 1 FROM work_items w WHERE w.owner_id = d.id AND w.kind = 'plugin_deliver')`)
}

// buildEvaluateBacklog inserts n fresh release_evaluate items shaped like the
// generator's live backlog (gen_misc_test.go): owner_type 'release', the
// subscription's anime frozen as managed_anime_id, snapshot
// '{"trigger":"refresh"}'. Candidates are non-one-shot releases whose own live
// release_evaluate item has already retired - work_items holds live rows only,
// so the per-owner slot is free again. Triggers stay off via replicaExec, like
// every other backlog arrangement (owner checks are for product writes). The
// run tag keeps ids and idempotency keys unique across halves. Returns how
// many items were built; a profile with fewer candidate releases builds less.
func buildEvaluateBacklog(t *testing.T, owner *sql.DB, n int, run string) int64 {
	t.Helper()
	built := replicaExec(t, owner, `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wi-' || $2 || '-' || r.id, 'release', r.id, 'release_evaluate', 'available', now() - interval '1 minute', 0,
       'release_evaluate:' || $2 || ':' || r.id, 0, 5, '{"trigger":"refresh"}'::jsonb, 1, 7, now(), now(), r.aid, 1
FROM (SELECT rel.id, sub.managed_anime_id AS aid
      FROM releases rel
      JOIN rss_sources src ON src.id = rel.rss_source_id
      JOIN subscriptions sub ON sub.id = src.subscription_id AND sub.origin <> 'oneshot'
      WHERE NOT EXISTS (SELECT 1 FROM work_items w
                        WHERE w.owner_type = 'release' AND w.owner_id = rel.id AND w.kind = 'release_evaluate')
      ORDER BY rel.id
      LIMIT $1) r`, n, run)
	if built < int64(n) {
		t.Logf("worker_under_reads: fresh backlog shorter than requested: %d of %d (not enough free release_evaluate slots)", built, n)
	}
	return built
}

// diagnoseFinish claims one item of the kind, runs the handler and applies its
// outcome the way the runtime does, logging the error the runtime keeps to itself.
func diagnoseFinish(t *testing.T, store *workitems.Store, handler runtimeworker.Handler, kind string) {
	t.Helper()
	ctx := context.Background()
	claim, err := store.ClaimDue(ctx, "soak-diag", []string{kind}, time.Minute)
	if err != nil {
		t.Logf("diagnose %s: claim: %v", kind, err)
		return
	}
	out, err := handler.Handle(ctx, claim)
	if err != nil {
		t.Logf("diagnose %s: handler: %v", kind, err)
		return
	}
	t.Logf("diagnose %s: outcome kind=%s terminal=%s owner_result=%+v reason=%s", kind, out.Kind, out.TerminalState, out.OwnerResult, out.ReasonCode)
	switch out.Kind {
	case runtimeworker.OutcomeFinish:
		t.Logf("diagnose %s: finish error: %v", kind, store.Finish(ctx, claim, out.TerminalState, out.WriteOwner))
	default:
		t.Logf("diagnose %s: outcome not applied here", kind)
	}
}

// wireRSS gives the RSS service the collaborators the composition root sets that
// the handlers need to apply their outcomes (activity journal, diagnostics, media
// facts). The external-effect collaborators (downloader admission, target probe,
// notification enqueue) stay unset: the soak never leaves the database.
func wireRSS(s *rssapplication.Service, rt *sql.DB, svc *ownerServices) {
	s.SetActivityWriter(svc.Activity)
	s.SetDiagnosticRecorder(svc.Activity, nil)
	s.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(rt)))
	s.SetCandidateFactsRepository(facts.NewPGRepository(rt))
}
