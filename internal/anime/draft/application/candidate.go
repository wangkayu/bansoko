package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
)

const candidateColumns = `id, origin, origin_entry_ref, title, title_override, instance_label,
	draft, version, activated_managed_anime_id, created_at, updated_at,
	draft_digest, current_revision, current_revision_digest, last_activity, status, retention_until,
	cleanup_eligible_at, cleanup_reason`

type ListAnimeConfigCandidatesInput struct {
	Origin         *animedomain.SubscriptionOrigin
	OriginEntryRef *string
}

type CandidateDraftInput struct {
	Draft animedomain.AnimeDraft
}

type AnimeConfigCandidate struct {
	ActivatedManagedAnimeId *string                        `json:"activated_managed_anime_id"`
	CleanupEligibleAt       *time.Time                     `json:"cleanup_eligible_at"`
	CleanupReason           *string                        `json:"cleanup_reason"`
	CreatedAt               time.Time                      `json:"created_at"`
	CurrentRevision         *int64                         `json:"current_revision"`
	CurrentRevisionDigest   *string                        `json:"current_revision_digest"`
	Draft                   animedomain.AnimeDraft         `json:"draft"`
	DraftDigest             string                         `json:"draft_digest"`
	Id                      string                         `json:"id"`
	InstanceLabel           *string                        `json:"instance_label"`
	LastActivity            time.Time                      `json:"last_activity"`
	Origin                  animedomain.SubscriptionOrigin `json:"origin"`
	OriginEntryRef          *string                        `json:"origin_entry_ref"`
	RetentionUntil          time.Time                      `json:"retention_until"`
	Status                  string                         `json:"status"`
	Title                   string                         `json:"title"`
	TitleOverride           *string                        `json:"title_override"`
	UpdatedAt               time.Time                      `json:"updated_at"`
	Version                 int64                          `json:"version"`
}

// ListAnimeConfigCandidates returns saved planning drafts, optionally narrowed
// to one provider entry. Reading a candidate performs no runtime write: no
// owner, no work item, no provider call.
func (s *Service) ListAnimeConfigCandidates(ctx context.Context, params ListAnimeConfigCandidatesInput) ([]AnimeConfigCandidate, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("anime service database is nil")
	}
	// Both filters are optional and independent, so each is compared against a
	// NULL-guarded parameter rather than assembling SQL by string concatenation.
	var origin any
	if params.Origin != nil {
		origin = string(*params.Origin)
	}
	var entryRef any
	if params.OriginEntryRef != nil {
		entryRef = strings.TrimSpace(*params.OriginEntryRef)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+candidateColumns+`
FROM anime_config_candidates
WHERE ($1::text IS NULL OR origin = $1)
  AND ($2::text IS NULL OR origin_entry_ref = $2)
ORDER BY last_activity DESC, updated_at DESC, id DESC`, origin, entryRef)
	if err != nil {
		return nil, fmt.Errorf("list anime config candidates: %w", err)
	}
	defer rows.Close()
	result := make([]AnimeConfigCandidate, 0)
	for rows.Next() {
		candidate, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

// GetAnimeConfigCandidate reads one saved candidate by id.
func (s *Service) GetAnimeConfigCandidate(ctx context.Context, candidateID string) (AnimeConfigCandidate, error) {
	if s == nil || s.db == nil {
		return AnimeConfigCandidate{}, errors.New("anime service database is nil")
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+candidateColumns+` FROM anime_config_candidates WHERE id = $1`, candidateID)
	candidate, err := scanCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AnimeConfigCandidate{}, &apperrors.NotFound{Resource: "anime_config_candidate", ID: candidateID}
	}
	if err != nil {
		return AnimeConfigCandidate{}, err
	}
	return candidate, nil
}

// SaveAnimeConfigCandidate persists a complete draft as a new server-owned row.
// Provider identity is protected by the partial unique index; a second working
// save returns a stable conflict instead of silently replacing the owner's draft.
func (s *Service) SaveAnimeConfigCandidate(ctx context.Context, input CandidateDraftInput) (AnimeConfigCandidate, error) {
	if s == nil || s.db == nil {
		return AnimeConfigCandidate{}, errors.New("anime service database is nil")
	}
	if violations := animedomain.ValidateDraft(input.Draft); len(violations) > 0 {
		return AnimeConfigCandidate{}, draftInvalidError(violations)
	}
	draftJSON, err := json.Marshal(input.Draft)
	if err != nil {
		return AnimeConfigCandidate{}, fmt.Errorf("encode candidate draft: %w", err)
	}
	draftDigest, err := canonicalAnimeDraftHash(input.Draft)
	if err != nil {
		return AnimeConfigCandidate{}, fmt.Errorf("hash candidate draft: %w", err)
	}
	origin := string(input.Draft.Origin)
	entryRef := candidateEntryRef(input.Draft)
	title := strings.TrimSpace(input.Draft.Title)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AnimeConfigCandidate{}, fmt.Errorf("begin save candidate: %w", err)
	}
	defer tx.Rollback()

	// Resolve the complete effective configuration inside the save transaction.
	// The saved revision must be self-explanatory: it never depends on a later
	// global change, and a draft that cannot resolve is not a valid saved version.
	resolved, policy, err := s.resolveCandidateInputTx(ctx, tx, input.Draft)
	if err != nil {
		return AnimeConfigCandidate{}, err
	}

	row := tx.QueryRowContext(ctx, `
INSERT INTO anime_config_candidates(
    id, origin, origin_entry_ref, title, title_override, instance_label, draft,
    draft_digest, last_activity, status, retention_until
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_TIMESTAMP, 'working', CURRENT_TIMESTAMP + INTERVAL '30 days')
RETURNING `+candidateColumns,
		uuid.NewString(), origin, entryRef, title,
		nullableOptionalText(input.Draft.TitleOverride),
		nullableOptionalText(input.Draft.InstanceLabel), draftJSON, draftDigest)
	candidate, err := scanCandidate(row)
	if err != nil {
		if isCandidateWorkingDuplicate(err) {
			return AnimeConfigCandidate{}, candidateWorkingDuplicateError()
		}
		return AnimeConfigCandidate{}, err
	}
	// A brand-new candidate starts at version 1, so its first immutable revision
	// is revision 1. The revision row is inserted before the pointer moves.
	revision, err := s.appendCandidateRevisionTx(ctx, tx, candidate.Id, input.Draft, draftDigest, resolved, policy)
	if err != nil {
		return AnimeConfigCandidate{}, err
	}
	if err := moveCandidateRevisionPointerTx(ctx, tx, candidate.Id, revision.Revision, revision.EffectiveDigest); err != nil {
		return AnimeConfigCandidate{}, err
	}
	candidate.CurrentRevision = &revision.Revision
	candidate.CurrentRevisionDigest = &revision.EffectiveDigest
	if err := tx.Commit(); err != nil {
		return AnimeConfigCandidate{}, fmt.Errorf("commit save candidate: %w", err)
	}
	return candidate, nil
}

// PatchAnimeConfigCandidate replaces a candidate's draft under expected-version
// CAS, so a stale editor never overwrites a newer draft saved elsewhere.
func (s *Service) PatchAnimeConfigCandidate(ctx context.Context, candidateID string, expectedVersion int64, input CandidateDraftInput) (AnimeConfigCandidate, error) {
	if s == nil || s.db == nil {
		return AnimeConfigCandidate{}, errors.New("anime service database is nil")
	}
	// A bearer without the secrets scope reads every RSS source locator as the
	// fixed placeholder (SEC-033). Sending it back keeps the stored locator of
	// the same source in the current revision; that is resolved under the row
	// lock below, so validation of such a draft waits until then.
	placeholders := animedomain.DraftHasSecretPlaceholder(input.Draft)
	var draftJSON []byte
	var draftDigest string
	prepare := func() error {
		if violations := animedomain.ValidateDraft(input.Draft); len(violations) > 0 {
			return draftInvalidError(violations)
		}
		encoded, err := json.Marshal(input.Draft)
		if err != nil {
			return fmt.Errorf("encode candidate draft: %w", err)
		}
		digest, err := canonicalAnimeDraftHash(input.Draft)
		if err != nil {
			return fmt.Errorf("hash candidate draft: %w", err)
		}
		draftJSON, draftDigest = encoded, digest
		return nil
	}
	if !placeholders {
		if err := prepare(); err != nil {
			return AnimeConfigCandidate{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AnimeConfigCandidate{}, fmt.Errorf("begin patch candidate: %w", err)
	}
	defer tx.Rollback()

	var version int64
	var activated sql.NullString
	var status string
	var currentDraftJSON []byte
	err = tx.QueryRowContext(ctx, `
SELECT version, activated_managed_anime_id, status, draft
FROM anime_config_candidates
WHERE id = $1
FOR UPDATE`, candidateID).Scan(&version, &activated, &status, &currentDraftJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return AnimeConfigCandidate{}, &apperrors.NotFound{Resource: "anime_config_candidate", ID: candidateID}
	}
	if err != nil {
		return AnimeConfigCandidate{}, fmt.Errorf("lock candidate: %w", err)
	}
	if s.candidateLockHook != nil {
		s.candidateLockHook("patch")
	}
	if version != expectedVersion {
		return AnimeConfigCandidate{}, &apperrors.VersionConflict{Owner: "anime_config_candidate", CurrentVersion: version}
	}
	if activated.Valid || status != "working" {
		return AnimeConfigCandidate{}, &apperrors.Business{
			Status: 409, Code: "CANDIDATE_NOT_WORKING",
			Message: "候选不是可编辑的 working 草稿。",
		}
	}
	if placeholders {
		var current animedomain.AnimeDraft
		if err := json.Unmarshal(currentDraftJSON, &current); err != nil {
			return AnimeConfigCandidate{}, fmt.Errorf("decode current candidate draft: %w", err)
		}
		input.Draft = animedomain.ResolveSecretLocators(input.Draft, current)
		if err := prepare(); err != nil {
			return AnimeConfigCandidate{}, err
		}
	}

	// Resolve the complete effective configuration under the same row lock. A
	// patch appends the next immutable revision; the CAS version still decides
	// concurrency, while the revision number is the historical identity.
	resolved, policy, err := s.resolveCandidateInputTx(ctx, tx, input.Draft)
	if err != nil {
		return AnimeConfigCandidate{}, err
	}
	revision, err := s.appendCandidateRevisionTx(ctx, tx, candidateID, input.Draft, draftDigest, resolved, policy)
	if err != nil {
		return AnimeConfigCandidate{}, err
	}

	row := tx.QueryRowContext(ctx, `
UPDATE anime_config_candidates
SET origin = $2, origin_entry_ref = $3, title = $4, title_override = $5,
    instance_label = $6, draft = $7, draft_digest = $8,
    last_activity = CURRENT_TIMESTAMP, retention_until = CURRENT_TIMESTAMP + INTERVAL '30 days',
    version = version + 1, updated_at = CURRENT_TIMESTAMP,
    current_revision = $10, current_revision_digest = $11
WHERE id = $1 AND version = $9 AND status = 'working' AND activated_managed_anime_id IS NULL
RETURNING `+candidateColumns,
		candidateID, string(input.Draft.Origin), candidateEntryRef(input.Draft),
		strings.TrimSpace(input.Draft.Title), nullableOptionalText(input.Draft.TitleOverride),
		nullableOptionalText(input.Draft.InstanceLabel), draftJSON, draftDigest, expectedVersion,
		revision.Revision, revision.EffectiveDigest)
	candidate, err := scanCandidate(row)
	if err != nil {
		if isCandidateWorkingDuplicate(err) {
			return AnimeConfigCandidate{}, candidateWorkingDuplicateError()
		}
		return AnimeConfigCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return AnimeConfigCandidate{}, fmt.Errorf("commit patch candidate: %w", err)
	}
	return candidate, nil
}

// DeleteAnimeConfigCandidate explicitly authorizes and performs local candidate
// cleanup. An unlinked candidate is deleted with its revision leaves. A candidate
// linked to an active ManagedAnime is protected; after that owner is retired the
// same explicit command may unlink and delete it. A revision a retained business
// config still pins (a retired instance keeps its frozen business as history)
// cannot be deleted: the candidate is archived with cleanup authority and the
// retention purge removes it once nothing pins it. Missing is idempotent success.
func (s *Service) DeleteAnimeConfigCandidate(ctx context.Context, candidateID string) error {
	if s == nil || s.db == nil {
		return errors.New("anime service database is nil")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin candidate cleanup: %w", err)
	}
	defer tx.Rollback()

	var activated, lifecycleState sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT candidate.activated_managed_anime_id, lifecycle.lifecycle_state
FROM anime_config_candidates candidate
LEFT JOIN managed_anime lifecycle
  ON lifecycle.id = candidate.activated_managed_anime_id
WHERE candidate.id = $1
FOR UPDATE OF candidate`, candidateID).Scan(&activated, &lifecycleState)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return fmt.Errorf("lock candidate for cleanup: %w", err)
	}
	if activated.Valid && (!lifecycleState.Valid || lifecycleState.String != "retired") {
		return &apperrors.Business{
			Status: 409, Code: "CANDIDATE_ALREADY_ACTIVATED",
			Message: "候选已关联活跃实例，需先显式退役该实例。",
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_config_candidates
SET activated_managed_anime_id = NULL,
    status = 'archived',
    cleanup_eligible_at = CURRENT_TIMESTAMP,
    cleanup_reason = 'user_deleted',
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1`, candidateID); err != nil {
		return fmt.Errorf("authorize candidate cleanup: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM anime_config_candidates AS candidate
WHERE candidate.id = $1
  AND NOT EXISTS (
      SELECT 1 FROM anime_business_configs AS config
      WHERE config.candidate_id = candidate.id
  )`, candidateID); err != nil {
		return fmt.Errorf("delete eligible candidate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit candidate cleanup: %w", err)
	}
	return nil
}

// ActivateAnimeConfigCandidate activates the draft read under the candidate row
// lock. Draft decoding, validation, Global resolution, idempotency, owner
// creation, and the activation link all stay in that one transaction. The
// caller MUST pass the candidate version it read (If-Match): the activation
// identity includes that expected version, so a stale activation is rejected
// and an exact replay returns the first result even after the version
// advanced. The optional initial state creates a stopped one-off instance: no
// automatic work runs, but explicit manual commands remain allowed.
func (s *Service) ActivateAnimeConfigCandidate(ctx context.Context, candidateID string, expectedVersion int64, idempotencyKey string, input CandidateSubscribeInput) (SubscribeResult, error) {
	return s.activateCandidateOnce(ctx, candidateID, expectedVersion, idempotencyKey, input)
}

// version became. Activation consumes the caller-supplied expected version
// (If-Match) exactly once: the UPDATE is the CAS, so a stale expected version
// or a concurrently activated candidate updates zero rows. The current locked
// version is only used to report the conflict's CurrentVersion.
func linkActivatedCandidate(ctx context.Context, tx *sql.Tx, candidateID, managedAnimeID string, expectedVersion, currentVersion int64) error {
	result, err := tx.ExecContext(ctx, `
UPDATE anime_config_candidates
SET activated_managed_anime_id = $2,
    status = 'dormant',
    version = version + 1,
    last_activity = CURRENT_TIMESTAMP,
    retention_until = CURRENT_TIMESTAMP + INTERVAL '365 days',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND version = $3
  AND status = 'working'
  AND activated_managed_anime_id IS NULL`, candidateID, managedAnimeID, expectedVersion)
	if err != nil {
		return fmt.Errorf("link activated candidate: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read activated candidate update count: %w", err)
	}
	if updated != 1 {
		return &apperrors.VersionConflict{Owner: "anime_config_candidate", CurrentVersion: currentVersion}
	}
	return nil
}

// rowScanner covers both *sql.Row and *sql.Rows so one scan body serves the
// single-row reads and the list loop.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanCandidate(row rowScanner) (AnimeConfigCandidate, error) {
	var candidate AnimeConfigCandidate
	var origin string
	var entryRef, titleOverride, instanceLabel, activated, currentRevisionDigest, cleanupReason sql.NullString
	var currentRevision sql.NullInt64
	var cleanupEligibleAt sql.NullTime
	var draftJSON []byte
	if err := row.Scan(&candidate.Id, &origin, &entryRef, &candidate.Title, &titleOverride,
		&instanceLabel, &draftJSON, &candidate.Version, &activated,
		&candidate.CreatedAt, &candidate.UpdatedAt, &candidate.DraftDigest,
		&currentRevision, &currentRevisionDigest, &candidate.LastActivity,
		&candidate.Status, &candidate.RetentionUntil, &cleanupEligibleAt, &cleanupReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AnimeConfigCandidate{}, err
		}
		return AnimeConfigCandidate{}, fmt.Errorf("scan anime config candidate: %w", err)
	}
	draft, err := animedomain.DecodeDraftJSON(draftJSON)
	if err != nil {
		return AnimeConfigCandidate{}, fmt.Errorf("decode candidate draft: %w", err)
	}
	candidate.Draft = draft
	if currentRevision.Valid {
		value := currentRevision.Int64
		candidate.CurrentRevision = &value
	}
	candidate.CurrentRevisionDigest = nullStringPtr(currentRevisionDigest)
	candidate.Origin = animedomain.SubscriptionOrigin(origin)
	candidate.OriginEntryRef = nullStringPtr(entryRef)
	candidate.TitleOverride = nullStringPtr(titleOverride)
	candidate.InstanceLabel = nullStringPtr(instanceLabel)
	candidate.ActivatedManagedAnimeId = nullStringPtr(activated)
	candidate.CleanupReason = nullStringPtr(cleanupReason)
	if cleanupEligibleAt.Valid {
		value := cleanupEligibleAt.Time
		candidate.CleanupEligibleAt = &value
	}
	return candidate, nil
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func candidateEntryRef(draft animedomain.AnimeDraft) any {
	if draft.Origin == animedomain.SubscriptionOriginCustom || draft.Origin == animedomain.SubscriptionOriginOneshot {
		return nil
	}
	return nullableOptionalText(draft.OriginEntryRef)
}

func isCandidateWorkingDuplicate(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "working_provider")
}

func candidateWorkingDuplicateError() error {
	return &apperrors.Business{
		Status: 409, Code: "CANDIDATE_WORKING_DUPLICATE",
		Message: "该 provider 条目已有 working 草稿，请重新加载最新配置。",
	}
}
