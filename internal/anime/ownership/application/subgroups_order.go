package application

import (
	"sort"
	"strings"
)

// Every group list in the product answers in one order: 选中 → 分数 → 名字.
// A selected group lists before an unselected one, a higher effective score
// next, and the display name breaks the remaining ties (group_ref as the
// final deterministic fallback). This covers the owner tab's effective
// selection, the section the editor reads back, both selection previews and
// the owner's RSS source rows; the readmodel's group ordering
// (rss_list_view.go) carries the same policy for the RSS tabs.

// subgroupOrderNameKey is the name tier of the group order: the display name,
// or the group_ref when a row carries none, compared case-insensitively.
func subgroupOrderNameKey(displayName, groupRef string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = groupRef
	}
	return strings.ToLower(name) + "\x00" + groupRef
}

func subgroupScoreEntryBefore(a, b SubgroupScoreEntry) bool {
	if a.EffectiveSelected != b.EffectiveSelected {
		return a.EffectiveSelected
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return subgroupOrderNameKey(a.DisplayName, a.GroupRef) < subgroupOrderNameKey(b.DisplayName, b.GroupRef)
}

func providerGroupSelectionBefore(a, b ProviderGroupSelection) bool {
	if a.EffectiveSelected != b.EffectiveSelected {
		return a.EffectiveSelected
	}
	if a.EffectiveScore != b.EffectiveScore {
		return a.EffectiveScore > b.EffectiveScore
	}
	return subgroupOrderNameKey(a.DisplayName, a.GroupRef) < subgroupOrderNameKey(b.DisplayName, b.GroupRef)
}

func subgroupSelectionPreviewFactBefore(a, b SubgroupSelectionPreviewFact) bool {
	if a.EffectiveSelected != b.EffectiveSelected {
		return a.EffectiveSelected
	}
	if scoreOf(a.ScoreBreakdown) != scoreOf(b.ScoreBreakdown) {
		return scoreOf(a.ScoreBreakdown) > scoreOf(b.ScoreBreakdown)
	}
	// A preview fact carries no display name, so the name tier is the group_ref.
	return a.GroupRef < b.GroupRef
}

// scoreOf reads the breakdown's final score; a decision without one ranks
// below any scored group of its tier.
func scoreOf(breakdown *SubgroupScoreBreakdown) int {
	if breakdown == nil || breakdown.FinalScore == nil {
		return -1
	}
	return *breakdown.FinalScore
}

func sortSubgroupScoreEntries(entries []SubgroupScoreEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return subgroupScoreEntryBefore(entries[i], entries[j]) })
}

func sortProviderGroupSelections(rows []ProviderGroupSelection) {
	sort.SliceStable(rows, func(i, j int) bool { return providerGroupSelectionBefore(rows[i], rows[j]) })
}

func sortSubgroupSelectionPreviewFacts(facts []SubgroupSelectionPreviewFact) {
	sort.SliceStable(facts, func(i, j int) bool { return subgroupSelectionPreviewFactBefore(facts[i], facts[j]) })
}
