package domain

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
)

func TestResolveProviderGroupPolicyPinnedScoreIsTheFinalScore(t *testing.T) {
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "trusted-group")
	if err != nil {
		t.Fatal(err)
	}
	site := ProviderDefaultPolicy{
		Mode: Ranked, Score: 50, Version: 1, ListMode: SiteBlacklist,
		Pinned: map[string]int{"trusted-group": 77},
	}
	resolved, err := ResolveProviderGroupPolicyWithNameKeywords(ref, "", "Trusted Group", true, nil, site,
		[]NameKeywordRule{{Keyword: "trusted", Score: 15}})
	if err != nil {
		t.Fatal(err)
	}
	b := resolved.Breakdown
	if resolved.Mode != Ranked || resolved.Score != 77 || b == nil || b.PinnedScore == nil || *b.PinnedScore != 77 ||
		b.FinalScore == nil || *b.FinalScore != 77 || len(b.Keywords) != 0 || b.SiteDefaultScore != 50 {
		t.Fatalf("pinned resolution: %+v breakdown %+v", resolved, b)
	}

	site.Pinned["trusted-group"] = 0
	if _, err := ResolveProviderGroupPolicyWithNameKeywords(ref, "", "Trusted Group", true, nil, site, nil); err == nil {
		t.Fatal("a pinned score outside 1..100 must fail closed")
	}
}

func TestResolveProviderGroupPolicyBlacklistBeatsPin(t *testing.T) {
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderMikan, "9")
	if err != nil {
		t.Fatal(err)
	}
	site := ProviderDefaultPolicy{
		Mode: Ranked, Score: 50, Version: 1, ListMode: SiteBlacklist, Listed: []string{"9"},
		Pinned: map[string]int{"9": 100},
	}
	resolved, err := ResolveProviderGroupPolicyWithNameKeywords(ref, "", "Nine", true, nil, site, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Mode != Never || resolved.Breakdown.PinnedScore != nil || resolved.Breakdown.ListEffect != ListEffectBlacklisted {
		t.Fatalf("blacklisted pinned group: %+v breakdown %+v", resolved, resolved.Breakdown)
	}
}
