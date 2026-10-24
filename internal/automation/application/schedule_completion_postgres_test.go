package application

import (
	"context"
	"testing"
	"time"

	catalogapp "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
)

type slowCatalogReader struct{ delay time.Duration }

func (r slowCatalogReader) ReadSeason(context.Context, string, catalogdomain.ReadHints) (catalogdomain.ReadResult, error) {
	time.Sleep(r.delay)
	return catalogdomain.ReadResult{}, nil
}

// TestScheduledRunNextRunFromCompletion proves a scheduled dispatch records
// its real completion time and computes next_run_at from it, not from the
// moment it was claimed.
func TestScheduledRunNextRunFromCompletion(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	catalog := catalogapp.NewService(db, nil)
	catalog.SetProviderReader(catalogapp.ProviderAniBT, slowCatalogReader{delay: 400 * time.Millisecond})
	catalog.SetProviderReader(catalogapp.ProviderMikan, slowCatalogReader{delay: 400 * time.Millisecond})
	dispatcher := &Dispatcher{DB: db, CatalogRefresher: catalog}
	if _, err := db.ExecContext(ctx, `UPDATE automation_schedules SET enabled = (kind = 'catalog_refresh'), next_run_at = CASE WHEN kind = 'catalog_refresh' THEN CURRENT_TIMESTAMP - INTERVAL '1 minute' ELSE NULL END`); err != nil {
		t.Fatal(err)
	}
	if count, err := dispatcher.DispatchDue(ctx); err != nil || count != 1 {
		t.Fatalf("DispatchDue = %d, %v", count, err)
	}
	var startedAt, finishedAt, nextRunAt time.Time
	if err := db.QueryRowContext(ctx, `
SELECT r.started_at, r.finished_at, s.next_run_at
FROM automation_dispatch_runs r JOIN automation_schedules s ON s.kind = r.kind
WHERE r.kind = 'catalog_refresh'`).Scan(&startedAt, &finishedAt, &nextRunAt); err != nil {
		t.Fatal(err)
	}
	if finishedAt.Sub(startedAt) < time.Second {
		t.Fatalf("finished_at must be the completion time: started=%s finished=%s", startedAt, finishedAt)
	}
	if !nextRunAt.Equal(finishedAt.Add(12 * time.Hour)) {
		t.Fatalf("next_run_at = %s, want finished_at %s + 12h", nextRunAt, finishedAt)
	}
}
