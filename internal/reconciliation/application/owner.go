package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ScanState is the durable reconciliation scan owner state machine.
type ScanState string

const (
	ScanIdle      ScanState = "idle"
	ScanRunning   ScanState = "running"
	ScanCompleted ScanState = "completed"
	ScanFailed    ScanState = "failed"
	ScanCanceled  ScanState = "canceled"
)

// ScanOwner is one reconciliation_scans row with CAS helpers. Completed
// generations are immutable: downstream effects may only consume a
// generation whose owner row is completed (GenerationUsable).
type ScanOwner struct {
	ManagedAnimeID   string
	State            ScanState
	Generation       int64
	ScanScope        json.RawMessage
	Progress         json.RawMessage
	CapacityStats    json.RawMessage
	InputFingerprint string
	FailureCode      string
	FailureReason    string
	Version          int64
	StartedAt        *time.Time
	CompletedAt      *time.Time
	UpdatedAt        time.Time
}

// ErrScanInProgress is returned when an anime already has a running scan.
var ErrScanInProgress = errors.New("reconciliation scan already in progress")

// ErrGenerationImmutable is returned when writing observations for a
// completed generation is attempted.
var ErrGenerationImmutable = errors.New("completed reconciliation generation is immutable")

// loadScanOwner reads the owner row (no row -> nil owner).
func loadScanOwner(ctx context.Context, db *sql.DB, animeID string) (*ScanOwner, error) {
	return readScanOwner(ctx, db, animeID, false)
}

type scanOwnerQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readScanOwner(ctx context.Context, queryer scanOwnerQuerier, animeID string, forUpdate bool) (*ScanOwner, error) {
	query := `
SELECT managed_anime_id, state, generation, scan_scope, progress, capacity_stats,
       input_fingerprint, COALESCE(failure_code, ''), COALESCE(failure_reason, ''),
       version, started_at, completed_at, updated_at
FROM reconciliation_scans WHERE managed_anime_id = $1`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var (
		owner              ScanOwner
		started, completed sql.NullTime
	)
	err := queryer.QueryRowContext(ctx, query, animeID).
		Scan(&owner.ManagedAnimeID, &owner.State, &owner.Generation, &owner.ScanScope,
			&owner.Progress, &owner.CapacityStats, &owner.InputFingerprint,
			&owner.FailureCode, &owner.FailureReason, &owner.Version,
			&started, &completed, &owner.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load reconciliation scan owner: %w", err)
	}
	if started.Valid {
		owner.StartedAt = &started.Time
	}
	if completed.Valid {
		owner.CompletedAt = &completed.Time
	}
	return &owner, nil
}

// startScanOwnerTx advances the owner into running using the caller-owned
// transaction. Existing rows are locked before the state/generation CAS, so a
// successful return can be followed by WorkItem admission and committed as one
// atomic owner boundary. Idempotent completed replays return unchanged.
func startScanOwnerTx(ctx context.Context, tx *sql.Tx, now time.Time, animeID string, scope json.RawMessage, inputFingerprint string) (*ScanOwner, bool, error) {
	if tx == nil {
		return nil, false, errors.New("start reconciliation scan requires a transaction")
	}
	existing, err := readScanOwner(ctx, tx, animeID, true)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.State == ScanRunning {
			return existing, false, ErrScanInProgress
		}
		if existing.State == ScanCompleted && existing.InputFingerprint == inputFingerprint {
			return existing, false, nil // idempotent replay of a completed scan
		}
	}
	progress, _ := json.Marshal(map[string]any{"last_committed_batch": -1, "scanned_files": 0, "hashed_files": 0})
	stats, _ := json.Marshal(map[string]any{"files": 0, "bytes": 0})
	newGeneration := int64(1)
	version := int64(1)
	if existing != nil {
		newGeneration = existing.Generation + 1
		version = existing.Version + 1
	}
	owner := &ScanOwner{
		ManagedAnimeID:   animeID,
		State:            ScanRunning,
		Generation:       newGeneration,
		ScanScope:        scope,
		Progress:         progress,
		CapacityStats:    stats,
		InputFingerprint: inputFingerprint,
		Version:          version,
		StartedAt:        &now,
		UpdatedAt:        now,
	}
	if existing != nil {
		result, err := tx.ExecContext(ctx, `
UPDATE reconciliation_scans
SET state = 'running', generation = $3, scan_scope = $4, progress = $5, capacity_stats = $6,
    input_fingerprint = $7, failure_code = NULL, failure_reason = NULL,
    version = version + 1, started_at = $8, completed_at = NULL, updated_at = $9
WHERE managed_anime_id = $1 AND version = $2`,
			animeID, existing.Version, newGeneration, scope, progress, stats, inputFingerprint, now, now)
		if err != nil {
			return nil, false, fmt.Errorf("start reconciliation scan: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, false, err
		}
		if affected != 1 {
			return nil, false, fmt.Errorf("reconciliation scan changed concurrently (anime %s version %d)", animeID, existing.Version)
		}
		return owner, true, nil
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO reconciliation_scans
(managed_anime_id, state, generation, scan_scope, progress, capacity_stats, input_fingerprint, version, started_at, updated_at)
VALUES ($1, 'running', $2, $3, $4, $5, $6, 1, $7, $8)`,
		animeID, newGeneration, scope, progress, stats, inputFingerprint, now, now)
	if err != nil {
		return nil, false, fmt.Errorf("create reconciliation scan: %w", err)
	}
	return owner, true, nil
}

// transaction and CAS-advances the owner version.
func advanceScanBatch(ctx context.Context, tx *sql.Tx, animeID string, generation, expectedVersion int64, lastBatch int, scanned, hashed, bytes int64) error {
	progress, _ := json.Marshal(map[string]any{
		"last_committed_batch": lastBatch, "scanned_files": scanned, "hashed_files": hashed,
	})
	result, err := tx.ExecContext(ctx, `
UPDATE reconciliation_scans
SET progress = $3, capacity_stats = $4, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND generation = $2 AND version = $5 AND state = 'running'`,
		animeID, generation, progress, statsJSON(bytes), expectedVersion)
	if err != nil {
		return fmt.Errorf("advance reconciliation batch: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("reconciliation scan changed concurrently (anime %s gen %d ver %d)", animeID, generation, expectedVersion)
	}
	return nil
}

func statsJSON(bytes int64) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"files": 0, "bytes": bytes})
	return raw
}

// completeScanOwner freezes the generation as immutable.
func completeScanOwner(ctx context.Context, tx *sql.Tx, animeID string, generation, expectedVersion int64, scanned, hashed, bytes int64) error {
	progress, _ := json.Marshal(map[string]any{
		"last_committed_batch": "done", "scanned_files": scanned, "hashed_files": hashed,
	})
	result, err := tx.ExecContext(ctx, `
UPDATE reconciliation_scans
SET state = 'completed', progress = $3, capacity_stats = $4, version = version + 1,
    completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND generation = $2 AND version = $5 AND state = 'running'`,
		animeID, generation, progress, statsJSON(bytes), expectedVersion)
	if err != nil {
		return fmt.Errorf("complete reconciliation scan: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("reconciliation scan changed concurrently on completion (anime %s gen %d ver %d)", animeID, generation, expectedVersion)
	}
	return nil
}

// failScanOwner marks the scan failed with a bounded reason.
func failScanOwner(ctx context.Context, db *sql.DB, animeID string, generation, expectedVersion int64, code, reason string) error {
	result, err := db.ExecContext(ctx, `
UPDATE reconciliation_scans
SET state = 'failed', failure_code = $3, failure_reason = $4, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND generation = $2 AND version = $5 AND state = 'running'`,
		animeID, generation, code, reason, expectedVersion)
	if err != nil {
		return fmt.Errorf("fail reconciliation scan: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("reconciliation scan changed concurrently on failure (anime %s gen %d ver %d)", animeID, generation, expectedVersion)
	}
	return nil
}
