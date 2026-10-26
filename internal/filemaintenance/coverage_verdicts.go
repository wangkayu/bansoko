package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/attrrules"
	"bansoko/internal/batchplan"
	facts "bansoko/internal/release/facts"
)

// inFlightClaim is one non-terminal instance acquisition and the coverage its
// release was accepted with.
type inFlightClaim struct {
	// Group is the stable group identity (provider:group_ref) the running
	// release froze in its compare_values; "" when it froze none.
	Group string
	Keys  []string
}

// CoverageVerdicts answers, read-only, which planned coverage keys the anime
// already holds. Nothing is persisted: the pools are evaluated as
// the replacement decision would, then the plan drops the covered keys before
// the decision is frozen over what remains.
//
//   - all mode: no deduplication at all.
//   - retain/keep: the present placement stays (covered_existing); a
//     present placement that also covers a key outside the candidate file is
//     never replaced by it (replace_partial).
//   - replace with losers: the candidate file is better and replaces it.
//   - empty pool, queue, blocked: the key stays open.
//   - a running instance acquisition of another release that was accepted with
//     the key claims it (covered_in_flight); per_group only within the same
//     stable group identity.
//
// The candidate is the not-yet-admitted release: its title (quality dimensions
// only), its byte size, and the coverage keys and file units of its planned
// videos.
func (s *ReplacementDecisionService) CoverageVerdicts(ctx context.Context, managedAnimeID, releaseID string, candidate acquisitiontarget.CandidateEvidence) (map[string]batchplan.Verdict, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("replacement decision service is not wired")
	}
	if len(candidate.Coverage) == 0 {
		return map[string]batchplan.Verdict{}, nil
	}
	var (
		modeRaw    string
		profileRaw []byte
	)
	err := s.db.QueryRowContext(ctx, `
SELECT admission_mode, quality_profile
FROM anime_business_configs WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL`, managedAnimeID).Scan(&modeRaw, &profileRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]batchplan.Verdict{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read replacement policy: %w", err)
	}
	mode := AdmissionMode(modeRaw)
	if mode == ModeAll {
		return map[string]batchplan.Verdict{}, nil
	}
	profile, err := ParseProfile(profileRaw)
	if err != nil {
		return nil, err
	}
	rules, _, err := attrrules.Current()
	if err != nil {
		return nil, fmt.Errorf("read active attr rules: %w", err)
	}
	projected := facts.CandidateFromCoverage(candidate.Title, rules, candidate.Coverage)
	candidateFacts := Facts{Resolution: projected.Resolution, Revision: projected.Revision, Language: projected.Language, Group: projected.Group, Size: candidate.SizeBytes}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The candidate is not accepted yet: its comparison values come from the
	// same release inputs acceptance freezes.
	source, err := loadReleaseCompareSourceTx(ctx, tx, releaseID)
	if err != nil {
		return nil, err
	}
	candidateFacts = candidateFacts.withFrozenValues(facts.CompareValues(projected, source))
	placements, err := s.loadPlacementFacts(ctx, tx, managedAnimeID)
	if err != nil {
		return nil, err
	}
	scores, err := loadSubgroupScoresTx(ctx, tx, managedAnimeID)
	if err != nil {
		return nil, err
	}
	candidateFacts = candidateFacts.scored(scores)
	for index := range placements {
		placements[index].Facts = placements[index].Facts.scored(scores)
	}
	claims, err := loadInFlightClaimsTx(ctx, tx, managedAnimeID, releaseID)
	if err != nil {
		return nil, err
	}
	candidateRef := PlacementRef{PlacementID: "release:" + releaseID, Version: 1}
	pools := EvaluatePools(mode, candidate.Coverage, candidateFacts, candidateRef, placements, profile, candidate.Units)
	group, _ := candidateFacts.poolGroup()
	return coverageVerdicts(mode, candidate.Coverage, group, pools, claims), nil
}

// loadReleaseCompareSourceTx reads a release's frozen source facts and the
// RSS source identity it arrived through.
func loadReleaseCompareSourceTx(ctx context.Context, tx *sql.Tx, releaseID string) (facts.CompareSource, error) {
	var (
		source             facts.CompareSource
		provider, groupRef sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
SELECT r.source_facts, src.id, src.source_kind, src.provider, src.provider_group_ref
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
WHERE r.id = $1`, releaseID).Scan(&source.SourceFacts, &source.RSSSourceID, &source.SourceKind, &provider, &groupRef)
	if errors.Is(err, sql.ErrNoRows) {
		return facts.CompareSource{}, nil
	}
	if err != nil {
		return facts.CompareSource{}, fmt.Errorf("read release comparison source: %w", err)
	}
	source.Provider, source.ProviderGroupRef = provider.String, groupRef.String
	return source, nil
}

// coverageVerdicts maps the read-only pool evidence and the in-flight claims
// onto one verdict per key. A present placement wins over an in-flight claim
// (both cover the key; the existing one is the more precise reason).
func coverageVerdicts(mode AdmissionMode, keys []string, group string, pools []PoolEvidence, claims []inFlightClaim) map[string]batchplan.Verdict {
	verdicts := make(map[string]batchplan.Verdict, len(keys))
	byKey := make(map[string]PoolEvidence, len(pools))
	for _, pool := range pools {
		key := pool.PoolKey
		if mode == ModePerGroup {
			key, _, _ = strings.Cut(key, "|")
		}
		byKey[key] = pool
	}
	inFlight := map[string]bool{}
	for _, claim := range claims {
		if mode == ModePerGroup && (strings.TrimSpace(group) == "" || claim.Group != group) {
			continue
		}
		for _, key := range claim.Keys {
			inFlight[key] = true
		}
	}
	for _, key := range keys {
		pool, ok := byKey[key]
		switch {
		case ok && pool.Verdict == "keep" && pool.Reason == ReasonReplacementPartialCoverage:
			verdicts[key] = batchplan.VerdictReplacePartial
		case ok && (pool.Verdict == "retain" || pool.Verdict == "keep"):
			verdicts[key] = batchplan.VerdictExisting
		case inFlight[key]:
			verdicts[key] = batchplan.VerdictInFlight
		case ok && pool.Verdict == "replace" && len(pool.Losers) > 0:
			verdicts[key] = batchplan.VerdictReplace
		default:
			verdicts[key] = batchplan.VerdictOpen
		}
	}
	return verdicts
}

// loadInFlightClaimsTx reads the coverage claimed by the anime's other
// non-terminal instance acquisitions. The claim is derived, never stored: a
// running acquisition's release candidate facts carry the plan coverage it was
// accepted with, and the claim disappears the moment the acquisition settles
// (succeeded, failed, cancelled, stalled). A key the release already placed is
// no longer in flight even while the root is still settling: the placement is
// in the pool and the pool decides it (keep or replace). A placement the
// latest calibrated scan reports missing or changed holds no coverage, so its
// key stays claimed. One-shot acquisitions
// are a temporary owner that never enters the library, so they claim nothing.
func loadInFlightClaimsTx(ctx context.Context, tx *sql.Tx, managedAnimeID, releaseID string) ([]inFlightClaim, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT COALESCE((
         SELECT jsonb_agg(k.key ORDER BY k.key)
         FROM jsonb_array_elements_text(f.coverage_keys) AS k(key)
         WHERE NOT EXISTS (
             SELECT 1
             FROM placements p
             JOIN placement_coverage pc ON pc.placement_id = p.id
             WHERE p.managed_anime_id = $1
               AND p.quality_facts_ref = a.source_release_ref
               AND p.state = 'active'
               AND p.verification_state = 'verified'
               AND `+latestObservationIssue+` = ''
               AND pc.observation_key = k.key)
       ), '[]'::jsonb),
       COALESCE((f.compare_values->'subgroup'->>'provider') || ':' || (f.compare_values->'subgroup'->>'group_ref'), '')
FROM acquisitions a
JOIN LATERAL (
    SELECT coverage_keys, compare_values
    FROM release_candidate_facts
    WHERE release_id = a.source_release_ref
    ORDER BY release_version DESC
    LIMIT 1
) f ON true
WHERE a.origin_managed_anime_ref = $1
  AND a.state = 'running'
  AND a.entry_kind <> 'one_shot'
  AND a.source_release_ref IS DISTINCT FROM $2`, managedAnimeID, releaseID)
	if err != nil {
		return nil, fmt.Errorf("read in-flight coverage: %w", err)
	}
	defer rows.Close()
	claims := []inFlightClaim{}
	for rows.Next() {
		var raw []byte
		var claim inFlightClaim
		if err := rows.Scan(&raw, &claim.Group); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &claim.Keys); err != nil {
			return nil, fmt.Errorf("decode in-flight coverage: %w", err)
		}
		claims = append(claims, claim)
	}
	return claims, rows.Err()
}
