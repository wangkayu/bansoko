package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"bansoko/internal/batchplan"
	"bansoko/internal/filekinds"
	"bansoko/internal/subscription/competition"
)

// competitionCandidate is one release of the instance the competition
// compares: a release waiting for it, or a fallback candidate (a filtered
// release whose latest plan skipped a video as covered_in_flight).
type competitionCandidate struct {
	releaseID    string
	version      int64
	discoveredAt time.Time
	awaiting     bool
	keys         []string
	units        map[string][]string
	facts        Facts
	group        string
	pools        []PoolEvidence
}

// roundClaim is one key a candidate took in the current round: from then on
// it claims the key in flight for every other candidate.
type roundClaim struct {
	releaseID string
	group     string
	key       string
}

// CompetitionDecisions decides one competition round of a subscription
// instance inside the caller's transaction (read-only, or the commit
// transaction that holds the subscription row lock). Only the instance's own
// releases, managed files and running downloads take part: nothing is read
// across instances.
//
// The candidates are the instance's releases from a source in scope (custom,
// or an available selected group) with no download task that either wait for
// the competition or are fallback candidates (filtered, in the re-evaluation
// scope, latest plan with a covered_in_flight video). A candidate's keys and
// file units come from the keyed videos of its latest frozen plan (planned,
// covered_in_flight or covered_existing); its comparison values from its
// latest candidate facts, scored with the subscription's current scores.
//
//   - all mode, or no replacement policy: every waiting release is admitted
//     with no verdict (every key open); there are no fallback candidates.
//   - otherwise each pool (the key, or the key and the stable group identity
//     in per_group) is decided in pool order: its candidates ranked by the
//     policy profile (per_group without the score), then the earlier
//     discovery, then the id; the first whose coverage verdict of the key
//     (placements via EvaluatePools, running downloads and the keys taken
//     earlier in the round) is open or replace takes the key. A candidate's
//     final verdicts are its coverage verdicts against every claim but its
//     own, so at most one candidate takes a pool; the others read in_flight,
//     existing or replace_partial there.
//
// Every waiting release gets a decision (admitted or not); a fallback
// candidate only when it is admitted. Decisions are ordered by release id.
//
// excluded names releases the caller leaves out of this round (their decided
// stage could not finish): they are no candidate, so they take no key and
// keep no other candidate out.
func (s *ReplacementDecisionService) CompetitionDecisions(ctx context.Context, tx *sql.Tx, managedAnimeID string, excluded map[string]bool) (competition.Round, error) {
	if s == nil || tx == nil {
		return competition.Round{}, errors.New("subscription competition is not wired")
	}
	var subscriptionState string
	err := tx.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE managed_anime_id = $1`, managedAnimeID).Scan(&subscriptionState)
	if errors.Is(err, sql.ErrNoRows) {
		return competition.Round{Reason: "subscription_missing"}, nil
	}
	if err != nil {
		return competition.Round{}, fmt.Errorf("read subscription: %w", err)
	}
	if subscriptionState != "active" {
		return competition.Round{Reason: "subscription_inactive"}, nil
	}
	var (
		modeRaw    string
		profileRaw []byte
	)
	mode := ModeAll
	err = tx.QueryRowContext(ctx, `
SELECT admission_mode, quality_profile
FROM anime_business_configs WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL`, managedAnimeID).Scan(&modeRaw, &profileRaw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return competition.Round{}, fmt.Errorf("read replacement policy: %w", err)
	default:
		mode = AdmissionMode(modeRaw)
	}
	candidates, err := loadCompetitionCandidatesTx(ctx, tx, managedAnimeID)
	if err != nil {
		return competition.Round{}, err
	}
	if len(excluded) > 0 {
		kept := candidates[:0]
		for _, candidate := range candidates {
			if !excluded[candidate.releaseID] {
				kept = append(kept, candidate)
			}
		}
		candidates = kept
	}
	if mode == ModeAll {
		round := competition.Round{}
		for _, candidate := range candidates {
			if candidate.awaiting {
				round.Decisions = append(round.Decisions, competition.Decision{ReleaseID: candidate.releaseID, Version: candidate.version, Awaiting: true, Admitted: true, Verdicts: map[string]batchplan.Verdict{}})
			}
		}
		return finishRound(round), nil
	}
	if len(candidates) == 0 {
		return competition.Round{Reason: "no_candidate"}, nil
	}
	profile, err := ParseProfile(profileRaw)
	if err != nil {
		return competition.Round{}, err
	}
	placements, err := s.loadPlacementFacts(ctx, tx, managedAnimeID)
	if err != nil {
		return competition.Round{}, err
	}
	scores, err := loadSubgroupScoresTx(ctx, tx, managedAnimeID)
	if err != nil {
		return competition.Round{}, err
	}
	for index := range placements {
		placements[index].Facts = placements[index].Facts.scored(scores)
	}
	running, err := loadInFlightClaimsTx(ctx, tx, managedAnimeID, "")
	if err != nil {
		return competition.Round{}, err
	}
	rank := profileForMode(mode, profile)
	pools := map[string][]*competitionCandidate{}
	poolKeyOf := map[string]string{}
	for index := range candidates {
		candidate := &candidates[index]
		candidate.facts = candidate.facts.scored(scores)
		candidate.group, _ = candidate.facts.poolGroup()
		candidate.pools = EvaluatePools(mode, candidate.keys, candidate.facts, PlacementRef{PlacementID: "release:" + candidate.releaseID, Version: 1}, placements, profile, candidate.units)
		for _, key := range candidate.keys {
			pool := key
			if mode == ModePerGroup {
				pool = key + "|" + candidate.group
			}
			pools[pool] = append(pools[pool], candidate)
			poolKeyOf[pool] = key
		}
	}
	var taken []roundClaim
	claimsFor := func(releaseID string) []inFlightClaim {
		claims := append([]inFlightClaim(nil), running...)
		for _, claim := range taken {
			if claim.releaseID != releaseID {
				claims = append(claims, inFlightClaim{Group: claim.group, Keys: []string{claim.key}})
			}
		}
		return claims
	}
	poolNames := make([]string, 0, len(pools))
	for pool := range pools {
		poolNames = append(poolNames, pool)
	}
	sort.Strings(poolNames)
	for _, pool := range poolNames {
		key := poolKeyOf[pool]
		ranked := append([]*competitionCandidate(nil), pools[pool]...)
		sort.SliceStable(ranked, func(i, j int) bool { return competitionRanksAbove(ranked[i], ranked[j], rank) })
		for _, candidate := range ranked {
			verdict := coverageVerdicts(mode, candidate.keys, candidate.group, candidate.pools, claimsFor(candidate.releaseID))[key]
			if verdict == batchplan.VerdictOpen || verdict == batchplan.VerdictReplace {
				taken = append(taken, roundClaim{releaseID: candidate.releaseID, group: candidate.group, key: key})
				break
			}
		}
	}
	round := competition.Round{}
	for _, candidate := range candidates {
		verdicts := coverageVerdicts(mode, candidate.keys, candidate.group, candidate.pools, claimsFor(candidate.releaseID))
		admitted := len(candidate.keys) == 0
		for _, verdict := range verdicts {
			if verdict == batchplan.VerdictOpen || verdict == batchplan.VerdictReplace {
				admitted = true
			}
		}
		if !candidate.awaiting && !admitted {
			continue
		}
		round.Decisions = append(round.Decisions, competition.Decision{ReleaseID: candidate.releaseID, Version: candidate.version, Awaiting: candidate.awaiting, Admitted: admitted, Exhaustive: true, Verdicts: verdicts})
	}
	return finishRound(round), nil
}

func finishRound(round competition.Round) competition.Round {
	sort.Slice(round.Decisions, func(i, j int) bool { return round.Decisions[i].ReleaseID < round.Decisions[j].ReleaseID })
	if len(round.Decisions) == 0 && round.Reason == "" {
		round.Reason = "no_candidate"
	}
	return round
}

// competitionRanksAbove orders two candidates of one pool: the policy profile
// decides, then the earlier discovery, then the id.
func competitionRanksAbove(a, b *competitionCandidate, profile Profile) bool {
	comparison := Compare(a.facts, b.facts, profile)
	if comparison.Better {
		return true
	}
	if !comparison.Equal {
		return false
	}
	if !a.discoveredAt.Equal(b.discoveredAt) {
		return a.discoveredAt.Before(b.discoveredAt)
	}
	return a.releaseID < b.releaseID
}

// loadCompetitionCandidatesTx reads the competition candidates of one
// instance (see CompetitionDecisions) with their latest frozen plan and
// candidate facts. A candidate without a frozen plan cannot be compared and
// is left out (fail closed); release evaluation always freezes one.
func loadCompetitionCandidatesTx(ctx context.Context, tx *sql.Tx, managedAnimeID string) ([]competitionCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT r.id, r.version, r.discovered_at, r.verdict = 'awaiting_competition', m.plan,
       COALESCE(f.resolution, ''), COALESCE(f.revision, ''), COALESCE(f.source, ''),
       COALESCE(f.language, ''), COALESCE(f."group", ''), f.size_bytes,
       COALESCE(f.compare_values, '{}'::jsonb)
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
LEFT JOIN provider_group_selections selection
  ON selection.subscription_id = src.subscription_id AND selection.provider = src.provider
 AND selection.group_ref = src.provider_group_ref
JOIN LATERAL (
    SELECT release_facts->'batch_plan' AS plan
    FROM media_fact_snapshots
    WHERE release_id = r.id
    ORDER BY created_at DESC, id DESC
    LIMIT 1
) m ON true
LEFT JOIN LATERAL (
    SELECT resolution, revision, source, language, "group", size_bytes, compare_values
    FROM release_candidate_facts
    WHERE release_id = r.id
    ORDER BY release_version DESC
    LIMIT 1
) f ON true
WHERE sub.managed_anime_id = $1
  AND (src.source_kind <> 'provider_group' OR COALESCE(selection.available AND selection.effective_selected, FALSE))
  AND r.download_task_id IS NULL
  AND jsonb_typeof(m.plan) = 'object'
  AND (r.verdict = 'awaiting_competition'
       OR (r.verdict NOT IN ('adopted_existing', 'acquisition_failed', 'resume_baseline', 'start_baseline')
           AND jsonb_path_exists(m.plan, '$.files[*] ? (@.outcome == "covered_in_flight")')))
ORDER BY r.discovered_at, r.id`, managedAnimeID)
	if err != nil {
		return nil, fmt.Errorf("read competition candidates: %w", err)
	}
	defer rows.Close()
	out := []competitionCandidate{}
	for rows.Next() {
		var (
			candidate                                     competitionCandidate
			planRaw, compareRaw                           []byte
			resolution, revision, source, language, title string
			size                                          sql.NullInt64
		)
		if err := rows.Scan(&candidate.releaseID, &candidate.version, &candidate.discoveredAt, &candidate.awaiting, &planRaw,
			&resolution, &revision, &source, &language, &title, &size, &compareRaw); err != nil {
			return nil, err
		}
		var plan batchplan.Plan
		if err := json.Unmarshal(planRaw, &plan); err != nil {
			continue
		}
		candidate.keys, candidate.units = competitionKeys(plan)
		if !candidate.awaiting && len(candidate.keys) == 0 {
			continue
		}
		candidate.facts = Facts{Resolution: resolution, Revision: revision, Source: source, Language: language, Group: title}
		if size.Valid {
			candidate.facts.Size = &size.Int64
		}
		candidate.facts = candidate.facts.withFrozenValues(compareRaw)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

// competitionKeys are the sorted coverage keys and file units of a plan's
// keyed videos: planned ones, and the ones an earlier round covered by a
// present file or a running download (a fallback candidate's plan is the
// deduplicated one). A video excluded by the rules or without an episode
// identity never competes.
func competitionKeys(plan batchplan.Plan) ([]string, map[string][]string) {
	seen := map[string]bool{}
	units := map[string][]string{}
	for _, file := range plan.Files {
		if file.Kind != filekinds.KindVideo {
			continue
		}
		switch file.Outcome {
		case batchplan.OutcomePlanned, batchplan.OutcomeCoveredInFlight, batchplan.OutcomeCoveredExisting:
		default:
			continue
		}
		fileKeys := file.Keys()
		for _, key := range fileKeys {
			seen[key] = true
			units[key] = mergeCompetitionUnit(units[key], fileKeys)
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, units
}

func mergeCompetitionUnit(current, keys []string) []string {
	set := map[string]bool{}
	for _, key := range current {
		set[key] = true
	}
	for _, key := range keys {
		set[key] = true
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
