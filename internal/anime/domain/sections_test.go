package domain

import (
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/transport/httpapi/generated"
)

// A draft's subgroups section is its own one-time value even with no entry:
// nothing falls back to a Global section.
func TestResolveDraftEffectiveUsesTheDraftSubgroupsWithoutEntries(t *testing.T) {
	effective, err := ResolveDraftEffective(domainValue[AnimeDraft](generated.AnimeDraft{
		Subgroups: generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: 7},
		Rules:     generated.RulesDraft{Sections: completeRulesForTest(t)},
	}))
	if err != nil {
		t.Fatalf("resolve draft: %v", err)
	}
	if effective.Selection.MaxRss != 7 || len(effective.Selection.Entries) != 0 {
		t.Fatalf("expected the draft subgroup section, got %+v", effective.Selection)
	}
	if effective.Versions[SectionSubgroups] != 0 {
		t.Fatalf("expected the draft-owned version 0, got %d", effective.Versions[SectionSubgroups])
	}
}

func TestResolveDraftEffectiveUsesPopulatedProviderEntriesAsOverride(t *testing.T) {
	draftGroups := generated.SubgroupsSection{
		MaxRss: 2,
		Entries: []generated.SubgroupScoreEntry{
			{
				Provider:       generated.ProviderKindAnibt,
				GroupRef:       "anime-group",
				DisplayName:    "Anime Group",
				SelectionMode:  generated.SelectionModeManual,
				ManualSelected: boolPtrForTest(true),
				CountInLimit:   false,
			},
		},
	}
	effective, err := ResolveDraftEffective(domainValue[AnimeDraft](generated.AnimeDraft{
		Subgroups: draftGroups,
		Rules:     generated.RulesDraft{Sections: completeRulesForTest(t)},
	}))
	if err != nil {
		t.Fatalf("resolve draft: %v", err)
	}
	if effective.Selection.MaxRss != draftGroups.MaxRss || len(effective.Selection.Entries) != 1 {
		t.Fatalf("expected draft subgroup override, got %+v", effective.Selection)
	}
	if effective.Selection.Entries[0].GroupRef != "anime-group" {
		t.Fatalf("expected anime group, got %+v", effective.Selection.Entries[0])
	}
	if effective.Versions[SectionSubgroups] != 0 {
		t.Fatalf("expected anime override version 0 at resolve layer, got %d", effective.Versions[SectionSubgroups])
	}
}

// completeRulesForTest is the wire form of completeDraftRules: every rules
// section a complete replace value at its code default.
func completeRulesForTest(t *testing.T) generated.RulesSections {
	t.Helper()
	raw, err := json.Marshal(completeDraftRules(t))
	if err != nil {
		t.Fatal(err)
	}
	var sections generated.RulesSections
	if err := json.Unmarshal(raw, &sections); err != nil {
		t.Fatal(err)
	}
	return sections
}

func boolPtrForTest(value bool) *bool {
	return &value
}

func TestValidateSectionValueRejectsUnsupportedNamingLanguage(t *testing.T) {
	rules := generated.NamingRules{
		Language: generated.NamingRulesLanguage(""),
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSectionValue(SectionNamingRules, raw); err == nil {
		t.Fatal("missing naming language was accepted")
	}
	rules.Language = generated.NamingRulesLanguage("go-template-naming-1")
	raw, err = json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSectionValue(SectionNamingRules, raw); err == nil {
		t.Fatal("legacy naming language was accepted")
	}
}

// A template that reads a naming variable the deployment has not loaded cannot
// be saved: the snapshot-carrying validator is the form the save paths use.
func TestValidateSectionValueRefusesUnknownNamingVarsAtSave(t *testing.T) {
	snapshot := map[string]map[string]string{"anime": {"loaded": "yes"}}
	rules := generated.NamingRules{
		Language: generated.NamingRulesLanguage(naming.Language),
		Tv:       generated.NamingVariant{DirectoryTemplate: "${'vars.anime.missing'}", VideoFilenameTemplate: "${vars.anime.missing}"},
		Movie:    generated.NamingVariant{DirectoryTemplate: "movie", VideoFilenameTemplate: "file.mkv"},
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateSectionValueWithVars(SectionNamingRules, raw, snapshot)
	var namingErr *naming.Error
	if !errors.As(err, &namingErr) || namingErr.Code != naming.ErrCodeNamingVarsUnknown {
		t.Fatalf("save validation error = %v, want typed %s", err, naming.ErrCodeNamingVarsUnknown)
	}
	// Without a snapshot the same section is structure-valid: read-back of stored
	// configuration does not re-judge variables that were verified at save time.
	if err := ValidateSectionValue(SectionNamingRules, raw); err != nil {
		t.Fatalf("snapshot-free validation of a stored value rejected it: %v", err)
	}
	// A loaded key is accepted, so the refusal tracks the snapshot rather than
	// any `${vars...}` reference.
	rules.Tv.VideoFilenameTemplate = "${vars.anime.loaded}"
	raw, err = json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSectionValueWithVars(SectionNamingRules, raw, snapshot); err != nil {
		t.Fatalf("loaded var refused at save: %v", err)
	}
	// Nothing loaded means every reference is unknown, not that none are checked.
	if err := ValidateSectionValueWithVars(SectionNamingRules, raw, map[string]map[string]string{}); err == nil {
		t.Fatal("an empty snapshot must refuse every vars reference")
	}
}
func TestValidateSectionValueValidatesProviderDefaults(t *testing.T) {
	// The entries are the providers' own group keys (AniBT slugs, Mikan numeric
	// ids), never display names: the policy build parses them as GroupRefs.
	valid := generated.SubgroupsSection{
		MaxRss:  100,
		Entries: []generated.SubgroupScoreEntry{},
		ProviderDefaults: &generated.ProviderGroupDefaults{
			Anibt: &generated.ProviderGroupDefault{
				Mode:   generated.ProviderGroupDefaultMode("blacklist"),
				Score:  50,
				Groups: []string{"alice", "samplegroup"},
			},
			Mikan: &generated.ProviderGroupDefault{
				Mode:         generated.ProviderGroupDefaultMode("whitelist"),
				Score:        60,
				Groups:       []string{"58"},
				PinnedScores: &[]generated.PinnedGroupScore{{GroupRef: "58", Score: 90}, {GroupRef: "12", Score: 1}},
			},
		},
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSectionValue(SectionSubgroups, raw); err != nil {
		t.Fatalf("valid provider defaults rejected: %v", err)
	}

	cases := []struct {
		name string
		edit func(*generated.ProviderGroupDefaults)
	}{
		{name: "mode", edit: func(defaults *generated.ProviderGroupDefaults) { defaults.Anibt.Mode = "invalid" }},
		{name: "score", edit: func(defaults *generated.ProviderGroupDefaults) { defaults.Anibt.Score = 101 }},
		{name: "empty group", edit: func(defaults *generated.ProviderGroupDefaults) { defaults.Anibt.Groups = []string{" "} }},
		{name: "duplicate group", edit: func(defaults *generated.ProviderGroupDefaults) { defaults.Anibt.Groups = []string{"alice", "alice"} }},
		// A display name is not a key. AniBT slugs may not contain spaces...
		{name: "anibt display name", edit: func(defaults *generated.ProviderGroupDefaults) { defaults.Anibt.Groups = []string{"Group A"} }},
		// ...and a Mikan entry must be a positive integer id at all.
		{name: "mikan display name", edit: func(defaults *generated.ProviderGroupDefaults) { defaults.Mikan.Groups = []string{"SampleGroup"} }},
		{name: "mikan zero id", edit: func(defaults *generated.ProviderGroupDefaults) { defaults.Mikan.Groups = []string{"0"} }},
		// Duplicates are caught on the normalized key, so two spellings of one id
		// cannot enter the list.
		{name: "unnormalized duplicate", edit: func(defaults *generated.ProviderGroupDefaults) { defaults.Mikan.Groups = []string{"58", "058"} }},
		// Pinned scores target the same normalized keys, once each, 1..100.
		{name: "pin display name", edit: func(defaults *generated.ProviderGroupDefaults) { pin(defaults, "SampleGroup", 50) }},
		{name: "pin unnormalized", edit: func(defaults *generated.ProviderGroupDefaults) { pin(defaults, "058", 50) }},
		{name: "pin score low", edit: func(defaults *generated.ProviderGroupDefaults) { pin(defaults, "7", 0) }},
		{name: "pin score high", edit: func(defaults *generated.ProviderGroupDefaults) { pin(defaults, "7", 101) }},
		{name: "pin duplicate", edit: func(defaults *generated.ProviderGroupDefaults) {
			defaults.Mikan.PinnedScores = &[]generated.PinnedGroupScore{{GroupRef: "7", Score: 5}, {GroupRef: "7", Score: 6}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := generated.SubgroupsSection{
				MaxRss:  valid.MaxRss,
				Entries: []generated.SubgroupScoreEntry{},
				ProviderDefaults: &generated.ProviderGroupDefaults{
					Anibt: &generated.ProviderGroupDefault{
						Mode: valid.ProviderDefaults.Anibt.Mode, Score: valid.ProviderDefaults.Anibt.Score,
						Groups: append([]string(nil), valid.ProviderDefaults.Anibt.Groups...),
					},
					Mikan: &generated.ProviderGroupDefault{
						Mode: valid.ProviderDefaults.Mikan.Mode, Score: valid.ProviderDefaults.Mikan.Score,
						Groups: append([]string(nil), valid.ProviderDefaults.Mikan.Groups...),
					},
				},
			}
			test.edit(candidate.ProviderDefaults)
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateSectionValue(SectionSubgroups, raw); err == nil {
				t.Fatalf("invalid provider default %s was accepted", test.name)
			}
		})
	}
}

func pin(defaults *generated.ProviderGroupDefaults, ref string, score int) {
	defaults.Mikan.PinnedScores = &[]generated.PinnedGroupScore{{GroupRef: ref, Score: score}}
}

// TestValidateFileEffectCollisionPolicy pins the overwrite unlock (B6/R1):
// fail/versioned/overwrite are all valid file-effect collision policies, and
// anything else is rejected.
func TestValidateFileEffectCollisionPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy generated.CollisionPolicy
		valid  bool
	}{
		{generated.CollisionPolicyFail, true},
		{generated.CollisionPolicyVersioned, true},
		{generated.CollisionPolicyOverwrite, true},
		{generated.CollisionPolicy("nope"), false},
	} {
		raw, err := json.Marshal(generated.FileEffectRules{
			Operation:       generated.FileEffectRulesOperationHardlink,
			CollisionPolicy: tc.policy,
		})
		if err != nil {
			t.Fatal(err)
		}
		err = ValidateSectionValue(SectionFileEffect, raw)
		if tc.valid && err != nil {
			t.Fatalf("valid policy %q rejected: %v", tc.policy, err)
		}
		if !tc.valid && err == nil {
			t.Fatalf("invalid policy %q accepted", tc.policy)
		}
	}
}
