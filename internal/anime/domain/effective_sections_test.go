package domain

import (
	"encoding/json"
	"reflect"
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

func resolvedTestDraft(t *testing.T) AnimeDraft {
	t.Helper()
	draft := domainValue[AnimeDraft](generated.AnimeDraft{
		Subgroups: generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: 100},
		Rules:     generated.RulesDraft{Sections: completeRulesForTest(t)},
	})
	// tag_filters stays outside the six frozen sections; a draft without it
	// resolves as the code default, exactly as a frozen envelope decodes.
	draft.Rules.Sections.TagFilters = nil
	return draft
}

// A saved revision must decode to exactly the same typed configuration a live
// resolution produces. If these ever diverge, a saved revision would change
// behaviour relative to the configuration the user reviewed.
func TestResolvedSectionsDecodeMatchesLiveResolution(t *testing.T) {
	draft := resolvedTestDraft(t)

	live, err := ResolveDraftEffective(draft)
	if err != nil {
		t.Fatalf("live resolve: %v", err)
	}
	resolved, err := ResolveDraftEffectiveSections(draft)
	if err != nil {
		t.Fatalf("resolve sections: %v", err)
	}
	stored, err := EffectiveConfigFromSections(resolved)
	if err != nil {
		t.Fatalf("decode resolved sections: %v", err)
	}
	if !reflect.DeepEqual(live, stored) {
		t.Fatalf("saved revision decode diverged from live resolution:\nlive=%+v\nstored=%+v", live, stored)
	}
	for _, section := range EffectiveSectionOrder {
		if _, ok := resolved.Sections[section]; !ok {
			t.Fatalf("resolved sections missing %q", section)
		}
		if resolved.SchemaVersions[section] != SectionSchemaVersions[section] {
			t.Fatalf("schema version for %q = %q, want %q", section, resolved.SchemaVersions[section], SectionSchemaVersions[section])
		}
	}
}

// The digest identifies one exact resolved configuration. It must be stable
// across repeated computation and across a store/load round trip, and it must
// change when the resolved configuration changes.
func TestResolvedSectionsDigestIsStableAndSensitive(t *testing.T) {
	draft := resolvedTestDraft(t)

	resolved, err := ResolveDraftEffectiveSections(draft)
	if err != nil {
		t.Fatalf("resolve sections: %v", err)
	}
	first, err := resolved.EffectiveDigest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	second, err := resolved.EffectiveDigest()
	if err != nil {
		t.Fatalf("digest again: %v", err)
	}
	if first != second {
		t.Fatalf("digest is not deterministic: %s != %s", first, second)
	}
	if len(first) != len("sha256:")+64 || first[:7] != "sha256:" {
		t.Fatalf("digest has unexpected shape: %s", first)
	}

	// Round trip through the stored JSON columns must reproduce the same digest.
	effectiveJSON, err := json.Marshal(resolved.Sections)
	if err != nil {
		t.Fatal(err)
	}
	versionsJSON, err := json.Marshal(resolved.Versions)
	if err != nil {
		t.Fatal(err)
	}
	schemasJSON, err := json.Marshal(resolved.SchemaVersions)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ResolvedSectionsFromStored(effectiveJSON, versionsJSON, schemasJSON)
	if err != nil {
		t.Fatalf("restore stored: %v", err)
	}
	restoredDigest, err := restored.EffectiveDigest()
	if err != nil {
		t.Fatalf("digest restored: %v", err)
	}
	if restoredDigest != first {
		t.Fatalf("digest changed across store/load: %s != %s", restoredDigest, first)
	}

	// Changing the draft's subgroup policy must change the digest.
	draft.Subgroups.MaxRss = 3
	changedResolved, err := ResolveDraftEffectiveSections(draft)
	if err != nil {
		t.Fatalf("resolve changed sections: %v", err)
	}
	changedDigest, err := changedResolved.EffectiveDigest()
	if err != nil {
		t.Fatalf("digest changed: %v", err)
	}
	if changedDigest == first {
		t.Fatal("digest did not change when the resolved configuration changed")
	}
}

// A stored revision missing a canonical section is not silently executable.
func TestResolvedSectionsFromStoredRejectsMissingSection(t *testing.T) {
	if _, err := ResolvedSectionsFromStored(json.RawMessage(`{"subgroups":{}}`), json.RawMessage(`{}`), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected a missing-section error")
	}
}
