package application

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/selection/domain"
)

// A pinned score is the final score: the site default and every keyword stop
// applying to that group, and the pin decides its rank.
func TestLivePolicyPinnedScoreOverridesDefaultAndKeywords(t *testing.T) {
	site := `{"entries":[],"max_rss":1,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[],` +
		`"pinned_scores":[{"group_ref":"1","score":95},{"group_ref":"3","score":10}]}},` +
		`"name_keywords":{"mikan":[{"keyword":"简繁","score":40}]}}`
	got := liveDecisions(t, `{"entries":[],"max_rss":1}`, site,
		map[string]string{"1": "A 字幕组", "2": "B 简繁", "3": "C 简繁"})
	if got["1"].Outcome != domain.GroupSelected || got["1"].Group.Score != 95 {
		t.Fatalf("the pinned 95 group wins over the 90 keyword group: %+v", got["1"])
	}
	if b := got["1"].Group.Breakdown; b.PinnedScore == nil || *b.PinnedScore != 95 || len(b.Keywords) != 0 {
		t.Fatalf("pinned breakdown: %+v", b)
	}
	if got["2"].Group.Score != 90 || got["2"].Group.Breakdown.PinnedScore != nil {
		t.Fatalf("an unpinned group keeps default plus keywords: %+v", got["2"].Group)
	}
	if got["3"].Group.Score != 10 || len(got["3"].Group.Breakdown.Keywords) != 0 {
		t.Fatalf("a pin below the default ignores its matching keyword: %+v", got["3"].Group)
	}
	for _, ref := range []string{"1", "2", "3"} {
		assertBreakdownSums(t, got[ref])
	}
}

func TestLivePolicyPinnedGroupStillGatedBySiteList(t *testing.T) {
	blacklist := `{"entries":[],"max_rss":5,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":["1"],` +
		`"pinned_scores":[{"group_ref":"1","score":100}]}}}`
	got := liveDecisions(t, `{"entries":[],"max_rss":5}`, blacklist, map[string]string{"1": "One", "2": "Two"})
	if got["1"].Outcome != domain.GroupNotSelected || got["1"].Reason != domain.ReasonSiteBlacklisted {
		t.Fatalf("a blacklisted pinned group stays excluded: %+v", got["1"])
	}
	if b := got["1"].Group.Breakdown; b.PinnedScore != nil || b.FinalScore != nil {
		t.Fatalf("an excluded group has no pinned or final score: %+v", b)
	}

	whitelist := `{"entries":[],"max_rss":5,"provider_defaults":{"mikan":{"mode":"whitelist","score":50,"groups":["2"],` +
		`"pinned_scores":[{"group_ref":"1","score":100},{"group_ref":"2","score":80}]}}}`
	got = liveDecisions(t, `{"entries":[],"max_rss":5}`, whitelist, map[string]string{"1": "One", "2": "Two"})
	if got["1"].Outcome != domain.GroupNotSelected || got["1"].Reason != domain.ReasonSiteNotWhitelisted {
		t.Fatalf("a pinned group outside the whitelist stays unselected: %+v", got["1"])
	}
	if got["2"].Outcome != domain.GroupSelected || got["2"].Group.Score != 80 {
		t.Fatalf("a whitelisted pinned group ranks at its pin: %+v", got["2"])
	}
	assertBreakdownSums(t, got["2"])
}

func TestLivePolicyManualEntryWinsOverPinnedScore(t *testing.T) {
	instance := `{"max_rss":5,"entries":[` +
		`{"provider":"mikan","group_ref":"1","display_name":"One","selection_mode":"manual","manual_selected":false,"effective_selected":false,"score":1}]}`
	site := `{"entries":[],"max_rss":5,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[],` +
		`"pinned_scores":[{"group_ref":"1","score":100}]}}}`
	got := liveDecisions(t, instance, site, map[string]string{"1": "One", "2": "Two"})
	if got["1"].Outcome != domain.GroupNotSelected || got["1"].Reason != domain.ReasonPolicyNever {
		t.Fatalf("a manual exclude wins over the pin: %+v", got["1"])
	}
	if b := got["1"].Group.Breakdown; b == nil || b.EntryOverride != domain.EntryOverrideNever || b.PinnedScore != nil || b.FinalScore != nil {
		t.Fatalf("a manually decided group shows no score: %+v", b)
	}
}

// The frozen representation (witnesses) never reads pins.
func TestFrozenPolicyIgnoresPinnedScores(t *testing.T) {
	section := `{"entries":[],"max_rss":1,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[],` +
		`"pinned_scores":[{"group_ref":"1","score":100}]}}}`
	policy, err := DiscoveryPolicyFromSubgroupsSection([]byte(section), 1)
	if err != nil {
		t.Fatal(err)
	}
	if pinned := policy.ProviderDefaults[providerdomain.ProviderMikan].Pinned; len(pinned) != 0 {
		t.Fatalf("frozen policy carries pins: %+v", pinned)
	}
}
