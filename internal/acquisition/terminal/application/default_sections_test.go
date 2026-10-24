package application

import (
	anime "bansoko/internal/anime/domain"
	"bansoko/internal/persistence/txscope"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
)

type directoryProbe struct{ calls atomic.Int32 }

func (p *directoryProbe) Connect(context.Context) (driver.Conn, error) {
	return &directoryProbeConn{p}, nil
}
func (p *directoryProbe) Driver() driver.Driver { return directoryProbeDriver{p} }

type directoryProbeDriver struct{ p *directoryProbe }

func (d directoryProbeDriver) Open(string) (driver.Conn, error) { return &directoryProbeConn{d.p}, nil }

type directoryProbeConn struct{ p *directoryProbe }

func (c *directoryProbeConn) Prepare(string) (driver.Stmt, error) {
	c.p.calls.Add(1)
	return nil, errors.New("unexpected SQL")
}
func (c *directoryProbeConn) Close() error              { return nil }
func (c *directoryProbeConn) Begin() (driver.Tx, error) { return directoryProbeTx{}, nil }
func (c *directoryProbeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.p.calls.Add(1)
	return nil, errors.New("unexpected SQL")
}
func (c *directoryProbeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.p.calls.Add(1)
	return nil, errors.New("unexpected SQL")
}

type directoryProbeTx struct{}

func (directoryProbeTx) Commit() error   { return nil }
func (directoryProbeTx) Rollback() error { return nil }

func TestDefaultDirectoryDomainInvalidNeverCallsSQL(t *testing.T) {
	probe := &directoryProbe{}
	db := sql.OpenDB(probe)
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	directory := func() (json.RawMessage, error) {
		return defaultSectionDirectoryFrom(func(key string) (any, error) {
			if key == anime.SectionReleaseRules {
				return anime.ReleaseFilterRules{MustContain: "["}, nil
			}
			return anime.DefaultSectionValue(key)
		})
	}
	_, err = acceptNativeTxWithDefaults(context.Background(), txscope.Wrap(tx), preparedNative{}, directory)
	var refusal *AcceptanceError
	if !errors.As(err, &refusal) || refusal.Status != 500 || refusal.Code != "INTERNAL_INVARIANT" || refusal.Reason != "default_directory_invalid" {
		t.Fatalf("invalid default: %v", err)
	}
	if calls := probe.calls.Load(); calls != 0 {
		t.Fatalf("invalid default reached SQL: %d calls", calls)
	}
}
