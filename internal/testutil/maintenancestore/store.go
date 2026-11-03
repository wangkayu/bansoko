// Package maintenancestore is the in-memory ReceiptStore for tests: the same
// start/final/discard contract as the PostgreSQL store, with a per-token
// mutex standing in for the advisory lock.
package maintenancestore

import (
	"context"
	"errors"
	"sync"

	maintenance "bansoko/internal/filemaintenance/executor"
)

type receiptRow struct {
	started []byte
	final   []byte
}

// Store is one process's receipt set. It is not shared across processes, so
// it proves sequencing only inside the test that owns it.
type Store struct {
	mu    sync.Mutex
	rows  map[string]*receiptRow
	locks map[string]*sync.Mutex
}

func New() *Store {
	return &Store{rows: map[string]*receiptRow{}, locks: map[string]*sync.Mutex{}}
}

var _ maintenance.ReceiptStore = (*Store)(nil)

func (s *Store) Lock(_ context.Context, token string) (func(), error) {
	s.mu.Lock()
	m, ok := s.locks[token]
	if !ok {
		m = &sync.Mutex{}
		s.locks[token] = m
	}
	s.mu.Unlock()
	if !m.TryLock() {
		return nil, maintenance.ErrMaintenanceLocked
	}
	return m.Unlock, nil
}

func (s *Store) WriteStarted(_ context.Context, token string, proof []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rows[token]; exists {
		return errors.New("maintenance invocation already has a start proof")
	}
	s.rows[token] = &receiptRow{started: append([]byte(nil), proof...)}
	return nil
}

func (s *Store) ReadStarted(_ context.Context, token string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, exists := s.rows[token]
	if !exists {
		return nil, maintenance.ErrMaintenanceNotStarted
	}
	return append([]byte(nil), row.started...), nil
}

func (s *Store) WriteFinal(_ context.Context, token string, receipt []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, exists := s.rows[token]
	if !exists || row.final != nil {
		return errors.New("maintenance final receipt refused: no open started row")
	}
	row.final = append([]byte(nil), receipt...)
	return nil
}

func (s *Store) ReadFinal(_ context.Context, token string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, exists := s.rows[token]
	if !exists || row.final == nil {
		return nil, maintenance.ErrMaintenanceFinalAbsent
	}
	return append([]byte(nil), row.final...), nil
}

func (s *Store) DiscardStarted(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, exists := s.rows[token]
	if !exists {
		return nil
	}
	if row.final != nil {
		return errors.New("a final maintenance receipt is never discarded")
	}
	delete(s.rows, token)
	return nil
}
