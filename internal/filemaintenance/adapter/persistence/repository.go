// Package persistence implements the PostgreSQL adapter for the cleanup owner.
// It owns cleanup-operation SQL while the filemaintenance application service
// retains the typed lifecycle and transaction orchestration.
package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sqlc-dev/pqtype"

	generated "bansoko/internal/filemaintenance/adapter/persistence/generated"
	"bansoko/internal/persistence/txscope"
)

// Repository owns PostgreSQL access for cleanup_operations. Methods that
// receive a transaction scope never begin, commit, or roll back it; the caller
// retains ownership of the surrounding transaction.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the PostgreSQL cleanup-owner adapter.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// WithTx is used only by application-facing operations whose compatibility API
// historically opened its own transaction (for example PersistAttempt and
// Transition). The callback still receives the opaque caller scope, and all
// owner SQL remains in this adapter.
func (r *Repository) WithTx(ctx context.Context, fn func(txscope.Tx) error) error {
	if fn == nil {
		return errors.New("cleanup transaction callback is nil")
	}
	if r == nil || r.db == nil {
		return errors.New("cleanup database is nil")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(txscope.Wrap(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// CleanupWorkItem is the minimal recovery identity/state/version row. It keeps
// WorkItem persistence facts in the cleanup adapter boundary.
type CleanupWorkItem struct {
	ID                   string
	OwnerType            string
	OwnerID              string
	Kind                 string
	State                string
	ExpectedOwnerVersion int64
}

// LockCleanupWorkItemTx locks the first live cleanup WorkItem in the caller's
// transaction. It never begins, commits, or rolls back the caller's scope.
func (r *Repository) LockCleanupWorkItemTx(ctx context.Context, scope txscope.Tx, cleanupID, kind string) (CleanupWorkItem, error) {
	tx, err := transaction(scope, "lock cleanup work item")
	if err != nil {
		return CleanupWorkItem{}, err
	}
	row, err := generated.New(tx).LockCleanupWorkItem(ctx, generated.LockCleanupWorkItemParams{
		CleanupID: cleanupID,
		Kind:      kind,
	})
	if err != nil {
		return CleanupWorkItem{}, err
	}
	return CleanupWorkItem{
		ID:                   row.ID,
		OwnerType:            row.OwnerType,
		OwnerID:              row.OwnerID,
		Kind:                 row.Kind,
		State:                row.State,
		ExpectedOwnerVersion: row.ExpectedOwnerVersion,
	}, nil
}

// OperationInput is the persistence-neutral frozen cleanup admission row.
type OperationInput struct {
	ID                string
	IdempotencyKey    string
	PlacementID       string
	ManagedAnimeID    string
	Coverage          json.RawMessage
	ReplacementRef    string
	RetentionMode     string
	SourcePath        string
	SourceFingerprint string
	// State is the admission state: admitted, or skipped when the loser's
	// identity is already known to be unverifiable (zero effect).
	State         string
	FailureCode   string
	Reason        string
	CorrelationID string
	CausationType string
	CausationID   string
}

// Operation is the typed cleanup owner row returned by this adapter. State is
// intentionally a string so this package does not import the application owner
// package and create an import cycle.
type Operation struct {
	ID                string
	IdempotencyKey    string
	PlacementID       string
	ManagedAnimeID    string
	Coverage          []string
	ReplacementRef    string
	RetentionMode     string
	SourcePath        string
	SourceFingerprint string
	State             string
	Attempt           json.RawMessage
	FailureCode       string
	Reason            string
	CorrelationID     string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Insert persists one cleanup operation. Admission historically commits this
// owner row before the optional WorkItem enqueue transaction, so this method
// deliberately preserves that compatibility behavior.
func (r *Repository) Insert(ctx context.Context, input OperationInput) error {
	if r == nil || r.db == nil {
		return errors.New("cleanup database is nil")
	}
	return r.insert(ctx, r.db, input)
}

// InsertTx persists one cleanup operation in the caller-owned transaction.
func (r *Repository) InsertTx(ctx context.Context, scope txscope.Tx, input OperationInput) error {
	tx, err := transaction(scope, "insert cleanup operation")
	if err != nil {
		return err
	}
	return r.insert(ctx, tx, input)
}

func (r *Repository) insert(ctx context.Context, execer generated.DBTX, input OperationInput) error {
	if err := generated.New(execer).InsertCleanupOperation(ctx, generated.InsertCleanupOperationParams{
		ID:                input.ID,
		IdempotencyKey:    input.IdempotencyKey,
		PlacementID:       input.PlacementID,
		ManagedAnimeID:    input.ManagedAnimeID,
		Coverage:          input.Coverage,
		ReplacementRef:    input.ReplacementRef,
		RetentionMode:     input.RetentionMode,
		SourcePath:        input.SourcePath,
		SourceFingerprint: input.SourceFingerprint,
		State:             admissionState(input.State),
		FailureCode:       nullableText(input.FailureCode),
		Reason:            nullableText(input.Reason),
		CorrelationID:     nullableText(input.CorrelationID),
		CausationType:     nullableText(input.CausationType),
		CausationID:       nullableText(input.CausationID),
	}); err != nil {
		return fmt.Errorf("admit cleanup: %w", err)
	}
	return nil
}

// LockVersionTx locks one cleanup owner and returns its current version.
func (r *Repository) LockVersionTx(ctx context.Context, scope txscope.Tx, cleanupID string) (int64, error) {
	tx, err := transaction(scope, "lock cleanup owner")
	if err != nil {
		return 0, err
	}
	version, err := generated.New(tx).LockCleanupOwnerVersion(ctx, cleanupID)
	if err != nil {
		return 0, err
	}
	return version, nil
}

// LockTx locks and loads one cleanup owner row in the caller's transaction.
func (r *Repository) LockTx(ctx context.Context, scope txscope.Tx, cleanupID string) (*Operation, error) {
	tx, err := transaction(scope, "lock cleanup owner")
	if err != nil {
		return nil, err
	}
	row, err := generated.New(tx).LockCleanupOperation(ctx, cleanupID)
	if err != nil {
		return nil, err
	}
	return operationFromFullRow(row.ID, row.IdempotencyKey, row.PlacementID, row.ManagedAnimeID,
		row.CoverageKeys, row.ReplacementRef, row.RetentionMode, row.SourcePath,
		row.SourceFingerprint, row.State,
		row.Attempt.RawMessage, row.Attempt.Valid, row.FailureCode, row.Reason, row.CorrelationID,
		row.Version, row.CreatedAt, row.UpdatedAt)
}

// PersistAttemptTx writes the durable pre-effect checkpoint with owner CAS.
func (r *Repository) PersistAttemptTx(ctx context.Context, scope txscope.Tx, cleanupID string, expectedVersion int64, attempt json.RawMessage) (int64, error) {
	tx, err := transaction(scope, "persist cleanup attempt")
	if err != nil {
		return 0, err
	}
	version, err := generated.New(tx).PersistCleanupAttempt(ctx, generated.PersistCleanupAttemptParams{
		Attempt:         nullableRawMessage(attempt),
		CleanupID:       cleanupID,
		ExpectedVersion: expectedVersion,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("cleanup %s changed concurrently (version %d)", cleanupID, expectedVersion)
	}
	if err != nil {
		return 0, err
	}
	return version, nil
}

// TransitionTx moves the cleanup owner state with expected-version CAS.
func (r *Repository) TransitionTx(ctx context.Context, scope txscope.Tx, cleanupID string, expectedVersion int64, state, code, reason string) (int64, error) {
	tx, err := transaction(scope, "transition cleanup owner")
	if err != nil {
		return 0, err
	}
	version, err := generated.New(tx).TransitionCleanup(ctx, generated.TransitionCleanupParams{
		State:           state,
		FailureCode:     code,
		Reason:          reason,
		CleanupID:       cleanupID,
		ExpectedVersion: expectedVersion,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("cleanup %s changed concurrently on transition (version %d)", cleanupID, expectedVersion)
	}
	if err != nil {
		return 0, err
	}
	return version, nil
}

// PathOwnedTx reports whether an active placement or an active companion
// now holds path. A deletion never removes such a path.
func (r *Repository) PathOwnedTx(ctx context.Context, scope txscope.Tx, path string) (bool, error) {
	tx, err := transaction(scope, "read cleanup path owner")
	if err != nil {
		return false, err
	}
	owned, err := generated.New(tx).CleanupPathOwned(ctx, path)
	if err != nil {
		return false, err
	}
	return owned.Valid && owned.Bool, nil
}

// Get returns one cleanup owner row (nil when absent).
func (r *Repository) Get(ctx context.Context, cleanupID string) (*Operation, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("cleanup database is nil")
	}
	row, err := generated.New(r.db).GetCleanupOperation(ctx, cleanupID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return operationFromFullRow(row.ID, row.IdempotencyKey, row.PlacementID, row.ManagedAnimeID,
		row.CoverageKeys, row.ReplacementRef, row.RetentionMode, row.SourcePath,
		row.SourceFingerprint, row.State,
		row.Attempt.RawMessage, row.Attempt.Valid, row.FailureCode, row.Reason, row.CorrelationID,
		row.Version, row.CreatedAt, row.UpdatedAt)
}

// ListPending returns cleanup owners whose filesystem effects may still need
// recovery evidence.
func (r *Repository) ListPending(ctx context.Context) ([]Operation, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("cleanup database is nil")
	}
	rows, err := generated.New(r.db).ListPendingCleanupOperations(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Operation, 0, len(rows))
	for _, row := range rows {
		operation, err := operationFromFullRow(row.ID, row.IdempotencyKey, row.PlacementID, row.ManagedAnimeID,
			row.CoverageKeys, row.ReplacementRef, row.RetentionMode, row.SourcePath,
			row.SourceFingerprint, row.State,
			row.Attempt.RawMessage, row.Attempt.Valid, row.FailureCode, row.Reason, row.CorrelationID,
			row.Version, row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, *operation)
	}
	return result, nil
}

// ListRow is the bounded read shape used by the application owner view.
type ListRow struct {
	ID                string
	PlacementID       string
	ManagedAnimeID    string
	ReplacementRef    string
	RetentionMode     string
	SourcePath        string
	SourceFingerprint string
	State             string
	FailureCode       *string
	Reason            *string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// List returns cleanup owner rows for one anime, newest first.
func (r *Repository) List(ctx context.Context, animeID string, limit, offset int) ([]ListRow, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("cleanup database is nil")
	}
	rows, err := generated.New(r.db).ListCleanupOperations(ctx, generated.ListCleanupOperationsParams{
		AnimeID: animeID, LimitCount: int32(limit), OffsetCount: int32(offset),
	})
	if err != nil {
		return nil, err
	}
	result := make([]ListRow, 0, len(rows))
	for _, generatedRow := range rows {
		row := ListRow{
			ID:                generatedRow.ID,
			PlacementID:       generatedRow.PlacementID,
			ManagedAnimeID:    generatedRow.ManagedAnimeID,
			ReplacementRef:    generatedRow.ReplacementRef,
			RetentionMode:     generatedRow.RetentionMode,
			SourcePath:        generatedRow.SourcePath,
			SourceFingerprint: generatedRow.SourceFingerprint,
			State:             generatedRow.State,
			Version:           generatedRow.Version,
			CreatedAt:         generatedRow.CreatedAt,
			UpdatedAt:         generatedRow.UpdatedAt,
		}
		if generatedRow.FailureCode != "" {
			value := generatedRow.FailureCode
			row.FailureCode = &value
		}
		if generatedRow.Reason != "" {
			value := generatedRow.Reason
			row.Reason = &value
		}
		result = append(result, row)
	}
	return result, nil
}

// SubscriptionIDForAnimeTx keeps the activity transaction on the same caller
// scope without exposing owner SQL to the application service.
func (r *Repository) SubscriptionIDForAnimeTx(ctx context.Context, scope txscope.Tx, animeID string) (string, error) {
	tx, err := transaction(scope, "read cleanup activity subscription")
	if err != nil {
		return "", err
	}
	return generated.New(tx).SubscriptionIDForAnime(ctx, animeID)
}

// NullString returns a valid SQL string for a non-empty adapter row field.
func NullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func transaction(scope txscope.Tx, operation string) (*sql.Tx, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return nil, fmt.Errorf("%s requires a transaction", operation)
	}
	return tx, nil
}

func nullableText(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// admissionState defaults an unset admission state to admitted.
func admissionState(state string) string {
	if state == "" {
		return "admitted"
	}
	return state
}

func nullableRawMessage(value json.RawMessage) pqtype.NullRawMessage {
	return pqtype.NullRawMessage{RawMessage: value, Valid: value != nil}
}

func operationFromFullRow(
	id, idempotencyKey, placementID, managedAnimeID string,
	coverageRaw json.RawMessage, replacementRef string, retentionMode, sourcePath, sourceFingerprint string,
	state string,
	attemptRaw json.RawMessage, attemptValid bool, failureCode, reason, correlation sql.NullString,
	version int64, createdAt, updatedAt time.Time,
) (*Operation, error) {
	operation := Operation{
		ID:                id,
		IdempotencyKey:    idempotencyKey,
		PlacementID:       placementID,
		ManagedAnimeID:    managedAnimeID,
		ReplacementRef:    replacementRef,
		RetentionMode:     retentionMode,
		SourcePath:        sourcePath,
		SourceFingerprint: sourceFingerprint,
		State:             state,
		Version:           version,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
	}
	if err := json.Unmarshal(coverageRaw, &operation.Coverage); err != nil {
		return nil, fmt.Errorf("decode cleanup coverage: %w", err)
	}
	if attemptValid {
		operation.Attempt = append(json.RawMessage(nil), attemptRaw...)
	}
	if failureCode.Valid {
		operation.FailureCode = failureCode.String
	}
	if reason.Valid {
		operation.Reason = reason.String
	}
	if correlation.Valid {
		operation.CorrelationID = correlation.String
	}
	return &operation, nil
}
