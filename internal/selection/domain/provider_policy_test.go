package domain

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
)

func TestSelectProviderGroupsUsesMaxRSSAsGroupBudget(t *testing.T) {
	alwaysFree := effectiveGroup(t, providerdomain.ProviderMikan, "1", Always, 0, false, true)
	alwaysCounted := effectiveGroup(t, providerdomain.ProviderMikan, "2", Always, 0, true, true)
	rankedHigh := effectiveGroup(t, providerdomain.ProviderAniBT, "group-b", Ranked, 80, true, true)
	rankedTieA := effectiveGroup(t, providerdomain.ProviderAniBT, "group-a", Ranked, 50, true, true)
	rankedTieC := effectiveGroup(t, providerdomain.ProviderAniBT, "group-c", Ranked, 50, true, true)
	never := effectiveGroup(t, providerdomain.ProviderMikan, "3", Never, 0, false, true)
	unavailable := effectiveGroup(t, providerdomain.ProviderMikan, "4", Ranked, 100, true, false)

	decisions, err := SelectProviderGroups([]EffectiveProviderGroup{rankedTieC, unavailable, never, rankedHigh, alwaysCounted, rankedTieA, alwaysFree}, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]GroupSelectionOutcome{
		"mikan:1": GroupSelected, "mikan:2": GroupSelected,
		"anibt:group-b": GroupSelected, "anibt:group-a": GroupSelected,
		"anibt:group-c": GroupNotSelected, "mikan:3": GroupNotSelected,
		"mikan:4": GroupUnavailable,
	}
	for _, decision := range decisions {
		if want[decision.Group.Ref.Key()] != decision.Outcome {
			t.Fatalf("unexpected outcome for %s: %+v", decision.Group.Ref.Key(), decision)
		}
	}
}

func TestAutomaticAdmissionKeepsUserStateSeparateFromSelection(t *testing.T) {
	cases := []struct {
		active  bool
		outcome GroupSelectionOutcome
		want    bool
	}{
		{true, GroupSelected, true},
		{true, GroupNotSelected, false},
		{true, GroupUnavailable, false},
		{false, GroupSelected, false},
	}
	for _, testCase := range cases {
		if got := AutomaticAdmissionAllowed(testCase.active, testCase.outcome); got != testCase.want {
			t.Fatalf("admission active=%v outcome=%s: got %v", testCase.active, testCase.outcome, got)
		}
	}
}

func TestProviderPolicyRequiresExplicitModeFields(t *testing.T) {
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "group")
	target := PolicyTarget{Kind: TargetProviderGroup, ProviderRef: &ref}
	if err := ValidateProviderGroupPolicy(ProviderGroupPolicy{Target: target, Mode: Ranked, CountInLimit: true}); err == nil {
		t.Fatal("ranked policy without score accepted")
	}
	score := 50
	if err := ValidateProviderGroupPolicy(ProviderGroupPolicy{Target: target, Mode: Ranked, Score: &score, CountInLimit: true}); err != nil {
		t.Fatalf("valid ranked policy rejected: %v", err)
	}
}

func TestResolveProviderGroupPolicyUsesExactCanonicalAndDefaultPrecedence(t *testing.T) {
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderMikan, "42")
	otherRef, _ := providerdomain.NewGroupRef(providerdomain.ProviderMikan, "43")
	animeCanonicalScore := 80
	animeExactScore := 90
	rules := func(exactRef providerdomain.GroupRef, exactScore, canonicalScore int) []ProviderGroupPolicy {
		return []ProviderGroupPolicy{
			{Target: PolicyTarget{Kind: TargetCanonicalGroup, CanonicalKey: "group-key"}, Mode: Ranked, Score: &canonicalScore, CountInLimit: true, Version: 2},
			{Target: PolicyTarget{Kind: TargetProviderGroup, ProviderRef: &exactRef}, Mode: Ranked, Score: &exactScore, CountInLimit: true, Version: 3},
		}
	}
	resolved, err := ResolveProviderGroupPolicy(ref, "group-key", true,
		rules(ref, animeExactScore, animeCanonicalScore),
		ProviderDefaultPolicy{Mode: Ranked, Score: 50, Version: 1})
	if err != nil || resolved.Score != 90 || resolved.PolicySource != SourceAnimeExact {
		t.Fatalf("exact anime policy did not win: %+v err=%v", resolved, err)
	}

	resolved, err = ResolveProviderGroupPolicy(ref, "group-key", true,
		rules(otherRef, animeExactScore, animeCanonicalScore),
		ProviderDefaultPolicy{Mode: Ranked, Score: 50, Version: 1})
	if err != nil || resolved.Score != 80 || resolved.PolicySource != SourceAnimeCanonical {
		t.Fatalf("anime canonical policy did not win: %+v err=%v", resolved, err)
	}

	resolved, err = ResolveProviderGroupPolicy(ref, "", true, nil,
		ProviderDefaultPolicy{Mode: Ranked, Score: 50, Version: 1})
	if err != nil || resolved.Score != 50 || resolved.PolicySource != SourceProviderDefault {
		t.Fatalf("provider default Ranked 50 was not resolved: %+v err=%v", resolved, err)
	}
}

func TestResolveProviderGroupPolicyAppliesNameKeywordAdjustmentsToDefault(t *testing.T) {
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "trusted-group")
	if err != nil {
		t.Fatal(err)
	}
	rules := []NameKeywordRule{
		{Keyword: "trusted", Score: 15},
		{Keyword: "group", Score: -20},
	}
	resolved, err := ResolveProviderGroupPolicyWithNameKeywords(
		ref, "", "Trusted Group", true, nil,
		ProviderDefaultPolicy{Mode: Ranked, Score: 50, Version: 1}, rules,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Score != 45 || resolved.PolicySource != SourceProviderDefault {
		t.Fatalf("keyword adjustments got score=%d source=%s, want score=45 provider_default", resolved.Score, resolved.PolicySource)
	}
}

func TestResolveProviderGroupPolicyDoesNotApplyNameKeywordsToExplicitPolicy(t *testing.T) {
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderMikan, "42")
	if err != nil {
		t.Fatal(err)
	}
	exactScore := 80
	resolved, err := ResolveProviderGroupPolicyWithNameKeywords(
		ref, "", "Preferred Group", true,
		[]ProviderGroupPolicy{{Target: PolicyTarget{Kind: TargetProviderGroup, ProviderRef: &ref}, Mode: Ranked, Score: &exactScore, CountInLimit: true}},
		ProviderDefaultPolicy{Mode: Ranked, Score: 50, Version: 1},
		[]NameKeywordRule{{Keyword: "preferred", Score: 20}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Score != 80 || resolved.PolicySource != SourceAnimeExact {
		t.Fatalf("explicit policy was adjusted: %+v", resolved)
	}
}

func TestResolveProviderGroupPolicyNameKeywordMissAndNegativeScore(t *testing.T) {
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "other")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveProviderGroupPolicyWithNameKeywords(
		ref, "canonical", "Unrelated", true, nil,
		ProviderDefaultPolicy{Mode: Ranked, Score: 50, Version: 1},
		[]NameKeywordRule{{Keyword: "trusted", Score: -100}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Score != 50 {
		t.Fatalf("unmatched keyword changed score to %d", resolved.Score)
	}
	resolved, err = ResolveProviderGroupPolicyWithNameKeywords(
		ref, "canonical", "Trusted", true, nil,
		ProviderDefaultPolicy{Mode: Ranked, Score: 50, Version: 1},
		[]NameKeywordRule{{Keyword: "trusted", Score: -100}},
	)
	if err != nil || resolved.Score != -50 {
		t.Fatalf("negative keyword adjustment got %+v err=%v", resolved, err)
	}
	decisions, err := SelectProviderGroups([]EffectiveProviderGroup{resolved}, 1)
	if err != nil || decisions[0].Outcome != GroupNotSelected || decisions[0].Reason != "policy_score_negative" {
		t.Fatalf("negative score should be excluded: %+v err=%v", decisions, err)
	}
}

func TestApplyNameKeywordAdjustmentsAddsEachMatchingRuleOnce(t *testing.T) {
	got := ApplyNameKeywordAdjustments(50, "", "Alpha Trusted Group", "alpha", []NameKeywordRule{
		{Keyword: "alpha", Score: 5},
		{Keyword: "trusted", Score: 10},
		{Keyword: "group", Score: -3},
	})
	if got != 62 {
		t.Fatalf("combined keyword score=%d, want 62", got)
	}
}

func effectiveGroup(t *testing.T, provider providerdomain.Kind, externalID string, mode SelectionMode, score int, count, observed bool) EffectiveProviderGroup {
	t.Helper()
	ref, err := providerdomain.NewGroupRef(provider, externalID)
	if err != nil {
		t.Fatal(err)
	}
	return EffectiveProviderGroup{Ref: ref, Observed: observed, Mode: mode, Score: score, CountInLimit: count, PolicySource: SourceProviderDefault}
}
