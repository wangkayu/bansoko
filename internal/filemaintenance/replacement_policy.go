package filemaintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"bansoko/internal/anime/animeconfig"
	"bansoko/internal/apperrors"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/policyowner"
	"bansoko/internal/qualityprofile"
)

// ReplacementPolicy is the complete business replacement policy value: two
// disjoint owned fields with no NULL=inherit merge. It is what a new instance
// copies at creation/activation and what an instance save fully replaces with
// expected-version CAS. A replaced managed file is deleted directly; there is
// no cleanup policy.
type ReplacementPolicy struct {
	AdmissionMode  AdmissionMode
	QualityProfile json.RawMessage
}

// Digest is the canonical policy digest over the normalized fields. The JSON
// field is canonicalized (unmarshal + re-marshal) so the digest is stable
// across JSONB key ordering and whitespace. It is used for migration
// verification and tests, never as a second business truth.
func (p ReplacementPolicy) Digest() string {
	profile, err := canonicalJSON(p.QualityProfile)
	if err != nil {
		return ""
	}
	canonical, err := json.Marshal(struct {
		AdmissionMode  string          `json:"admission_mode"`
		QualityProfile json.RawMessage `json:"quality_profile"`
	}{AdmissionMode: string(p.AdmissionMode), QualityProfile: profile})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonicalJSON re-marshals a JSON document in Go's deterministic key order
// (lexicographic for objects), independent of input order and whitespace.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil, errors.New("invalid replacement policy JSON")
	}
	return json.Marshal(value)
}

// DefaultQualityProfileJSON is the built-in comparison profile
// (qualityprofile.Default), used when no default row exists yet.
var DefaultQualityProfileJSON = qualityprofile.DefaultJSON()

// DefaultReplacementPolicy is the versioned code default for new instances.
func DefaultReplacementPolicy() ReplacementPolicy {
	return ReplacementPolicy{
		AdmissionMode:  ModeAll,
		QualityProfile: append(json.RawMessage(nil), DefaultQualityProfileJSON...),
	}
}

// replacementSectionValue encodes one complete replacement policy as the JSON object
// stored in global_config_sections(section_name='replacement'). The value was
// validated before any write, so marshalling cannot fail.
func replacementSectionValue(policy ReplacementPolicy) string {
	raw, err := json.Marshal(struct {
		AdmissionMode  string          `json:"admission_mode"`
		QualityProfile json.RawMessage `json:"quality_profile"`
	}{string(policy.AdmissionMode), policy.QualityProfile})
	if err != nil {
		return ""
	}
	return string(raw)
}

// policyInvalid builds the typed 422 POLICY_INVALID error from the spec error
// matrix (invalid admission mode or incomplete typed policy).
func policyInvalid(detail string) error {
	return &apperrors.Business{Status: 422, Code: "POLICY_INVALID", Message: detail}
}

// ValidateReplacementPolicy rejects incomplete or wrongly typed values before
// any write: admission_mode must be the enum, quality_profile a valid
// profile-v1 (qualityprofile.Parse: known unique items, well-formed rankings,
// no duplicate value sets).
func ValidateReplacementPolicy(policy ReplacementPolicy) error {
	switch policy.AdmissionMode {
	case ModeAll, ModePerGroup, ModeSingleCopy:
	default:
		return policyInvalid("admission_mode must be one of all, per_group, single_copy")
	}
	return validateQualityProfile(policy.QualityProfile)
}

func validateQualityProfile(raw json.RawMessage) error {
	if _, err := qualityprofile.Parse(raw); err != nil {
		return policyInvalid("quality_profile: " + err.Error())
	}
	return nil
}

// ReplacementPolicyDefault is the global_config_sections('replacement') value:
// the creation template for future instances only.
type ReplacementPolicyDefault = policyowner.Default[ReplacementPolicy]

// InstanceReplacementPolicy is the replacement part of one
// anime_business_configs row: one complete frozen policy per ManagedAnime.
type InstanceReplacementPolicy = policyowner.Instance[ReplacementPolicy]

// ReplacementPolicyService owns the frozen per-instance business retention
// policy and the singleton creation default. It never merges the global value
// into an existing instance at runtime and never touches Placement, Cleanup,
// WorkItem, or filesystem owners inside a policy save.
type ReplacementPolicyService struct {
	db  *sql.DB
	now func() time.Time
}

// NewReplacementPolicyService wires the replacement owner.
func NewReplacementPolicyService(db *sql.DB) *ReplacementPolicyService {
	return &ReplacementPolicyService{db: db, now: time.Now}
}

var _ policyowner.SnapshotPort = (*ReplacementPolicyService)(nil)

func (s *ReplacementPolicyService) PolicyCapability() policyowner.Capability[ReplacementPolicy] {
	return policyowner.Capability[ReplacementPolicy]{
		Metadata: policyowner.Metadata{
			Kind:                        "replacement",
			DefaultScope:                "global",
			InstanceVersionFloor:        1,
			SourceDefaultVersionFloor:   1,
			MissingDefaultSourceVersion: 1,
			AuditRequired:               true,
		},
		Validate: ValidateReplacementPolicy,
	}
}

// GetDefault returns the singleton creation default (nil when absent).
func (s *ReplacementPolicyService) GetDefault(ctx context.Context) (*ReplacementPolicyDefault, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT value->>'admission_mode', value->'quality_profile', version, updated_at
FROM global_config_sections WHERE section_name = 'replacement'`)
	var (
		mode      string
		profile   []byte
		version   int64
		updatedAt time.Time
	)
	if err := row.Scan(&mode, &profile, &version, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &ReplacementPolicyDefault{
		Policy: ReplacementPolicy{
			AdmissionMode:  AdmissionMode(mode),
			QualityProfile: json.RawMessage(profile),
		},
		Version:   version,
		UpdatedAt: updatedAt,
	}, nil
}

// SaveDefault persists the singleton creation default with expected-version
// CAS (0 = create). It never enumerates instances.
func (s *ReplacementPolicyService) SaveDefault(ctx context.Context, expectedVersion int64, input ReplacementPolicy, actor, channel string) (ReplacementPolicyDefault, error) {
	if err := ValidateReplacementPolicy(input); err != nil {
		return ReplacementPolicyDefault{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReplacementPolicyDefault{}, err
	}
	defer tx.Rollback()
	var currentVersion int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM global_config_sections WHERE section_name = 'replacement' FOR UPDATE`).Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		currentVersion = 0
	} else if err != nil {
		return ReplacementPolicyDefault{}, err
	}
	if currentVersion != expectedVersion {
		return ReplacementPolicyDefault{}, &apperrors.VersionConflict{Owner: "replacement_policy_default", CurrentVersion: currentVersion}
	}
	now := s.now().UTC()
	var newVersion int64
	if currentVersion == 0 {
		err = tx.QueryRowContext(ctx, `
		INSERT INTO global_config_sections
		(section_name, schema_version, value, version, updated_at)
		VALUES ('replacement', 'replacement-policy-v1', $1::jsonb, 1, $2)
		RETURNING version`, replacementSectionValue(input), now).Scan(&newVersion)
	} else {
		err = tx.QueryRowContext(ctx, `
		UPDATE global_config_sections
		SET value = $1::jsonb, version = version + 1, updated_at = $2
		WHERE section_name = 'replacement' AND version = $3
		RETURNING version`, replacementSectionValue(input), now, expectedVersion).Scan(&newVersion)
	}
	if err != nil {
		return ReplacementPolicyDefault{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReplacementPolicyDefault{}, err
	}
	return ReplacementPolicyDefault{Policy: input, Version: newVersion, UpdatedAt: now}, nil
}

// GetInstance returns the complete per-instance snapshot (nil when absent).
func (s *ReplacementPolicyService) GetInstance(ctx context.Context, animeID string) (*InstanceReplacementPolicy, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT admission_mode, quality_profile,
       replacement_source_default_version, replacement_version, updated_at
FROM anime_business_configs WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL`, animeID)
	var (
		mode                   string
		profile                []byte
		sourceDefault, version int64
		updatedAt              time.Time
	)
	if err := row.Scan(&mode, &profile, &sourceDefault, &version, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &InstanceReplacementPolicy{
		ManagedAnimeID: animeID,
		Policy: ReplacementPolicy{
			AdmissionMode:  AdmissionMode(mode),
			QualityProfile: json.RawMessage(profile),
		},
		SourceDefaultVersion: sourceDefault,
		Version:              version,
		UpdatedAt:            updatedAt,
	}, nil
}

// SaveInstance fully replaces one complete instance snapshot with
// expected-version CAS. The save transaction writes only the policy row; it
// never updates Placements, creates deletions, or performs filesystem effects.
func (s *ReplacementPolicyService) SaveInstance(ctx context.Context, animeID string, expectedVersion int64, input ReplacementPolicy, actor, channel string) (InstanceReplacementPolicy, error) {
	if err := ValidateReplacementPolicy(input); err != nil {
		return InstanceReplacementPolicy{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InstanceReplacementPolicy{}, err
	}
	defer tx.Rollback()
	var currentVersion, sourceDefaultVersion int64
	err = tx.QueryRowContext(ctx, `
SELECT replacement_source_default_version, replacement_version FROM anime_business_configs WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL FOR UPDATE`, animeID).Scan(&sourceDefaultVersion, &currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return InstanceReplacementPolicy{}, &apperrors.NotFound{Resource: "instance_replacement_policy", ID: animeID}
	}
	if err != nil {
		return InstanceReplacementPolicy{}, err
	}
	if currentVersion != expectedVersion {
		return InstanceReplacementPolicy{}, &apperrors.VersionConflict{Owner: "instance_replacement_policy", CurrentVersion: currentVersion}
	}
	now := s.now().UTC()
	var newVersion int64
	err = tx.QueryRowContext(ctx, `
UPDATE anime_business_configs
SET admission_mode = $2, quality_profile = $3,
    replacement_version = replacement_version + 1, updated_at = $4
WHERE managed_anime_id = $1 AND replacement_version = $5
RETURNING replacement_version`, animeID, input.AdmissionMode, input.QualityProfile, now, expectedVersion).Scan(&newVersion)
	if err != nil {
		return InstanceReplacementPolicy{}, err
	}
	// Ruling 3: the merged row is the one configuration source, so the frozen
	// business snapshot is rewritten in the same transaction as the policy save.
	if _, err := animeconfig.SyncInstanceBusinessSnapshotTx(ctx, tx, animeID); err != nil {
		return InstanceReplacementPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return InstanceReplacementPolicy{}, err
	}
	return InstanceReplacementPolicy{ManagedAnimeID: animeID, Policy: input, SourceDefaultVersion: sourceDefaultVersion, Version: newVersion, UpdatedAt: now}, nil
}

// CopyDefaultToInstanceTx copies the singleton creation default into a
// complete instance snapshot (version 1) inside the caller's transaction. An
// absent default is initialized from the versioned code defaults (version 1).
// It is the anime Subscribe/activation creation port and never runs outside
// the caller's transaction.
func (s *ReplacementPolicyService) CopyDefaultToInstanceTx(ctx context.Context, scope txscope.Tx, animeID string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("replacement policy snapshot transaction is nil")
	}
	var (
		mode    string
		profile []byte
		version int64
	)
	err := tx.QueryRowContext(ctx, `
SELECT value->>'admission_mode', value->'quality_profile', version
FROM global_config_sections WHERE section_name = 'replacement' FOR UPDATE`).
		Scan(&mode, &profile, &version)
	if errors.Is(err, sql.ErrNoRows) {
		// No default template yet: initialize the singleton from the versioned
		// code defaults so the instance provenance stays truthful.
		defaults := DefaultReplacementPolicy()
		now := s.now().UTC()
		if err := tx.QueryRowContext(ctx, `
		INSERT INTO global_config_sections
		(section_name, schema_version, value, version, updated_at)
		VALUES ('replacement', 'replacement-policy-v1', $1::jsonb, 1, $2)
		RETURNING version`, replacementSectionValue(defaults), now).Scan(&version); err != nil {
			return err
		}
		mode = string(defaults.AdmissionMode)
		profile = defaults.QualityProfile
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_business_configs
(managed_anime_id, admission_mode, quality_profile, replacement_source_default_version, replacement_version, updated_at)
VALUES ($1, $2, $3, $4, 1, CURRENT_TIMESTAMP)
ON CONFLICT (managed_anime_id) DO UPDATE SET
    admission_mode = EXCLUDED.admission_mode, quality_profile = EXCLUDED.quality_profile,
    replacement_source_default_version = EXCLUDED.replacement_source_default_version, replacement_version = 1, updated_at = CURRENT_TIMESTAMP
WHERE anime_business_configs.replacement_version IS NULL`,
		animeID, mode, profile, version); err != nil {
		return err
	}
	return nil
}

func (s *ReplacementPolicyService) Capability() policyowner.Capability[ReplacementPolicy] {
	return s.PolicyCapability()
}

func (s *ReplacementPolicyService) ReadDefault(ctx context.Context) (*policyowner.Default[ReplacementPolicy], error) {
	value, err := s.GetDefault(ctx)
	if err != nil || value == nil {
		return nil, err
	}
	return &policyowner.Default[ReplacementPolicy]{Policy: value.Policy, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

func (s *ReplacementPolicyService) WriteDefault(ctx context.Context, input policyowner.DefaultSave[ReplacementPolicy]) (policyowner.Default[ReplacementPolicy], error) {
	value, err := s.SaveDefault(ctx, input.ExpectedVersion, input.Value, input.Actor, input.Channel)
	if err != nil {
		return policyowner.Default[ReplacementPolicy]{}, err
	}
	return policyowner.Default[ReplacementPolicy]{Policy: value.Policy, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

func (s *ReplacementPolicyService) ReadInstance(ctx context.Context, animeID string) (*policyowner.Instance[ReplacementPolicy], error) {
	value, err := s.GetInstance(ctx, animeID)
	if err != nil || value == nil {
		return nil, err
	}
	return &policyowner.Instance[ReplacementPolicy]{ManagedAnimeID: value.ManagedAnimeID, Policy: value.Policy, SourceDefaultVersion: value.SourceDefaultVersion, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

func (s *ReplacementPolicyService) WriteInstance(ctx context.Context, input policyowner.InstanceSave[ReplacementPolicy]) (policyowner.Instance[ReplacementPolicy], error) {
	value, err := s.SaveInstance(ctx, input.ManagedAnimeID, input.ExpectedVersion, input.Value, input.Actor, input.Channel)
	if err != nil {
		return policyowner.Instance[ReplacementPolicy]{}, err
	}
	return policyowner.Instance[ReplacementPolicy]{ManagedAnimeID: value.ManagedAnimeID, Policy: value.Policy, SourceDefaultVersion: value.SourceDefaultVersion, Version: value.Version, UpdatedAt: value.UpdatedAt}, nil
}

var _ policyowner.Lifecycle[ReplacementPolicy] = (*ReplacementPolicyService)(nil)
