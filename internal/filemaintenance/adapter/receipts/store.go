// Package receipts keeps the maintenance invocation receipts (start proof and
// final receipt) and the invocation lock in the database. It replaces the
// marker files the executor used to leave next to the operation's target:
// the library holds no bansoko state anymore.
package receipts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	maintenance "bansoko/internal/filemaintenance/executor"
)

// Store is the PostgreSQL ReceiptStore. The lock is a session-scoped advisory
// lock on the invocation token, held on a dedicated connection for the whole
// execution window (start proof, effect, final receipt).
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	if db == nil {
		panic("maintenance receipt store requires a database")
	}
	return &Store{db: db}
}

// Lock takes the invocation's advisory lock without waiting, mirroring the
// non-blocking file lock it replaces: a contended invocation is retried by
// its owner later, never queued here.
func (s *Store) Lock(ctx context.Context, token string) (func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var held bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, token).Scan(&held); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("take maintenance advisory lock: %w", err)
	}
	if !held {
		_ = conn.Close()
		return nil, maintenance.ErrMaintenanceLocked
	}
	return func() {
		// The unlock must run even when the caller's context is done: the lock
		// otherwise leaks until the pool discards the connection.
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, token)
		_ = conn.Close()
	}, nil
}

// WriteStarted records the start proof. An existing token — even one of an
// interrupted invocation — is an error: the recovery path reads that row, and
// a stage that may run again does so under a fresh invocation.
func (s *Store) WriteStarted(ctx context.Context, token string, proof []byte) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO maintenance_receipts (token, started) VALUES ($1, $2) ON CONFLICT (token) DO NOTHING`, token, proof)
	if err != nil {
		return fmt.Errorf("write maintenance start proof: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("maintenance invocation already has a start proof")
	}
	return nil
}

func (s *Store) ReadStarted(ctx context.Context, token string) ([]byte, error) {
	var proof []byte
	err := s.db.QueryRowContext(ctx, `SELECT started FROM maintenance_receipts WHERE token = $1`, token).Scan(&proof)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, maintenance.ErrMaintenanceNotStarted
	}
	if err != nil {
		return nil, fmt.Errorf("read maintenance start proof: %w", err)
	}
	return proof, nil
}

// WriteFinal completes the started row exactly once. A missing row or an
// existing final receipt is an error: neither proves this invocation ran.
func (s *Store) WriteFinal(ctx context.Context, token string, receipt []byte) error {
	res, err := s.db.ExecContext(ctx, `UPDATE maintenance_receipts SET final = $2, finalized_at = CURRENT_TIMESTAMP WHERE token = $1 AND final IS NULL`, token, receipt)
	if err != nil {
		return fmt.Errorf("write maintenance final receipt: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("maintenance final receipt refused: no open started row")
	}
	return nil
}

func (s *Store) ReadFinal(ctx context.Context, token string) ([]byte, error) {
	var receipt []byte
	err := s.db.QueryRowContext(ctx, `SELECT final FROM maintenance_receipts WHERE token = $1 AND final IS NOT NULL`, token).Scan(&receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, maintenance.ErrMaintenanceFinalAbsent
	}
	if err != nil {
		return nil, fmt.Errorf("read maintenance final receipt: %w", err)
	}
	return receipt, nil
}

// DiscardStarted removes the start proof of an invocation whose effect
// provably never applied. A row with a final receipt is never discarded.
func (s *Store) DiscardStarted(ctx context.Context, token string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM maintenance_receipts WHERE token = $1 AND final IS NULL`, token)
	if err != nil {
		return fmt.Errorf("discard refused maintenance start proof: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var finalized bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_receipts WHERE token = $1)`, token).Scan(&finalized)
	if err != nil {
		return err
	}
	if finalized {
		return errors.New("a final maintenance receipt is never discarded")
	}
	return nil
}
