//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// target is one locked performance target row. Numbers are frozen and never
// changed here; a verdict is computed against them.
type target struct {
	ID         string
	Desc       string
	P50ms      float64 // 0 = no p50 target
	P95ms      float64 // 0 = no p95 target
	MaxBytes   int
	Note       string
	P50MaxMult float64 // p50 must stay within this multiple of another row's p50 (TargetRef)
	TargetRef  string
}

// result is one measured value with its verdict, appended to results.jsonl.
type result struct {
	Phase    string             `json:"phase"`
	ID       string             `json:"id"`
	Desc     string             `json:"desc"`
	Profile  string             `json:"profile"`
	Runs     int                `json:"runs,omitempty"`
	ColdMs   float64            `json:"cold_ms,omitempty"`
	P50Ms    float64            `json:"p50_ms,omitempty"`
	P95Ms    float64            `json:"p95_ms,omitempty"`
	P99Ms    float64            `json:"p99_ms,omitempty"`
	MaxMs    float64            `json:"max_ms,omitempty"`
	Bytes    int                `json:"bytes,omitempty"`
	Values   map[string]float64 `json:"values,omitempty"`
	Target   string             `json:"target,omitempty"`
	Verdict  string             `json:"verdict"`
	Detail   string             `json:"detail,omitempty"`
	Recorded time.Time          `json:"recorded_at"`
}

var (
	resultsMu   sync.Mutex
	allResults  []result
	resultsFile *os.File
)

func recordResult(t *testing.T, r result) {
	t.Helper()
	e := getEnv(t)
	r.Profile = e.Profile.Name
	r.Recorded = time.Now().UTC()
	resultsMu.Lock()
	defer resultsMu.Unlock()
	allResults = append(allResults, r)
	if resultsFile == nil {
		f, err := os.OpenFile(filepath.Join(e.Evidence, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("results file: %v", err)
		}
		resultsFile = f
	}
	line, _ := json.Marshal(r)
	_, _ = resultsFile.Write(append(line, '\n'))
	var vals string
	if len(r.Values) > 0 {
		keys := make([]string, 0, len(r.Values))
		for k := range r.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			vals += fmt.Sprintf(" %s=%.2f", k, r.Values[k])
		}
	}
	t.Logf("RESULT %-28s %-5s cold=%.1fms p50=%.1fms p95=%.1fms p99=%.1fms bytes=%d %s %s%s", r.ID, r.Verdict, r.ColdMs, r.P50Ms, r.P95Ms, r.P99Ms, r.Bytes, r.Target, r.Detail, vals)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

type sample []time.Duration

func (s sample) percentile(p float64) time.Duration {
	if len(s) == 0 {
		return 0
	}
	c := append(sample(nil), s...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	idx := int(float64(len(c))*p+0.5) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(c) {
		idx = len(c) - 1
	}
	return c[idx]
}

func (s sample) max() time.Duration {
	var m time.Duration
	for _, d := range s {
		if d > m {
			m = d
		}
	}
	return m
}

// measurement is one cold run plus the warm runs of one read.
type measurement struct {
	Cold  time.Duration
	Warm  sample
	Bytes int
	Err   error
}

// timeRead runs fn once cold and `warm` times warm; the response is marshalled to
// JSON inside the timed section (service layer + serialization, as the PRD
// defines the latency) and its size recorded from the last run.
func timeRead(warm int, fn func(context.Context) (any, error)) measurement {
	ctx := context.Background()
	run := func() (time.Duration, int, error) {
		start := time.Now()
		v, err := fn(ctx)
		if err != nil {
			return time.Since(start), 0, err
		}
		raw, err := json.Marshal(v)
		d := time.Since(start)
		return d, len(raw), err
	}
	var m measurement
	var err error
	m.Cold, m.Bytes, err = run()
	if err != nil {
		m.Err = err
		return m
	}
	for i := 0; i < warm; i++ {
		var d time.Duration
		d, m.Bytes, err = run()
		if err != nil {
			m.Err = err
			return m
		}
		m.Warm = append(m.Warm, d)
	}
	return m
}

// timeReadConcurrent runs `workers` goroutines, each doing runs/workers warm runs.
func timeReadConcurrent(workers, runs int, fn func(context.Context) (any, error)) measurement {
	var mu sync.Mutex
	var m measurement
	var wg sync.WaitGroup
	per := runs / workers
	if per < 1 {
		per = 1
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				start := time.Now()
				v, err := fn(context.Background())
				var size int
				if err == nil {
					raw, merr := json.Marshal(v)
					err, size = merr, len(raw)
				}
				d := time.Since(start)
				mu.Lock()
				if err != nil && m.Err == nil {
					m.Err = err
				}
				m.Warm = append(m.Warm, d)
				m.Bytes = size
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return m
}

// judge compares a measurement with a locked target and returns the verdict.
func judge(tg target, m measurement, refP50 float64) (string, string) {
	if m.Err != nil {
		return "error", m.Err.Error()
	}
	var misses []string
	p50, p95 := ms(m.Warm.percentile(0.50)), ms(m.Warm.percentile(0.95))
	if tg.P50ms > 0 && p50 > tg.P50ms {
		misses = append(misses, fmt.Sprintf("p50 %.1f > %.0f ms", p50, tg.P50ms))
	}
	if tg.P95ms > 0 && p95 > tg.P95ms {
		misses = append(misses, fmt.Sprintf("p95 %.1f > %.0f ms", p95, tg.P95ms))
	}
	if tg.MaxBytes > 0 && m.Bytes > tg.MaxBytes {
		misses = append(misses, fmt.Sprintf("bytes %d > %d", m.Bytes, tg.MaxBytes))
	}
	if tg.P50MaxMult > 0 && refP50 > 0 && p50 > tg.P50MaxMult*refP50 {
		misses = append(misses, fmt.Sprintf("p50 %.1f > %.0fx reference %.1f ms", p50, tg.P50MaxMult, refP50))
	}
	if len(misses) > 0 {
		return "miss", strings.Join(misses, "; ")
	}
	return "pass", ""
}

func targetText(tg target) string {
	var parts []string
	if tg.P50ms > 0 {
		parts = append(parts, fmt.Sprintf("p50<=%.0fms", tg.P50ms))
	}
	if tg.P95ms > 0 {
		parts = append(parts, fmt.Sprintf("p95<=%.0fms", tg.P95ms))
	}
	if tg.MaxBytes > 0 {
		parts = append(parts, fmt.Sprintf("bytes<=%d", tg.MaxBytes))
	}
	if tg.P50MaxMult > 0 {
		parts = append(parts, fmt.Sprintf("p50<=%.0fx(%s)", tg.P50MaxMult, tg.TargetRef))
	}
	return strings.Join(parts, " ")
}

// recordRead measures-judges-records one read row.
func recordRead(t *testing.T, tg target, m measurement, refP50 float64) float64 {
	t.Helper()
	verdict, detail := judge(tg, m, refP50)
	r := result{Phase: "read", ID: tg.ID, Desc: tg.Desc, Runs: len(m.Warm), ColdMs: ms(m.Cold), Bytes: m.Bytes,
		Target: targetText(tg), Verdict: verdict, Detail: detail}
	if m.Err == nil && len(m.Warm) > 0 {
		r.P50Ms, r.P95Ms, r.P99Ms, r.MaxMs = ms(m.Warm.percentile(0.50)), ms(m.Warm.percentile(0.95)), ms(m.Warm.percentile(0.99)), ms(m.Warm.max())
	}
	recordResult(t, r)
	return r.P50Ms
}

// ---- pg_stat_statements / table size evidence -------------------------------------

// pgssReset clears the statistics before a phase.
func pgssReset(t *testing.T, owner *sql.DB) {
	t.Helper()
	if _, err := owner.ExecContext(context.Background(), `SELECT soak_ext.pg_stat_statements_reset()`); err != nil {
		t.Logf("pg_stat_statements_reset: %v", err)
	}
}

// pgssExport writes the top 20 statements by total_exec_time since the last
// reset to <phase>.pgss.txt, plus the total database time of the window.
func pgssExport(t *testing.T, owner *sql.DB, phase string) {
	t.Helper()
	e := getEnv(t)
	ctx := context.Background()
	rows, err := owner.QueryContext(ctx, `
SELECT calls, round(total_exec_time::numeric, 1), round(mean_exec_time::numeric, 3), round(max_exec_time::numeric, 1), rows,
       shared_blks_hit, shared_blks_read, CASE WHEN toplevel THEN 'T' ELSE 'n' END || ' ' || left(regexp_replace(query, '\s+', ' ', 'g'), 260)
FROM soak_ext.pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY total_exec_time DESC LIMIT 20`)
	if err != nil {
		t.Logf("pg_stat_statements export: %v", err)
		return
	}
	defer rows.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "# pg_stat_statements top 20 by total_exec_time, phase %s, %s\n", phase, time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "# the letter before the query: T = top-level statement, n = nested inside a function (its time is already in the caller)\n")
	fmt.Fprintf(&b, "%-10s %12s %10s %10s %10s %12s %10s  %s\n", "calls", "total_ms", "mean_ms", "max_ms", "rows", "blks_hit", "blks_read", "query")
	for rows.Next() {
		var calls, rws, hit, read int64
		var total, mean, max string
		var q string
		if err := rows.Scan(&calls, &total, &mean, &max, &rws, &hit, &read, &q); err != nil {
			t.Logf("scan pgss: %v", err)
			return
		}
		fmt.Fprintf(&b, "%-10d %12s %10s %10s %10d %12d %10d  %s\n", calls, total, mean, max, rws, hit, read, q)
	}
	var totalMs, calls float64
	_ = owner.QueryRowContext(ctx, `SELECT COALESCE(sum(total_exec_time), 0), COALESCE(sum(calls), 0) FROM soak_ext.pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&totalMs, &calls)
	fmt.Fprintf(&b, "\n# all statements in the window: total_exec_time=%.1f ms, calls=%.0f\n", totalMs, calls)
	if err := os.WriteFile(filepath.Join(e.Evidence, phase+".pgss.txt"), []byte(b.String()), 0o600); err != nil {
		t.Logf("write pgss: %v", err)
	}
}

// pgssTotals returns summed total_exec_time (ms) and calls for statements whose
// text matches the regular expression, since the last reset.
// Only top-level statements count: pg_stat_statements.track=all also lists the statements a
// function runs inside, whose time is already part of the calling statement.
func pgssTotals(owner *sql.DB, pattern string) (totalMs float64, calls int64) {
	_ = owner.QueryRowContext(context.Background(), `SELECT COALESCE(sum(total_exec_time), 0), COALESCE(sum(calls), 0)::bigint FROM soak_ext.pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND toplevel AND query ~* $1`, pattern).Scan(&totalMs, &calls)
	return
}

func pgssAllTotals(owner *sql.DB) (totalMs float64) {
	_ = owner.QueryRowContext(context.Background(), `SELECT COALESCE(sum(total_exec_time), 0) FROM soak_ext.pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND toplevel`).Scan(&totalMs)
	return
}

// tablesExport appends pg_stat_user_tables sizes (rows, dead tuples, bytes) to
// <phase>.tables.txt and returns the database size in bytes.
func tablesExport(t *testing.T, owner *sql.DB, phase string) int64 {
	t.Helper()
	e := getEnv(t)
	ctx := context.Background()
	rows, err := owner.QueryContext(ctx, `
SELECT relname, n_live_tup, n_dead_tup, pg_total_relation_size(relid), pg_relation_size(relid), COALESCE(last_autovacuum::text, ''), COALESCE(last_autoanalyze::text, '')
FROM pg_stat_user_tables WHERE schemaname = 'public' ORDER BY pg_total_relation_size(relid) DESC LIMIT 45`)
	if err != nil {
		t.Logf("tables export: %v", err)
		return 0
	}
	defer rows.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "# pg_stat_user_tables (public), phase %s, %s\n", phase, time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "%-40s %12s %10s %14s %14s  %s\n", "table", "live", "dead", "total_bytes", "heap_bytes", "last_autovacuum")
	for rows.Next() {
		var name, av, aa string
		var live, dead, total, heap int64
		if err := rows.Scan(&name, &live, &dead, &total, &heap, &av, &aa); err != nil {
			return 0
		}
		fmt.Fprintf(&b, "%-40s %12d %10d %14d %14d  %s\n", name, live, dead, total, heap, av)
	}
	var size int64
	_ = owner.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&size)
	fmt.Fprintf(&b, "\n# database size: %d bytes\n", size)
	if err := os.WriteFile(filepath.Join(e.Evidence, phase+".tables.txt"), []byte(b.String()), 0o600); err != nil {
		t.Logf("write tables: %v", err)
	}
	return size
}

func writeEvidenceFile(t *testing.T, name, content string) {
	t.Helper()
	e := getEnv(t)
	if err := os.WriteFile(filepath.Join(e.Evidence, name), []byte(content), 0o600); err != nil {
		t.Logf("write %s: %v", name, err)
	}
}
