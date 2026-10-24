package target

import (
	"context"
	"encoding/json"
	"errors"

	"bansoko/internal/persistence/txscope"
)

// Repository is the application-facing target persistence port. PostgreSQL
// SQL, database handles, and transaction lifecycle are owned by the adapter.
// Transactional writes receive only the caller-owned opaque tx scope.
type Repository interface {
	ReplaceSet(context.Context, txscope.Tx, Set) error
	UpsertObservation(context.Context, txscope.Tx, Observation) error
	ReadTargets(context.Context, string) ([]TargetRow, bool, error)
	PersistObservations(context.Context, []Observation) error
	ReadObservationVersions(context.Context, []string) (map[string]int64, error)
}

// Store persists frozen targets and latest observations. It is the
// application-facing facade over an adapter supplied by the composition root.
type Store struct {
	repository Repository
}

// NewStore wires the target application facade to a persistence adapter.
func NewStore(repository Repository) Store {
	return Store{repository: repository}
}

// Configured reports whether the facade carries a persistence adapter. The
// composition root asserts this at startup so a production boot cannot wire an
// empty target store that would only fail at admission time.
func (s Store) Configured() bool {
	return s.repository != nil
}

func (s Store) repositoryOrError() (Repository, error) {
	if s.repository == nil {
		return nil, errors.New("target store: repository is not configured")
	}
	return s.repository, nil
}

// ReplaceSet persists a frozen target set in the caller-owned transaction.
// The caller retains commit/rollback authority.
func (s Store) ReplaceSet(ctx context.Context, scope txscope.Tx, set Set) error {
	if set.ReleaseID == "" || len(set.Targets) == 0 {
		return ErrNoTargets
	}
	repository, err := s.repositoryOrError()
	if err != nil {
		return err
	}
	return repository.ReplaceSet(ctx, scope, set)
}

// UpsertObservation persists one latest observation in the caller-owned
// transaction. The caller retains commit/rollback authority.
func (s Store) UpsertObservation(ctx context.Context, scope txscope.Tx, observation Observation) error {
	if observation.TargetID == "" {
		return errors.New("target store: target id is required")
	}
	repository, err := s.repositoryOrError()
	if err != nil {
		return err
	}
	return repository.UpsertObservation(ctx, scope, observation)
}

// ReadTargets reads only frozen target facts for one managed-anime instance.
// The bool is false when the instance is missing/deleted.
func (s Store) ReadTargets(ctx context.Context, managedAnimeID string) ([]TargetRow, bool, error) {
	repository, err := s.repositoryOrError()
	if err != nil {
		return nil, false, err
	}
	return repository.ReadTargets(ctx, managedAnimeID)
}

// PersistObservations owns the observation transaction for the explicit scan
// operation. Filesystem probing is performed before this method is called.
func (s Store) PersistObservations(ctx context.Context, observations []Observation) error {
	repository, err := s.repositoryOrError()
	if err != nil {
		return err
	}
	return repository.PersistObservations(ctx, observations)
}

// ReadObservationVersions returns persisted versions for a completed scan.
func (s Store) ReadObservationVersions(ctx context.Context, targetIDs []string) (map[string]int64, error) {
	repository, err := s.repositoryOrError()
	if err != nil {
		return nil, err
	}
	return repository.ReadObservationVersions(ctx, targetIDs)
}

// MarshalSet is used by frozen input snapshots and keeps the target contract
// stable without exposing a second ad-hoc JSON shape to callers.
func MarshalSet(set Set) ([]byte, error) { return json.Marshal(set) }

// TargetRow is the narrow read shape used by Scanner.
type TargetRow struct {
	ID                string
	TargetPath        string
	ExpectedSizeBytes int64
}
