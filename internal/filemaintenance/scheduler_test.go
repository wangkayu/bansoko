package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/testutil/disposabledb"
)

type fakeSubmitter struct {
	mu    sync.Mutex
	calls map[string]int
	tops  map[string]string
	err   error
}

func (f *fakeSubmitter) SubmitScan(_ context.Context, animeID, treeTop, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	if f.tops == nil {
		f.tops = map[string]string{}
	}
	f.calls[animeID]++
	f.tops[animeID] = treeTop
	return f.err
}

// debugCapturingLogger records the debug events the scheduler emits for a work
// it has no scope to scan.
type debugCapturingLogger struct {
	debugs []observabilityport.RuntimeEvent
}

func (l *debugCapturingLogger) Debug(_ context.Context, event observabilityport.RuntimeEvent) {
	l.debugs = append(l.debugs, event)
}
func (l *debugCapturingLogger) Info(context.Context, observabilityport.RuntimeEvent)  {}
func (l *debugCapturingLogger) Warn(context.Context, observabilityport.RuntimeEvent)  {}
func (l *debugCapturingLogger) Error(context.Context, observabilityport.RuntimeEvent) {}

type fakeAnimeLister struct {
	ids   []string
	calls int
}

func (f *fakeAnimeLister) ListAnimeIDs(context.Context) ([]string, error) {
	f.calls++
	return f.ids, nil
}

// seedSchedulerInstance creates a live instance so the scheduler's active-only
// gate can evaluate it.
func seedSchedulerInstance(t *testing.T, db *sql.DB, animeID, state string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ($1, 'Show')`, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state, version)
VALUES ($1, $2, 'custom', 'Show', $3, 1)`, "sub-"+animeID, animeID, state); err != nil {
		t.Fatal(err)
	}
}

// seedSchedulerPlacement records one file the work put on disk. It is the
// placement evidence the scheduler derives the work's scan scope from: a work
// without it has no tree top and nothing is scanned for it.
func seedSchedulerPlacement(t *testing.T, db *sql.DB, animeID, placementID, physicalPath string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, state)
VALUES ($1, $2, $3, 'reconciliation', 'obs-'||$1, 'fp', 'fp', 'parser-v1', 'active')`,
		placementID, physicalPath, animeID); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerSubmitsOverrideAndGlobalDueScans(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	policies := NewPolicyService(db)

	seedSchedulerInstance(t, db, "anime-a", "active")
	seedSchedulerInstance(t, db, "anime-b", "active")
	// Both works placed files: each scan scope is the work's own tree top.
	seedSchedulerPlacement(t, db, "anime-a", "plc-a-1", "/media/Show A/Season 01/Show A - E01.mkv")
	seedSchedulerPlacement(t, db, "anime-a", "plc-a-2", "/media/Show A/Season 02/Show A - E01.mkv")
	seedSchedulerPlacement(t, db, "anime-b", "plc-b-1", "/media/Show B/Show B - E01.mkv")
	interval := 3600
	if _, err := policies.SavePolicy(ctx, "anime:anime-a", 0, PolicyRow{Enabled: true, ScanIntervalSeconds: &interval, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := policies.SavePolicy(ctx, GlobalScope, 0, PolicyRow{Enabled: true, ScanIntervalSeconds: &interval, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	submitter := &fakeSubmitter{}
	scheduler := NewScanScheduler(db, policies, submitter, &fakeAnimeLister{ids: []string{"anime-b"}}, observabilityport.NopRuntimeLogger{})
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	submitter.mu.Lock()
	defer submitter.mu.Unlock()
	if submitter.calls["anime-a"] != 1 || submitter.calls["anime-b"] != 1 {
		t.Fatalf("override and global schedules must both submit: %+v", submitter.calls)
	}
	if submitter.tops["anime-a"] != "/media/Show A" || submitter.tops["anime-b"] != "/media/Show B" {
		t.Fatalf("each scan takes the work's tree top: %+v", submitter.tops)
	}
	// Second tick within the interval is skipped.
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if submitter.calls["anime-a"] != 1 || submitter.calls["anime-b"] != 1 {
		t.Fatalf("due-window must dedupe ticks: %+v", submitter.calls)
	}
}

func TestSchedulerSkipsDisabledAndNoInterval(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	policies := NewPolicyService(db)
	seedSchedulerInstance(t, db, "anime-x", "active")
	seedSchedulerPlacement(t, db, "anime-x", "plc-x-1", "/media/Show X/Show X - E01.mkv")
	if _, err := policies.SavePolicy(ctx, "anime:anime-x", 0, PolicyRow{Enabled: false, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	submitter := &fakeSubmitter{}
	scheduler := NewScanScheduler(db, policies, submitter, &fakeAnimeLister{}, observabilityport.NopRuntimeLogger{})
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	submitter.mu.Lock()
	defer submitter.mu.Unlock()
	if len(submitter.calls) != 0 {
		t.Fatalf("disabled policy must not submit: %+v", submitter.calls)
	}
}

func TestSchedulerPeriodicSkipsStoppedAndDeletedInstances(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	policies := NewPolicyService(db)
	interval := 3600
	// Stopped instance: policy says scan, but the periodic schedule is
	// active-only. Both works placed files, so a missing scan scope is never
	// why they are skipped.
	seedSchedulerInstance(t, db, "anime-stopped", "stopped")
	seedSchedulerPlacement(t, db, "anime-stopped", "plc-stopped-1", "/media/Show Stopped/Show Stopped - E01.mkv")
	if _, err := policies.SavePolicy(ctx, "anime:anime-stopped", 0, PolicyRow{Enabled: true, ScanIntervalSeconds: &interval, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	// Deleted instance: policy row survives (defense in depth) but the live
	// gate skips it.
	seedSchedulerPlacement(t, db, "anime-deleted", "plc-deleted-1", "/media/Show Deleted/Show Deleted - E01.mkv")
	if _, err := policies.SavePolicy(ctx, "anime:anime-deleted", 0, PolicyRow{Enabled: true, ScanIntervalSeconds: &interval, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	submitter := &fakeSubmitter{}
	scheduler := NewScanScheduler(db, policies, submitter, &fakeAnimeLister{}, observabilityport.NopRuntimeLogger{})
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	submitter.mu.Lock()
	defer submitter.mu.Unlock()
	if len(submitter.calls) != 0 {
		t.Fatalf("stopped/deleted instances must not be periodically scanned: %+v", submitter.calls)
	}
}

// PRUNE-046: with the global policy absent or disabled only an enabled
// override can be due, so the tick never lists every managed anime; with
// nothing enabled it submits nothing.
func TestSchedulerGlobalDisabledReadsOnlyEnabledOverrides(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	policies := NewPolicyService(db)
	interval := 3600
	for _, id := range []string{"anime-on", "anime-off", "anime-inherit"} {
		seedSchedulerInstance(t, db, id, "active")
		seedSchedulerPlacement(t, db, id, "plc-"+id, "/media/"+id+"/"+id+" - E01.mkv")
	}
	lister := &fakeAnimeLister{ids: []string{"anime-on", "anime-off", "anime-inherit"}}
	submitter := &fakeSubmitter{}
	scheduler := NewScanScheduler(db, policies, submitter, lister, observabilityport.NopRuntimeLogger{})

	// No policy at all: nothing is listed or submitted.
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if lister.calls != 0 || len(submitter.calls) != 0 {
		t.Fatalf("no policy: lister calls=%d submits=%+v", lister.calls, submitter.calls)
	}

	if _, err := policies.SavePolicy(ctx, GlobalScope, 0, PolicyRow{Enabled: false, ScanIntervalSeconds: &interval, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := policies.SavePolicy(ctx, "anime:anime-on", 0, PolicyRow{Enabled: true, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := policies.SavePolicy(ctx, "anime:anime-off", 0, PolicyRow{Enabled: false, ScanIntervalSeconds: &interval, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if lister.calls != 0 {
		t.Fatalf("global disabled: anime lister read %d times", lister.calls)
	}
	if submitter.calls["anime-on"] != 1 || submitter.calls["anime-off"] != 0 || submitter.calls["anime-inherit"] != 0 {
		t.Fatalf("global disabled: submits %+v, want only anime-on (inherited interval)", submitter.calls)
	}
}

// A due work with no placement evidence has no tree top: the tick submits
// nothing and leaves a diagnostic instead of scanning a directory the work
// does not live in.
func TestSchedulerSkipsWorkWithoutTreeTop(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	policies := NewPolicyService(db)
	interval := 3600
	seedSchedulerInstance(t, db, "anime-noscope", "active")
	if _, err := policies.SavePolicy(ctx, "anime:anime-noscope", 0, PolicyRow{Enabled: true, ScanIntervalSeconds: &interval, Safety: json.RawMessage(`{}`)}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
	submitter := &fakeSubmitter{}
	logger := &debugCapturingLogger{}
	scheduler := NewScanScheduler(db, policies, submitter, &fakeAnimeLister{}, logger)
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(submitter.calls) != 0 {
		t.Fatalf("a work with no tree top must not be scanned: %+v", submitter.calls)
	}
	if len(logger.debugs) != 1 || logger.debugs[0].ReasonCode != "scan_scope_unavailable" ||
		logger.debugs[0].OwnerID != "anime-noscope" || logger.debugs[0].Outcome != "skipped" {
		t.Fatalf("a skipped work leaves its reason: %+v", logger.debugs)
	}
}
