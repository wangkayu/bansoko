package application

import (
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
)

func TestTypedObservedSatisfactionUnknownCommitKeepsHistoricalProof(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "committed"
		if rollback {
			name = "not_committed"
		}
		t.Run(name, func(t *testing.T) {
			db, s, c, b, observed := preparedObservedBatch(t)
			ctx := context.Background()
			before := planGraphSnapshot(t, db)
			dsn := os.Getenv("BANSOKO_TEST_RUNTIME_DATABASE_URL")
			if dsn == "" {
				dsn = os.Getenv("BANSOKO_TEST_DATABASE_URL")
			}
			cfg, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			cfg.RuntimeParams["role"] = "bansoko_runtime"
			connector := &planLostCommitConnector{Connector: stdlib.GetConnector(*cfg), rollbackBeforeReply: rollback}
			faultDB := sql.OpenDB(connector)
			defer faultDB.Close()
			s.workItems = &workitems.Store{DB: faultDB, RetentionDays: workitems.DefaultRetentionDays}
			_, err = s.commitTypedPlan(ctx, c, b)
			if (err != nil) != rollback {
				t.Fatalf("rollback=%v err=%v", rollback, err)
			}
			if connector.commits.Load() != 1 || connector.calls.Load() != 1 {
				t.Fatalf("blind replay: %d/%d", connector.commits.Load(), connector.calls.Load())
			}
			if rollback {
				if planGraphSnapshot(t, db) != before {
					t.Fatal("rolled-back graph changed")
				}
				if _, ok, err := s.confirmTypedPlanCommit(ctx, c); err == nil && ok {
					t.Fatal("rollback claimed committed")
				}
				return
			}
			if err := os.Remove(observed); err != nil {
				t.Fatal(err)
			}
			committed := planGraphSnapshot(t, db)
			if _, ok, err := s.confirmTypedPlanCommit(ctx, c); err != nil || !ok {
				t.Fatalf("historical proof=%v %v", ok, err)
			}
			if planGraphSnapshot(t, db) != committed {
				t.Fatal("proof wrote graph")
			}
		})
	}
}
