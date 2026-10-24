// Package persistence implements the PostgreSQL adapter for the shared update
// fence. SQL, database handles, driver errors, and row scanning stop at this
// boundary; the parent fence package exposes only the typed application port.
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	generated "bansoko/internal/update/fence/adapter/persistence/generated"
)

const uniqueViolation = "23505"

// OpenFenceRow is the adapter-local projection of one unreleased fence row.
// The parent package maps it to its application-owned row value.
type OpenFenceRow struct {
	OperationID    string
	AdmittedBy     string
	Sequence       int64
	ManifestDigest string
	State          string
}

// FenceRow is the adapter-local projection of one fence row in any state,
// including a released one.
type FenceRow struct {
	OperationID    string
	AdmittedBy     string
	Sequence       int64
	ManifestDigest string
	State          string
	Released       bool
}

var (
	ErrDatabaseNotConfigured = errors.New("update fence database is not configured")
	ErrLocked                = errors.New("another update operation holds the update fence")
	ErrNotFound              = errors.New("update fence row was not found")
	ErrOwnerConflict         = errors.New("update fence row belongs to a different owner")
	ErrManifestConflict      = errors.New("update fence row belongs to a different manifest")
	ErrStateConflict         = errors.New("update fence row is not in the required state")
	ErrSequenceConflict      = errors.New("update fence row holds a different release sequence")
)

// Repository owns all PostgreSQL access for the update fence. Methods do not
// expose database/sql values to callers and retain the existing transaction
// ownership: Acquire and Recover own their short transactions; other methods
// use one autocommitted statement (plus an identity read when required).
type Repository struct {
	db *sql.DB
}

// NewRepository wires the PostgreSQL fence adapter.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) requireDB() error {
	if r == nil || r.db == nil {
		return ErrDatabaseNotConfigured
	}
	return nil
}

// Acquire checks the persisted sequence floor and inserts the active singleton
// row in one transaction. The partial unique index remains the concurrent
// cross-channel mutex.
func (r *Repository) Acquire(ctx context.Context, operationID, admittedBy string, sequence int64, manifestDigest string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	queries := generated.New(tx)
	floor, err := queries.ReadSequenceFloor(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		floor = 0
	} else if err != nil {
		return err
	}
	if sequence <= floor {
		return fmt.Errorf("%w: sequence %d is not above floor %d", ErrTargetStale, sequence, floor)
	}
	if err := queries.InsertActiveFence(ctx, generated.InsertActiveFenceParams{
		OperationID: operationID, AdmittedBy: admittedBy, Sequence: sequence, ManifestDigest: manifestDigest,
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return ErrLocked
		}
		return err
	}
	return tx.Commit()
}

// ErrTargetStale is returned by Acquire when the signed sequence does not
// advance the durable floor. It is declared here to avoid importing the parent
// fence package and creating an adapter cycle.
var ErrTargetStale = errors.New("update target does not advance beyond accepted releases")

// ListOpen returns unreleased rows for one owner.
func (r *Repository) ListOpen(ctx context.Context, admittedBy string) ([]OpenFenceRow, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	rows, err := generated.New(r.db).ListOpenFencesByOwner(ctx, admittedBy)
	if err != nil {
		return nil, err
	}
	return openRows(rows), nil
}

// ListOpenAll returns every unreleased row for startup diagnostics.
func (r *Repository) ListOpenAll(ctx context.Context) ([]OpenFenceRow, error) {
	if err := r.requireDB(); err != nil {
		return nil, err
	}
	rows, err := generated.New(r.db).ListAllOpenFences(ctx)
	if err != nil {
		return nil, err
	}
	return openRowsAll(rows), nil
}

// LookupRow returns the durable row of one operation, released or not. It is a
// single primary-key read with no sweep semantics: recovery needs the exact
// stored identity and release state of its own operation to decide whether a
// fence close is still pending, already proven, or never required. A missing
// row is reported as found=false rather than as an error.
func (r *Repository) LookupRow(ctx context.Context, operationID string) (FenceRow, bool, error) {
	if err := r.requireDB(); err != nil {
		return FenceRow{}, false, err
	}
	const query = `SELECT op_id, admitted_by, sequence, manifest_digest, state, released_at IS NOT NULL
FROM embedded_update_locks WHERE op_id = $1`
	var row FenceRow
	var released bool
	err := r.db.QueryRowContext(ctx, query, operationID).Scan(
		&row.OperationID, &row.AdmittedBy, &row.Sequence, &row.ManifestDigest, &row.State, &released)
	if errors.Is(err, sql.ErrNoRows) {
		return FenceRow{}, false, nil
	}
	if err != nil {
		return FenceRow{}, false, err
	}
	row.Released = released
	return row, true, nil
}

func openRows(rows []generated.ListOpenFencesByOwnerRow) []OpenFenceRow {
	result := make([]OpenFenceRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, OpenFenceRow{
			OperationID:    row.OpID,
			AdmittedBy:     row.AdmittedBy,
			Sequence:       row.Sequence,
			ManifestDigest: row.ManifestDigest,
			State:          row.State,
		})
	}
	return result
}
func openRowsAll(rows []generated.ListAllOpenFencesRow) []OpenFenceRow {
	result := make([]OpenFenceRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, OpenFenceRow{
			OperationID:    row.OpID,
			AdmittedBy:     row.AdmittedBy,
			Sequence:       row.Sequence,
			ManifestDigest: row.ManifestDigest,
			State:          row.State,
		})
	}
	return result
}

// predicate matches. Already-released rows are classified by the recovery
// orchestrator before it calls this primitive; this method remains a strict
// open-state CAS and never treats a closed row as a successful transition.
func (r *Repository) ReconcileExact(ctx context.Context, operationID, admittedBy, manifestDigest string, expectedSequence int64, expectedState, storageState string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	result, err := generated.New(r.db).CloseFenceExact(ctx, generated.CloseFenceExactParams{
		StorageState: storageState, OperationID: operationID, AdmittedBy: admittedBy,
		ManifestDigest: manifestDigest, ExpectedSequence: expectedSequence, ExpectedState: expectedState,
	})
	if err != nil {
		return err
	}
	if transitionErr := checkTransitionResult(result); transitionErr == nil {
		return nil
	} else if !errors.Is(transitionErr, ErrNotFound) {
		return transitionErr
	}

	identity, lookupErr := generated.New(r.db).GetFenceIdentity(ctx, operationID)
	owner, sequence, digest, state, releasedAt := identity.AdmittedBy, identity.Sequence, identity.ManifestDigest, identity.State, identity.ReleasedAt
	if errors.Is(lookupErr, sql.ErrNoRows) {
		return ErrNotFound
	}
	if lookupErr != nil {
		return lookupErr
	}
	if owner != admittedBy {
		return ErrOwnerConflict
	}
	if digest != manifestDigest {
		return ErrManifestConflict
	}
	if sequence != expectedSequence {
		return ErrSequenceConflict
	}
	if releasedAt.Valid {
		return ErrStateConflict
	}
	if state != expectedState {
		return ErrStateConflict
	}
	return ErrStateConflict
}

// ReconcileExactCurrentState reads the actual open state before applying the
// same exact identity/CAS close. It is used by recovery after a pending handoff
// may already have been persisted.
func (r *Repository) ReconcileExactCurrentState(ctx context.Context, operationID, admittedBy, manifestDigest string, expectedSequence int64, storageState string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	identity, lookupErr := generated.New(r.db).GetFenceIdentity(ctx, operationID)
	owner, sequence, digest, state, releasedAt := identity.AdmittedBy, identity.Sequence, identity.ManifestDigest, identity.State, identity.ReleasedAt
	if errors.Is(lookupErr, sql.ErrNoRows) {
		return ErrNotFound
	}
	if lookupErr != nil {
		return lookupErr
	}
	if owner != admittedBy {
		return ErrOwnerConflict
	}
	if digest != manifestDigest {
		return ErrManifestConflict
	}
	if sequence != expectedSequence {
		return ErrSequenceConflict
	}
	if releasedAt.Valid {
		return ErrStateConflict
	}
	if state != "active" {
		return ErrStateConflict
	}
	return r.ReconcileExact(ctx, operationID, admittedBy, manifestDigest, expectedSequence, state, storageState)
}

// Recover closes a row and records the operator evidence on the closed row in
// one transaction. The parent package validates evidence and terminal semantics.
func (r *Repository) Recover(ctx context.Context, operationID, admittedBy, manifestDigest, expectedState, storageState, terminalState, evidence string) error {
	if err := r.requireDB(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := generated.New(tx).CloseFenceOrphanExact(ctx, generated.CloseFenceOrphanExactParams{
		StorageState: storageState, OperationID: operationID, AdmittedBy: admittedBy,
		ManifestDigest: manifestDigest, ExpectedState: expectedState,
		TerminalState: sql.NullString{String: terminalState, Valid: true},
		Evidence:      sql.NullString{String: evidence, Valid: true},
	})
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		identity, lookupErr := generated.New(tx).GetFenceIdentity(ctx, operationID)
		owner, digest, state, releasedAt := identity.AdmittedBy, identity.ManifestDigest, identity.State, identity.ReleasedAt
		if errors.Is(lookupErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if lookupErr != nil {
			return lookupErr
		}
		if owner != admittedBy {
			return ErrOwnerConflict
		}
		if digest != manifestDigest {
			return ErrManifestConflict
		}
		if !releasedAt.Valid || state != storageState {
			return ErrStateConflict
		}
	}
	return tx.Commit()
}

func checkTransitionResult(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	return ErrNotFound
}
