package domain

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
)

func observedByKey(decisions []GroupSelectionDecision) map[string]GroupSelectionDecision {
	byKey := make(map[string]GroupSelectionDecision, len(decisions))
	for _, decision := range decisions {
		byKey[decision.Group.Ref.ExternalID] = decision
	}
	return byKey
}

func boolPtr(value bool) *bool { return &value }

// A group the provider listed but that has no explicit policy still takes part
// in selection through the provider default: this is what a not-yet-subscribed
// draft needs so its discovered groups can be listed and selected at all.
func TestRankObservedGroupsAppliesProviderDefaultAndMaxRSS(t *testing.T) {
	policy := DiscoveryPolicy{
		MaxRSS: 2,
		ProviderDefaults: map[providerdomain.Kind]ProviderDefaultPolicy{
			providerdomain.ProviderMikan: {Mode: Ranked, Score: 50, Version: 1},
		},
	}
	groups := []ObservedGroup{
		{ExternalID: "30", Name: "Group C", Available: true},
		{ExternalID: "10", Name: "Group A", Available: true},
		{ExternalID: "20", Name: "Group B", Available: true},
	}
	decisions, err := RankObservedGroups(providerdomain.ProviderMikan, groups, policy)
	if err != nil {
		t.Fatalf("RankObservedGroups: %v", err)
	}
	got := observedByKey(decisions)
	if len(got) != 3 {
		t.Fatalf("decisions = %d, want one per observed group", len(got))
	}
	// Equal default scores fall back to the key order the engine already uses.
	if got["10"].Outcome != GroupSelected || got["20"].Outcome != GroupSelected {
		t.Fatalf("the two lowest keys fill max_rss: %+v", got)
	}
	if got["30"].Outcome != GroupNotSelected || got["30"].Reason != "max_rss_exhausted" {
		t.Fatalf("the third group exceeds max_rss: %+v", got["30"])
	}
	if got["10"].Group.PolicySource != SourceProviderDefault {
		t.Fatalf("an unconfigured group must resolve through the provider default: %+v", got["10"].Group)
	}
}

func TestRankObservedGroupsNameKeywordLiftsAnUnconfiguredGroup(t *testing.T) {
	policy := DiscoveryPolicy{
		MaxRSS: 1,
		ProviderDefaults: map[providerdomain.Kind]ProviderDefaultPolicy{
			providerdomain.ProviderMikan: {Mode: Ranked, Score: 50, Version: 1},
		},
		NameKeywords: map[providerdomain.Kind][]NameKeywordRule{
			providerdomain.ProviderMikan: {{Keyword: "chs", Score: 30}},
		},
	}
	decisions, err := RankObservedGroups(providerdomain.ProviderMikan, []ObservedGroup{
		{ExternalID: "1", Name: "Plain Group", Available: true},
		{ExternalID: "2", Name: "Fansub CHS", Available: true},
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	got := observedByKey(decisions)
	if got["2"].Outcome != GroupSelected || got["2"].Group.Score != 80 {
		t.Fatalf("keyword group must score 80 and take the only slot: %+v", got["2"])
	}
	if got["1"].Outcome != GroupNotSelected {
		t.Fatalf("plain group loses the slot: %+v", got["1"])
	}
}

func TestRankObservedGroupsHonoursManualOverrideAndAvailability(t *testing.T) {
	policy := DiscoveryPolicy{
		MaxRSS: 5,
		ProviderDefaults: map[providerdomain.Kind]ProviderDefaultPolicy{
			providerdomain.ProviderAniBT: {Mode: Ranked, Score: 50, Version: 1},
		},
	}
	decisions, err := RankObservedGroups(providerdomain.ProviderAniBT, []ObservedGroup{
		{ExternalID: "blocked", Name: "Blocked", Available: true, Manual: boolPtr(false)},
		{ExternalID: "pinned", Name: "Pinned", Available: true, Manual: boolPtr(true), CountInLimit: true},
		{ExternalID: "gone", Name: "Gone", Available: false},
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	got := observedByKey(decisions)
	if got["blocked"].Outcome != GroupNotSelected || got["blocked"].Reason != "policy_never" {
		t.Fatalf("manual false is never: %+v", got["blocked"])
	}
	if got["pinned"].Outcome != GroupSelected || got["pinned"].Reason != "policy_always" {
		t.Fatalf("manual true is always: %+v", got["pinned"])
	}
	if got["gone"].Outcome != GroupUnavailable {
		t.Fatalf("an unavailable group is never selected: %+v", got["gone"])
	}
}

func TestRankObservedGroupsRejectsDuplicateExternalIDs(t *testing.T) {
	_, err := RankObservedGroups(providerdomain.ProviderMikan, []ObservedGroup{
		{ExternalID: "1", Name: "A", Available: true},
		{ExternalID: "1", Name: "B", Available: true},
	}, DiscoveryPolicy{MaxRSS: 1})
	if err == nil {
		t.Fatal("two observations of the same group must be rejected, not merged silently")
	}
}
