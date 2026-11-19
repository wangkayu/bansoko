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
