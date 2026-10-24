//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	readmodel "bansoko/internal/anime/readmodel/application"
	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/filemanager"
	filemanagerpersistence "bansoko/internal/filemanager/persistence"
	observabilityapplication "bansoko/internal/observability/application"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	"bansoko/internal/transport/animeworkspace"
)

// Locked read targets. Numbers are never edited here.
var (
	tListAnime      = target{ID: "list_anime", Desc: "subscription list listAnime", P50ms: 150, P95ms: 400, MaxBytes: 1 << 20}
	tListAnimeLoad  = target{ID: "list_anime_under_load", Desc: "subscription list with the worker running", P50ms: 250, P95ms: 700}
	tCatalog        = target{ID: "catalog_season", Desc: "catalog season view (~300 entries)", P50ms: 80, P95ms: 200, MaxBytes: 1 << 20}
	tWorkspaceTyp   = target{ID: "workspace_typical", Desc: "workspace, typical owner (60 releases)", P50ms: 40, P95ms: 120, MaxBytes: 300 << 10}
	tWorkspaceHeavy = target{ID: "workspace_heavy", Desc: "workspace, heavy owner (2,500 releases)", P50ms: 600, P95ms: 1500, MaxBytes: 15 << 20}
	tRSSListTyp     = target{ID: "rss_list_typical", Desc: "owner RSS list, typical owner (limit 100)", P50ms: 100, P95ms: 300}
	tRSSListHeavy   = target{ID: "rss_list_heavy", Desc: "owner RSS list, heavy owner (limit 100)", P95ms: 2000, P50MaxMult: 3, TargetRef: "rss_list_typical"}
	// Supplementary (not a locked row): the heavy owner at a limit that renders as many
	// items as the typical owner has, to show the cost follows the limit.
	tRSSListHeavySameItems = target{ID: "rss_list_heavy_limit30", Desc: "supplementary: owner RSS list, heavy owner at limit 30 (about as many items as the typical owner)", P50MaxMult: 3, TargetRef: "rss_list_typical"}
	tRecordsFirst          = target{ID: "records_first", Desc: "download records, first page (limit 50)", P50ms: 50, P95ms: 150}
	tRecordsDeep           = target{ID: "records_deep", Desc: "download records, page 50", P50ms: 50, P95ms: 150}
	tRecordsOwner          = target{ID: "records_by_owner", Desc: "download records, filtered by owner", P50ms: 50, P95ms: 150}
	tRecordFiles           = target{ID: "record_files", Desc: "download record file page", P50ms: 40, P95ms: 100}
	tLogsFirst             = target{ID: "logs_first", Desc: "run log (observability records), first page (limit 50)", P50ms: 150, P95ms: 400}
	tLogsOwner             = target{ID: "logs_by_owner", Desc: "run log filtered by owner", P50ms: 150, P95ms: 400}
	tLogsRange             = target{ID: "logs_by_range", Desc: "run log filtered by time range", P50ms: 150, P95ms: 400}
	tFMDirs                = target{ID: "filemanager_dirs", Desc: "file manager page of 200 sub-directories", P50ms: 150, P95ms: 400}
	tFMFiles               = target{ID: "filemanager_files", Desc: "file manager page of 200 files", P50ms: 100, P95ms: 300}
	tNotifications         = target{ID: "notifications_first", Desc: "notification records first page", P50ms: 20, P95ms: 80}
	tPluginTargets         = target{ID: "plugin_targets", Desc: "plugin target list", P50ms: 20, P95ms: 80}
)

type readSet struct {
	ds       *dataset
	owner    *sql.DB
	svc      *ownerServices
	catalog  *catalogapplication.Service
	obs      *observabilityapplication.Service
	fm       *filemanager.Service
	plugin   *plugintargetapplication.Service
	season   string
	fmDir    string // absolute directory holding the 200-file page
	recOwner string // owner with many acquisitions
	recFile  string // a full acquisition root with files
}

func newReadSet(t *testing.T) *readSet {
	t.Helper()
	ds := loadDataset(t)
	owner, rt := dbs(t)
	svc := ensureServices(t)
	rs := &readSet{ds: ds, owner: owner, svc: svc, obs: svc.Activity, season: "2026-spring"}
	rs.catalog = catalogapplication.NewProviderService(rt, map[string]catalogapplication.Reader{}, catalogapplication.NewSubscriptionItemReader(rt))
	store, err := filemanagerpersistence.NewStore(rt)
	if err != nil {
		t.Fatal(err)
	}
	if rs.fm, err = filemanager.NewService(store, filemanager.NewDenialZone(filepath.Join(os.TempDir(), "soak-backups"))); err != nil {
		t.Fatal(err)
	}
	if rs.plugin, err = plugintargetapplication.NewService(rt, nil, svc.Store); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := owner.QueryRowContext(ctx, `SELECT origin_managed_anime_ref FROM acquisitions WHERE id LIKE 'acq-%' GROUP BY 1 ORDER BY count(*) DESC, 1 LIMIT 1`).Scan(&rs.recOwner); err != nil {
		t.Fatalf("records owner: %v", err)
	}
	if err := owner.QueryRowContext(ctx, `SELECT id FROM acquisitions WHERE id LIKE 'acq-%' AND state = 'succeeded' ORDER BY id LIMIT 1`).Scan(&rs.recFile); err != nil {
		t.Fatalf("records root: %v", err)
	}
	rs.prepareLibraryTree(t)
	return rs
}

// prepareLibraryTree creates the on-disk library the file manager browses:
// the directories the placements live under (the 200 sub-directory page) and
// one directory with 200 files (the placements of the busiest directory, topped
// up with unmanaged files).
func (rs *readSet) prepareLibraryTree(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	lib := libraryRootPath()
	dirs := rs.ds.Profile.PlacementDirs
	for i := 0; i < dirs; i++ {
		if err := os.MkdirAll(filepath.Join(lib, fmt.Sprintf("Soak Anime %d", i), "Season 01"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var dir string
	if err := rs.owner.QueryRowContext(ctx, `SELECT regexp_replace(physical_path, '/[^/]+$', '') FROM placements WHERE physical_path LIKE $1 GROUP BY 1 ORDER BY count(*) DESC, 1 LIMIT 1`, lib+"/%").Scan(&dir); err != nil {
		t.Fatalf("busiest placement directory: %v", err)
	}
	rs.fmDir = dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rows, err := rs.owner.QueryContext(ctx, `SELECT physical_path FROM placements WHERE physical_path LIKE $1 ORDER BY physical_path LIMIT 200`, dir+"/%")
	if err != nil {
		t.Fatal(err)
	}
	made := 0
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		made++
	}
	rows.Close()
	for made < 200 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("unmanaged-%03d.mkv", made)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		made++
	}
}

// pgssSnap is a point-in-time copy of the per-statement counters, used to name the
// statements one read spent its time in.
type pgssSnap map[string]stmt

type stmt struct {
	Calls int64
	Total float64
	Query string
}

func snapPgss(owner *sql.DB) pgssSnap {
	out := pgssSnap{}
	rows, err := owner.QueryContext(context.Background(), `SELECT queryid::text, calls, total_exec_time, left(regexp_replace(query, '\s+', ' ', 'g'), 220)
FROM soak_ext.pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, q string
		var s stmt
		if rows.Scan(&id, &s.Calls, &s.Total, &q) == nil {
			s.Query = q
			out[id] = s
		}
	}
	return out
}

// topDelta renders the statements that spent the most time between two snapshots.
func topDelta(before, after pgssSnap, n int) string {
	type d struct {
		calls int64
		total float64
		q     string
	}
	var ds []d
	for id, a := range after {
		b := before[id]
		if a.Calls-b.Calls > 0 && a.Total-b.Total > 0 {
			ds = append(ds, d{a.Calls - b.Calls, a.Total - b.Total, a.Query})
		}
	}
	for i := 0; i < len(ds); i++ {
		for j := i + 1; j < len(ds); j++ {
			if ds[j].total > ds[i].total {
				ds[i], ds[j] = ds[j], ds[i]
			}
		}
	}
	if len(ds) > n {
		ds = ds[:n]
	}
	var out string
	for _, x := range ds {
		out += fmt.Sprintf("  %8d calls %10.1f ms  %s\n", x.calls, x.total, x.q)
	}
	return out
}

// TestScaleReads measures the read paths: 1 cold + 50 warm runs each, p50/p95/p99
// and response size, typical versus heavy owner, a 4-way concurrent group, and the
// subscription list again while the real worker runtime processes the backlog.
func TestScaleReads(t *testing.T) {
	e := getEnv(t)
	if !e.Phases["read"] {
		t.Skip("phase read not selected")
	}
	rs := newReadSet(t)
	owner, rt := dbs(t)
	pgssReset(t, owner)
	defer func() { pgssExport(t, owner, "read"); tablesExport(t, owner, "read") }()
	var stmtLog string
	// SOAK_READS=id1,id2 narrows the run to those reads (fix iteration); a read
	// named in SOAK_PROFILE_READS also writes a CPU profile of its warm runs.
	only := splitIDs(os.Getenv("SOAK_READS"))
	profiled := splitIDs(os.Getenv("SOAK_PROFILE_READS"))
	selected := func(id string) bool { return len(only) == 0 || only[id] }
	record := func(tg target, fn func(context.Context) (any, error), warm int, refP50 float64) float64 {
		if !selected(tg.ID) {
			return 0
		}
		before := snapPgss(owner)
		start := time.Now()
		if profiled[tg.ID] {
			if f, err := os.Create(filepath.Join(e.Evidence, tg.ID+".cpu.pprof")); err == nil {
				if pprof.StartCPUProfile(f) == nil {
					defer func() { pprof.StopCPUProfile(); f.Close() }()
				}
			}
		}
		m := timeRead(warm, fn)
		if m.Err == nil && m.Cold > 5*time.Second && warm > 10 {
			// A read this slow would run for minutes: shrink the warm sample and say so.
			cold := m.Cold
			m = timeRead(10, fn)
			t.Logf("%s: cold run took %v, warm sample reduced to 10", tg.ID, cold)
		}
		p50 := recordRead(t, tg, m, refP50)
		stmtLog += fmt.Sprintf("== %s (%s)\n%s\n", tg.ID, time.Since(start).Round(time.Millisecond), topDelta(before, snapPgss(owner), 14))
		return p50
	}
	ctx := context.Background()
	rm := rs.svc.Readmode

	record(tListAnime, func(ctx context.Context) (any, error) { return rm.ListAnimeApplication(ctx) }, 50, 0)
	record(tCatalog, func(ctx context.Context) (any, error) { return rs.catalog.WorkspaceProvider(ctx, "mikan", rs.season) }, 50, 0)
	// The byte limit of the workspace is judged on the HTTP DTO (recordWorkspaceDTO
	// below), not on the readmodel struct the timed section serializes.
	timedTyp, timedHeavy := tWorkspaceTyp, tWorkspaceHeavy
	timedTyp.MaxBytes, timedHeavy.MaxBytes = 0, 0
	record(timedTyp, func(ctx context.Context) (any, error) {
		return rm.GetAnimeWorkspaceApplication(ctx, rs.ds.TypicalOwner)
	}, 50, 0)
	record(timedHeavy, func(ctx context.Context) (any, error) {
		return rm.GetAnimeWorkspaceApplication(ctx, rs.ds.HeavyOwners[0])
	}, 50, 0)
	// The HTTP DTO of the workspace (what the API sends), not the readmodel struct:
	// bytes only, the latency targets above are the service layer.
	recordWorkspaceDTO(t, rs, tWorkspaceTyp, rs.ds.TypicalOwner, selected)
	recordWorkspaceDTO(t, rs, tWorkspaceHeavy, rs.ds.HeavyOwners[0], selected)
	typRSS := record(tRSSListTyp, func(ctx context.Context) (any, error) {
		return rm.ReadAnimeRSSListView(ctx, readmodel.ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: rs.ds.TypicalOwner, LimitPerGroup: 100})
	}, 50, 0)
	record(tRSSListHeavy, func(ctx context.Context) (any, error) {
		return rm.ReadAnimeRSSListView(ctx, readmodel.ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: rs.ds.HeavyOwners[0], LimitPerGroup: 100})
	}, 50, typRSS)

	record(tRSSListHeavySameItems, func(ctx context.Context) (any, error) {
		return rm.ReadAnimeRSSListView(ctx, readmodel.ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: rs.ds.HeavyOwners[0], LimitPerGroup: 30})
	}, 50, typRSS)

	// download records
	record(tRecordsFirst, func(ctx context.Context) (any, error) {
		return rm.ListAcquisitionRecords(ctx, readmodel.AcquisitionRecordQuery{Limit: 50})
	}, 50, 0)
	cursor := ""
	for page := 1; selected("records_deep") && page < 50; page++ {
		p, err := rm.ListAcquisitionRecords(ctx, readmodel.AcquisitionRecordQuery{Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatalf("walk to record page %d: %v", page, err)
		}
		if p.NextCursor == nil {
			t.Logf("record list ends at page %d (dataset has fewer than 50 pages)", page)
			break
		}
		cursor = *p.NextCursor
	}
	record(tRecordsDeep, func(ctx context.Context) (any, error) {
		return rm.ListAcquisitionRecords(ctx, readmodel.AcquisitionRecordQuery{Limit: 50, Cursor: cursor})
	}, 50, 0)
	record(tRecordsOwner, func(ctx context.Context) (any, error) {
		return rm.ListAcquisitionRecords(ctx, readmodel.AcquisitionRecordQuery{Limit: 50, ManagedAnimeID: rs.recOwner})
	}, 50, 0)
	record(tRecordFiles, func(ctx context.Context) (any, error) {
		return rm.ListAcquisitionRecordFiles(ctx, rs.recFile, readmodel.AcquisitionFileQuery{Limit: 50})
	}, 50, 0)

	// run log
	lim := 50
	record(tLogsFirst, func(ctx context.Context) (any, error) {
		return rs.obs.ListRecords(ctx, observabilityapplication.RecordQuery{Limit: &lim})
	}, 50, 0)
	record(tLogsOwner, func(ctx context.Context) (any, error) {
		return rs.obs.ListRecords(ctx, observabilityapplication.RecordQuery{Limit: &lim, ManagedAnimeID: &rs.recOwner})
	}, 50, 0)
	now := time.Now().UTC()
	from, to := now.Add(-10*24*time.Hour), now.Add(-3*24*time.Hour)
	record(tLogsRange, func(ctx context.Context) (any, error) {
		return rs.obs.ListRecords(ctx, observabilityapplication.RecordQuery{Limit: &lim, From: &from, To: &to})
	}, 50, 0)

	// file manager
	record(tFMDirs, func(ctx context.Context) (any, error) { return rs.fm.List(ctx, libraryRootPath(), "", 200) }, 50, 0)
	record(tFMFiles, func(ctx context.Context) (any, error) { return rs.fm.List(ctx, rs.fmDir, "", 200) }, 50, 0)

	// small tables
	dl := 20
	record(tNotifications, func(ctx context.Context) (any, error) {
		return rs.plugin.ListDeliveries(ctx, plugintargetapplication.DeliveryQuery{Limit: &dl})
	}, 50, 0)
	record(tPluginTargets, func(ctx context.Context) (any, error) { return rs.plugin.ListTargets(ctx) }, 50, 0)

	// 4-way concurrent group (no locked target: recorded for the notes).
	concurrent := []struct {
		id string
		fn func(context.Context) (any, error)
	}{
		{"conc4_list_anime", func(ctx context.Context) (any, error) { return rm.ListAnimeApplication(ctx) }},
		{"conc4_workspace_typical", func(ctx context.Context) (any, error) {
			return rm.GetAnimeWorkspaceApplication(ctx, rs.ds.TypicalOwner)
		}},
		{"conc4_records_first", func(ctx context.Context) (any, error) {
			return rm.ListAcquisitionRecords(ctx, readmodel.AcquisitionRecordQuery{Limit: 50})
		}},
		{"conc4_logs_first", func(ctx context.Context) (any, error) {
			return rs.obs.ListRecords(ctx, observabilityapplication.RecordQuery{Limit: &lim})
		}},
	}
	for _, c := range concurrent {
		if len(only) != 0 && !only["conc4"] {
			break
		}
		m := timeReadConcurrent(4, 48, c.fn)
		r := result{Phase: "read", ID: c.id, Desc: "4 concurrent requests", Runs: len(m.Warm), Bytes: m.Bytes, Verdict: "info"}
		if m.Err != nil {
			r.Verdict, r.Detail = "error", m.Err.Error()
		} else {
			r.P50Ms, r.P95Ms, r.P99Ms, r.MaxMs = ms(m.Warm.percentile(0.5)), ms(m.Warm.percentile(0.95)), ms(m.Warm.percentile(0.99)), ms(m.Warm.max())
		}
		recordResult(t, r)
	}

	// The subscription list while the real worker runtime drains rss_refresh /
	// release_evaluate backlog (all runtime kinds wired to no-op handlers).
	if len(only) != 0 && !only["under_load"] {
		writeEvidenceFile(t, "read-statements.txt", stmtLog)
		return
	}
	stop, processed := startNoopRuntime(t, rs.svc.Store)
	time.Sleep(2 * time.Second)
	before := snapPgss(owner)
	m := timeRead(50, func(ctx context.Context) (any, error) { return rm.ListAnimeApplication(ctx) })
	proc := processed()
	stop()
	loadTarget := tListAnimeLoad
	recordRead(t, loadTarget, m, 0)
	stmtLog += fmt.Sprintf("== list_anime_under_load (worker processed %d items meanwhile)\n%s\n", proc, topDelta(before, snapPgss(owner), 6))
	writeEvidenceFile(t, "read-statements.txt", stmtLog)
	_ = rt
	_ = ctx
}

func recordQuery(limit *int) observabilityapplication.RecordQuery {
	return observabilityapplication.RecordQuery{Limit: limit}
}

func recordsFirstPage() readmodel.AcquisitionRecordQuery {
	return readmodel.AcquisitionRecordQuery{Limit: 50}
}

// publicWorkspace is the workspace as the HTTP response carries it: the internal
// feed facts of a release (the opaque acquisition locator) are not part of the
// API (api/openapi.yaml has no such field), so they do not count as response bytes.
func publicWorkspace(w readmodel.AnimeWorkspace) readmodel.AnimeWorkspace {
	releases := make([]readmodel.Release, len(w.Releases))
	copy(releases, w.Releases)
	for i := range releases {
		releases[i].InternalFacts = nil
	}
	w.Releases = releases
	return w
}

func splitIDs(v string) map[string]bool {
	out := map[string]bool{}
	for _, id := range strings.Split(v, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

// recordWorkspaceDTO maps one workspace to the HTTP DTO the API serves
// (transport/animeworkspace, the mapping GET /anime/{id}/workspace uses) and
// records the JSON size next to the readmodel-struct size of the timed row.
func recordWorkspaceDTO(t *testing.T, rs *readSet, tg target, owner string, selected func(string) bool) {
	t.Helper()
	if !selected(tg.ID) {
		return
	}
	w, err := rs.svc.Readmode.GetAnimeWorkspaceApplication(context.Background(), owner)
	if err != nil {
		t.Fatalf("workspace for DTO size: %v", err)
	}
	start := time.Now()
	mapped, err := animeworkspace.MapAnimeWorkspace(w)
	if err != nil {
		t.Fatalf("map workspace: %v", err)
	}
	raw, err := json.Marshal(mapped)
	if err != nil {
		t.Fatalf("marshal workspace DTO: %v", err)
	}
	structRaw, _ := json.Marshal(w)
	r := result{Phase: "read", ID: tg.ID + "_http_dto", Desc: tg.Desc + ", HTTP DTO size", Bytes: len(raw), Target: fmt.Sprintf("bytes<=%d", tg.MaxBytes),
		Verdict: "pass", Detail: fmt.Sprintf("map+marshal %.1f ms; readmodel struct JSON %d bytes", ms(time.Since(start)), len(structRaw))}
	if tg.MaxBytes > 0 && len(raw) > tg.MaxBytes {
		r.Verdict, r.Detail = "miss", fmt.Sprintf("bytes %d > %d; ", len(raw), tg.MaxBytes)+r.Detail
	}
	recordResult(t, r)
}
