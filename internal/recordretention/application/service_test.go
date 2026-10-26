package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

type fakeRetentionDependencies struct {
	calls      []CleanerID
	batchSizes []int
	counts     map[CleanerID]int64
	errors     map[CleanerID]error
}

func (f *fakeRetentionDependencies) purge(id CleanerID, batchSize int) (int64, error) {
	f.calls = append(f.calls, id)
	f.batchSizes = append(f.batchSizes, batchSize)
	if err := f.errors[id]; err != nil {
		return 0, err
	}
	return f.counts[id], nil
}

func (f *fakeRetentionDependencies) PurgePluginDeliveryAttempts(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerPluginDeliveryAttempts, batchSize)
}

func (f *fakeRetentionDependencies) PurgePluginDeliveries(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerPluginDeliveries, batchSize)
}

func (f *fakeRetentionDependencies) PurgeSubscriptionLogEntries(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerSubscriptionLogEntries, batchSize)
}

func (f *fakeRetentionDependencies) PurgeAutomationDispatchRuns(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerAutomationDispatchRuns, batchSize)
}

func (f *fakeRetentionDependencies) PurgeDiagnosticEpisodes(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerDiagnosticEpisodes, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpiredProviderCatalogSnapshots(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerProviderCatalog, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpiredProviderGroupSnapshots(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerProviderGroups, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpiredProviderFeedSnapshots(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerProviderFeeds, batchSize)
}

func (f *fakeRetentionDependencies) PurgeEligibleAnimeConfigCandidates(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerAnimeCandidates, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpiredTx(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerWorkItems, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpired(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerMediaPosterCache, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpiredDiscoveryReleases(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerDiscoveryReleases, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpiredIdempotencyRecords(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerIdempotencyRecords, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpiredNotificationAdmissions(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerNotificationAdmissions, batchSize)
}

func (f *fakeRetentionDependencies) PurgeExpiredAlignmentOperations(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerAlignmentOperations, batchSize)
}

func (f *fakeRetentionDependencies) PurgeSupersededReconciliationFiles(_ context.Context, _ txscope.Tx, batchSize int) (int64, error) {
	return f.purge(CleanerReconciliationFiles, batchSize)
}

type fakeRetentionTxRunner struct {
	runs      int
	commits   int
	rollbacks int
}

func (r *fakeRetentionTxRunner) Run(_ context.Context, fn func(txscope.Tx) error) error {
	r.runs++
	err := fn(txscope.Wrap(nil))
	if err != nil {
		r.rollbacks++
		return err
	}
	r.commits++
	return nil
}

type retentionRuntimeLogger struct {
	levels []string
	events []observabilityport.RuntimeEvent
}

func (l *retentionRuntimeLogger) Debug(_ context.Context, event observabilityport.RuntimeEvent) {
	l.add("debug", event)
}
func (l *retentionRuntimeLogger) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	l.add("info", event)
}
func (l *retentionRuntimeLogger) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	l.add("warn", event)
}
func (l *retentionRuntimeLogger) Error(_ context.Context, event observabilityport.RuntimeEvent) {
	l.add("error", event)
}
func (l *retentionRuntimeLogger) add(level string, event observabilityport.RuntimeEvent) {
	l.levels = append(l.levels, level)
	l.events = append(l.events, event)
}

type retentionRecorder struct {
	routines  []observabilityport.RoutineRequest
	failures  []observabilityport.DiagnosticFailureRequest
	successes []observabilityport.DiagnosticSuccessRequest
}

func (r *retentionRecorder) UpsertRoutine(_ context.Context, req observabilityport.RoutineRequest) error {
	r.routines = append(r.routines, req)
	return nil
}
func (r *retentionRecorder) RecordDiagnosticFailure(_ context.Context, req observabilityport.DiagnosticFailureRequest) error {
	r.failures = append(r.failures, req)
	return nil
}
func (r *retentionRecorder) RecordDiagnosticSuccess(_ context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	r.successes = append(r.successes, req)
	return nil
}
func (*retentionRecorder) UpsertRoutineTx(context.Context, txscope.Tx, observabilityport.RoutineRequest) error {
	return nil
}
func (*retentionRecorder) RecordDiagnosticFailureTx(context.Context, txscope.Tx, observabilityport.DiagnosticFailureRequest) error {
	return nil
}
func (*retentionRecorder) RecordDiagnosticSuccessTx(context.Context, txscope.Tx, observabilityport.DiagnosticSuccessRequest) error {
	return nil
}

func newFakeRetentionDependencies() *fakeRetentionDependencies {
	return &fakeRetentionDependencies{
		counts: map[CleanerID]int64{
			CleanerPluginDeliveryAttempts: 2,
			CleanerPluginDeliveries:       1,
			CleanerSubscriptionLogEntries: 0,
			CleanerAutomationDispatchRuns: 3,
			CleanerDiagnosticEpisodes:     4,
			CleanerProviderCatalog:        5,
			CleanerProviderGroups:         6,
			CleanerProviderFeeds:          7,
			CleanerAnimeCandidates:        8,
			CleanerWorkItems:              9,
			CleanerMediaPosterCache:       10,
			CleanerDiscoveryReleases:      11,
			CleanerIdempotencyRecords:     12,
			CleanerNotificationAdmissions: 13,
			CleanerAlignmentOperations:    14,
			CleanerReconciliationFiles:    15,
		},
		errors: map[CleanerID]error{},
	}
}

func TestRegistryIsFixedAndRejectsForbiddenClasses(t *testing.T) {
	dependencies := newFakeRetentionDependencies()
	registry, err := NewRegistry(Dependencies{
		Observability:   dependencies,
		Catalog:         dependencies,
		AnimeCandidates: dependencies,
		WorkItems:       dependencies,
		MediaPosters:    dependencies,
		Discovery:       dependencies,
		Maintenance:     dependencies,
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	want := []CleanerID{
		CleanerWorkItems,
		CleanerPluginDeliveryAttempts,
		CleanerPluginDeliveries,
		CleanerSubscriptionLogEntries,
		CleanerAutomationDispatchRuns,
		CleanerDiagnosticEpisodes,
		CleanerProviderCatalog,
		CleanerProviderGroups,
		CleanerProviderFeeds,
		CleanerAnimeCandidates,
		CleanerMediaPosterCache,
		CleanerDiscoveryReleases,
		CleanerIdempotencyRecords,
		CleanerNotificationAdmissions,
		CleanerAlignmentOperations,
		CleanerReconciliationFiles,
	}
	if got := registry.IDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry IDs: got %v, want %v", got, want)
	}
	ids := registry.IDs()
	ids[0] = "mutated"
	if got := registry.IDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry IDs were not isolated from caller mutation: got %v", got)
	}

	seen := make(map[CleanerID]struct{}, len(want))
	for _, id := range want {
		if _, exists := seen[id]; exists {
			t.Fatalf("registry contains duplicate cleaner %q", id)
		}
		seen[id] = struct{}{}
	}
	for _, forbidden := range []CleanerID{
		"managed_anime",
		"subscription",
		"rss_source",
		"release",
		"download_task",
		"artifact",
		"file_operation",
		"configuration",
		"routine_health",
		"secret",
		"candidate",
		"filemaintenance_policy",
		"one_shot_acquisitions",
		"one_shot_owner_graph",
	} {
		if registry.Contains(forbidden) {
			t.Fatalf("forbidden cleaner is registered: %q", forbidden)
		}
	}
}

func TestRunPassRecordsOneSummaryAndDurableFailureIdentity(t *testing.T) {
	dependencies := newFakeRetentionDependencies()
	failure := errors.New("raw SQL password should never reach the runtime event")
	dependencies.errors[CleanerSubscriptionLogEntries] = failure
	registry, err := NewRegistry(Dependencies{
		Observability: dependencies, Catalog: dependencies, AnimeCandidates: dependencies,
		WorkItems: dependencies, MediaPosters: dependencies, Discovery: dependencies, Maintenance: dependencies,
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	runner, logger, recorder := &fakeRetentionTxRunner{}, &retentionRuntimeLogger{}, &retentionRecorder{}
	service, err := newServiceWithRunner(runner, registry, logger, recorder)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	pass, err := service.RunPass(context.Background())
	if err == nil || !errors.Is(err, failure) {
		t.Fatalf("run pass error = %v, want wrapped cleaner failure", err)
	}
	if len(pass.Results) != len(registry.IDs()) || !reflect.DeepEqual(dependencies.calls, registry.IDs()) {
		t.Fatalf("pass/results = %d/%v", len(pass.Results), dependencies.calls)
	}
	if runner.runs != 16 || runner.commits != 15 || runner.rollbacks != 1 {
		t.Fatalf("transactions = runs %d commits %d rollbacks %d", runner.runs, runner.commits, runner.rollbacks)
	}
	if len(logger.events) != 1 || len(logger.levels) != 1 || logger.levels[0] != "error" {
		t.Fatalf("summary events = %d levels=%v", len(logger.events), logger.levels)
	}
	if logger.events[0].Component != "retention" || logger.events[0].Operation != "sweep" || logger.events[0].Outcome != "failure" {
		t.Fatalf("summary event = %+v", logger.events[0])
	}
	if len(recorder.routines) != 1 {
		t.Fatalf("routine writes = %d", len(recorder.routines))
	}
	routine := recorder.routines[0]
	if routine.Identity.Kind != observabilityport.RoutineRetentionPass || routine.Identity.ScopeKind != observabilityport.RoutineScopeGlobal || routine.ScopeID != "global" || routine.Identity.ResultCode != observabilityport.RoutineResultRetentionPassFailed {
		t.Fatalf("routine write = %+v", routine)
	}
	if len(recorder.failures) != 1 {
		t.Fatalf("incident writes = %d", len(recorder.failures))
	}
	incident := recorder.failures[0]
	if incident.Identity != observabilityport.DiagnosticRetentionSweepFailure() || incident.ScopeID != "global" || incident.Severity != observabilityport.SeverityError {
		t.Fatalf("incident = %+v", incident)
	}
	if incident.LastError == failure.Error() || logger.events[0].Message == failure.Error() {
		t.Fatal("raw cleaner error escaped observability")
	}
}

func TestRetentionPassPersistsRoutineAndIncidentPostgres(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	dependencies := newFakeRetentionDependencies()
	dependencies.errors[CleanerSubscriptionLogEntries] = errors.New("injected cleaner fault")
	registry, err := NewRegistry(Dependencies{Observability: dependencies, Catalog: dependencies, AnimeCandidates: dependencies, WorkItems: dependencies, MediaPosters: dependencies, Discovery: dependencies, Maintenance: dependencies})
	if err != nil {
		t.Fatal(err)
	}
	logger := &retentionRuntimeLogger{}
	recorder, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, registry, logger, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunPass(context.Background()); err == nil {
		t.Fatal("injected cleaner failure did not fail pass")
	}
	if len(logger.events) != 1 {
		t.Fatalf("summary events = %d", len(logger.events))
	}
	var routineCount, incidentCount int
	if err := db.QueryRow(`SELECT count(*) FROM routine_activities WHERE kind='retention_pass' AND scope_kind='global' AND scope_id='global' AND outcome='failure' AND last_result_code='retention_pass_failed'`).Scan(&routineCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM diagnostic_episodes WHERE component='retention' AND operation='sweep' AND error_code='retention_sweep_failed' AND scope_kind='global' AND scope_id='global' AND succeeded_after_at IS NULL`).Scan(&incidentCount); err != nil {
		t.Fatal(err)
	}
	if routineCount != 1 || incidentCount != 1 {
		t.Fatalf("persisted routine=%d incident=%d", routineCount, incidentCount)
	}
}

// backlogDependencies serves each cleaner a backlog of rows in batches.
type backlogDependencies struct {
	*fakeRetentionDependencies
	backlog map[CleanerID]int64
	failAt  map[CleanerID]int // fail the n-th call (1-based) of a cleaner
	seen    map[CleanerID]int
}

func (b *backlogDependencies) take(id CleanerID, batchSize int) (int64, error) {
	b.calls = append(b.calls, id)
	b.seen[id]++
	if n, ok := b.failAt[id]; ok && b.seen[id] == n {
		return 0, errors.New("injected batch fault")
	}
	n := min(b.backlog[id], int64(batchSize))
	b.backlog[id] -= n
	return n, nil
}

func newBacklogDependencies(backlog map[CleanerID]int64) *backlogDependencies {
	return &backlogDependencies{fakeRetentionDependencies: newFakeRetentionDependencies(), backlog: backlog, failAt: map[CleanerID]int{}, seen: map[CleanerID]int{}}
}

func (b *backlogDependencies) registry(t *testing.T) *Registry {
	t.Helper()
	deps := Dependencies{Observability: backlogObservability{b}, Catalog: backlogCatalog{b}, AnimeCandidates: backlogCandidates{b}, WorkItems: backlogWorkItems{b}, MediaPosters: backlogPosters{b}, Discovery: backlogDiscovery{b}, Maintenance: backlogMaintenance{b}}
	registry, err := NewRegistry(deps)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

type backlogObservability struct{ b *backlogDependencies }

func (o backlogObservability) PurgePluginDeliveryAttempts(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return o.b.take(CleanerPluginDeliveryAttempts, n)
}
func (o backlogObservability) PurgePluginDeliveries(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return o.b.take(CleanerPluginDeliveries, n)
}
func (o backlogObservability) PurgeSubscriptionLogEntries(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return o.b.take(CleanerSubscriptionLogEntries, n)
}
func (o backlogObservability) PurgeAutomationDispatchRuns(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return o.b.take(CleanerAutomationDispatchRuns, n)
}
func (o backlogObservability) PurgeDiagnosticEpisodes(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return o.b.take(CleanerDiagnosticEpisodes, n)
}

type backlogCatalog struct{ b *backlogDependencies }

func (c backlogCatalog) PurgeExpiredProviderCatalogSnapshots(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return c.b.take(CleanerProviderCatalog, n)
}
func (c backlogCatalog) PurgeExpiredProviderGroupSnapshots(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return c.b.take(CleanerProviderGroups, n)
}
func (c backlogCatalog) PurgeExpiredProviderFeedSnapshots(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return c.b.take(CleanerProviderFeeds, n)
}

type backlogCandidates struct{ b *backlogDependencies }

func (c backlogCandidates) PurgeEligibleAnimeConfigCandidates(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return c.b.take(CleanerAnimeCandidates, n)
}

type backlogWorkItems struct{ b *backlogDependencies }

func (w backlogWorkItems) PurgeExpiredTx(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return w.b.take(CleanerWorkItems, n)
}

type backlogPosters struct{ b *backlogDependencies }

func (p backlogPosters) PurgeExpired(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return p.b.take(CleanerMediaPosterCache, n)
}

type backlogMaintenance struct{ b *backlogDependencies }

func (m backlogMaintenance) PurgeExpiredIdempotencyRecords(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return m.b.take(CleanerIdempotencyRecords, n)
}
func (m backlogMaintenance) PurgeExpiredNotificationAdmissions(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return m.b.take(CleanerNotificationAdmissions, n)
}
func (m backlogMaintenance) PurgeExpiredAlignmentOperations(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return m.b.take(CleanerAlignmentOperations, n)
}
func (m backlogMaintenance) PurgeSupersededReconciliationFiles(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return m.b.take(CleanerReconciliationFiles, n)
}

type backlogDiscovery struct{ b *backlogDependencies }

func (d backlogDiscovery) PurgeExpiredDiscoveryReleases(_ context.Context, _ txscope.Tx, n int) (int64, error) {
	return d.b.take(CleanerDiscoveryReleases, n)
}

// A pass repeats full batches round-robin until every backlog is cleared; each
// batch is its own transaction and no cleaner runs again after a short batch.
func TestRunPassRepeatsFullBatchesUntilBacklogsClear(t *testing.T) {
	b := newBacklogDependencies(map[CleanerID]int64{CleanerWorkItems: 1700, CleanerSubscriptionLogEntries: 1000, CleanerPluginDeliveries: 20})
	runner := &fakeRetentionTxRunner{}
	service, err := newServiceWithRunner(runner, b.registry(t), &retentionRuntimeLogger{}, &retentionRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	pass, err := service.RunPass(context.Background())
	if err != nil {
		t.Fatalf("run pass: %v", err)
	}
	counts := map[CleanerID]int64{}
	for _, r := range pass.Results {
		counts[r.ID] = r.Count
	}
	if counts[CleanerWorkItems] != 1700 || counts[CleanerSubscriptionLogEntries] != 1000 || counts[CleanerPluginDeliveries] != 20 {
		t.Fatalf("pass counts = %v", counts)
	}
	// work items: 500,500,500,200 (4 batches); log: 500,500,0 (3); every other cleaner once.
	if want := 4 + 3 + 14; runner.runs != want || len(b.calls) != want {
		t.Fatalf("batches = %d (calls %d), want %d", runner.runs, len(b.calls), want)
	}
	// Round 2 interleaves the two unfinished cleaners in registry order.
	round2 := b.calls[16:18]
	if round2[0] != CleanerWorkItems || round2[1] != CleanerSubscriptionLogEntries {
		t.Fatalf("round 2 = %v", round2)
	}
}

// The pass budget stops repeating batches; the first round always runs whole.
func TestRunPassStopsRepeatingAtBudget(t *testing.T) {
	b := newBacklogDependencies(map[CleanerID]int64{CleanerWorkItems: 1 << 40})
	runner := &fakeRetentionTxRunner{}
	service, err := newServiceWithRunner(runner, b.registry(t), &retentionRuntimeLogger{}, &retentionRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(0, 0)
	service.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	service.budget = 5 * time.Second
	if _, err := service.RunPass(context.Background()); err != nil {
		t.Fatalf("run pass: %v", err)
	}
	if b.seen[CleanerWorkItems] < 2 || b.seen[CleanerWorkItems] > 6 {
		t.Fatalf("work item batches = %d under a 5 s budget", b.seen[CleanerWorkItems])
	}
	for _, id := range b.registry(t).IDs() {
		if b.seen[id] == 0 {
			t.Fatalf("cleaner %s skipped by the budget", id)
		}
	}
}

// A failed batch ends that cleaner for the pass, keeps its committed batches
// and does not stop the others.
func TestRunPassFailedBatchKeepsEarlierBatches(t *testing.T) {
	b := newBacklogDependencies(map[CleanerID]int64{CleanerWorkItems: 2000, CleanerSubscriptionLogEntries: 1500})
	b.failAt[CleanerWorkItems] = 3
	runner := &fakeRetentionTxRunner{}
	service, err := newServiceWithRunner(runner, b.registry(t), &retentionRuntimeLogger{}, &retentionRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	pass, err := service.RunPass(context.Background())
	if err == nil {
		t.Fatal("failed batch not reported")
	}
	for _, r := range pass.Results {
		switch r.ID {
		case CleanerWorkItems:
			if r.Count != 1000 || r.Error == nil {
				t.Fatalf("work items result = %+v", r)
			}
		case CleanerSubscriptionLogEntries:
			if r.Count != 1500 || r.Error != nil {
				t.Fatalf("log result = %+v", r)
			}
		}
	}
	if b.seen[CleanerWorkItems] != 3 {
		t.Fatalf("work item batches after failure = %d", b.seen[CleanerWorkItems])
	}
}
