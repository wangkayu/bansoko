package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
)

// candidateRevisionColumns is the canonical projection of one immutable
// revision row. The mutable candidate row's own projection deliberately does
// not include the pointer columns, so adding revisions stayed contract-neutral
// on the existing candidate read shape.
const candidateRevisionColumns = `candidate_id, revision, draft, draft_digest, effective,
	section_versions, section_schemas, effective_digest, parser_policy, parser_policy_digest, created_at, business_snapshot, business_digest`

// appendCandidateRevisionTx appends one immutable revision row inside the
// caller's transaction. Callers MUST insert the revision before moving the
// candidate's current_revision pointer, because the pointer guard checks
// referential integrity at statement time. The revision number is assigned by
// the database as the next contiguous value for the candidate; the trigger
// rejects a gap, so a concurrent writer cannot reuse a number.
func (s *Service) appendCandidateRevisionTx(ctx context.Context, tx *sql.Tx, candidateID string, draft animedomain.AnimeDraft, draftDigest string, resolved animedomain.ResolvedSections, policy animedomain.ParserPolicyEnvelope) (CandidateRevision, error) {
	if err := policy.Validate(); err != nil {
		return CandidateRevision{}, fmt.Errorf("freeze candidate revision parser policy: %w", err)
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return CandidateRevision{}, err
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return CandidateRevision{}, fmt.Errorf("encode candidate revision parser policy: %w", err)
	}
	draftJSON, err := json.Marshal(draft)
	if err != nil {
		return CandidateRevision{}, fmt.Errorf("encode candidate revision draft: %w", err)
	}
	effectiveJSON, err := json.Marshal(resolved.Sections)
	if err != nil {
		return CandidateRevision{}, fmt.Errorf("encode candidate revision effective sections: %w", err)
	}
	versionsJSON, err := json.Marshal(resolved.Versions)
	if err != nil {
		return CandidateRevision{}, fmt.Errorf("encode candidate revision section versions: %w", err)
	}
	schemasJSON, err := json.Marshal(resolved.SchemaVersions)
	if err != nil {
		return CandidateRevision{}, fmt.Errorf("encode candidate revision section schemas: %w", err)
	}
	effectiveDigest, err := resolved.EffectiveDigest()
	if err != nil {
		return CandidateRevision{}, fmt.Errorf("digest candidate revision effective config: %w", err)
	}
	business, err := submittedBusiness(draft)
	if err != nil {
		return CandidateRevision{}, err
	}
	if business != nil {
		if err := verifyBusinessProgramsTx(ctx, tx, *business); err != nil {
			return CandidateRevision{}, err
		}
	}
	businessRaw, err := businessJSON(business)
	if err != nil {
		return CandidateRevision{}, err
	}
	row := tx.QueryRowContext(ctx, `
INSERT INTO anime_config_candidate_revisions(
    candidate_id, revision, draft, draft_digest, effective,
    section_versions, section_schemas, effective_digest,
    parser_policy, parser_policy_digest, business_snapshot, business_digest
)
VALUES (
    $1,
    (SELECT COALESCE(MAX(revision), 0) + 1 FROM anime_config_candidate_revisions WHERE candidate_id = $1),
    $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
RETURNING `+candidateRevisionColumns,
		candidateID, draftJSON, draftDigest, effectiveJSON,
		versionsJSON, schemasJSON, effectiveDigest, policyJSON, policyDigest, businessRaw, draft.BusinessDigest)
	return scanCandidateRevision(row)
}

// moveCandidateRevisionPointerTx points the mutable candidate at the revision
// that was just appended. It never touches the CAS `version`: the pointer is
// bookkeeping, not a user-visible mutation.
func moveCandidateRevisionPointerTx(ctx context.Context, tx *sql.Tx, candidateID string, revision int64, effectiveDigest string) error {
	result, err := tx.ExecContext(ctx, `
UPDATE anime_config_candidates
SET current_revision = $2, current_revision_digest = $3
WHERE id = $1`, candidateID, revision, effectiveDigest)
	if err != nil {
		return fmt.Errorf("move candidate revision pointer: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read candidate revision pointer update count: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("move candidate revision pointer: candidate %s not found", candidateID)
	}
	return nil
}

// GetAnimeConfigCandidateRevision reads one immutable revision by identity.
func (s *Service) GetAnimeConfigCandidateRevision(ctx context.Context, candidateID string, revision int64) (CandidateRevision, error) {
	if s == nil || s.db == nil {
		return CandidateRevision{}, errors.New("anime service database is nil")
	}
	row := s.db.QueryRowContext(ctx, `
SELECT `+candidateRevisionColumns+`
FROM anime_config_candidate_revisions
WHERE candidate_id = $1 AND revision = $2`, candidateID, revision)
	result, err := scanCandidateRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CandidateRevision{}, &apperrors.NotFound{Resource: "anime_config_candidate_revision", ID: fmt.Sprintf("%s:%d", candidateID, revision)}
	}
	if err != nil {
		return CandidateRevision{}, err
	}
	return result, nil
}

// LoadCandidateRevisionTx reads and verifies one immutable candidate revision
// inside the caller's transaction. The returned digest is the database's
// canonical draft digest used to cross-check the application digest.
func (s *Service) LoadCandidateRevisionTx(ctx context.Context, tx *sql.Tx, candidateID string, revision int64) (CandidateRevision, string, error) {
	if tx == nil {
		return CandidateRevision{}, "", errors.New("candidate revision read requires a caller-owned transaction")
	}
	value, databaseDigest, err := loadCandidateRevisionTx(ctx, tx, candidateID, revision)
	if err != nil {
		return CandidateRevision{}, "", err
	}
	if err := value.VerifyIntegrity(); err != nil {
		return CandidateRevision{}, "", err
	}
	draftDigest, err := canonicalAnimeDraftHash(value.Draft)
	if err != nil || value.DraftDigest == "" || value.DraftDigest != databaseDigest || databaseDigest != draftDigest {
		return CandidateRevision{}, "", candidateDigestMismatchError()
	}
	return value, databaseDigest, nil
}

// CandidateRevisionRequiredError returns the stable conflict for a candidate
// that has no immutable saved revision.
func (s *Service) CandidateRevisionRequiredError() error { return candidateRevisionRequiredError() }

// VerifyCandidateRevision checks the immutable revision's stored digests and
// effective-configuration identity before it is used by another application.
func (s *Service) VerifyCandidateRevision(revision CandidateRevision) error {
	return revision.VerifyIntegrity()
}

// candidateRevisionColumnsWithCanonicalDigest adds the database's canonical view
// of the stored draft digest. Activation cross-checks it against the
// application-computed digest exactly as the mutable candidate row's own digest
// trigger does, so a stored revision can never be executed with a digest the
// database and the application disagree about.
const candidateRevisionColumnsWithCanonicalDigest = candidateRevisionColumns + `,
	'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'), 'hex')`

// loadCandidateRevisionTx reads one immutable revision inside the caller's
// transaction, so an activation sees the revision its locked candidate pointer
// names rather than a value that a concurrent writer could move underneath it.
// It also returns the database's canonical draft digest for the integrity check.
func loadCandidateRevisionTx(ctx context.Context, tx *sql.Tx, candidateID string, revision int64) (CandidateRevision, string, error) {
	row := tx.QueryRowContext(ctx, `
SELECT `+candidateRevisionColumnsWithCanonicalDigest+`
FROM anime_config_candidate_revisions
WHERE candidate_id = $1 AND revision = $2`, candidateID, revision)
	var canonicalDigest string
	result, err := scanCandidateRevision(row, &canonicalDigest)
	if err != nil {
		return CandidateRevision{}, "", err
	}
	return result, canonicalDigest, nil
}

// GetCurrentAnimeConfigCandidateRevision reads the candidate's current saved
// revision. A candidate with no revision is a legacy row that predates the
// revision store; it is reported as a typed re-save requirement instead of
// being resolved against current globals.
func (s *Service) GetCurrentAnimeConfigCandidateRevision(ctx context.Context, candidateID string) (CandidateRevision, error) {
	if s == nil || s.db == nil {
		return CandidateRevision{}, errors.New("anime service database is nil")
	}
	var current sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT current_revision
FROM anime_config_candidates
WHERE id = $1`, candidateID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return CandidateRevision{}, &apperrors.NotFound{Resource: "anime_config_candidate", ID: candidateID}
	}
	if err != nil {
		return CandidateRevision{}, fmt.Errorf("read candidate current revision: %w", err)
	}
	if !current.Valid {
		return CandidateRevision{}, candidateRevisionRequiredError()
	}
	return s.GetAnimeConfigCandidateRevision(ctx, candidateID, current.Int64)
}

// scanCandidateRevision reads one revision projection. Extra destinations are
// appended after the canonical columns so a caller can request one additional
// computed column (for example the database's canonical draft digest) without a
// second query.
func scanCandidateRevision(row rowScanner, extra ...any) (CandidateRevision, error) {
	var result CandidateRevision
	var draftJSON []byte
	var effectiveJSON, versionsJSON, schemasJSON, policyJSON, businessRaw []byte
	destinations := []any{&result.CandidateID, &result.Revision, &draftJSON, &result.DraftDigest,
		&effectiveJSON, &versionsJSON, &schemasJSON, &result.EffectiveDigest,
		&policyJSON, &result.ParserPolicyDigest, &result.CreatedAt, &businessRaw, &result.BusinessDigest}
	destinations = append(destinations, extra...)
	if err := row.Scan(destinations...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CandidateRevision{}, err
		}
		return CandidateRevision{}, fmt.Errorf("scan candidate revision: %w", err)
	}
	draft, err := animedomain.DecodeDraftJSON(draftJSON)
	if err != nil {
		return CandidateRevision{}, fmt.Errorf("decode candidate revision draft: %w", err)
	}
	result.Draft = draft
	result.Effective = append(json.RawMessage(nil), effectiveJSON...)
	result.SectionVersions = append(json.RawMessage(nil), versionsJSON...)
	result.SectionSchemas = append(json.RawMessage(nil), schemasJSON...)
	policy, err := animedomain.DecodeParserPolicyEnvelope(policyJSON)
	if err != nil {
		return CandidateRevision{}, err
	}
	result.ParserPolicy = policy
	if len(businessRaw) > 0 {
		var business animedomain.BusinessSnapshot
		if err := json.Unmarshal(businessRaw, &business); err != nil {
			return CandidateRevision{}, err
		}
		result.Business = &business
	}
	return result, nil
}
