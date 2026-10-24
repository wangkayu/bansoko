package application

import (
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"context"
	"database/sql"
	"encoding/json"
)

type BusinessOwnerVersions struct {
	Rules      int64 `json:"rules"`
	Subgroups  int64 `json:"subgroups"`
	Parser     int64 `json:"parser"`
	Retention  int64 `json:"retention"`
	FileEffect int64 `json:"file_effect"`
	Business   int64 `json:"business"`
}
type CandidateApplyInput struct {
	Revision                 int64                 `json:"revision"`
	TargetManagedAnimeID     string                `json:"target_managed_anime_id"`
	ExpectedCandidateVersion int64                 `json:"expected_candidate_version"`
	ExpectedOwnerVersions    BusinessOwnerVersions `json:"expected_owner_versions"`
}
type CandidateApplyResult struct {
	TargetManagedAnimeID string                `json:"target_managed_anime_id"`
	CandidateID          string                `json:"candidate_id"`
	Revision             int64                 `json:"revision"`
	BusinessDigest       string                `json:"business_digest"`
	OwnerVersions        BusinessOwnerVersions `json:"owner_versions"`
}

func (s *Service) ApplyAnimeConfigCandidate(ctx context.Context, candidateID, key string, input CandidateApplyInput) (CandidateApplyResult, error) {
	var result CandidateApplyResult
	if input.TargetManagedAnimeID == "" || input.Revision < 1 || key == "" {
		return result, businessInputError("exact target, revision and idempotency key are required")
	}
	digest, err := canonicalJSONHash(struct {
		Candidate string              `json:"candidate"`
		Input     CandidateApplyInput `json:"input"`
	}{candidateID, input})
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return result, err
	}
	var previousDigest string
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash,result_snapshot FROM idempotency_records WHERE kind='business_apply' AND idempotency_key=$1`, key).Scan(&previousDigest, &previous)
	if err == nil {
		if previousDigest != digest {
			return result, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "apply request identity changed"}
		}
		err = json.Unmarshal(previous, &result)
		return result, err
	}
	if err != sql.ErrNoRows {
		return result, err
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle_state FROM managed_anime WHERE id=$1 FOR UPDATE`, input.TargetManagedAnimeID).Scan(&lifecycle); err != nil {
		return result, err
	}
	if lifecycle == "retired" {
		return result, &apperrors.Business{Status: 409, Code: "INSTANCE_RETIRED", Message: "retired instance cannot apply business revisions"}
	}
	var version, current int64
	if err := tx.QueryRowContext(ctx, `SELECT version,current_revision FROM anime_config_candidates WHERE id=$1 FOR UPDATE`, candidateID).Scan(&version, &current); err != nil {
		return result, err
	}
	if version != input.ExpectedCandidateVersion || current != input.Revision {
		return result, &apperrors.VersionConflict{Owner: "anime_config_candidate", CurrentVersion: version}
	}
	revision, _, err := s.LoadCandidateRevisionTx(ctx, tx, candidateID, input.Revision)
	if err != nil {
		return result, err
	}
	if revision.Business == nil || revision.BusinessDigest == nil {
		return result, businessInputError("apply requires a complete frozen business revision")
	}
	actual, err := lockBusinessOwners(ctx, tx, input.TargetManagedAnimeID)
	if err != nil {
		return result, err
	}
	if actual != input.ExpectedOwnerVersions {
		return result, &apperrors.Business{Status: 409, Code: "BUSINESS_OWNER_VERSION_CONFLICT", Message: "target owner version vector changed"}
	}
	b := revision.Business
	if err := verifyBusinessProgramsTx(ctx, tx, *b); err != nil {
		return result, err
	}
	animeID := input.TargetManagedAnimeID
	// No source, metadata, provider-selection/manual-intent, download or artifact writes.
	for _, name := range animedomain.EffectiveSectionOrder {
		owner := "rules"
		if name == animedomain.SectionSubgroups {
			owner = "subgroups"
		}
		value := animedomain.OverrideValue{Value: b.Sections[name]}
		if err := upsertOverride(ctx, tx, animeID, owner, name, value); err != nil {
			return result, err
		}
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return result, err
	}
	// One row carries every owner version, the three policy axes and the frozen
	// business, so the version vector and the snapshots can never drift apart.
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_business_configs
SET rules_version = rules_version + 1, subgroups_version = subgroups_version + 1,
    parser_kind = $2, parser_source_default_version = $3, parser_version = parser_version + 1,
    admission_mode = $4, quality_profile = $5, cleanup_policy = $6, retention_source_default_version = $7, retention_version = retention_version + 1,
    unclassified_route = $8, file_effect_source_default_version = $9, file_effect_version = file_effect_version + 1,
    candidate_id = $10, revision = $11, business_snapshot = $12, business_digest = $13,
    business_version = business_version + 1, updated_at = NOW()
WHERE managed_anime_id = $1`,
		animeID, b.Parser.Kind, b.Parser.PolicyVersion,
		b.Retention.AdmissionMode, []byte(b.Retention.QualityProfile), []byte(b.Retention.CleanupPolicy), b.RetentionVersion,
		b.UnclassifiedRoute, b.FileEffectVersion,
		candidateID, input.Revision, raw, *revision.BusinessDigest); err != nil {
		return result, err
	}
	result = CandidateApplyResult{animeID, candidateID, input.Revision, *revision.BusinessDigest, BusinessOwnerVersions{actual.Rules + 1, actual.Subgroups + 1, actual.Parser + 1, actual.Retention + 1, actual.FileEffect + 1, actual.Business + 1}}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(kind,idempotency_key,request_hash,managed_anime_id,result_snapshot) VALUES('business_apply',$1,$2,$3,$4)`, key, digest, animeID, encoded); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func lockBusinessOwners(ctx context.Context, tx *sql.Tx, animeID string) (BusinessOwnerVersions, error) {
	var (
		v                                       BusinessOwnerVersions
		parser, retention, fileEffect, business sql.NullInt64
		hasSnapshot                             bool
	)
	if err := tx.QueryRowContext(ctx, `
SELECT rules_version, subgroups_version, parser_version, retention_version, file_effect_version,
       business_version, business_snapshot IS NOT NULL
FROM anime_business_configs WHERE managed_anime_id = $1 FOR UPDATE`, animeID).
		Scan(&v.Rules, &v.Subgroups, &parser, &retention, &fileEffect, &business, &hasSnapshot); err != nil {
		return v, err
	}
	if !parser.Valid || !retention.Valid || !fileEffect.Valid {
		// An instance whose policy axes were never provisioned has no frozen
		// business to apply against; keep the former three-table "row required"
		// contract so the caller fails closed.
		return v, sql.ErrNoRows
	}
	v.Parser, v.Retention, v.FileEffect = parser.Int64, retention.Int64, fileEffect.Int64
	if hasSnapshot {
		v.Business = business.Int64
	}
	return v, nil
}
