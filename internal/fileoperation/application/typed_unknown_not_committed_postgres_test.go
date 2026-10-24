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

func TestTypedPlanUnknownNotCommittedNeverReplays(t *testing.T) {
	s, c := preparedPlanPGFixture(t)
	ctx := context.Background()
	plan, err := s.loadPlan(ctx, c.Owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := s.captureArtifactBatch(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.buildArtifactBatch(ctx, batch, nil); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, s.db)
	dsn := os.Getenv("BANSOKO_TEST_RUNTIME_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("BANSOKO_TEST_DATABASE_URL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["role"] = "bansoko_runtime"
	connector := &planLostCommitConnector{Connector: stdlib.GetConnector(*cfg), rollbackBeforeReply: true}
	db := sql.OpenDB(connector)
	defer db.Close()
	s.workItems = &workitems.Store{DB: db}
	out, err := s.commitTypedPlan(ctx, c, batch)
	if err == nil || out.CommittedClaim != nil {
		t.Fatalf("unknown rollback falsely committed: %+v %v", out, err)
	}
	if connector.calls.Load() != 1 || connector.commits.Load() != 1 {
		t.Fatalf("fault/replay=%d/%d", connector.commits.Load(), connector.calls.Load())
	}
	if after := planGraphSnapshot(t, s.db); after != before {
		t.Fatal("unknown not-committed graph changed")
	}
	if _, ok, err := s.confirmTypedPlanCommit(ctx, c); err != nil || ok {
		t.Fatalf("not-committed confirmed=%v err=%v", ok, err)
	}
}
