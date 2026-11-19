package application

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	catalogapp "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	observabilityapp "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
)

// conditionalCatalogReader serves one entry with an ETag and answers 304 when
// the refresh sends that ETag back. It records every requested season.
type conditionalCatalogReader struct {
	mu         sync.Mutex
	seasons    []string
	failSeason string
	etags      []string
}

func (r *conditionalCatalogReader) ReadSeason(_ context.Context, season string, hints catalogdomain.ReadHints) (catalogdomain.ReadResult, error) {
	r.mu.Lock()
	r.seasons = append(r.seasons, season)
	r.etags = append(r.etags, hints.ETag)
	r.mu.Unlock()
	if season == r.failSeason {
		return catalogdomain.ReadResult{}, errors.New("fixture provider unavailable")
	}
	etag := `"` + season + `-v1"`
	if hints.ETag == etag {
		return catalogdomain.ReadResult{ETag: etag, NotModified: true}, nil
	}
	return catalogdomain.ReadResult{ETag: etag, Entries: []catalogdomain.Entry{{ProviderEntryRef: "1001", BGMID: "1001", Title: "Entry " + season}}}, nil
}

func (r *conditionalCatalogReader) take() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seasons, etags := r.seasons, r.etags
	r.seasons, r.etags = nil, nil
	return seasons, etags
}

func runDueCatalogRefresh(t *testing.T, dispatcher *Dispatcher) DispatchRun {
	t.Helper()
	ctx := context.Background()
	if _, err := dispatcher.DB.ExecContext(ctx, `UPDATE automation_schedules SET enabled = (kind = 'catalog_refresh'), next_run_at = CASE WHEN kind = 'catalog_refresh' THEN CURRENT_TIMESTAMP - INTERVAL '1 minute' ELSE NULL END`); err != nil {
		t.Fatal(err)
	}
	if count, err := dispatcher.DispatchDue(ctx); err != nil || count != 1 {
		t.Fatalf("DispatchDue = %d, %v; want one catalog_refresh dispatch", count, err)
	}
	var run DispatchRun
	if err := dispatcher.DB.QueryRowContext(ctx, `
SELECT state, target_count, completed_count, enqueued_count, merged_count, skipped_count, dispatch_error_count
FROM automation_dispatch_runs WHERE kind = 'catalog_refresh' ORDER BY created_at DESC LIMIT 1`).Scan(&run.State, &run.TargetCount, &run.CompletedCount, &run.EnqueuedCount, &run.MergedCount, &run.SkippedCount, &run.DispatchErrorCount); err != nil {
		t.Fatal(err)
	}
	return run
}

func catalogEntryRows(t *testing.T, dispatcher *Dispatcher) int {
	t.Helper()
	var count int
	if err := dispatcher.DB.QueryRow(`SELECT COUNT(*) FROM provider_catalog_entries`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestCatalogRefreshDispatchesCurrentAndNextSeasonConditionally proves the
// scheduled catalog_refresh refreshes exactly the current and next season per
// provider, revalidates with the stored ETag so a 304 rewrites no entries, and
// honours the per-provider refresh_enabled opt-out.
func TestCatalogRefreshDispatchesCurrentAndNextSeasonConditionally(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	anibt, mikan := &conditionalCatalogReader{}, &conditionalCatalogReader{}
	catalog := catalogapp.NewService(db, nil)
	catalog.SetProviderReader(catalogapp.ProviderAniBT, anibt)
	catalog.SetProviderReader(catalogapp.ProviderMikan, mikan)
	clock := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	dispatcher := &Dispatcher{DB: db, CatalogRefresher: catalog, now: func() time.Time { return clock }}
	wantSeasons := []string{"2026-fall", "2026-summer"}

	first := runDueCatalogRefresh(t, dispatcher)
	if first.State != "completed" || first.TargetCount != 4 || !hasCompletedCount(first, 4) || first.EnqueuedCount != 0 || first.DispatchErrorCount != 0 {
		t.Fatalf("first catalog_refresh run = %+v", first)
	}
	for name, reader := range map[string]*conditionalCatalogReader{"anibt": anibt, "mikan": mikan} {
		seasons, etags := reader.take()
		sort.Strings(seasons)
		if len(seasons) != 2 || seasons[0] != wantSeasons[0] || seasons[1] != wantSeasons[1] {
			t.Fatalf("%s refreshed seasons %v, want %v", name, seasons, wantSeasons)
		}
		for _, etag := range etags {
			if etag != "" {
				t.Fatalf("%s first refresh sent validator %q", name, etag)
			}
		}
	}
	entriesAfterFirst := catalogEntryRows(t, dispatcher)
	if entriesAfterFirst != 4 {
		t.Fatalf("entry rows after first refresh = %d, want 4", entriesAfterFirst)
	}

	second := runDueCatalogRefresh(t, dispatcher)
	if second.State != "completed" || !hasCompletedCount(second, 4) || second.EnqueuedCount != 0 {
		t.Fatalf("second catalog_refresh run = %+v", second)
	}
	for name, reader := range map[string]*conditionalCatalogReader{"anibt": anibt, "mikan": mikan} {
		_, etags := reader.take()
		for _, etag := range etags {
			if etag == "" {
				t.Fatalf("%s revalidation sent no ETag", name)
			}
		}
	}
	if got := catalogEntryRows(t, dispatcher); got != entriesAfterFirst {
		t.Fatalf("304 revalidation rewrote entries: %d -> %d", entriesAfterFirst, got)
	}
	var snapshots int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_catalog_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 4 {
		t.Fatalf("304 revalidation created snapshots: %d, want 4", snapshots)
	}

	if _, err := catalog.SetProviderPreference(ctx, catalogapp.ProviderMikan, catalogdomain.Preference{Season: "2026-summer", RefreshEnabled: false}, 0); err != nil {
		t.Fatal(err)
	}
	third := runDueCatalogRefresh(t, dispatcher)
	if third.State != "completed" || !hasCompletedCount(third, 2) || third.EnqueuedCount != 0 || third.SkippedCount != 2 {
		t.Fatalf("opt-out catalog_refresh run = %+v", third)
	}
	if seasons, _ := mikan.take(); len(seasons) != 0 {
		t.Fatalf("disabled provider was refreshed: %v", seasons)
	}
	if seasons, _ := anibt.take(); len(seasons) != 2 {
		t.Fatalf("enabled provider refreshed %v, want two seasons", seasons)
	}

	// Season rollover: the opt-out saved in 2026-summer still governs 2026-fall
	// and 2027-winter; no new preference row re-enables the provider.
	clock = time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)
	rolled := runDueCatalogRefresh(t, dispatcher)
	if rolled.State != "completed" || !hasCompletedCount(rolled, 2) || rolled.EnqueuedCount != 0 || rolled.SkippedCount != 2 {
		t.Fatalf("rolled-over catalog_refresh run = %+v", rolled)
	}
	if seasons, _ := mikan.take(); len(seasons) != 0 {
		t.Fatalf("opt-out did not survive the season rollover: %v", seasons)
	}
	seasons, _ := anibt.take()
	sort.Strings(seasons)
	if len(seasons) != 2 || seasons[0] != "2026-fall" || seasons[1] != "2027-winter" {
		t.Fatalf("rolled-over seasons = %v, want 2026-fall and 2027-winter", seasons)
	}
	pref, err := catalog.ReadProviderPreference(ctx, catalogapp.ProviderMikan, "2026-fall")
	if err != nil || pref.RefreshEnabled {
		t.Fatalf("new-season preference must show the carried opt-out: %+v err=%v", pref, err)
	}

	var nextRunInFuture bool
	if err := db.QueryRowContext(ctx, `SELECT next_run_at > CURRENT_TIMESTAMP + INTERVAL '11 hours' FROM automation_schedules WHERE kind = 'catalog_refresh'`).Scan(&nextRunInFuture); err != nil {
		t.Fatal(err)
	}
	if !nextRunInFuture {
		t.Fatal("scheduled catalog_refresh did not advance next_run_at by its 12-hour interval")
	}
}

func hasCompletedCount(run DispatchRun, want int) bool {
	return run.CompletedCount != nil && *run.CompletedCount == want
}

func TestCatalogDispatchProjectsInlinePartialResult(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	catalog := catalogapp.NewService(db, nil)
	catalog.SetProviderReader(catalogapp.ProviderAniBT, &conditionalCatalogReader{failSeason: "2026-fall"})
	if _, err := catalog.SetProviderPreference(ctx, catalogapp.ProviderMikan, catalogdomain.Preference{Season: "2026-summer", RefreshEnabled: false}, 0); err != nil {
		t.Fatal(err)
	}
	recorder, err := observabilityapp.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	logger := &dispatchResultLogger{}
	d := &Dispatcher{DB: db, CatalogRefresher: catalog, Recorder: recorder, Logger: logger, now: func() time.Time {
		return time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	}}
	run := runDueCatalogRefresh(t, d)
	if run.State != "partial" || !hasCompletedCount(run, 1) || run.EnqueuedCount != 0 || run.DispatchErrorCount != 1 || run.SkippedCount != 2 {
		t.Fatalf("run = %+v", run)
	}
	if len(logger.infos) != 0 || len(logger.warns) != 2 {
		t.Fatalf("infos = %+v; warnings = %+v", logger.infos, logger.warns)
	}
	event := logger.warns[1]
	if event.Outcome != "partial" || event.ReasonCode != string(observabilityport.RoutineResultDispatchPartial) || !strings.Contains(event.Message, "1 seasons checked") {
		t.Fatalf("run log = %+v", event)
	}
	var outcome, code string
	var checked, queued, failures sql.NullInt64
	if err := db.QueryRowContext(ctx, `
SELECT outcome, last_result_code, (details->>'completed_count')::integer,
       (details->>'enqueued_count')::integer, (details->>'error_count')::integer
FROM routine_activities WHERE kind = 'automation_dispatch' AND scope_id = 'catalog_refresh'`).Scan(&outcome, &code, &checked, &queued, &failures); err != nil {
		t.Fatal(err)
	}
	if outcome != string(observabilityport.RoutineOutcomeWarning) || code != event.ReasonCode || !checked.Valid || checked.Int64 != 1 || !queued.Valid || queued.Int64 != 0 || !failures.Valid || failures.Int64 != 1 {
		t.Fatalf("routine = %q/%q, completed=%v queued=%v errors=%v", outcome, code, checked, queued, failures)
	}
	var id string
	if err := db.QueryRowContext(ctx, `SELECT id FROM automation_dispatch_runs WHERE kind = 'catalog_refresh'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	if detail, err := service.GetRun(ctx, id); err != nil || detail.State != "partial" || !hasCompletedCount(detail, 1) {
		t.Fatalf("run detail = %+v, err = %v", detail, err)
	}
}
