package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrRunNotFound is returned by the store when no run exists for the id.
var ErrRunNotFound = errors.New("deployment recovery run not found")

// PGStore persists recovery runs in deployment_recovery_runs with CAS
// versioning. A missing table (pre-030 backup) surfaces as
// ErrCheckpointSchemaMissing so the CLI fails closed.
type PGStore struct {
	db *sql.DB
}

// NewPGStore wires the store to the deployment database.
func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

func relationMissing(err error) bool {
	// PostgreSQL error code 42P01 = undefined_table.
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() == "42P01" {
		return true
	}
	return false
}

// Load reads one run by operation id.
func (s *PGStore) Load(ctx context.Context, id string) (*Run, error) {
	var (
		run           Run
		inventoryRaw  []byte
		checkpointRaw []byte
		version       int64
		started, upd  time.Time
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, window_id, old_bundle_identity, new_bundle_identity, backup_identity,
       schema_identity, build_identity, registry_inventory, checkpoint, state,
       version, started_at, updated_at
FROM deployment_recovery_runs WHERE id = $1`, id).
		Scan(&run.ID, &run.WindowID, &run.OldBundleIdentity, &run.NewBundleIdentity,
			&run.BackupIdentity, &run.SchemaIdentity, &run.BuildIdentity,
			&inventoryRaw, &checkpointRaw, &run.State, &version, &started, &upd)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		if relationMissing(err) {
			return nil, &ErrCheckpointSchemaMissing{}
		}
		return nil, fmt.Errorf("load deployment recovery run: %w", err)
	}
	run.Version = version
	run.StartedAt = started
	run.UpdatedAt = upd
	if err := json.Unmarshal(inventoryRaw, &run.RegistryInventory); err != nil {
		return nil, fmt.Errorf("decode registry inventory: %w", err)
	}
	checkpoint, err := UnmarshalCheckpoint(checkpointRaw)
	if err != nil {
		return nil, err
	}
	run.Checkpoint = checkpoint
	return &run, nil
}

// Create inserts a fresh run.
func (s *PGStore) Create(ctx context.Context, run *Run) error {
	inventory, err := json.Marshal(run.RegistryInventory)
	if err != nil {
		return fmt.Errorf("encode registry inventory: %w", err)
	}
	checkpoint, err := run.Checkpoint.MarshalJSON()
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	if run.Version == 0 {
		run.Version = 1
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO deployment_recovery_runs
(id, window_id, old_bundle_identity, new_bundle_identity, backup_identity,
 schema_identity, build_identity, registry_inventory, checkpoint, state, version, started_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		run.ID, run.WindowID, run.OldBundleIdentity, run.NewBundleIdentity,
		run.BackupIdentity, run.SchemaIdentity, run.BuildIdentity,
		inventory, checkpoint, run.State, run.Version, run.StartedAt, run.UpdatedAt); err != nil {
		if relationMissing(err) {
			return &ErrCheckpointSchemaMissing{}
		}
		return fmt.Errorf("create deployment recovery run: %w", err)
	}
	return nil
}

// Update applies the mutator with optimistic concurrency and returns the new
// version.
func (s *PGStore) Update(ctx context.Context, id string, expectedVersion int64, mutate func(*Run)) (int64, error) {
	run, err := s.Load(ctx, id)
	if err != nil {
		return 0, err
	}
	if run.Version != expectedVersion {
		return 0, fmt.Errorf("deployment recovery run version conflict (current %d, expected %d)", run.Version, expectedVersion)
	}
	mutate(run)
	checkpoint, err := run.Checkpoint.MarshalJSON()
	if err != nil {
		return 0, fmt.Errorf("encode checkpoint: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE deployment_recovery_runs
SET checkpoint = $2, state = $3, updated_at = $4, version = version + 1
WHERE id = $1 AND version = $5`,
		id, checkpoint, run.State, run.UpdatedAt, expectedVersion)
	if err != nil {
		if relationMissing(err) {
			return 0, &ErrCheckpointSchemaMissing{}
		}
		return 0, fmt.Errorf("update deployment recovery run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, fmt.Errorf("deployment recovery run CAS conflict on %s (affected %d)", id, affected)
	}
	return expectedVersion + 1, nil
}

// Latest returns the most recently updated run, or nil when no run exists.
func (s *PGStore) Latest(ctx context.Context) (*Run, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
SELECT id FROM deployment_recovery_runs ORDER BY updated_at DESC LIMIT 1`).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if relationMissing(err) {
			return nil, &ErrCheckpointSchemaMissing{}
		}
		return nil, fmt.Errorf("load latest deployment recovery run: %w", err)
	}
	return s.Load(ctx, id)
}
