package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/attrrules"
	"bansoko/internal/persistence/txscope"
	generated "bansoko/internal/release/facts/adapter/persistence/candidate/generated"
)

// currentProfileRecomputer is supplied by the owner facade so parser and
// business recomputation remain outside this PostgreSQL adapter. The frozen
// placeable coverage (the media fact snapshot's BatchPlan coverage keys) is
// passed through so coverage can only be projected, never re-parsed from the
// title; nil means the release has no frozen plan.
type currentProfileRecomputer func(CandidateFacts, attrrules.Rules, *[]string, CompareSource) (CandidateFacts, bool, error)

// CompareSource is the frozen release input of the comparison values beyond
// the title: the Release source_facts and the RSS source identity it arrived
// through. Field order and types mirror the owner's facts.CompareSource.
type CompareSource struct {
	SourceFacts      []byte
	SourceKind       string
	Provider         string
	ProviderGroupRef string
}

// EnsureCurrentProfile lazily appends a candidate-facts projection when the
// latest row was parsed with an older attr-rules profile. The adapter owns
// PostgreSQL SQL and transaction lifecycle; recomputation is delegated to the
// parent callback. The callback must be non-nil so this helper fails closed.
func (r *Repository) EnsureCurrentProfile(ctx context.Context, releaseID string, recompute currentProfileRecomputer) error {
	if r == nil {
		return ErrRepositoryUnavailable
	}
	return ensureCurrentProfile(ctx, r.db, releaseID, recompute)
}

// EnsureCurrentProfile is the explicit current-aware write helper. It remains
// outside the read service so read paths stay side-effect free.
func EnsureCurrentProfile(ctx context.Context, db *sql.DB, releaseID string, recompute currentProfileRecomputer) error {
	return ensureCurrentProfile(ctx, db, releaseID, recompute)
}

func ensureCurrentProfile(ctx context.Context, db *sql.DB, releaseID string, recompute currentProfileRecomputer) error {
	if db == nil {
		return errors.New("candidate facts database is nil")
	}
	if recompute == nil {
		return errors.New("candidate facts profile recomputer is nil")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin candidate facts profile refresh: %w", err)
	}
	defer tx.Rollback()

	// Candidate facts are a projection of the selected parser's frozen evidence.
	// Read that evidence (the latest release media fact snapshot identity) before
	// touching the candidate row: a release with no frozen selected evidence is
	// legacy and stays read-only here. Re-parsing the title would make this
	// writer a second coverage authority, which the automatic path forbids.
	frozenCoverage, hasFrozen, err := loadFrozenCoverage(ctx, tx, releaseID)
	if err != nil {
		return fmt.Errorf("read frozen release coverage: %w", err)
	}
	if !hasFrozen {
		return nil
	}
	if err := projectCandidateFactsTx(ctx, tx, releaseID, nil, frozenCoverage, nil, recompute); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsureSelectedEvidenceProjection materializes the versioned candidate-facts
// projection directly from a frozen selected-evidence identity supplied by the
// caller (the Release evaluation that already resolved the one selection),
// instead of the latest media fact snapshot. Replacement admission uses it so a
// pre-acquisition decision is frozen from the same evidence the target plan
// consumes, rather than depending on the Release owner writer's later
// projection. The write is idempotent and shares the candidate-facts
// lock/version discipline with the lazy profile writer: a later owner
// projection of the same profile is a no-op, so a candidate-facts row that
// arrives after the decision cannot move the frozen decision.
func EnsureSelectedEvidenceProjection(ctx context.Context, db *sql.DB, releaseID, title string, coverage []string, size *int64, recompute currentProfileRecomputer) error {
	if db == nil {
		return errors.New("candidate facts database is nil")
	}
	if recompute == nil {
		return errors.New("candidate facts profile recomputer is nil")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin candidate facts projection: %w", err)
	}
	defer tx.Rollback()
	frozen := append([]string{}, coverage...)
	if err := projectCandidateFactsTx(ctx, tx, releaseID, &title, &frozen, size, recompute); err != nil {
		return err
	}
	return tx.Commit()
}

// projectCandidateFactsTx writes the versioned candidate-facts projection of
// one frozen release identity. title overrides the Release raw_facts title (the
// frozen-evidence path already carries it); nil reads it from the locked
// Release row. size, when non-nil, is the release-level byte size the caller
// already resolved; the lazy profile refresh leaves an existing size untouched.
// The Release row is locked first so initial materialization and lazy profile
// upgrades serialize with the Release owner writer's own projection.
func projectCandidateFactsTx(ctx context.Context, tx *sql.Tx, releaseID string, title *string, coverage *[]string, size *int64, recompute currentProfileRecomputer) error {
	if coverage == nil {
		return nil
	}
	rules, _, err := attrrules.Current()
	if err != nil {
		// There is no safe parser when startup has not published an active
		// profile. Do not invent a fallback profile or mutate facts.
		return fmt.Errorf("read active attr rules: %w", err)
	}
	profileVersion, err := attrrules.CurrentProfileVersion()
	if err != nil {
		return fmt.Errorf("read current attr rules profile version: %w", err)
	}
	queries := generated.New(tx)
	rawFacts, err := queries.LockReleaseRawFacts(ctx, releaseID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read release raw facts: %w", err)
	}

	var latest CandidateFacts
	latestRow, err := queries.LockLatestCandidateFacts(ctx, releaseID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read latest candidate facts: %w", err)
	}
	hasLatest := err == nil
	if hasLatest {
		latest = fromGenerated(latestRow)
		// A row of the current profile is final unless it predates frozen
		// comparison values (written before they existed); that row is
		// projected once more so comparisons read the release's values.
		if latest.ParserVersion == profileVersion && hasCompareValues(latest.CompareValues) {
			return nil
		}
	}

	evidenceTitle := ""
	if title != nil {
		evidenceTitle = strings.TrimSpace(*title)
	} else {
		// The source of truth for the title is always the Release's raw_facts,
		// not a possibly stale title copied into the old candidate-facts row.
		var envelope struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(rawFacts, &envelope); err != nil || strings.TrimSpace(envelope.Title) == "" {
			return fmt.Errorf("release %s has no usable raw_facts.title; candidate facts projection is blocked", releaseID)
		}
		evidenceTitle = strings.TrimSpace(envelope.Title)
	}
	if evidenceTitle == "" {
		return fmt.Errorf("release %s has no release title; candidate facts projection is blocked", releaseID)
	}
	rawEvidence, err := json.Marshal(map[string]any{"title": evidenceTitle})
	if err != nil {
		return fmt.Errorf("encode candidate facts raw evidence: %w", err)
	}
	if !hasLatest {
		latest = CandidateFacts{ReleaseID: releaseID}
	}
	latest.RawEvidence = rawEvidence
	source, err := loadCompareSource(ctx, tx, releaseID)
	if err != nil {
		return fmt.Errorf("read release comparison source: %w", err)
	}
	refreshed, changed, err := recompute(latest, rules, coverage, source)
	if err != nil {
		return fmt.Errorf("recompute candidate facts: %w", err)
	}
	if !changed {
		return nil
	}
	refreshed.ReleaseID = releaseID
	if hasLatest {
		refreshed.ReleaseVersion = latest.ReleaseVersion + 1
	} else {
		refreshed.ReleaseVersion = 1
	}
	if size != nil {
		refreshed.SizeBytes = size
	}
	if err := (&Repository{}).UpsertForRelease(ctx, txscope.Wrap(tx), refreshed); err != nil {
		return fmt.Errorf("persist refreshed candidate facts: %w", err)
	}
	return nil
}

// loadCompareSource reads the frozen source facts of the Release and the RSS
// source identity it arrived through.
func loadCompareSource(ctx context.Context, tx *sql.Tx, releaseID string) (CompareSource, error) {
	var (
		source             CompareSource
		provider, groupRef sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
SELECT r.source_facts, src.source_kind, src.provider, src.provider_group_ref
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
WHERE r.id = $1`, releaseID).Scan(&source.SourceFacts, &source.SourceKind, &provider, &groupRef)
	if err != nil {
		return CompareSource{}, err
	}
	source.Provider = provider.String
	source.ProviderGroupRef = groupRef.String
	return source, nil
}

// hasCompareValues reports whether a stored compare_values document carries
// a version (the empty document is "no values").
func hasCompareValues(raw json.RawMessage) bool {
	var probe struct {
		Version string `json:"version"`
	}
	return len(raw) > 0 && json.Unmarshal(raw, &probe) == nil && probe.Version != ""
}

// loadFrozenCoverage reads the placeable coverage keys frozen in the BatchPlan
// of the latest media fact snapshot. It returns ok=false when the release has
// no snapshot or the snapshot carries no plan, so the caller leaves the
// candidate facts untouched instead of re-deriving coverage.
func loadFrozenCoverage(ctx context.Context, tx *sql.Tx, releaseID string) (*[]string, bool, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `
SELECT release_facts->'batch_plan'->'coverage_keys'
FROM media_fact_snapshots
WHERE release_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1`, releaseID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	keys := []string{}
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, false, fmt.Errorf("decode frozen release coverage: %w", err)
	}
	return &keys, true, nil
}
