package domain

import (
	"encoding/json"
	"testing"
)

func businessForEffectiveTest(t *testing.T) *BusinessSnapshot {
	t.Helper()
	business := &BusinessSnapshot{
		Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{},
		Parser: ParserPolicyEnvelope{Kind: "regex"},
	}
	for name, schema := range SectionSchemaVersions {
		value, err := DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		business.Sections[name], business.SectionVersions[name], business.SectionSchemas[name] = raw, 42, schema
	}
	return business
}

func completeDraftWithBusiness(t *testing.T, business *BusinessSnapshot) AnimeDraft {
	t.Helper()
	return AnimeDraft{
		Business:  business,
		Subgroups: SubgroupsSection{Entries: []SubgroupScoreEntry{}, MaxRss: 4},
		Rules:     RulesDraft{Sections: completeDraftRules(t)},
	}
}

func TestResolveDraftSectionsFromBusinessRequiresASnapshot(t *testing.T) {
	if _, err := ResolveDraftSectionsFromBusiness(AnimeDraft{}, nil); err == nil {
		t.Fatal("a draft without a business snapshot has nothing to resolve from")
	}
}

// Every rules section is the draft's own complete value (owned, version 0); a
// section the draft lacks fails closed instead of falling back to anything.
func TestResolveDraftSectionsFromBusinessUsesTheDraftValues(t *testing.T) {
	draft := completeDraftWithBusiness(t, businessForEffectiveTest(t))
	sections, err := ResolveDraftSectionsFromBusiness(draft, nil)
	if err != nil {
		t.Fatalf("ResolveDraftSectionsFromBusiness: %v", err)
	}
	for _, section := range []string{SectionParser, SectionReleaseRules, SectionFileRules, SectionNamingRules, SectionFileEffect} {
		if sections.Versions[section] != 0 {
			t.Fatalf("section %s must be the draft's own value (version 0), got %d", section, sections.Versions[section])
		}
	}
	draft.Rules.Sections.Naming = nil
	if _, err := ResolveDraftSectionsFromBusiness(draft, nil); err == nil {
		t.Fatal("a draft without a naming value must fail closed")
	}
}

// The draft's subgroups section always applies, even when it lists no entry: its
// max_rss is part of the frozen value.
func TestResolveDraftSectionsFromBusinessKeepsTheDraftSubgroupsWithoutEntries(t *testing.T) {
	draft := completeDraftWithBusiness(t, businessForEffectiveTest(t))
	sections, err := ResolveDraftSectionsFromBusiness(draft, nil)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := EffectiveConfigFromSections(sections)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Selection.MaxRss != 4 {
		t.Fatalf("draft max_rss 4 must apply with no entries, got %d", effective.Selection.MaxRss)
	}
	digest, err := sections.EffectiveDigest()
	if err != nil || digest == "" {
		t.Fatalf("the sections form must be digestible like a saved revision's: %q %v", digest, err)
	}
}
