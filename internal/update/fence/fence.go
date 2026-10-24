// Package fence owns the durable singleton admission fence shared by every
// update channel. The journal records the operation's progress, while this
// typed application-facing port delegates PostgreSQL storage to its adapter.
package fence

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	persistence "bansoko/internal/update/fence/adapter/persistence"
)

// AdmittedByEmbedded is the only fence owner: the embedded updater.
const AdmittedByEmbedded = "embedded"

var (
	ErrLocked           = persistence.ErrLocked
	ErrTargetStale      = persistence.ErrTargetStale
	ErrSequenceMissing  = errors.New("update target is missing a positive release sequence")
	ErrNotFound         = persistence.ErrNotFound
	ErrOwnerInvalid     = errors.New("update fence owner is not recognized")
	ErrOwnerConflict    = persistence.ErrOwnerConflict
	ErrManifestConflict = persistence.ErrManifestConflict
	ErrStateConflict    = persistence.ErrStateConflict
	ErrSequenceConflict = persistence.ErrSequenceConflict
	ErrEvidenceMissing  = errors.New("update fence recovery evidence is required")
)

// OpenFenceRow is the complete identity of one unreleased fence row. Recovery
// callers must use all of these fields when closing a row; an operation id by
// itself is not sufficient evidence across update channels.
type OpenFenceRow struct {
	OperationID    string
	AdmittedBy     string
	Sequence       int64
	ManifestDigest string
	State          string
}

// Store is the application-facing update fence port. PostgreSQL handles, SQL,
// and row scanners are owned by the persistence adapter and do not cross this
// boundary.
type Store struct {
	adapter *persistence.Repository
}

// New returns a fence store over db. The constructor remains database/sql-based
// for composition-root compatibility; the handle is immediately handed to the
// persistence adapter and is not retained by this application-facing type.
func New(db *sql.DB) *Store {
	return &Store{adapter: persistence.NewRepository(db)}
}

// Acquire atomically checks the publisher-owned monotonic sequence and inserts
// the open singleton row. The partial unique index arbitrates concurrent
// admissions from separate processes after both have read the sequence floor.
func (s *Store) Acquire(ctx context.Context, operationID, admittedBy string, sequence int64, manifestDigest string) error {
	if err := s.validateAvailable(); err != nil {
		return err
	}
	if sequence < 1 {
		return ErrSequenceMissing
	}
	if operationID == "" {
		return ErrStateConflict
	}
	if manifestDigest == "" {
		return ErrManifestConflict
	}
	if !validOwner(admittedBy) {
		if admittedBy == "" {
			return ErrStateConflict
		}
		return ErrOwnerInvalid
	}
	return s.adapter.Acquire(ctx, operationID, admittedBy, sequence, manifestDigest)
}

// Row is the complete durable identity of one fence row in any state,
// released or not. Recovery reads it to prove the exact stored identity of its
// own operation before it closes, settles or archives anything.
type Row struct {
	OperationID    string
	AdmittedBy     string
	Sequence       int64
	ManifestDigest string
	State          string
	Released       bool
}

// Lookup returns the exact row of one operation, released or not. It is a
// read-only primary-key lookup: it is not a sweep, never mutates, and reports a
// missing row as found=false so callers fail closed instead of guessing. A
// different owner's row for the same operation is returned as-is and must be
// refused by the caller.
func (s *Store) Lookup(ctx context.Context, operationID string) (Row, bool, error) {
	if err := s.validateAvailable(); err != nil {
		return Row{}, false, err
	}
	if strings.TrimSpace(operationID) == "" {
		return Row{}, false, ErrStateConflict
	}
	row, found, err := s.adapter.LookupRow(ctx, operationID)
	if err != nil {
		return Row{}, false, err
	}
	if !found {
		return Row{}, false, nil
	}
	return Row{
		OperationID:    row.OperationID,
		AdmittedBy:     row.AdmittedBy,
		Sequence:       row.Sequence,
		ManifestDigest: row.ManifestDigest,
		State:          row.State,
		Released:       row.Released,
	}, true, nil
}

// ListOpen returns only unreleased rows owned by admittedBy. It deliberately
// does not implement a stale sweep: a missing journal cannot prove that a row
// is orphaned, and the other update channel must remain untouched. Callers must
// compare the complete row with durable journal evidence before closing.
func (s *Store) ListOpen(ctx context.Context, admittedBy string) ([]OpenFenceRow, error) {
	if err := s.validateAvailable(); err != nil {
		return nil, err
	}
	if !validOwner(admittedBy) {
		if admittedBy == "" {
			return nil, ErrStateConflict
		}
		return nil, ErrOwnerInvalid
	}
	rows, err := s.adapter.ListOpen(ctx, admittedBy)
	if err != nil {
		return nil, err
	}
	return openFenceRows(rows), nil
}

// ListOpenAll returns every unreleased fence row for startup diagnostics. It is
// intentionally read-only and is not a cleanup primitive; callers must match
// each row to same-owner, same-operation, same-manifest journal evidence before
// attempting a close.
func (s *Store) ListOpenAll(ctx context.Context) ([]OpenFenceRow, error) {
	if err := s.validateAvailable(); err != nil {
		return nil, err
	}
	rows, err := s.adapter.ListOpenAll(ctx)
	if err != nil {
		return nil, err
	}
	return openFenceRows(rows), nil
}

// ReconcileExact closes one open row after a terminal journal outcome has been
// durably recorded. The identity and expected open state are checked together.
// terminalState is the journal state, not the storage state: rolled_back is
// intentionally recorded as failed while succeeded is recorded as settled.
func (s *Store) ReconcileExact(ctx context.Context, operationID, admittedBy, manifestDigest string, expectedSequence int64, expectedState, terminalState string) error {
	if err := s.validateAvailable(); err != nil {
		return err
	}
	if err := validateTransitionInput(operationID, admittedBy); err != nil {
		return err
	}
	if manifestDigest == "" {
		return ErrManifestConflict
	}
	if expectedSequence < 1 {
		return ErrSequenceMissing
	}
	if !validOpenState(expectedState) {
		return ErrStateConflict
	}
	storageState, ok := fenceStorageState(terminalState)
	if !ok {
		return ErrStateConflict
	}
	return s.adapter.ReconcileExact(ctx, operationID, admittedBy, manifestDigest, expectedSequence, expectedState, storageState)
}

// ReconcileExactCurrentState closes one open row after a terminal journal
// outcome, first reading the row's identity and open state under the same
// exact compare-and-swap.
func (s *Store) ReconcileExactCurrentState(ctx context.Context, operationID, admittedBy, manifestDigest string, expectedSequence int64, terminalState string) error {
	if err := s.validateAvailable(); err != nil {
		return err
	}
	if err := validateTransitionInput(operationID, admittedBy); err != nil {
		return err
	}
	if manifestDigest == "" {
		return ErrManifestConflict
	}
	if expectedSequence < 1 {
		return ErrSequenceMissing
	}
	storageState, ok := fenceStorageState(terminalState)
	if !ok {
		return ErrStateConflict
	}
	return s.adapter.ReconcileExactCurrentState(ctx, operationID, admittedBy, manifestDigest, expectedSequence, storageState)
}

// ReconcileExactCurrentStateOrProveClosed replays a terminal close without
// weakening the strict CAS primitive. It first proves the complete row
// identity; an already-released row is accepted only when its stored terminal
// state is exactly the requested state. Otherwise the open row is closed by
// the strict current-state CAS.
func (s *Store) ReconcileExactCurrentStateOrProveClosed(ctx context.Context, operationID, admittedBy, manifestDigest string, expectedSequence int64, terminalState string) error {
	if err := s.validateAvailable(); err != nil {
		return err
	}
	if err := validateTransitionInput(operationID, admittedBy); err != nil {
		return err
	}
	if manifestDigest == "" {
		return ErrManifestConflict
	}
	if expectedSequence < 1 {
		return ErrSequenceMissing
	}
	storageState, ok := fenceStorageState(terminalState)
	if !ok {
		return ErrStateConflict
	}
	row, found, err := s.Lookup(ctx, operationID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if row.AdmittedBy != admittedBy {
		return ErrOwnerConflict
	}
	if row.ManifestDigest != manifestDigest {
		return ErrManifestConflict
	}
	if row.Sequence != expectedSequence {
		return ErrSequenceConflict
	}
	if row.Released {
		if row.State == storageState {
			return nil
		}
		return ErrStateConflict
	}
	return s.adapter.ReconcileExactCurrentState(ctx, operationID, admittedBy, manifestDigest, expectedSequence, storageState)
}

// RecoverExact is the operator-only exact orphan recovery path. It closes the
// row and records the supplied evidence atomically in the adapter transaction.
//
// It is the one close path that is deliberately not sequence-bound: it exists
// for rows that have no journal record to derive an expected release sequence
// from, and the host-owned recovery binary has no journal access. Its exactness
// comes from the identity fields plus the operator evidence stored with the
// close.
func (s *Store) RecoverExact(ctx context.Context, operationID, admittedBy, manifestDigest, expectedState, terminalState, evidence string) error {
	if err := s.validateAvailable(); err != nil {
		return err
	}
	if err := validateTransitionInput(operationID, admittedBy); err != nil {
		return err
	}
	if manifestDigest == "" {
		return ErrManifestConflict
	}
	if !validOpenState(expectedState) {
		return ErrStateConflict
	}
	if strings.TrimSpace(evidence) == "" || len(evidence) > 4096 {
		return ErrEvidenceMissing
	}
	storageState, ok := fenceStorageState(terminalState)
	if !ok {
		return ErrStateConflict
	}
	return s.adapter.Recover(ctx, operationID, admittedBy, manifestDigest, expectedState, storageState, terminalState, evidence)
}

func (s *Store) validateAvailable() error {
	if s == nil || s.adapter == nil {
		return persistence.ErrDatabaseNotConfigured
	}
	return nil
}

func fenceStorageState(terminalState string) (string, bool) {
	switch terminalState {
	case "succeeded":
		return "settled", true
	case "failed", "rolled_back":
		return "failed", true
	default:
		return "", false
	}
}

func validOpenState(state string) bool { return state == "active" }

func validateTransitionInput(operationID, admittedBy string) error {
	if operationID == "" {
		return ErrStateConflict
	}
	if !validOwner(admittedBy) {
		if admittedBy == "" {
			return ErrStateConflict
		}
		return ErrOwnerInvalid
	}
	return nil
}

func validOwner(owner string) bool {
	return owner == AdmittedByEmbedded
}

func openFenceRows(rows []persistence.OpenFenceRow) []OpenFenceRow {
	result := make([]OpenFenceRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, OpenFenceRow{
			OperationID:    row.OperationID,
			AdmittedBy:     row.AdmittedBy,
			Sequence:       row.Sequence,
			ManifestDigest: row.ManifestDigest,
			State:          row.State,
		})
	}
	return result
}

// Compile-time documentation guard: all adapter errors are intentionally
// surfaced through the application-owned sentinel values above.
