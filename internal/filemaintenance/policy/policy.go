// Package policy owns the FileMaintenance policy row and its CAS
// persistence. It has no dependency on the aggregate FileMaintenance service,
// so policy callers can depend on this owner without importing planner or
// executor concerns.
package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// PolicyRow is one filemaintenance_policies row.
type PolicyRow struct {
	ScopeKey            string
	Enabled             bool
	ScanIntervalSeconds *int
	AlignmentEnabled    *bool
	Safety              json.RawMessage
	Version             int64
	UpdatedAt           time.Time
}

// EffectivePolicy merges global + anime override (NULL = inherit).
type EffectivePolicy struct {
	Enabled             bool
	ScanIntervalSeconds *int
	AlignmentEnabled    *bool
	Safety              json.RawMessage
	PolicyVersion       int64
	Source              string // global | override
}

// PolicyService is the CAS policy owner.
type PolicyService struct {
	db  *sql.DB
	now func() time.Time
}

// NewPolicyService wires the policy owner.
func NewPolicyService(db *sql.DB) *PolicyService {
	return &PolicyService{db: db, now: time.Now}
}

// ErrPolicyConflict is the explicit CAS conflict (client must reload).
var ErrPolicyConflict = errors.New("file maintenance policy version conflict")

// GlobalScope is the global policy scope key.
const GlobalScope = "global"

// SavePolicy upserts one scope with CAS. actor and channel are retained for
// the caller's audit vocabulary but no longer persisted.
func (s *PolicyService) SavePolicy(ctx context.Context, scopeKey string, expectedVersion int64, input PolicyRow, actor, channel string) (PolicyRow, error) {
	safety := input.Safety
	if len(safety) == 0 {
		safety = json.RawMessage(`{"max_operations":100}`)
	}
	if scopeKey == "" {
		scopeKey = GlobalScope
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PolicyRow{}, err
	}
	defer tx.Rollback()
	var currentVersion int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM filemaintenance_policies WHERE scope_key = $1 FOR UPDATE`, scopeKey).Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		currentVersion = 0
	} else if err != nil {
		return PolicyRow{}, err
	}
	if currentVersion != expectedVersion {
		return PolicyRow{}, ErrPolicyConflict
	}
	now := s.now().UTC()
	var newVersion int64
	if currentVersion == 0 {
		err = tx.QueryRowContext(ctx, `
INSERT INTO filemaintenance_policies
(scope_key, enabled, scan_interval_seconds, alignment_enabled, safety, version, updated_at)
VALUES ($1, $2, $3, $4, $5, 1, $6)
RETURNING version`, scopeKey, input.Enabled, input.ScanIntervalSeconds, input.AlignmentEnabled,
			safety, now).Scan(&newVersion)
	} else {
		err = tx.QueryRowContext(ctx, `
UPDATE filemaintenance_policies
SET enabled = $2, scan_interval_seconds = $3, alignment_enabled = $4, safety = $5,
    version = version + 1, updated_at = $6
WHERE scope_key = $1 AND version = $7
RETURNING version`, scopeKey, input.Enabled, input.ScanIntervalSeconds, input.AlignmentEnabled,
			safety, now, expectedVersion).Scan(&newVersion)
	}
	if err != nil {
		return PolicyRow{}, err
	}
	if err := tx.Commit(); err != nil {
		return PolicyRow{}, err
	}
	return PolicyRow{ScopeKey: scopeKey, Enabled: input.Enabled, ScanIntervalSeconds: input.ScanIntervalSeconds,
		AlignmentEnabled: input.AlignmentEnabled, Safety: safety, Version: newVersion, UpdatedAt: now}, nil
}

// GetPolicy returns one scope (nil when absent).
func (s *PolicyService) GetPolicy(ctx context.Context, scopeKey string) (*PolicyRow, error) {
	var (
		row       PolicyRow
		interval  sql.NullInt64
		alignment sql.NullBool
	)
	err := s.db.QueryRowContext(ctx, `
SELECT scope_key, enabled, scan_interval_seconds, alignment_enabled, safety, version, updated_at
FROM filemaintenance_policies WHERE scope_key = $1`, scopeKey).
		Scan(&row.ScopeKey, &row.Enabled, &interval, &alignment, &row.Safety, &row.Version, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if interval.Valid {
		v := int(interval.Int64)
		row.ScanIntervalSeconds = &v
	}
	if alignment.Valid {
		row.AlignmentEnabled = &alignment.Bool
	}
	return &row, nil
}

// GetEffective merges global + override for one anime.
func (s *PolicyService) GetEffective(ctx context.Context, animeID string) (EffectivePolicy, error) {
	global, err := s.GetPolicy(ctx, GlobalScope)
	if err != nil {
		return EffectivePolicy{}, err
	}
	override, err := s.GetPolicy(ctx, "anime:"+animeID)
	if err != nil {
		return EffectivePolicy{}, err
	}
	effective := EffectivePolicy{PolicyVersion: 0, Source: "global"}
	if global != nil {
		effective.Enabled = global.Enabled
		effective.ScanIntervalSeconds = global.ScanIntervalSeconds
		effective.AlignmentEnabled = global.AlignmentEnabled
		effective.Safety = global.Safety
		effective.PolicyVersion = global.Version
	}
	if override != nil {
		// Explicit override values replace the owned field (NULL = inherit).
		effective.Enabled = override.Enabled
		if override.ScanIntervalSeconds != nil {
			effective.ScanIntervalSeconds = override.ScanIntervalSeconds
		}
		if override.AlignmentEnabled != nil {
			effective.AlignmentEnabled = override.AlignmentEnabled
		}
		if len(override.Safety) > 0 {
			effective.Safety = override.Safety
		}
		effective.PolicyVersion = override.Version
		effective.Source = "override"
	}
	return effective, nil
}
