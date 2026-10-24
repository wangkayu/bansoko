// Package parserpolicy owns the frozen per-instance parser policy: the
// selected parser_kind. The singleton parser_policy_defaults row is only the
// creation template for future instances; existing instances never re-inherit
// from it at runtime. The policy carries no episode offset: offsets live only
// on the RSS link (rss_sources.episode_offset) and the one-shot input.
package parserpolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/anime/animeconfig"
	"bansoko/internal/apperrors"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/policyowner"
)

// Policy is the complete parser policy value. Kind must name a registered
// parser adapter.
type Policy struct {
	Kind parser.ParserKind
}

// DefaultPolicy returns the versioned code default: the vendored reltitle
// parser. regex stays registered and selectable.
func DefaultPolicy() Policy {
	return Policy{Kind: parser.ParserReltitle}
}

// policyInvalid builds the typed 422 POLICY_INVALID error.
func policyInvalid(detail string) error {
	return &apperrors.Business{Status: 422, Code: "POLICY_INVALID", Message: detail}
}

// Validate rejects unregistered parser kinds before any write. Unregistered kinds fail closed: only registered adapters may be
// stored as an instance policy.
func Validate(policy Policy) error {
	if strings.TrimSpace(string(policy.Kind)) == "" {
		return policyInvalid("parser_kind is required")
	}
	if !parser.IsRegistered(policy.Kind) {
		return policyInvalid("parser_kind must name a registered parser")
	}
	return nil
}

// Default is the parser_policy_defaults row: the creation template only.
type Default = policyowner.Default[Policy]

// Instance is the instance_parser_policies row: one complete frozen
// parser policy per ManagedAnime.
type Instance = policyowner.Instance[Policy]

// ReleaseReevaluationScheduler enqueues a release_evaluate WorkItem for every
// live, unsubmitted Release of one instance. Implemented by the RSS owner and
// invoked inside the parser-policy owner's CAS transaction, so a parser kind or
// profile change cannot commit without its reevaluation being scheduled.
type ReleaseReevaluationScheduler interface {
	ScheduleUnsubmittedReleaseReevaluationTx(ctx context.Context, scope txscope.Tx, managedAnimeID string) error
}

// Service owns the parser policy singleton and per-instance snapshots. It
// never merges the global value into an existing instance at runtime and
// never touches Releases, WorkItems, or filesystem effects inside a save.
type Service struct {
	db                  *sql.DB
	now                 func() time.Time
	releaseReevaluation ReleaseReevaluationScheduler
}

// NewService wires the parser policy owner.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now}
}

var _ policyowner.SnapshotPort = (*Service)(nil)

func (s *Service) PolicyCapability() policyowner.Capability[Policy] {
	return policyowner.Capability[Policy]{
		Metadata: policyowner.Metadata{
			Kind:                        "parser",
			DefaultScope:                "global",
			InstanceVersionFloor:        1,
			SourceDefaultVersionFloor:   1,
			MissingDefaultSourceVersion: 1,
			MutationHook:                "release_reevaluation",
		},
		Validate: Validate,
	}
}

// SetReleaseReevaluationScheduler wires the release reevaluation hook. It is
// required for any save that changes the instance parser kind: a kind change
// alters every unsubmitted Release's parse evidence, so it must schedule
// reevaluation instead of waiting for the next feed refresh. A save that leaves
// the kind unchanged never consults it.
func (s *Service) SetReleaseReevaluationScheduler(scheduler ReleaseReevaluationScheduler) {
	if s != nil {
		s.releaseReevaluation = scheduler
	}
}

// GetDefault returns the singleton creation default (nil when absent).
func (s *Service) GetDefault(ctx context.Context) (*Default, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT value->>'parser_kind', version, updated_at
FROM global_config_sections WHERE section_name = 'parser_policy'`)
	var (
		kind      string
		version   int64
		updatedAt time.Time
	)
	if err := row.Scan(&kind, &version, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &Default{
		Policy:    Policy{Kind: parser.ParserKind(kind)},
		Version:   version,
		UpdatedAt: updatedAt,
	}, nil
}

// SaveDefault persists the singleton creation template with expected-version
// CAS (0 = create). It never enumerates instances.
func (s *Service) SaveDefault(ctx context.Context, expectedVersion int64, input Policy) (Default, error) {
	if err := Validate(input); err != nil {
		return Default{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Default{}, err
	}
	defer tx.Rollback()
	var currentVersion int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM global_config_sections WHERE section_name = 'parser_policy' FOR UPDATE`).Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		currentVersion = 0
	} else if err != nil {
		return Default{}, err
	}
	if currentVersion != expectedVersion {
		return Default{}, &apperrors.VersionConflict{Owner: "parser_policy_default", CurrentVersion: currentVersion}
	}
	now := s.now().UTC()
	var newVersion int64
	if currentVersion == 0 {
		err = tx.QueryRowContext(ctx, `
		INSERT INTO global_config_sections
		(section_name, schema_version, value, version, updated_at)
		VALUES ('parser_policy', 'parser-policy-v1', jsonb_build_object('parser_kind', $1::text), 1, $2)
		RETURNING version`, input.Kind, now).Scan(&newVersion)
	} else {
		err = tx.QueryRowContext(ctx, `
		UPDATE global_config_sections
		SET value = jsonb_build_object('parser_kind', $1::text), version = version + 1, updated_at = $2
		WHERE section_name = 'parser_policy' AND version = $3
		RETURNING version`, input.Kind, now, expectedVersion).Scan(&newVersion)
	}
	if err != nil {
		return Default{}, err
	}
	if err := tx.Commit(); err != nil {
		return Default{}, err
	}
	return Default{Policy: input, Version: newVersion, UpdatedAt: now}, nil
}

// GetInstance returns the complete per-instance snapshot (nil when absent).
func (s *Service) GetInstance(ctx context.Context, animeID string) (*Instance, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT parser_kind, parser_source_default_version, parser_version, updated_at
FROM anime_business_configs WHERE managed_anime_id = $1 AND parser_version IS NOT NULL`, animeID)
	var (
		kind                   string
		sourceDefault, version int64
		updatedAt              time.Time
	)
	if err := row.Scan(&kind, &sourceDefault, &version, &updatedAt); err != nil {
		return nil, err
	}
	return &Instance{
		ManagedAnimeID:       animeID,
		Policy:               Policy{Kind: parser.ParserKind(kind)},
		SourceDefaultVersion: sourceDefault,
		Version:              version,
		UpdatedAt:            updatedAt,
	}, nil
}

// SaveInstance fully replaces one complete instance snapshot with
// expected-version CAS. A kind change schedules release reevaluation before
// commit; placement scans and effects stay outside.
func (s *Service) SaveInstance(ctx context.Context, animeID string, expectedVersion int64, input Policy) (Instance, error) {
	if err := Validate(input); err != nil {
		return Instance{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Instance{}, err
	}
	defer tx.Rollback()
	var currentVersion, sourceDefaultVersion int64
	var currentKind string
	err = tx.QueryRowContext(ctx, `
SELECT parser_version, parser_kind, parser_source_default_version
FROM anime_business_configs
WHERE managed_anime_id = $1 AND parser_version IS NOT NULL
FOR UPDATE`, animeID).Scan(&currentVersion, &currentKind, &sourceDefaultVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return Instance{}, &apperrors.NotFound{Resource: "instance_parser_policy", ID: animeID}
	}
	if err != nil {
		return Instance{}, err
	}
	if currentVersion != expectedVersion {
		return Instance{}, &apperrors.VersionConflict{Owner: "instance_parser_policy", CurrentVersion: currentVersion}
	}
	now := s.now().UTC()
	var newVersion int64
	err = tx.QueryRowContext(ctx, `
UPDATE anime_business_configs
SET parser_kind = $2, parser_version = parser_version + 1, updated_at = $3
WHERE managed_anime_id = $1 AND parser_version = $4
RETURNING parser_version`, animeID, input.Kind, now, expectedVersion).Scan(&newVersion)
	if err != nil {
		return Instance{}, err
	}
	// A kind change invalidates the parse evidence of every unsubmitted
	// Release, so it must re-evaluate them; submitted work is untouched (the
	// scheduler only selects Releases without a DownloadTask). A kind change
	// fails closed when the reevaluation cannot be scheduled.
	if currentKind != string(input.Kind) {
		if s.releaseReevaluation == nil {
			return Instance{}, errors.New("instance parser kind change requires a release reevaluation scheduler")
		}
		if err := s.releaseReevaluation.ScheduleUnsubmittedReleaseReevaluationTx(ctx, txscope.Wrap(tx), animeID); err != nil {
			return Instance{}, fmt.Errorf("schedule release reevaluation for parser policy change: %w", err)
		}
	}
	// Ruling 3: the merged row is the one configuration source, so the frozen
	// business snapshot is rewritten in the same transaction as the policy save.
	if _, err := animeconfig.SyncInstanceBusinessSnapshotTx(ctx, tx, animeID); err != nil {
		return Instance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Instance{}, err
	}
	return Instance{ManagedAnimeID: animeID, Policy: input, SourceDefaultVersion: sourceDefaultVersion, Version: newVersion, UpdatedAt: now}, nil
}

func (s *Service) Capability() policyowner.Capability[Policy] {
	return s.PolicyCapability()
}

func (s *Service) ReadDefault(ctx context.Context) (*policyowner.Default[Policy], error) {
	value, err := s.GetDefault(ctx)
	if err != nil || value == nil {
		return nil, err
	}
	return &policyowner.Default[Policy]{Policy: value.Policy, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

func (s *Service) WriteDefault(ctx context.Context, input policyowner.DefaultSave[Policy]) (policyowner.Default[Policy], error) {
	value, err := s.SaveDefault(ctx, input.ExpectedVersion, input.Value)
	if err != nil {
		return policyowner.Default[Policy]{}, err
	}
	return policyowner.Default[Policy]{Policy: value.Policy, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

func (s *Service) ReadInstance(ctx context.Context, animeID string) (*policyowner.Instance[Policy], error) {
	value, err := s.GetInstance(ctx, animeID)
	if err != nil || value == nil {
		return nil, err
	}
	return &policyowner.Instance[Policy]{ManagedAnimeID: value.ManagedAnimeID, Policy: value.Policy, SourceDefaultVersion: value.SourceDefaultVersion, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

func (s *Service) WriteInstance(ctx context.Context, input policyowner.InstanceSave[Policy]) (policyowner.Instance[Policy], error) {
	value, err := s.SaveInstance(ctx, input.ManagedAnimeID, input.ExpectedVersion, input.Value)
	if err != nil {
		return policyowner.Instance[Policy]{}, err
	}
	return policyowner.Instance[Policy]{ManagedAnimeID: value.ManagedAnimeID, Policy: value.Policy, SourceDefaultVersion: value.SourceDefaultVersion, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

var _ policyowner.Lifecycle[Policy] = (*Service)(nil)

// CopyFrozenParserPolicyToInstanceTx writes a revision-frozen parser policy as
// a complete instance snapshot (version 1) inside the caller's transaction. It
// is the activation/one-shot creation port: the kind comes from the saved
// revision, so a global parser change after the save can never alter the new
// instance. It validates the frozen kind and fails closed instead of falling
// back to the current global default. `sourceVersion` records the saved revision's policy version as
// provenance.
func (s *Service) CopyFrozenParserPolicyToInstanceTx(ctx context.Context, scope txscope.Tx, animeID, kind string, sourceVersion int64) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("frozen parser policy transaction is nil")
	}
	policy := Policy{Kind: parser.ParserKind(kind)}
	if err := Validate(policy); err != nil {
		return err
	}
	if sourceVersion < 1 {
		sourceVersion = 1
	}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO anime_business_configs
	(managed_anime_id, parser_kind, parser_source_default_version, parser_version, updated_at)
	VALUES ($1, $2, $3, 1, CURRENT_TIMESTAMP)
	ON CONFLICT (managed_anime_id) DO UPDATE SET
	    parser_kind = EXCLUDED.parser_kind, parser_source_default_version = EXCLUDED.parser_source_default_version,
	    parser_version = 1, updated_at = CURRENT_TIMESTAMP
	WHERE anime_business_configs.parser_version IS NULL`,
		animeID, policy.Kind, sourceVersion); err != nil {
		return err
	}
	return nil
}

// CopyDefaultToInstanceTx copies the singleton creation default into a
// complete instance snapshot (version 1) inside the caller's transaction. An
// absent default is initialized from the versioned code defaults. It is the
// Subscribe creation port and never runs outside the caller's transaction.
func (s *Service) CopyDefaultToInstanceTx(ctx context.Context, scope txscope.Tx, animeID string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("parser policy snapshot transaction is nil")
	}
	var (
		kind    string
		version int64
	)
	err := tx.QueryRowContext(ctx, `
SELECT value->>'parser_kind', version
FROM global_config_sections WHERE section_name = 'parser_policy' FOR UPDATE`).
		Scan(&kind, &version)
	if errors.Is(err, sql.ErrNoRows) {
		defaults := DefaultPolicy()
		now := s.now().UTC()
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO global_config_sections
			(section_name, schema_version, value, version, updated_at)
			VALUES ('parser_policy', 'parser-policy-v1', jsonb_build_object('parser_kind', $1::text), 1, $2)
			RETURNING version`, defaults.Kind, now).Scan(&version); err != nil {
			return err
		}
		kind = string(defaults.Kind)
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO anime_business_configs
	(managed_anime_id, parser_kind, parser_source_default_version, parser_version, updated_at)
	VALUES ($1, $2, $3, 1, CURRENT_TIMESTAMP)
	ON CONFLICT (managed_anime_id) DO UPDATE SET
	    parser_kind = EXCLUDED.parser_kind, parser_source_default_version = EXCLUDED.parser_source_default_version,
	    parser_version = 1, updated_at = CURRENT_TIMESTAMP
	WHERE anime_business_configs.parser_version IS NULL`,
		animeID, kind, version); err != nil {
		return err
	}
	return nil
}
