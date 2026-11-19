package application

import (
	"context"
	"database/sql"
	"testing"

	catalogdomain "bansoko/internal/catalog/domain"
	observabilityapp "bansoko/internal/observability/application"
)

type interruptedCatalog struct {
	stop func() error
}

func (r interruptedCatalog) ProviderRefreshEnabled(context.Context, string) (bool, error) {
	return true, nil
}

func (r interruptedCatalog) RefreshProviderSeason(context.Context, string, string) (catalogdomain.Snapshot, error) {
	return catalogdomain.Snapshot{}, r.stop()
}

func automationRecorder(t *testing.T, db *sql.DB) *observabilityapp.Service {
	t.Helper()
	recorder, err := observabilityapp.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

func TestAutomationInterruptedRunCommitsUnconfirmedOnce(t *testing.T) {
	for _, abnormalExit := range []bool{false, true} {
		name := "canceled"
		if abnormalExit {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			db := openDispatcherTestDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := db.ExecContext(ctx, `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state)
VALUES ('interrupted', 'catalog_refresh', 'manual', 'queued')`); err != nil {
				t.Fatal(err)
			}
			calls := 0
			logger := &dispatchResultLogger{}
			d := &Dispatcher{DB: db, Logger: logger, Recorder: automationRecorder(t, db), CatalogRefresher: interruptedCatalog{stop: func() error {
				calls++
				if abnormalExit {
					panic("unexpected provider abort")
				}
				cancel()
				return ctx.Err()
			}}}
			if _, err := d.DispatchManual(ctx); err != nil {
				t.Fatal(err)
			}
			assertUnconfirmedDispatch(t, db, "interrupted")
			if calls != 1 || len(logger.infos) != 0 || len(logger.warns) != 1 || logger.warns[0].Outcome != "unconfirmed" {
				t.Fatalf("calls=%d infos=%+v warnings=%+v", calls, logger.infos, logger.warns)
			}
		})
	}
}

func assertUnconfirmedDispatch(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	var unconfirmed bool
	if err := db.QueryRow(`
SELECT r.state = 'unconfirmed' AND r.completed_count IS NULL AND r.dispatch_error_count = 0
       AND a.outcome = 'warning' AND a.last_result_code = 'dispatch_unconfirmed' AND a.details IS NULL
       AND EXISTS (SELECT 1 FROM diagnostic_episodes d WHERE d.target_id = 'automation.dispatch'
                   AND d.severity = 'warning' AND d.succeeded_after_at IS NULL)
FROM automation_dispatch_runs r
JOIN routine_activities a ON a.kind = 'automation_dispatch' AND a.scope_kind = 'automation' AND a.scope_id = r.kind
WHERE r.id = $1`, id).Scan(&unconfirmed); err != nil || !unconfirmed {
		t.Fatalf("unconfirmed run, routine, and diagnostic did not agree: %t, %v", unconfirmed, err)
	}
}

func TestAutomationFinalizationRollsBackOnHealthWriteFailure(t *testing.T) {
	for _, target := range []string{"routine", "diagnostic"} {
		t.Run(target, func(t *testing.T) {
			db := openDispatcherTestDB(t)
			ctx := context.Background()
			logger := &dispatchResultLogger{}
			d := &Dispatcher{DB: db, Logger: logger, Recorder: automationRecorder(t, db)}
			for _, stmt := range []string{
				`UPDATE automation_schedules SET enabled = TRUE, version = 42,
                    next_run_at = '2026-01-01T00:00:00Z', last_dispatch_at = NULL WHERE kind = 'metadata_match'`,
				`INSERT INTO automation_dispatch_runs(id, kind, trigger, state, started_at)
                    VALUES ('rollback', 'metadata_match', 'scheduled', 'running', CURRENT_TIMESTAMP)`,
			} {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatal(err)
				}
			}
			blockWrite := `ALTER TABLE routine_activities ADD CHECK (kind <> 'automation_dispatch')`
			if target == "diagnostic" {
				blockWrite = `ALTER TABLE diagnostic_episodes ADD CHECK (target_id <> 'automation.dispatch')`
			}
			if _, err := ownerTestDB(t).ExecContext(ctx, blockWrite); err != nil {
				t.Fatal(err)
			}
			if err := d.finishRun(ctx, "rollback", unconfirmedDispatch()); err == nil {
				t.Fatal("finalized despite failed health write")
			}
			var unchanged bool
			if err := db.QueryRow(`
SELECT r.state = 'running' AND r.finished_at IS NULL AND s.version = 42
       AND s.next_run_at = '2026-01-01T00:00:00Z' AND s.last_dispatch_at IS NULL
       AND NOT EXISTS (SELECT 1 FROM routine_activities WHERE kind = 'automation_dispatch')
       AND NOT EXISTS (SELECT 1 FROM diagnostic_episodes WHERE target_id = 'automation.dispatch')
FROM automation_dispatch_runs r JOIN automation_schedules s ON s.kind = r.kind WHERE r.id = 'rollback'`).Scan(&unchanged); err != nil || !unchanged {
				t.Fatalf("transaction did not fully roll back: %t, %v", unchanged, err)
			}
			if len(logger.infos) != 0 || len(logger.warns) != 0 {
				t.Fatalf("published result before commit: infos=%+v, warnings=%+v", logger.infos, logger.warns)
			}
		})
	}
}
