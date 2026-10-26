package filemaintenance

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"bansoko/internal/qualityprofile"
)

// Facts are the comparable facts of one candidate/placement: the release
// title parse (evidence only; the legacy fields also feed the typed approval
// evidence — the title Group never pools), the frozen comparison values and
// subgroup source (the stable group identity per_group pools by and
// subgroup.score looks up), the subscription's score for that source
// (resolved per evaluation), and the release-level size.
type Facts struct {
	Revision   string                         `json:"revision,omitempty"`
	Resolution string                         `json:"resolution,omitempty"`
	Source     string                         `json:"source,omitempty"`
	Language   string                         `json:"language,omitempty"`
	Group      string                         `json:"group,omitempty"`
	Size       *int64                         `json:"size_bytes,omitempty"`
	Values     map[string][]string            `json:"values,omitempty"`
	Subgroup   *qualityprofile.SubgroupSource `json:"subgroup,omitempty"`
	Score      *int64                         `json:"score,omitempty"`
}

func (f Facts) subject() qualityprofile.Subject {
	return qualityprofile.Subject{Values: f.Values, Score: f.Score, Size: f.Size}
}

// withFrozenValues attaches a release's stored compare_values document.
func (f Facts) withFrozenValues(raw []byte) Facts {
	values := qualityprofile.ParseValues(raw)
	f.Values = values.Values
	f.Subgroup = values.Subgroup
	return f
}

// poolGroup is the stable group identity per_group pools by: the frozen
// subgroup source (provider, group ref), or (custom, rss source id) for a
// custom RSS source — the same value subgroup.score reads, never the title.
func (f Facts) poolGroup() (string, bool) {
	if f.Subgroup == nil {
		return "", false
	}
	return f.Subgroup.Key(), true
}

// SubgroupScores are a subscription's effective scores by provider group.
type SubgroupScores map[qualityprofile.SubgroupSource]int64

// scored resolves the subscription's score of the facts' subgroup source; no
// source or no score leaves it nil (ranks lowest).
func (f Facts) scored(scores SubgroupScores) Facts {
	f.Score = nil
	if f.Subgroup == nil {
		return f
	}
	if score, ok := scores[*f.Subgroup]; ok {
		f.Score = &score
	}
	return f
}

// Profile is the ordered comparison profile on the policy row.
type Profile = qualityprofile.Profile

// ParseProfile decodes a stored policy profile; a stored profile that is not
// a valid profile-v1 fails the evaluation closed.
func ParseProfile(raw json.RawMessage) (Profile, error) {
	profile, err := qualityprofile.Parse(raw)
	if err != nil {
		return Profile{}, fmt.Errorf("stored quality profile invalid: %w", err)
	}
	return profile, nil
}

// profileForMode drops the subgroup score in per_group mode: the candidate
// competes only inside its own group, so the score is inert.
func profileForMode(mode AdmissionMode, profile Profile) Profile {
	if mode == ModePerGroup {
		return profile.Without(qualityprofile.KeySubgroupScore)
	}
	return profile
}

// Comparison is one comparison verdict with its per-step evidence: Better
// when the candidate wins an item or, all items equal, on a strictly larger
// size; Equal when nothing decides (the caller keeps the existing file).
type Comparison struct {
	Better  bool                  `json:"better"`
	Equal   bool                  `json:"equal"`
	Reason  string                `json:"reason,omitempty"`
	Decided string                `json:"decided,omitempty"`
	Steps   []qualityprofile.Step `json:"steps,omitempty"`
}

// Compare compares the candidate with one current file item by item
// (qualityprofile.Compare): the first differing item decides, then size, then
// the existing file stays.
func Compare(candidate, current Facts, profile Profile) Comparison {
	result := qualityprofile.Compare(candidate.subject(), current.subject(), profile)
	comparison := Comparison{Better: result.Better, Equal: result.Equal, Decided: result.Decided, Steps: result.Steps}
	switch {
	case result.Equal:
		comparison.Reason = "equal: existing kept"
	case result.Better:
		comparison.Reason = "better on " + result.Decided
	default:
		comparison.Reason = "worse on " + result.Decided
	}
	return comparison
}

// AdmissionMode is the retention admission mode.
type AdmissionMode string

const (
	ModeAll        AdmissionMode = "all"
	ModePerGroup   AdmissionMode = "per_group"
	ModeSingleCopy AdmissionMode = "single_copy"
)

// SortCoverage canonicalizes the coverage set for stable idempotency keys.
func SortCoverage(coverage []string) []string {
	sorted := append([]string(nil), coverage...)
	sort.Strings(sorted)
	return sorted
}

// PlacementRef is the frozen placement identity evidence (id + version +
// fingerprint) settlement revalidates before any effect.
type PlacementRef struct {
	PlacementID string `json:"placement_id"`
	Version     int64  `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

// PlacementFacts couples one verified active placement with its server-read
// quality facts. FactsOK false means the placement has no release chain: a
// scan-managed file no release has claimed yet. It has no group identity and
// no comparison values, so any candidate is better than it, and per_group
// pools it under every group of its coverage keys.
type PlacementFacts struct {
	PlacementID string
	Version     int64
	Fingerprint string
	Coverage    []string
	Facts       Facts
	FactsOK     bool
	CreatedAt   time.Time
}

// PoolEvidence is one per-pool frozen outcome. Winner is the placement that
// owns the pool after the decision; losers are the exact placements settle may
// supersede. Blocked is reserved for the pool-building layer: coverage_unresolved,
// group_missing (current_winner_conflict is a defensive fail-closed only).
type PoolEvidence struct {
	PoolKey     string                        `json:"pool_key"`
	Verdict     string                        `json:"verdict"` // replace | retain | keep | blocked | queue
	Reason      string                        `json:"reason,omitempty"`
	Winner      *PlacementRef                 `json:"winner,omitempty"`
	Losers      []PlacementRef                `json:"losers,omitempty"`
	Comparisons []FrozenReplacementComparison `json:"comparisons,omitempty"`
	// Steps are the comparison steps that decided the pool: against the
	// member the candidate failed to beat (retain), or the strongest member
	// it beat (replace).
	Steps []qualityprofile.Step `json:"steps,omitempty"`
	// SubgroupScores are the subgroup scores the comparison used (only when
	// the profile compares subgroup.score); settle and reuse recheck them.
	SubgroupScores []FrozenSubgroupScore `json:"subgroup_scores,omitempty"`
}

// EvaluatePools builds the frozen per-pool evidence for one candidate. The
// candidate placement is already admitted (active) and may appear in
// placements; it is excluded from its own pool's members.
//   - all: no pools, no losers, verdict queue.
//   - per_group: pool key = coverage + "|" + stable group identity
//     (provider:group_ref, or custom:<rss source id>); the candidate competes
//     with placements of the same group identity and with placements that
//     have none (a scan-managed file no release claimed belongs to every
//     group's pool of its keys); the subgroup score item is inert.
//   - single_copy: pool key = coverage across groups.
//   - unresolvable coverage or a per_group candidate without a stable group
//     block the affected pool with a typed reason (pool-building layer); a
//     member without a group never blocks, and comparisons never block. The
//     decision never degrades modes or guesses.
//
// units maps a coverage key to the keys of the candidate file that holds it
// (a key absent from units is its own unit). One file replaces only what it
// covers:
//   - a present placement that also covers a key outside the unit (an existing
//     multi-episode file the candidate covers only partly) is never a loser:
//     the pool keeps it (replacement_partial_coverage);
//   - a multi-episode file replaces only when every key would: when any pool
//     of the unit keeps its present placement, the unit's other pools with
//     losers keep theirs too (replacement_unit_kept). An empty pool (a key the
//     library lacks) is still filled.
func EvaluatePools(mode AdmissionMode, coverage []string, candidate Facts, candidateRef PlacementRef, placements []PlacementFacts, profile Profile, units map[string][]string) []PoolEvidence {
	if len(coverage) == 0 || (len(coverage) == 1 && coverage[0] == "unresolved") {
		return []PoolEvidence{{PoolKey: "all", Verdict: "blocked", Reason: "coverage_unresolved"}}
	}
	if mode == ModeAll {
		return []PoolEvidence{{PoolKey: "all", Verdict: "queue", Reason: "all-mode keeps every Rules-passing placement"}}
	}
	profile = profileForMode(mode, profile)
	group, hasGroup := candidate.poolGroup()
	if mode == ModePerGroup && !hasGroup {
		// No stable group identity: every per_group pool is blocked, never
		// compared across groups.
		pools := make([]PoolEvidence, 0, len(coverage))
		for _, key := range SortCoverage(coverage) {
			pools = append(pools, PoolEvidence{PoolKey: key, Verdict: "blocked", Reason: "group_missing"})
		}
		return pools
	}
	byKey := make(map[string][]PlacementFacts, len(placements))
	for _, placement := range placements {
		if placement.PlacementID == candidateRef.PlacementID {
			continue
		}
		for _, key := range placement.Coverage {
			byKey[key] = append(byKey[key], placement)
		}
	}
	membersOf := poolMembers(byKey, mode, group)
	pools := make([]PoolEvidence, 0, len(coverage))
	for _, key := range SortCoverage(coverage) {
		poolKey := key
		members := membersOf(key)
		if mode == ModePerGroup {
			poolKey = key + "|" + group
		}
		pool := evaluatePool(poolKey, candidate, candidateRef, members, profile)
		if pool.Verdict == "replace" && len(pool.Losers) > 0 && outsideUnit(members, unitOf(units, key)) {
			pool = keepPresent(pool, members, profile, ReasonReplacementPartialCoverage)
		}
		pools = append(pools, pool)
	}
	pools = applyUnitKept(pools, mode, units, membersOf, profile)
	if profileUsesSubgroupScore(profile) {
		for index := range pools {
			key := pools[index].PoolKey
			if mode == ModePerGroup {
				key, _, _ = strings.Cut(key, "|")
			}
			if members := membersOf(key); len(members) > 0 {
				pools[index].SubgroupScores = frozenSubgroupScores(candidate, members)
			}
		}
	}
	return pools
}

// FrozenSubgroupScore is one subgroup score a pool comparison used: the
// subscription's effective score of a (provider, group_ref) source at decision
// time; nil = the source had no score. A later score edit makes the decision
// stale (subgroup_score_changed): the scores are read live, not versioned.
type FrozenSubgroupScore struct {
	Provider string `json:"provider"`
	GroupRef string `json:"group_ref"`
	Score    *int64 `json:"score"`
}

func profileUsesSubgroupScore(profile Profile) bool {
	for _, item := range profile.Items {
		if item.Key == qualityprofile.KeySubgroupScore {
			return true
		}
	}
	return false
}

// frozenSubgroupScores lists the scores of the candidate's and the members'
// subgroup sources, sorted, once each.
func frozenSubgroupScores(candidate Facts, members []PlacementFacts) []FrozenSubgroupScore {
	seen := map[qualityprofile.SubgroupSource]bool{}
	out := []FrozenSubgroupScore{}
	add := func(facts Facts) {
		if facts.Subgroup == nil || seen[*facts.Subgroup] {
			return
		}
		seen[*facts.Subgroup] = true
		entry := FrozenSubgroupScore{Provider: facts.Subgroup.Provider, GroupRef: facts.Subgroup.GroupRef}
		if facts.Score != nil {
			score := *facts.Score
			entry.Score = &score
		}
		out = append(out, entry)
	}
	add(candidate)
	for _, member := range members {
		add(comparableFacts(member))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].GroupRef < out[j].GroupRef
	})
	return out
}

// subgroupScoresChanged reports whether any frozen score differs from the
// current scores (a removed selection counts as no score).
func subgroupScoresChanged(frozen []FrozenSubgroupScore, current SubgroupScores) bool {
	for _, entry := range frozen {
		score, ok := current[qualityprofile.SubgroupSource{Provider: entry.Provider, GroupRef: entry.GroupRef}]
		switch {
		case entry.Score == nil && ok, entry.Score != nil && !ok:
			return true
		case entry.Score != nil && *entry.Score != score:
			return true
		}
	}
	return false
}

// poolsSubgroupScores gathers the frozen scores of every pool of a decision.
func poolsSubgroupScores(pools []PoolEvidence) []FrozenSubgroupScore {
	out := []FrozenSubgroupScore{}
	for _, pool := range pools {
		out = append(out, pool.SubgroupScores...)
	}
	return out
}

// Pool reasons of the per-file replacement rules.
const (
	ReasonReplacementPartialCoverage = "replacement_partial_coverage"
	ReasonReplacementUnitKept        = "replacement_unit_kept"
)

// unitOf returns the candidate file keys that hold key; a key without a unit
// is its own.
func unitOf(units map[string][]string, key string) []string {
	if unit := units[key]; len(unit) > 0 {
		return unit
	}
	return []string{key}
}

// outsideUnit reports whether any member covers a key outside the unit.
func outsideUnit(members []PlacementFacts, unit []string) bool {
	inUnit := make(map[string]bool, len(unit))
	for _, key := range unit {
		inUnit[key] = true
	}
	for _, member := range members {
		for _, key := range member.Coverage {
			if !inUnit[key] {
				return true
			}
		}
	}
	return false
}

// keepPresent turns a replace pool into keep: the present dominant member
// stays the winner and nothing is a loser.
func keepPresent(pool PoolEvidence, members []PlacementFacts, profile Profile, reason string) PoolEvidence {
	winner, ok := dominantMember(members, profile)
	if !ok {
		return PoolEvidence{PoolKey: pool.PoolKey, Verdict: "blocked", Reason: "current_winner_conflict"}
	}
	return PoolEvidence{PoolKey: pool.PoolKey, Verdict: "keep", Reason: reason, Winner: &winner, Steps: pool.Steps}
}

// poolMembers returns the pool members of one coverage key under the mode
// (the candidate excluded). per_group pools by coverage plus the stable group
// identity: placements of the candidate's group identity compete, and so does
// a placement without one (no release chain, or no frozen subgroup source):
// it belongs to every group's pool of its keys, so any group's candidate may
// replace it.
func poolMembers(byKey map[string][]PlacementFacts, mode AdmissionMode, group string) func(key string) []PlacementFacts {
	return func(key string) []PlacementFacts {
		list := byKey[key]
		if mode != ModePerGroup {
			return list
		}
		filtered := make([]PlacementFacts, 0, len(list))
		for _, member := range list {
			memberGroup, ok := comparableFacts(member).poolGroup()
			if !ok || memberGroup == group {
				filtered = append(filtered, member)
			}
		}
		return filtered
	}
}

// applyUnitKept enforces that a multi-episode candidate replaces only when
// every key of its file would: if any pool of a unit retains or keeps its
// present placement, the unit's replace pools that have losers keep theirs.
func applyUnitKept(pools []PoolEvidence, mode AdmissionMode, units map[string][]string, membersOf func(string) []PlacementFacts, profile Profile) []PoolEvidence {
	byKey := make(map[string]int, len(pools))
	for index, pool := range pools {
		key := pool.PoolKey
		if mode == ModePerGroup {
			key, _, _ = strings.Cut(key, "|")
		}
		byKey[key] = index
	}
	for index := range pools {
		pool := pools[index]
		if pool.Verdict != "replace" || len(pool.Losers) == 0 {
			continue
		}
		key := pool.PoolKey
		if mode == ModePerGroup {
			key, _, _ = strings.Cut(key, "|")
		}
		for _, sibling := range unitOf(units, key) {
			other, ok := byKey[sibling]
			if !ok || sibling == key {
				continue
			}
			if verdict := pools[other].Verdict; verdict == "retain" || verdict == "keep" {
				pools[index] = keepPresent(pool, membersOf(key), profile, ReasonReplacementUnitKept)
				break
			}
		}
	}
	return pools
}

// evaluatePool decides one pool: the candidate must be strictly better than
// every member (compareWithMember) to replace; a member without release facts
// (FactsOK false) loses to any candidate and never blocks. A
// non-replace pool freezes its dominant current winner (ties resolve to the
// incumbent by created_at, then id) and never guesses a winner.
func evaluatePool(poolKey string, candidate Facts, candidateRef PlacementRef, members []PlacementFacts, profile Profile) PoolEvidence {
	if len(members) == 0 {
		return PoolEvidence{
			PoolKey: poolKey, Verdict: "replace", Reason: "no current placement in pool",
			Winner: &PlacementRef{PlacementID: candidateRef.PlacementID, Version: candidateRef.Version, Fingerprint: candidateRef.Fingerprint},
		}
	}
	for _, member := range members {
		comparison := compareWithMember(candidate, member, profile)
		if comparison.Better {
			continue
		}
		winner, ok := dominantMember(members, profile)
		if !ok {
			return PoolEvidence{PoolKey: poolKey, Verdict: "blocked", Reason: "current_winner_conflict"}
		}
		return PoolEvidence{PoolKey: poolKey, Verdict: "retain", Reason: comparison.Reason, Winner: &winner, Steps: comparison.Steps}
	}
	// Every member is beaten: the evidence is the comparison against the
	// strongest member.
	strongest, ok := dominantMember(members, profile)
	if !ok {
		return PoolEvidence{PoolKey: poolKey, Verdict: "blocked", Reason: "current_winner_conflict"}
	}
	decisive := Comparison{}
	losers := make([]PlacementRef, 0, len(members))
	for _, member := range members {
		if member.PlacementID == strongest.PlacementID {
			decisive = compareWithMember(candidate, member, profile)
		}
		losers = append(losers, PlacementRef{PlacementID: member.PlacementID, Version: member.Version, Fingerprint: member.Fingerprint})
	}
	candidateRefCopy := candidateRef
	return PoolEvidence{
		PoolKey: poolKey, Verdict: "replace", Reason: decisive.Reason,
		Winner: &candidateRefCopy, Losers: losers, Steps: decisive.Steps,
	}
}

// ReasonExistingWithoutFacts is the comparison reason against a pool member
// without release facts (a scan-managed file no release has claimed).
const ReasonExistingWithoutFacts = "existing file has no release facts"

// compareWithMember compares the candidate with one pool member. A member
// without release facts has no group and no values: any candidate is better
// than it, whatever the profile.
func compareWithMember(candidate Facts, member PlacementFacts, profile Profile) Comparison {
	if !member.FactsOK {
		return Comparison{Better: true, Reason: ReasonExistingWithoutFacts, Decided: "facts"}
	}
	return Compare(candidate, member.Facts, profile)
}

// comparableFacts is a member's facts; a member without resolvable facts
// compares with no values (it ranks lowest on every item).
func comparableFacts(placement PlacementFacts) Facts {
	if !placement.FactsOK {
		return Facts{}
	}
	return placement.Facts
}

// dominantMember returns the member better than or equal to every other
// member; ties resolve to the incumbent (earliest created_at, then id).
// Facts-missing members participate without values. When no member dominates
// (defensive only) the pool is blocked — fail closed, never a guessed winner.
func dominantMember(members []PlacementFacts, profile Profile) (PlacementRef, bool) {
	sorted := append([]PlacementFacts(nil), members...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].PlacementID < sorted[j].PlacementID
	})
	for _, candidate := range sorted {
		dominates := true
		for _, other := range sorted {
			if candidate.PlacementID == other.PlacementID {
				continue
			}
			comparison := Compare(comparableFacts(candidate), comparableFacts(other), profile)
			if !(comparison.Better || comparison.Equal) {
				dominates = false
				break
			}
		}
		if dominates {
			return PlacementRef{PlacementID: candidate.PlacementID, Version: candidate.Version, Fingerprint: candidate.Fingerprint}, true
		}
	}
	return PlacementRef{}, false
}

// poolsVerdict aggregates the per-pool outcomes into the whole-decision
// verdict: any blocked pool blocks the decision; otherwise any replace pool
// replaces; otherwise an all-mode queue stays queue; otherwise the candidate
// keeps the current winners.
func poolsVerdict(pools []PoolEvidence) string {
	anyBlocked, anyReplace, anyQueue := false, false, false
	for _, pool := range pools {
		switch pool.Verdict {
		case "blocked":
			anyBlocked = true
		case "replace":
			anyReplace = true
		case "queue":
			anyQueue = true
		}
	}
	if anyBlocked {
		return "blocked"
	}
	if anyReplace {
		return "replace"
	}
	if anyQueue {
		return "queue"
	}
	return "keep"
}
