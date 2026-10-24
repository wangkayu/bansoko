package application

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
)

func TestProviderPolicyDecoderUsesTypedEntriesAndManualActions(t *testing.T) {
	manual := true
	excluded := false
	policies, err := providerPoliciesFromSection(persistedSubgroupsSection{
		Entries: []persistedSubgroupScoreEntry{
			{Provider: persistedSubgroupProviderAniBT, GroupRef: "blocked", DisplayName: "Blocked", SelectionMode: persistedSubgroupSelectionManual, ManualSelected: &manual, CountInLimit: false},
			{Provider: persistedSubgroupProviderMikan, GroupRef: "42", DisplayName: "Ranked", SelectionMode: persistedSubgroupSelectionAuto, Score: 70, CountInLimit: true},
			{Provider: persistedSubgroupProviderMikan, GroupRef: "43", DisplayName: "Excluded", SelectionMode: persistedSubgroupSelectionManual, ManualSelected: &excluded, CountInLimit: false},
		},
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 3 || policies[0].Mode != selectiondomain.Always || policies[1].Mode != selectiondomain.Ranked || policies[2].Mode != selectiondomain.Never {
		t.Fatalf("unexpected decoded policies: %+v", policies)
	}
	if policies[0].Score != nil || policies[1].Score == nil || *policies[1].Score != 70 {
		t.Fatalf("unexpected decoded scores: %+v", policies)
	}
	if policies[2].Score != nil || policies[2].CountInLimit {
		t.Fatalf("manual exclusion must be Never with no score and no slot: %+v", policies[2])
	}
	if policies[0].Version != 4 || policies[1].Version != 4 || policies[2].Version != 4 {
		t.Fatalf("section version was not retained: %+v", policies)
	}
}

func TestProviderDefaultsDecoderMapsBlacklistWhitelistAndDefaults(t *testing.T) {
	defaults := providerDefaultsFromSection(persistedSubgroupsSection{ProviderDefaults: &persistedProviderGroupDefaults{
		Anibt: &persistedProviderGroupDefault{Mode: persistedProviderGroupDefaultMode("blacklist"), Score: 50, Groups: []string{"bad"}},
		Mikan: &persistedProviderGroupDefault{Mode: persistedProviderGroupDefaultMode("whitelist"), Score: 80, Groups: []string{"good"}},
	}}, 3)
	if defaults[providerdomain.ProviderAniBT].Mode != selectiondomain.Ranked || defaults[providerdomain.ProviderAniBT].Score != 50 {
		t.Fatalf("blacklist default was not ranked: %+v", defaults)
	}
	if defaults[providerdomain.ProviderMikan].Mode != selectiondomain.Never || defaults[providerdomain.ProviderMikan].Score != 80 {
		t.Fatalf("whitelist default was not excluded: %+v", defaults)
	}
	if defaults[providerdomain.ProviderMikan].Version != 3 {
		t.Fatalf("provider default version was not retained: %+v", defaults)
	}
}

func TestNameKeywordsDecoderMapsProviderRules(t *testing.T) {
	aniBT := []persistedNameKeywordRule{{Keyword: "trusted", Score: 12}}
	mikan := []persistedNameKeywordRule{{Keyword: "raw", Score: -18}}
	section := persistedSubgroupsSection{
		Entries:      []persistedSubgroupScoreEntry{},
		NameKeywords: &persistedProviderNameKeywordRules{Anibt: &aniBT, Mikan: &mikan},
	}
	defaults := providerDefaultsFromSection(section, 9)
	if len(defaults[providerdomain.ProviderAniBT].NameKeywords) != 1 || defaults[providerdomain.ProviderAniBT].NameKeywords[0].Score != 12 {
		t.Fatalf("AniBT name keywords were not decoded: %+v", defaults[providerdomain.ProviderAniBT])
	}
	if len(defaults[providerdomain.ProviderMikan].NameKeywords) != 1 || defaults[providerdomain.ProviderMikan].NameKeywords[0].Score != -18 {
		t.Fatalf("Mikan name keywords were not decoded: %+v", defaults[providerdomain.ProviderMikan])
	}
}
