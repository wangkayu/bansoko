package migrations

import (
	"context"
	"testing"
)

func TestAutomationDispatchCompletedCountUpgradePreservesLegacyRuns(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "149_notification_message_snapshot.sql")
	if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(
    id, kind, trigger, state, target_count, enqueued_count,
    dispatch_error_count, started_at, finished_at
) VALUES ('legacy-catalog', 'catalog_refresh', 'scheduled', 'partial', 2, 1, 1,
    CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var targetCount, queued, failures int
	var completed *int
	if err := db.QueryRowContext(ctx, `
SELECT state, target_count, enqueued_count, dispatch_error_count, completed_count
FROM automation_dispatch_runs WHERE id = 'legacy-catalog'`).Scan(&state, &targetCount, &queued, &failures, &completed); err != nil {
		t.Fatal(err)
	}
	if state != "partial" || targetCount != 2 || queued != 1 || failures != 1 || completed != nil {
		t.Fatalf("legacy run: state=%s targets=%d enqueued=%d errors=%d completed=%v", state, targetCount, queued, failures, completed)
	}
}

func TestAutomationDispatchUnconfirmedUpgradePreservesFailures(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "150_automation_dispatch_completed_count.sql")
	if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(
    id, kind, trigger, state, dispatch_error_count, completed_count, started_at, finished_at
) VALUES
    ('legacy-unknown-count', 'catalog_refresh', 'scheduled', 'failed', 1, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
    ('confirmed-failure', 'rss_refresh', 'manual', 'failed', 1, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var unchanged int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM automation_dispatch_runs
WHERE state = 'failed' AND dispatch_error_count = 1
  AND ((id = 'legacy-unknown-count' AND completed_count IS NULL)
       OR (id = 'confirmed-failure' AND completed_count = 0))`).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged != 2 {
		t.Fatalf("preserved failures = %d, want 2", unchanged)
	}
}
