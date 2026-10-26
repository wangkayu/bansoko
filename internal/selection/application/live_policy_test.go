package application

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/selection/domain"
)

func liveDecisions(t *testing.T, instance, site string, groups map[string]string) map[string]domain.GroupSelectionDecision {
	t.Helper()
	sitePolicy := DefaultSitePolicy()
	if site != "" {
		var err error
		sitePolicy, err = SitePolicyFromSubgroupsSection([]byte(site))
		if err != nil {
			t.Fatal(err)
		}
	}
	policy, err := LiveDiscoveryPolicy([]byte(instance), 2, sitePolicy)
	if err != nil {
		t.Fatalf("LiveDiscoveryPolicy: %v", err)
	}
	observed := make([]domain.ObservedGroup, 0, len(groups))
	for ref, name := range groups {
		observed = append(observed, domain.ObservedGroup{ExternalID: ref, Name: name, Available: true})
	}
	decisions, err := domain.RankObservedGroups(providerdomain.ProviderMikan, observed, policy)
	if err != nil {
		t.Fatalf("RankObservedGroups: %v", err)
	}
	result := map[string]domain.GroupSelectionDecision{}
	for _, decision := range decisions {
		result[decision.Group.Ref.ExternalID] = decision
	}
	return result
}

func assertBreakdownSums(t *testing.T, decision domain.GroupSelectionDecision) {
	t.Helper()
	b := decision.Group.Breakdown
	if b == nil || b.FinalScore == nil {
		t.Fatalf("a ranked group needs a breakdown with a final score: %+v", decision.Group)
	}
	sum := b.SiteDefaultScore
	if b.PinnedScore != nil {
		if len(b.Keywords) != 0 {
			t.Fatalf("a pinned group applies no keyword: %+v", b)
		}
		sum = *b.PinnedScore
	}
	for _, keyword := range b.Keywords {
		sum += keyword.Score
	}
	if sum != *b.FinalScore || *b.FinalScore != decision.Group.Score {
		t.Fatalf("breakdown %+v does not sum to the decision score %d", b, decision.Group.Score)
	}
}

func TestLivePolicyBlacklistedGroupIsNeverSelected(t *testing.T) {
	got := liveDecisions(t, `{"entries":[],"max_rss":5}`,
		`{"entries":[],"max_rss":1,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":["180"]}}}`,
		map[string]string{"180": "Blocked", "181": "Other"})
	if got["180"].Outcome != domain.GroupNotSelected || got["180"].Reason != domain.ReasonSiteBlacklisted {
		t.Fatalf("a blacklisted group is excluded by the site list: %+v", got["180"])
	}
	if b := got["180"].Group.Breakdown; b == nil || b.ListEffect != domain.ListEffectBlacklisted || b.SiteMode != domain.SiteBlacklist || b.FinalScore != nil {
		t.Fatalf("blacklist breakdown: %+v", b)
	}
	if got["181"].Outcome != domain.GroupSelected {
		t.Fatalf("an unlisted group is ranked: %+v", got["181"])
	}
	assertBreakdownSums(t, got["181"])
}

func TestLivePolicyWhitelistSelectsOnlyListedGroups(t *testing.T) {
	got := liveDecisions(t, `{"entries":[],"max_rss":5}`,
		`{"entries":[],"max_rss":5,"provider_defaults":{"mikan":{"mode":"whitelist","score":70,"groups":["1","2"]}}}`,
		map[string]string{"1": "One", "2": "Two", "3": "Three"})
	for _, ref := range []string{"1", "2"} {
		if got[ref].Outcome != domain.GroupSelected || got[ref].Group.Breakdown.ListEffect != domain.ListEffectWhitelisted || got[ref].Group.Score != 70 {
			t.Fatalf("whitelisted %s is ranked at the site score: %+v", ref, got[ref])
		}
		assertBreakdownSums(t, got[ref])
	}
	if got["3"].Outcome != domain.GroupNotSelected || got["3"].Reason != domain.ReasonSiteNotWhitelisted {
		t.Fatalf("an unlisted group under a whitelist is excluded: %+v", got["3"])
	}
}

func TestLivePolicyNameKeywordsChangeRankingAndSumToTheScore(t *testing.T) {
	site := `{"entries":[],"max_rss":1,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[]}},` +
		`"name_keywords":{"mikan":[{"keyword":"简繁","score":20},{"keyword":"raw","score":-30}]}}`
	got := liveDecisions(t, `{"entries":[],"max_rss":1}`, site,
		map[string]string{"1": "A 字幕组", "2": "B 简繁 字幕组", "3": "C RAW"})
	if got["2"].Outcome != domain.GroupSelected || got["2"].Group.Score != 70 {
		t.Fatalf("the +20 keyword group wins the only slot: %+v", got["2"])
	}
	if got["1"].Reason != domain.ReasonMaxRSSExhausted || got["1"].Group.Score != 50 {
		t.Fatalf("the plain group loses the slot: %+v", got["1"])
	}
	if got["3"].Group.Score != 20 {
		t.Fatalf("a negative keyword lowers the score: %+v", got["3"])
	}
	for _, ref := range []string{"1", "2", "3"} {
		assertBreakdownSums(t, got[ref])
	}
	if keywords := got["2"].Group.Breakdown.Keywords; len(keywords) != 1 || keywords[0].Keyword != "简繁" || keywords[0].Score != 20 {
		t.Fatalf("matched keywords: %+v", keywords)
	}
}

// A per-anime manual intent wins over the site list, and an automatic entry's
// stored score is not an input.
func TestLivePolicyManualEntryWinsAndAutoEntryScoreIsIgnored(t *testing.T) {
	instance := `{"max_rss":1,"entries":[` +
		`{"provider":"mikan","group_ref":"180","display_name":"Blocked","selection_mode":"manual","manual_selected":true,"effective_selected":true,"score":1},` +
		`{"provider":"mikan","group_ref":"2","display_name":"Two","selection_mode":"auto","manual_selected":null,"effective_selected":true,"score":99}]}`
	site := `{"entries":[],"max_rss":1,"provider_defaults":{"mikan":{"mode":"blacklist","score":40,"groups":["180"]}}}`
	got := liveDecisions(t, instance, site, map[string]string{"180": "Blocked", "1": "One", "2": "Two"})
	if got["180"].Outcome != domain.GroupSelected || got["180"].Reason != domain.ReasonPolicyAlways {
		t.Fatalf("a manual include wins over the blacklist: %+v", got["180"])
	}
	if b := got["180"].Group.Breakdown; b == nil || b.EntryOverride != domain.EntryOverrideAlways || b.ListEffect != domain.ListEffectBlacklisted {
		t.Fatalf("override breakdown keeps the site fact: %+v", b)
	}
	if got["2"].Group.Score != 40 {
		t.Fatalf("an automatic entry scores at the site default, not its stored 99: %+v", got["2"].Group)
	}
}

func TestSitePolicyChangedIgnoresNewDraftDefaults(t *testing.T) {
	base := `{"entries":[],"max_rss":3,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[]}}}`
	cases := []struct {
		name string
		next string
		want bool
	}{
		{"max_rss only", `{"entries":[],"max_rss":9,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[]}}}`, false},
		{"entries only", `{"entries":[{"provider":"mikan","group_ref":"1","display_name":"G","selection_mode":"manual","manual_selected":true,"effective_selected":true,"score":1}],"max_rss":3,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[]}}}`, false},
		{"list", `{"entries":[],"max_rss":3,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":["180"]}}}`, true},
		{"mode", `{"entries":[],"max_rss":3,"provider_defaults":{"mikan":{"mode":"whitelist","score":50,"groups":[]}}}`, true},
		{"score", `{"entries":[],"max_rss":3,"provider_defaults":{"mikan":{"mode":"blacklist","score":60,"groups":[]}}}`, true},
		{"keywords", `{"entries":[],"max_rss":3,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[]}},"name_keywords":{"mikan":[{"keyword":"x","score":5}]}}`, true},
		{"pin", `{"entries":[],"max_rss":3,"provider_defaults":{"mikan":{"mode":"blacklist","score":50,"groups":[],"pinned_scores":[{"group_ref":"7","score":90}]}}}`, true},
	}
	for _, tc := range cases {
		got, err := SitePolicyChanged([]byte(base), []byte(tc.next))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("%s: changed=%t, want %t", tc.name, got, tc.want)
		}
	}
	// A first save compares against the code default.
	if got, err := SitePolicyChanged(nil, []byte(`{"entries":[],"max_rss":3}`)); err != nil || got {
		t.Fatalf("a first save without site parts is no change: %t %v", got, err)
	}
}
