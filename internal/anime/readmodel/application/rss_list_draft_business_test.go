package application

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
)

// Saving a draft that carries a business snapshot freezes the snapshot's
// sections and parser envelope. The preview of that same draft must be computed
// from the snapshot too, so the user reviews exactly what is saved. Global is
// never read for a per-anime value.

// globalDrivenConfig is a ConfigReader with no stored overrides; every value
// the preview uses must come from the draft itself.
type globalDrivenConfig struct{}

func (globalDrivenConfig) ReadOverrides(context.Context, string) (animedomain.OverrideValue, map[string]animedomain.OverrideValue, error) {
	return animedomain.OverrideValue{}, nil, nil
}

func businessSnapshotFixture(t *testing.T, maxRSS int) *animedomain.BusinessSnapshot {
	t.Helper()
	business := &animedomain.BusinessSnapshot{
		SchemaVersion:   "business-v1",
		Sections:        map[string]json.RawMessage{},
		SectionVersions: map[string]int64{},
		SectionSchemas:  map[string]string{},
		Parser: animedomain.ParserPolicyEnvelope{
			Kind: "regex", PolicyVersion: 3, ProfileVersion: 4, RulesOwnerVersion: 5,
			Provenance: "global", SchemaVersion: animedomain.ParserPolicyEnvelopeSchemaVersion, SourceOwner: "global",
		},
	}
	for name, schema := range animedomain.SectionSchemaVersions {
		value, err := animedomain.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == animedomain.SectionSubgroups {
			value = animedomain.SubgroupsSection{Entries: []animedomain.SubgroupScoreEntry{}, MaxRss: maxRSS}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		business.Sections[name] = raw
		business.SectionVersions[name] = 11
		business.SectionSchemas[name] = schema
	}
	return business
}

// businessDraft is a draft whose edits are consistent with its snapshot, the
// state freezeBusinessDraft produces before a save: every rules section replaces
// with the snapshot's value and the subgroups section is the snapshot's.
func businessDraft(t *testing.T, maxRSS int) animedomain.AnimeDraft {
	t.Helper()
	draft := previewTestDraft(animedomain.ProviderKindAnibt)
	draft.Business = businessSnapshotFixture(t, maxRSS)
	draft.Subgroups = animedomain.SubgroupsSection{Entries: []animedomain.SubgroupScoreEntry{}, MaxRss: maxRSS}
	replace := func(section string) json.RawMessage {
		return json.RawMessage(`{"mode":"replace","value":` + string(draft.Business.Sections[section]) + `}`)
	}
	draft.Rules.Sections = animedomain.RulesSections{
		FileEffect: replace(animedomain.SectionFileEffect), FileFilter: replace(animedomain.SectionFileRules),
		Naming: replace(animedomain.SectionNamingRules), Parser: replace(animedomain.SectionParser),
		ReleaseFilter: replace(animedomain.SectionReleaseRules),
	}
	return draft
}

func driverService() *Service {
	return &Service{config: globalDrivenConfig{}}
}

// The draft's own subgroup policy governs the preview, even when it lists no
// group of its own. This is also the policy the observed groups are ranked
// under, so it has to be the one the saved revision will run.
func TestPreviewSelectionPolicyComesFromTheDraftBusiness(t *testing.T) {
	draft := businessDraft(t, 1) // the draft caps subscriptions at 1
	effective, err := driverService().resolveDraftEffective(context.Background(), draft)
	if err != nil {
		t.Fatalf("resolveDraftEffective: %v", err)
	}
	if effective.Selection.MaxRss != 1 {
		t.Fatalf("max_rss must be the draft's 1: %d", effective.Selection.MaxRss)
	}
	ranked, _, err := applyObservedGroups(draft, effective, observedFacts("10", "20", "30"))
	if err != nil {
		t.Fatal(err)
	}
	selected := 0
	for _, entry := range ranked.Selection.Entries {
		if entry.EffectiveSelected {
			selected++
		}
	}
	if selected != 1 {
		t.Fatalf("with max_rss 1 only one observed group is selected, got %d", selected)
	}
}

// The preview resolves the parser selection exactly as the saved revision does:
// the draft's frozen envelope, not the current Global parser policy.
func TestPreviewParserSelectionMatchesTheSavedRevision(t *testing.T) {
	draft := businessDraft(t, 3)
	service := driverService()
	effective, err := service.resolveDraftEffective(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.draftParserSelectionInput(context.Background(), draft, effective)
	if err != nil {
		t.Fatalf("draftParserSelectionInput: %v", err)
	}

	// What saving freezes for this draft: its snapshot's sections and parser envelope.
	sections, _ := json.Marshal(draft.Business.Sections)
	versions, _ := json.Marshal(draft.Business.SectionVersions)
	schemas, _ := json.Marshal(draft.Business.SectionSchemas)
	revision := animeapplication.CandidateRevision{
		Draft: draft, Effective: sections, SectionVersions: versions, SectionSchemas: schemas,
		ParserPolicy: draft.Business.Parser, Business: draft.Business,
	}
	resolved, err := revision.Resolved()
	if err != nil {
		t.Fatal(err)
	}
	want, err := revision.ParserSelectionInput(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preview parser selection differs from the saved revision's\n got  %+v\n want %+v", got, want)
	}
	if got.Kind != "regex" {
		t.Fatalf("the preview must use the frozen envelope (regex): %+v", got)
	}
}
