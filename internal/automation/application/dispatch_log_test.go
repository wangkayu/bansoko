package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	catalogapp "bansoko/internal/catalog/application"
	observabilityport "bansoko/internal/observability/port"
)

type dispatchResultLogger struct {
	observabilityport.NopRuntimeLogger
	infos []observabilityport.RuntimeEvent
	warns []observabilityport.RuntimeEvent
}

func (l *dispatchResultLogger) Info(_ context.Context, e observabilityport.RuntimeEvent) {
	l.infos = append(l.infos, e)
}

func (l *dispatchResultLogger) Warn(_ context.Context, e observabilityport.RuntimeEvent) {
	l.warns = append(l.warns, e)
}

func TestDispatchSummarySeparatesMetadataWritesFromQueuedEvaluations(t *testing.T) {
	logger := &dispatchResultLogger{}
	d := &Dispatcher{Logger: logger}
	d.logDispatchResult(context.Background(), "run", "metadata_match", "manual", settleDispatch(dispatchCounts{
		targetCount: 2, completed: 1, errors: 1,
	}, nil))
	if len(logger.infos) != 0 || len(logger.warns) != 1 {
		t.Fatalf("infos = %+v, warns = %+v", logger.infos, logger.warns)
	}
	e := logger.warns[0]
	if e.OwnerType != "automation_dispatch_run" || e.OwnerID != "run" || e.Outcome != "partial" ||
		!strings.Contains(e.Message, "metadata saved for 1, 0 release evaluations queued") {
		t.Fatalf("event = %+v", e)
	}
}

func TestLateAutomationFinishCannotOverwriteRecoveredRun(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, started_at)
VALUES ('stale-run', 'rss_refresh', 'manual', 'running', CURRENT_TIMESTAMP - INTERVAL '20 minutes')`); err != nil {
		t.Fatal(err)
	}
	logger := &dispatchResultLogger{}
	d := &Dispatcher{DB: db, Logger: logger}
	if err := d.ReapStaleRuns(ctx, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	d.finishRun(ctx, "stale-run", "rss_refresh", settleDispatch(dispatchCounts{targetCount: 1, enqueued: 1}, nil), time.Now())
	var state string
	var completed *int
	if err := db.QueryRowContext(ctx, `SELECT state, completed_count FROM automation_dispatch_runs WHERE id = 'stale-run'`).Scan(&state, &completed); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || completed != nil || len(logger.infos) != 0 || len(logger.warns) != 1 || logger.warns[0].OwnerID != "stale-run" {
		t.Fatalf("state = %s; infos = %+v, warns = %+v", state, logger.infos, logger.warns)
	}
}

func TestAutomationFatalErrorRecordsNonzeroFailureCount(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, started_at)
VALUES ('broken-run', 'catalog_refresh', 'manual', 'running', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	logger := &dispatchResultLogger{}
	d := &Dispatcher{DB: db, Logger: logger}
	d.finishRun(ctx, "broken-run", "catalog_refresh", settleDispatch(dispatchCounts{}, errors.New("private upstream context")), time.Now())
	var state string
	var failures int
	if err := db.QueryRowContext(ctx, `SELECT state, dispatch_error_count FROM automation_dispatch_runs WHERE id = 'broken-run'`).Scan(&state, &failures); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || failures != 1 || len(logger.infos) != 0 || len(logger.warns) != 1 || strings.Contains(logger.warns[0].Message, "private upstream context") {
		t.Fatalf("state = %s, failures = %d; infos = %+v, warns = %+v", state, failures, logger.infos, logger.warns)
	}
}

func TestCatalogDispatchDoesNotCountLiveRefreshAsChecked(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version)
VALUES ('live-claim', 'mikan', '2026-summer', 'running', CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP, 1)`); err != nil {
		t.Fatal(err)
	}
	catalog := catalogapp.NewService(db, nil)
	catalog.SetProviderReader(catalogapp.ProviderAniBT, &conditionalCatalogReader{})
	mikan := &conditionalCatalogReader{}
	catalog.SetProviderReader(catalogapp.ProviderMikan, mikan)
	logger := &dispatchResultLogger{}
	d := &Dispatcher{DB: db, CatalogRefresher: catalog, Logger: logger, now: func() time.Time {
		return time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	}}
	run := runDueCatalogRefresh(t, d)
	if got, _ := mikan.take(); len(got) != 1 || got[0] != "2026-fall" {
		t.Fatalf("Mikan season requests = %v", got)
	}
	if run.State != "completed" || !hasCompletedCount(run, 3) || run.EnqueuedCount != 0 || run.MergedCount != 1 || run.DispatchErrorCount != 0 || len(logger.infos) != 1 ||
		!strings.Contains(logger.infos[0].Message, "3 seasons checked, 1 refreshes already running") {
		t.Fatalf("run = %+v; infos = %+v", run, logger.infos)
	}
}
