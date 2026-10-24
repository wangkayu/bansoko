package application

import (
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

type planLostCommitConnector struct {
	driver.Connector
	commits             atomic.Int32
	calls               atomic.Int32
	rollbackBeforeReply bool
	queryNeedle         string
	faultOrdinal        int32
}
type planLostCommitConn struct {
	driver.Conn
	c     *planLostCommitConnector
	armed bool
}
type planLostCommitTx struct {
	driver.Tx
	c *planLostCommitConn
}

func (c *planLostCommitConnector) Connect(ctx context.Context) (driver.Conn, error) {
	v, e := c.Connector.Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &planLostCommitConn{Conn: v, c: c}, nil
}
func (c *planLostCommitConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	v, e := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	return &planLostCommitTx{v, c}, nil
}
func (c *planLostCommitConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	needle := c.c.queryNeedle
	if needle == "" {
		needle = "postprocessing_plan_commit("
	}
	if strings.Contains(q, needle) {
		ordinal := c.c.calls.Add(1)
		c.armed = c.c.faultOrdinal == 0 || ordinal == c.c.faultOrdinal
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (t *planLostCommitTx) Commit() error {
	if t.c.armed && t.c.c.rollbackBeforeReply {
		t.c.armed = false
		if err := t.Tx.Rollback(); err != nil {
			return err
		}
		t.c.c.commits.Add(1)
		return errors.New("fixture connection lost before server commit")
	}
	e := t.Tx.Commit()
	if e == nil && t.c.armed {
		t.c.armed = false
		t.c.c.commits.Add(1)
		return errors.New("fixture connection lost after server commit")
	}
	return e
}
func TestTypedPlanRealUnknownCommitUsesIndependentProof(t *testing.T) {
	s, c := preparedPlanPGFixture(t)
	ctx := context.Background()
	plan, err := s.loadPlan(ctx, c.Owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.captureArtifactBatch(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.buildArtifactBatch(ctx, b, nil); err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("BANSOKO_TEST_RUNTIME_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("BANSOKO_TEST_DATABASE_URL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["role"] = "bansoko_runtime"
	connector := &planLostCommitConnector{Connector: stdlib.GetConnector(*cfg)}
	db := sql.OpenDB(connector)
	defer db.Close()
	s.workItems = &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	out, err := s.commitTypedPlan(ctx, c, b)
	if err != nil {
		t.Fatal(err)
	}
	if out.WriteOwner != nil || out.PostTransition != nil {
		t.Fatal("generic continuation")
	}
	if connector.commits.Load() != 1 || connector.calls.Load() != 1 {
		t.Fatalf("fault/replay=%d/%d", connector.commits.Load(), connector.calls.Load())
	}
	before := planGraphSnapshot(t, s.db)
	if _, ok, e := s.confirmTypedPlanCommit(ctx, c); e != nil || !ok {
		t.Fatalf("independent proof=%v %v", ok, e)
	}
	if after := planGraphSnapshot(t, s.db); after != before {
		t.Fatal("proof wrote graph")
	}
}
