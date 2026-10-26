package domain

import (
	"bytes"
	"encoding/json"
	"testing"

	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
)

// businessPresetForLoadTest is a complete Global preset as ReadBusinessPreset
// returns it: every frozen section, a parser envelope, retention, the
// file-effect route and immutable program references.
func businessPresetForLoadTest(t *testing.T) BusinessSnapshot {
	t.Helper()
	business := &BusinessSnapshot{SchemaVersion: BusinessSnapshotSchemaVersion,
		Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}}
	for _, name := range EffectiveSectionOrder {
		value, err := DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		business.Sections[name], business.SectionVersions[name], business.SectionSchemas[name] = raw, 42, SectionSchemaVersions[name]
	}
	business.Parser = ParserPolicyEnvelope{SchemaVersion: ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global_parser_policy_default", PolicyVersion: 2}
	business.Retention = RetentionValue{AdmissionMode: "all",
		QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.lang","ranks":[["chs"],["sc"],["cht"],["tc"],["jpn"],["eng"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}`)}
	business.RetentionVersion, business.UnclassifiedRoute, business.FileEffectVersion = 4, "skip", 5
	business.Programs = ProgramReferences{
		Vars:      namingvars.ProgramRef{Version: namingvars.ProfileVersion("sha256:vars"), Digest: "sha256:vars"},
		AttrRules: namingvars.ProgramRef{Version: attrrules.ProfileVersion("sha256:attr"), Digest: "sha256:attr"},
	}
	// The Global subgroups section knows one group the draft does not name.
	var subgroups SubgroupsSection
	if err := json.Unmarshal(business.Sections[SectionSubgroups], &subgroups); err != nil {
		t.Fatal(err)
	}
	subgroups.Entries = []SubgroupScoreEntry{{Provider: ProviderKindMikan, GroupRef: "999", DisplayName: "Global", SelectionMode: SelectionModeAuto, Score: 10}}
	raw, err := json.Marshal(subgroups)
	if err != nil {
		t.Fatal(err)
	}
	business.Sections[SectionSubgroups] = raw
	if _, err := business.Freeze(); err != nil {
		t.Fatalf("preset fixture is not a complete business snapshot: %v", err)
	}
	return *business
}

func providerDraftForLoadTest(t *testing.T) AnimeDraft {
	t.Helper()
	entry := "70011"
	releaseFilter, err := DefaultSectionValue(SectionReleaseRules)
	if err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(releaseFilter)
	if err != nil {
		t.Fatal(err)
	}
	replace, err := json.Marshal(map[string]json.RawMessage{"mode": json.RawMessage(`"replace"`), "value": value})
	if err != nil {
		t.Fatal(err)
	}
	selected := true
	return AnimeDraft{
		Origin: SubscriptionOriginMikan, OriginEntryRef: &entry, Title: "Show",
		Subgroups: SubgroupsSection{MaxRss: 3, Entries: []SubgroupScoreEntry{{
			Provider: ProviderKindMikan, GroupRef: "678", DisplayName: "Group 678", SelectionMode: SelectionModeManual,
			ManualSelected: &selected, EffectiveSelected: true, Score: 50,
		}}},
		// Only release_filter is present; the other sections are absent and
		// take a copy of the preset value.
		Rules:      RulesDraft{Sections: RulesSections{ReleaseFilter: replace}},
		RssSources: []RSSSourceDraft{},
	}
}

// The loaded draft carries a complete business snapshot whose digest the save
// path accepts, the draft's own edits are frozen into it, and resolving the
// draft the way preview does yields exactly the sections saving freezes.
func TestLoadBusinessIntoDraftFreezesTheDraftEdits(t *testing.T) {
	preset := businessPresetForLoadTest(t)
	draft := providerDraftForLoadTest(t)
	globalTagFilters := json.RawMessage(`{"anibt":{"require":["language:CHS"],"exclude":[]},"title":{"require":[],"exclude":[]}}`)
	loaded, err := LoadBusinessIntoDraft(draft, preset, globalTagFilters)
	if err != nil {
		t.Fatalf("LoadBusinessIntoDraft: %v", err)
	}
	if loaded.Business == nil || loaded.BusinessDigest == nil {
		t.Fatal("the loaded draft carries no business snapshot")
	}
	digest, err := loaded.Business.Digest()
	if err != nil || digest != *loaded.BusinessDigest {
		t.Fatalf("business digest = %v, %v; the draft says %s", digest, err, *loaded.BusinessDigest)
	}
	if loaded.Business.Programs != preset.Programs || loaded.Business.Parser.Kind != preset.Parser.Kind || loaded.Business.UnclassifiedRoute != preset.UnclassifiedRoute {
		t.Fatalf("non-section preset fields changed: %+v", loaded.Business)
	}

	var frozenSubgroups SubgroupsSection
	if err := json.Unmarshal(loaded.Business.Sections[SectionSubgroups], &frozenSubgroups); err != nil {
		t.Fatal(err)
	}
	groups := map[string]SubgroupScoreEntry{}
	for _, entry := range frozenSubgroups.Entries {
		groups[entry.GroupRef] = entry
	}
	if !groups["678"].EffectiveSelected || groups["678"].SelectionMode != SelectionModeManual {
		t.Fatalf("the draft's pinned group is not frozen as selected: %+v", frozenSubgroups.Entries)
	}
	if _, ok := groups["999"]; !ok {
		t.Fatalf("a Global group the draft does not name must stay: %+v", frozenSubgroups.Entries)
	}
	if frozenSubgroups.MaxRss != 3 {
		t.Fatalf("max_rss = %d, want the draft's 3", frozenSubgroups.MaxRss)
	}

	for name, field := range map[string]json.RawMessage{
		SectionParser: loaded.Rules.Sections.Parser, SectionReleaseRules: loaded.Rules.Sections.ReleaseFilter,
		SectionFileRules: loaded.Rules.Sections.FileFilter, SectionNamingRules: loaded.Rules.Sections.Naming,
		SectionFileEffect: loaded.Rules.Sections.FileEffect,
	} {
		override, err := decodeOverride(field)
		if err != nil {
			t.Fatalf("rules section %s = %s: %v", name, field, err)
		}
		if name != SectionReleaseRules {
			// An absent section takes a complete copy of the preset value.
			if !jsonEqual(t, override.Value, preset.Sections[name]) || loaded.Business.SectionVersions[name] != preset.SectionVersions[name] {
				t.Fatalf("absent section %s must copy the preset value and provenance", name)
			}
		}
		if !jsonEqual(t, override.Value, loaded.Business.Sections[name]) {
			t.Fatalf("rules section %s and the frozen snapshot disagree: %s vs %s", name, override.Value, loaded.Business.Sections[name])
		}
	}
	tagFilters, err := decodeOverride(loaded.Rules.Sections.TagFilters)
	if err != nil || !jsonEqual(t, tagFilters.Value, globalTagFilters) {
		t.Fatalf("an absent tag_filters must copy the Global value: %s (%v)", loaded.Rules.Sections.TagFilters, err)
	}

	// Preview resolves the draft over its snapshot; save takes the snapshot's
	// sections. They must be the same configuration.
	resolved, err := ResolveDraftSectionsFromBusiness(loaded, nil)
	if err != nil {
		t.Fatalf("preview resolution of the loaded draft: %v", err)
	}
	for _, name := range EffectiveSectionOrder {
		if !jsonEqual(t, resolved.Sections[name], loaded.Business.Sections[name]) {
			t.Fatalf("section %s: preview resolves %s, saving freezes %s", name, resolved.Sections[name], loaded.Business.Sections[name])
		}
	}
	if violations := ValidateDraft(loaded); len(violations) > 0 {
		t.Fatalf("the loaded draft is invalid: %+v", violations)
	}

	// Loading the loaded draft again changes nothing.
	again, err := LoadBusinessIntoDraft(loaded, preset, globalTagFilters)
	if err != nil || *again.BusinessDigest != *loaded.BusinessDigest {
		t.Fatalf("reloading changed the snapshot: %v", err)
	}
}

func TestLoadBusinessIntoDraftRejectsAnInvalidRulesSection(t *testing.T) {
	draft := providerDraftForLoadTest(t)
	draft.Rules.Sections.Naming = json.RawMessage(`{"mode":"patch"}`)
	tagFilters := json.RawMessage(`{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]}}`)
	if _, err := LoadBusinessIntoDraft(draft, businessPresetForLoadTest(t), tagFilters); err == nil {
		t.Fatal("an unknown override mode must be refused")
	}
	draft = providerDraftForLoadTest(t)
	draft.Rules.Sections.Naming = json.RawMessage(`{"mode":"inherit"}`)
	if _, err := LoadBusinessIntoDraft(draft, businessPresetForLoadTest(t), tagFilters); err == nil {
		t.Fatal("the retired inherit mode must be refused")
	}
	if _, err := LoadBusinessIntoDraft(providerDraftForLoadTest(t), BusinessSnapshot{}, tagFilters); err == nil {
		t.Fatal("an incomplete preset must be refused")
	}
}

func jsonEqual(t *testing.T, left, right json.RawMessage) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(left, &a); err != nil {
		t.Fatalf("decode %s: %v", left, err)
	}
	if err := json.Unmarshal(right, &b); err != nil {
		t.Fatalf("decode %s: %v", right, err)
	}
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return bytes.Equal(ra, rb)
}
