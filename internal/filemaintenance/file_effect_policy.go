package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/policyowner"

	"bansoko/internal/anime/animeconfig"
)

// FileEffectPolicyService owns the instance-scoped unclassified_route policy
// as a frozen snapshot (mirroring the parser policy owner): the singleton
// default is copied into each ManagedAnime row at subscribe time, and later
// default edits never change existing instances.
type FileEffectPolicyService struct {
	db  *sql.DB
	now func() time.Time
}

// NewFileEffectPolicyService wires the owner.
func NewFileEffectPolicyService(db *sql.DB) *FileEffectPolicyService {
	return &FileEffectPolicyService{db: db, now: time.Now}
}

var _ policyowner.SnapshotPort = (*FileEffectPolicyService)(nil)

func (s *FileEffectPolicyService) PolicyCapability() policyowner.Capability[UnclassifiedRoutePolicyValue] {
	return policyowner.Capability[UnclassifiedRoutePolicyValue]{
		Metadata: policyowner.Metadata{
			Kind:                        "file-effect",
			DefaultScope:                "global",
			InstanceVersionFloor:        1,
			SourceDefaultVersionFloor:   0,
			MissingDefaultSourceVersion: 0,
		},
		Validate: ValidateFileEffectPolicy,
	}
}

// DefaultPolicy returns the code-owned default when no singleton row exists.
func DefaultFileEffectPolicy() UnclassifiedRoutePolicyValue {
	return UnclassifiedRoutePolicyValueSkip
}

func ValidateFileEffectPolicy(value UnclassifiedRoutePolicyValue) error {
	if value != UnclassifiedRoutePolicyValueRoute && value != UnclassifiedRoutePolicyValueSkip {
		return fmt.Errorf("unclassified_route must be route or skip")
	}
	return nil
}

// GetDefault returns the singleton default (nil when absent).
func (s *FileEffectPolicyService) GetDefault(ctx context.Context) (*FileEffectPolicyDefaultView, error) {
	var (
		route     string
		version   int64
		updatedAt time.Time
	)
	err := s.db.QueryRowContext(ctx, `
SELECT value->>'unclassified_route', version, updated_at FROM global_config_sections WHERE section_name = 'file_effect_policy'`).
		Scan(&route, &version, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read file effect default: %w", err)
	}
	return &FileEffectPolicyDefaultView{
		UnclassifiedRoute: UnclassifiedRoutePolicyValue(route),
		UpdatedAt:         updatedAt,
		Version:           version,
	}, nil
}

// SaveDefault upserts the singleton default with CAS.
func (s *FileEffectPolicyService) SaveDefault(ctx context.Context, expectedVersion int64, value UnclassifiedRoutePolicyValue) (FileEffectPolicyDefaultView, error) {
	if err := ValidateFileEffectPolicy(value); err != nil {
		return FileEffectPolicyDefaultView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FileEffectPolicyDefaultView{}, err
	}
	defer tx.Rollback()
	var currentVersion int64
	err = tx.QueryRowContext(ctx, `
		SELECT version FROM global_config_sections WHERE section_name = 'file_effect_policy' FOR UPDATE`).Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		currentVersion = 0
	} else if err != nil {
		return FileEffectPolicyDefaultView{}, err
	}
	if currentVersion != expectedVersion {
		return FileEffectPolicyDefaultView{}, ErrPolicyConflict
	}
	var newVersion int64
	now := s.now().UTC()
	if currentVersion == 0 {
		err = tx.QueryRowContext(ctx, `
		INSERT INTO global_config_sections(section_name, schema_version, value, version, updated_at)
		VALUES ('file_effect_policy', 'file-effect-policy-v1', jsonb_build_object('unclassified_route', $1::text), 1, $2) RETURNING version`, string(value), now).Scan(&newVersion)
	} else {
		err = tx.QueryRowContext(ctx, `
		UPDATE global_config_sections
		SET value = jsonb_build_object('unclassified_route', $1::text), version = version + 1, updated_at = $2
		WHERE section_name = 'file_effect_policy' AND version = $3 RETURNING version`,
			string(value), now, expectedVersion).Scan(&newVersion)
	}
	if err != nil {
		return FileEffectPolicyDefaultView{}, err
	}
	if err := tx.Commit(); err != nil {
		return FileEffectPolicyDefaultView{}, err
	}
	return FileEffectPolicyDefaultView{UnclassifiedRoute: value, UpdatedAt: now, Version: newVersion}, nil
}

// CopyDefaultToInstanceTx freezes the current default into the instance row
// inside the caller's (subscribe) transaction. A missing singleton row falls
// back to the code default. Repeated calls are idempotent (ON CONFLICT DO
// NOTHING) so a replayed subscribe never overwrites an existing instance
// policy.
func (s *FileEffectPolicyService) CopyDefaultToInstanceTx(ctx context.Context, scope txscope.Tx, animeID string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("file effect policy snapshot transaction is nil")
	}
	var (
		route   string
		version int64
	)
	err := tx.QueryRowContext(ctx, `
SELECT value->>'unclassified_route', version
FROM global_config_sections WHERE section_name = 'file_effect_policy' FOR UPDATE`).
		Scan(&route, &version)
	if errors.Is(err, sql.ErrNoRows) {
		route = string(DefaultFileEffectPolicy())
		version = 0
	} else if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO anime_business_configs
(managed_anime_id, unclassified_route, file_effect_source_default_version, file_effect_version, updated_at)
VALUES ($1, $2, $3, 1, CURRENT_TIMESTAMP)
ON CONFLICT (managed_anime_id) DO UPDATE SET
    unclassified_route = EXCLUDED.unclassified_route, file_effect_source_default_version = EXCLUDED.file_effect_source_default_version,
    file_effect_version = 1, updated_at = CURRENT_TIMESTAMP
WHERE anime_business_configs.file_effect_version IS NULL`,
		animeID, route, version)
	return err
}

// GetInstance returns the frozen instance policy (nil when absent).
func (s *FileEffectPolicyService) GetInstance(ctx context.Context, animeID string) (*InstanceFileEffectPolicyView, error) {
	var (
		route     string
		sourceVer int64
		version   int64
		updatedAt time.Time
	)
	err := s.db.QueryRowContext(ctx, `
SELECT unclassified_route, file_effect_source_default_version, file_effect_version, updated_at
FROM anime_business_configs WHERE managed_anime_id = $1 AND file_effect_version IS NOT NULL`, animeID).
		Scan(&route, &sourceVer, &version, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read instance file effect policy: %w", err)
	}
	return &InstanceFileEffectPolicyView{
		ManagedAnimeID:       animeID,
		UnclassifiedRoute:    UnclassifiedRoutePolicyValue(route),
		SourceDefaultVersion: sourceVer,
		Version:              version,
		UpdatedAt:            updatedAt,
	}, nil
}

// SaveInstance replaces the frozen instance policy with CAS.
func (s *FileEffectPolicyService) SaveInstance(ctx context.Context, animeID string, expectedVersion int64, value UnclassifiedRoutePolicyValue) (InstanceFileEffectPolicyView, error) {
	if err := ValidateFileEffectPolicy(value); err != nil {
		return InstanceFileEffectPolicyView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InstanceFileEffectPolicyView{}, err
	}
	defer tx.Rollback()
	var (
		sourceDefaultVersion int64
		version              int64
		updatedAt            time.Time
	)
	err = tx.QueryRowContext(ctx, `
UPDATE anime_business_configs
SET unclassified_route = $1, file_effect_version = file_effect_version + 1, updated_at = $2
WHERE managed_anime_id = $3 AND file_effect_version = $4
RETURNING file_effect_source_default_version, file_effect_version, updated_at`, string(value), s.now().UTC(), animeID, expectedVersion).
		Scan(&sourceDefaultVersion, &version, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InstanceFileEffectPolicyView{}, ErrPolicyConflict
	}
	if err != nil {
		return InstanceFileEffectPolicyView{}, err
	}
	// Ruling 3: the merged row is the one configuration source, so the frozen
	// business snapshot is rewritten in the same transaction as the policy save.
	if _, err := animeconfig.SyncInstanceBusinessSnapshotTx(ctx, tx, animeID); err != nil {
		return InstanceFileEffectPolicyView{}, err
	}
	if err := tx.Commit(); err != nil {
		return InstanceFileEffectPolicyView{}, err
	}
	return InstanceFileEffectPolicyView{
		ManagedAnimeID:       animeID,
		SourceDefaultVersion: sourceDefaultVersion,
		UnclassifiedRoute:    value,
		Version:              version,
		UpdatedAt:            updatedAt,
	}, nil
}

func (s *FileEffectPolicyService) Capability() policyowner.Capability[UnclassifiedRoutePolicyValue] {
	return s.PolicyCapability()
}

func (s *FileEffectPolicyService) ReadDefault(ctx context.Context) (*policyowner.Default[UnclassifiedRoutePolicyValue], error) {
	value, err := s.GetDefault(ctx)
	if err != nil || value == nil {
		return nil, err
	}
	return &policyowner.Default[UnclassifiedRoutePolicyValue]{Policy: value.UnclassifiedRoute, UpdatedAt: value.UpdatedAt, Version: value.Version}, nil
}

func (s *FileEffectPolicyService) WriteDefault(ctx context.Context, input policyowner.DefaultSave[UnclassifiedRoutePolicyValue]) (policyowner.Default[UnclassifiedRoutePolicyValue], error) {
	value, err := s.SaveDefault(ctx, input.ExpectedVersion, input.Value)
	if err != nil {
		return policyowner.Default[UnclassifiedRoutePolicyValue]{}, err
	}
	return policyowner.Default[UnclassifiedRoutePolicyValue]{Policy: value.UnclassifiedRoute, UpdatedAt: value.UpdatedAt, Version: value.Version}, nil
}

func (s *FileEffectPolicyService) ReadInstance(ctx context.Context, animeID string) (*policyowner.Instance[UnclassifiedRoutePolicyValue], error) {
	value, err := s.GetInstance(ctx, animeID)
	if err != nil || value == nil {
		return nil, err
	}
	return &policyowner.Instance[UnclassifiedRoutePolicyValue]{ManagedAnimeID: value.ManagedAnimeID, Policy: value.UnclassifiedRoute, SourceDefaultVersion: value.SourceDefaultVersion, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

func (s *FileEffectPolicyService) WriteInstance(ctx context.Context, input policyowner.InstanceSave[UnclassifiedRoutePolicyValue]) (policyowner.Instance[UnclassifiedRoutePolicyValue], error) {
	value, err := s.SaveInstance(ctx, input.ManagedAnimeID, input.ExpectedVersion, input.Value)
	if err != nil {
		return policyowner.Instance[UnclassifiedRoutePolicyValue]{}, err
	}
	return policyowner.Instance[UnclassifiedRoutePolicyValue]{ManagedAnimeID: value.ManagedAnimeID, Policy: value.UnclassifiedRoute, SourceDefaultVersion: value.SourceDefaultVersion, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

var _ policyowner.Lifecycle[UnclassifiedRoutePolicyValue] = (*FileEffectPolicyService)(nil)
