package workitems

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
)

type transactionBoundaryState struct {
	mu        sync.Mutex
	begins    int
	commits   int
	rollbacks int
}

type transactionBoundaryConnector struct {
	state *transactionBoundaryState
}

type transactionBoundaryDriver struct {
	state *transactionBoundaryState
}

type transactionBoundaryConn struct {
	state *transactionBoundaryState
}

type transactionBoundaryTx struct {
	state *transactionBoundaryState
}

func (c transactionBoundaryConnector) Connect(context.Context) (driver.Conn, error) {
	return &transactionBoundaryConn{state: c.state}, nil
}

func (c transactionBoundaryConnector) Driver() driver.Driver {
	return transactionBoundaryDriver{state: c.state}
}

func (d transactionBoundaryDriver) Open(string) (driver.Conn, error) {
	return &transactionBoundaryConn{state: d.state}, nil
}

func (c *transactionBoundaryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("transaction-boundary test driver does not prepare statements")
}

func (c *transactionBoundaryConn) Close() error { return nil }

func (c *transactionBoundaryConn) Begin() (driver.Tx, error) {
	return c.beginTx()
}

func (c *transactionBoundaryConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.beginTx()
}

func (c *transactionBoundaryConn) beginTx() (driver.Tx, error) {
	c.state.mu.Lock()
	c.state.begins++
	c.state.mu.Unlock()
	return &transactionBoundaryTx{state: c.state}, nil
}

func (tx *transactionBoundaryTx) Commit() error {
	tx.state.mu.Lock()
	tx.state.commits++
	tx.state.mu.Unlock()
	return nil
}

func (tx *transactionBoundaryTx) Rollback() error {
	tx.state.mu.Lock()
	tx.state.rollbacks++
	tx.state.mu.Unlock()
	return nil
}

var (
	_ driver.Connector   = transactionBoundaryConnector{}
	_ driver.Driver      = transactionBoundaryDriver{}
	_ driver.ConnBeginTx = (*transactionBoundaryConn)(nil)
	_ driver.Conn        = (*transactionBoundaryConn)(nil)
	_ driver.Tx          = (*transactionBoundaryTx)(nil)
)

func openTransactionBoundaryDB(t *testing.T, state *transactionBoundaryState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(transactionBoundaryConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func transactionBoundaryCounts(state *transactionBoundaryState) (begins, commits, rollbacks int) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.begins, state.commits, state.rollbacks
}

func TestStoreWithTxRejectsNilStoreAndCallback(t *testing.T) {
	var nilStore *Store
	if err := nilStore.WithTx(context.Background(), func(*sql.Tx) error { return nil }); err == nil {
		t.Fatal("nil store must be rejected")
	}

	state := &transactionBoundaryState{}
	store := &Store{DB: openTransactionBoundaryDB(t, state)}
	if err := store.WithTx(context.Background(), nil); err == nil {
		t.Fatal("nil transaction callback must be rejected")
	}
	if begins, commits, rollbacks := transactionBoundaryCounts(state); begins != 0 || commits != 0 || rollbacks != 0 {
		t.Fatalf("nil callback opened a transaction: begins=%d commits=%d rollbacks=%d", begins, commits, rollbacks)
	}
}

func TestStoreWithTxRollsBackCallbackErrorAndCommitsSuccess(t *testing.T) {
	state := &transactionBoundaryState{}
	store := &Store{DB: openTransactionBoundaryDB(t, state)}
	callbackErr := errors.New("callback failed")

	err := store.WithTx(context.Background(), func(tx *sql.Tx) error {
		if tx == nil {
			t.Fatal("transaction callback received a nil transaction")
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v, want %v", err, callbackErr)
	}
	if begins, commits, rollbacks := transactionBoundaryCounts(state); begins != 1 || commits != 0 || rollbacks != 1 {
		t.Fatalf("callback error lifecycle = begins=%d commits=%d rollbacks=%d, want 1/0/1", begins, commits, rollbacks)
	}

	if err := store.WithTx(context.Background(), func(tx *sql.Tx) error {
		if tx == nil {
			t.Fatal("transaction callback received a nil transaction")
		}
		return nil
	}); err != nil {
		t.Fatalf("successful callback: %v", err)
	}
	if begins, commits, rollbacks := transactionBoundaryCounts(state); begins != 2 || commits != 1 || rollbacks != 1 {
		t.Fatalf("successful callback lifecycle = begins=%d commits=%d rollbacks=%d, want 2/1/1", begins, commits, rollbacks)
	}
}
