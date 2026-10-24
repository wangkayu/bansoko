package application

import (
	"testing"
)

// The group order is 选中 → 分数 → 名字 across every subgroup list
// (subgroups_order.go). These tests pin each comparator's tiers and the
// name tie-break's display-name fallback.
func TestSubgroupScoreEntryOrder(t *testing.T) {
	entries := []SubgroupScoreEntry{
		{Provider: "mikan", GroupRef: "g-d", DisplayName: "澪标", Score: 90},
		{Provider: "mikan", GroupRef: "g-c", DisplayName: "SB", Score: 100},
		{Provider: "mikan", GroupRef: "g-b", DisplayName: "桜都", Score: 100},
		{Provider: "mikan", GroupRef: "g-a", DisplayName: "LoliHouse", Score: 100, EffectiveSelected: true},
		{Provider: "mikan", GroupRef: "g-e", DisplayName: "", Score: 100, EffectiveSelected: true},
	}
	// ASCII names sort before CJK ones in byte order.
	want := []string{"g-e", "g-a", "g-c", "g-b", "g-d"}
	sortSubgroupScoreEntries(entries)
	for index, key := range want {
		if entries[index].GroupRef != key {
			t.Fatalf("entry %d = %s, want %s", index, entries[index].GroupRef, key)
		}
	}
}

func TestProviderGroupSelectionOrder(t *testing.T) {
	rows := []ProviderGroupSelection{
		{Provider: "mikan", GroupRef: "unavailable-selected", DisplayName: "失落", EffectiveSelected: true, EffectiveScore: 100},
		{Provider: "mikan", GroupRef: "selected", DisplayName: "在选", EffectiveSelected: true, EffectiveScore: 60},
		{Provider: "mikan", GroupRef: "high", DisplayName: "高分", EffectiveScore: 100},
		{Provider: "mikan", GroupRef: "low", DisplayName: "低分", EffectiveScore: 10},
	}
	// The read guards the tier with availability (readPersistedSelections);
	// an unavailable row's stored flag must not keep it in the selected tier.
	rows[0].EffectiveSelected = rows[0].EffectiveSelected && false
	// Within the unselected tier the name key is byte order, so 失落 sorts
	// before 高分.
	want := []string{"selected", "unavailable-selected", "high", "low"}
	sortProviderGroupSelections(rows)
	for index, key := range want {
		if rows[index].GroupRef != key {
			t.Fatalf("row %d = %s, want %s", index, rows[index].GroupRef, key)
		}
	}
}

func TestSubgroupSelectionPreviewFactOrder(t *testing.T) {
	fifty := 50
	hundred := 100
	facts := []SubgroupSelectionPreviewFact{
		{Provider: "mikan", GroupRef: "unscored"},
		{Provider: "mikan", GroupRef: "scored-high", EffectiveSelected: true, ScoreBreakdown: &SubgroupScoreBreakdown{FinalScore: &hundred}},
		{Provider: "mikan", GroupRef: "scored-low", ScoreBreakdown: &SubgroupScoreBreakdown{FinalScore: &fifty}},
		{Provider: "mikan", GroupRef: "selected", EffectiveSelected: true},
	}
	// Within the selected tier the score decides; a decision without a final
	// score ranks below any scored group of its tier.
	want := []string{"scored-high", "selected", "scored-low", "unscored"}
	sortSubgroupSelectionPreviewFacts(facts)
	for index, key := range want {
		if facts[index].GroupRef != key {
			t.Fatalf("fact %d = %s, want %s", index, facts[index].GroupRef, key)
		}
	}
}
