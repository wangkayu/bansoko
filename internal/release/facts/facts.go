// Package facts owns release-derived candidate and media fact contracts,
// parsers, application/domain mappings, and persistence facades. PostgreSQL
// handles and SQL live in adapter/persistence subpackages.
package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"bansoko/internal/attrrules"
	"bansoko/internal/persistence/txscope"
	persistence "bansoko/internal/release/facts/adapter/persistence/candidate"
	factsapplication "bansoko/internal/release/facts/application"
)

// ReleaseCandidateFacts is the application-owned durable versioned fact row.
type ReleaseCandidateFacts = factsapplication.ReleaseCandidateFacts

// Repository is the application-owned facts port. Transactional writes use an
// opaque caller-owned scope; the PostgreSQL implementation lives in the
// release/facts/adapter/persistence/candidate package.
type Repository = factsapplication.Repository

// PGRepository is the application-facing PostgreSQL candidate-facts facade.
// It contains no SQL or database handle; the adapter owns those details. The
// concrete type and constructor remain for existing composition callers.
type PGRepository struct {
	adapter *persistence.Repository
}

// NewPGRepository wires the candidate-facts PostgreSQL adapter behind the
// application-facing facade.
func NewPGRepository(db *sql.DB) *PGRepository {
	return &PGRepository{adapter: persistence.NewRepository(db)}
}

// UpsertForRelease delegates the caller-owned transaction to the persistence
// adapter without exposing database/sql at the application port.
func (r *PGRepository) UpsertForRelease(ctx context.Context, scope txscope.Tx, facts ReleaseCandidateFacts) error {
	if r == nil || r.adapter == nil {
		return factsapplication.ErrRepositoryUnavailable
	}
	return r.adapter.UpsertForRelease(ctx, scope, toPersistenceFacts(facts))
}

// GetForRelease returns the latest facts for a release.
func (r *PGRepository) GetForRelease(ctx context.Context, releaseID string) (*ReleaseCandidateFacts, error) {
	if r == nil || r.adapter == nil {
		return nil, factsapplication.ErrRepositoryUnavailable
	}
	facts, err := r.adapter.GetForRelease(ctx, releaseID)
	if err != nil || facts == nil {
		return nil, err
	}
	value := fromPersistenceFacts(*facts)
	return &value, nil
}

// GetVersion returns one versioned facts row.
func (r *PGRepository) GetVersion(ctx context.Context, releaseID string, version int64) (*ReleaseCandidateFacts, error) {
	if r == nil || r.adapter == nil {
		return nil, factsapplication.ErrRepositoryUnavailable
	}
	facts, err := r.adapter.GetVersion(ctx, releaseID, version)
	if err != nil || facts == nil {
		return nil, err
	}
	value := fromPersistenceFacts(*facts)
	return &value, nil
}

// EnsureCurrentProfile refreshes facts using the adapter-owned transaction
// lifecycle while delegating parsing and business recomputation to this parent
// package.
func (r *PGRepository) EnsureCurrentProfile(ctx context.Context, releaseID string) error {
	if r == nil || r.adapter == nil {
		return factsapplication.ErrRepositoryUnavailable
	}
	return r.adapter.EnsureCurrentProfile(ctx, releaseID, recomputePersistenceFacts)
}

// EnsureCurrentProfile is the explicit current-aware write helper. The
// compatibility signature remains while transaction ownership and SQL stay in
// the persistence adapter.
func EnsureCurrentProfile(ctx context.Context, db *sql.DB, releaseID string) error {
	return persistence.EnsureCurrentProfile(ctx, db, releaseID, recomputePersistenceFacts)
}

// EnsureSelectedEvidenceProjection materializes the versioned candidate-facts
// projection from a frozen selected-evidence identity the Release evaluation
// already resolved, without waiting for the Release owner writer's media fact
// snapshot. Replacement admission consumes the same evidence the target plan
// consumes, so a first accepted evaluation never has to depend on a later
// asynchronous projection. The write is idempotent and shares the
// candidate-facts lock/version discipline, so a later owner projection of the
// same profile is a no-op.
func EnsureSelectedEvidenceProjection(ctx context.Context, db *sql.DB, releaseID, title string, coverage []string, size *int64) error {
	return persistence.EnsureSelectedEvidenceProjection(ctx, db, releaseID, title, coverage, size, recomputePersistenceFacts)
}

func recomputePersistenceFacts(existing persistence.CandidateFacts, rules attrrules.Rules, coverage *[]string, source persistence.CompareSource) (persistence.CandidateFacts, bool, error) {
	updated, changed, err := RecomputeCurrentProfile(fromPersistenceFacts(existing), rules, coverage, CompareSource(source))
	return toPersistenceFacts(updated), changed, err
}

func toPersistenceFacts(facts ReleaseCandidateFacts) persistence.CandidateFacts {
	return persistence.CandidateFacts{
		ReleaseID:        facts.ReleaseID,
		ReleaseVersion:   facts.ReleaseVersion,
		ParserVersion:    facts.ParserVersion,
		InputFingerprint: facts.InputFingerprint,
		RawEvidence:      facts.RawEvidence,
		Facts:            facts.Facts,
		CoverageKeys:     facts.CoverageKeys,
		Resolution:       facts.Resolution,
		Revision:         facts.Revision,
		Source:           facts.Source,
		Language:         facts.Language,
		Group:            facts.Group,
		SizeBytes:        facts.SizeBytes,
		CreatedAt:        facts.CreatedAt,
		CompareValues:    facts.CompareValues,
	}
}

func fromPersistenceFacts(facts persistence.CandidateFacts) ReleaseCandidateFacts {
	return ReleaseCandidateFacts{
		ReleaseID:        facts.ReleaseID,
		ReleaseVersion:   facts.ReleaseVersion,
		ParserVersion:    facts.ParserVersion,
		InputFingerprint: facts.InputFingerprint,
		RawEvidence:      facts.RawEvidence,
		Facts:            facts.Facts,
		CoverageKeys:     facts.CoverageKeys,
		Resolution:       facts.Resolution,
		Revision:         facts.Revision,
		Source:           facts.Source,
		Language:         facts.Language,
		Group:            facts.Group,
		SizeBytes:        facts.SizeBytes,
		CreatedAt:        facts.CreatedAt,
		CompareValues:    facts.CompareValues,
	}
}

// RecomputeCurrentProfile is the pure current-profile projection used by the
// lazy writer. Coverage comes exclusively from the frozen BatchPlan coverage
// keys (the release media fact snapshot); quality dimensions are refreshed from
// the current attr profile. The title is never re-parsed into an episode: a nil
// coverage (no frozen plan) is a deliberate no-op rather than a second coverage
// authority.
func RecomputeCurrentProfile(existing ReleaseCandidateFacts, rules attrrules.Rules, coverage *[]string, source CompareSource) (ReleaseCandidateFacts, bool, error) {
	if coverage == nil {
		return existing, false, nil
	}
	var evidence struct {
		Title string `json:"title"`
	}
	if len(existing.RawEvidence) == 0 || json.Unmarshal(existing.RawEvidence, &evidence) != nil || strings.TrimSpace(evidence.Title) == "" {
		return existing, false, nil
	}
	candidate := CandidateFromCoverage(evidence.Title, rules, *coverage)
	factsJSON, err := json.Marshal(map[string]any{
		"resolution": candidate.Resolution, "quality": candidate.Quality,
		"language": candidate.Language, "group": candidate.Group,
		"special": candidate.Special, "season": candidate.Season, "episode": candidate.Episode,
		"coverage_unresolved": candidate.CoverageUnresolved, "unresolved_reason": candidate.UnresolvedReason,
	})
	if err != nil {
		return ReleaseCandidateFacts{}, false, err
	}
	refreshed := existing
	refreshed.ParserVersion = candidate.ParserVersion
	refreshed.InputFingerprint = InputFingerprint(evidence.Title, nil, false)
	refreshed.Facts = factsJSON
	refreshed.CoverageKeys = candidate.Coverage
	refreshed.Resolution = candidate.Resolution
	refreshed.Revision = candidate.Revision
	refreshed.Language = candidate.Language
	refreshed.Group = candidate.Group
	refreshed.CompareValues = CompareValues(candidate, source)
	return refreshed, true, nil
}
