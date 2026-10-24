package application

import (
	"encoding/json"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
)

// materializedDraftSections resolves one test draft against no saved Global
// rows, which is the code-default resolution a revision would have stored.
func materializedDraftSections(t *testing.T) animedomain.ResolvedSections {
	t.Helper()
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatalf("map test draft: %v", err)
	}
	resolved, err := animedomain.ResolveDraftEffectiveSections(draft)
	if err != nil {
		t.Fatalf("resolve draft sections: %v", err)
	}
	return resolved
}

func TestMaterializedOverridesCoverEveryCanonicalSectionInOrder(t *testing.T) {
	overrides, err := animeapplication.MaterializedOverrides(materializedDraftSections(t))
	if err != nil {
		t.Fatalf("materialize overrides: %v", err)
	}
	if len(overrides) != len(animedomain.EffectiveSectionOrder) {
		t.Fatalf("materialized overrides = %d, want %d", len(overrides), len(animedomain.EffectiveSectionOrder))
	}
	for index, section := range animedomain.EffectiveSectionOrder {
		override := overrides[index]
		if override.Section != section {
			t.Fatalf("overrides[%d].Section = %q, want %q", index, override.Section, section)
		}
		expectedOwner := "rules"
		if section == animedomain.SectionSubgroups {
			expectedOwner = "subgroups"
		}
		if override.OwnerType != expectedOwner {
			t.Fatalf("override %s owner = %q, want %q", section, override.OwnerType, expectedOwner)
		}
		if len(override.Value) == 0 {
			t.Fatalf("override %s carries no complete value", section)
		}
		if err := animedomain.ValidateSectionValue(section, override.Value); err != nil {
			t.Fatalf("override %s is not a typed section value: %v", section, err)
		}
	}
}

func TestMaterializedOverridesFailClosedOnMissingSection(t *testing.T) {
	resolved := materializedDraftSections(t)
	delete(resolved.Sections, animedomain.SectionNamingRules)
	if _, err := animeapplication.MaterializedOverrides(resolved); err == nil {
		t.Fatal("materialization accepted a resolved set with a missing section")
	}
}

// TestRevisionEffectiveDigestSurvivesJSONRoundTrip guards the activation
// integrity check. A revision's effective sections are stored as JSONB and read
// back as raw JSON, so the digest the activation recomputes must still match the
// stored one; otherwise every activation of a valid revision would fail closed.
func TestRevisionEffectiveDigestSurvivesJSONRoundTrip(t *testing.T) {
	resolved := materializedDraftSections(t)
	stored, err := resolved.EffectiveDigest()
	if err != nil {
		t.Fatalf("digest resolved sections: %v", err)
	}
	effectiveJSON, err := json.Marshal(resolved.Sections)
	if err != nil {
		t.Fatalf("marshal effective sections: %v", err)
	}
	versionsJSON, err := json.Marshal(resolved.Versions)
	if err != nil {
		t.Fatalf("marshal section versions: %v", err)
	}
	schemasJSON, err := json.Marshal(resolved.SchemaVersions)
	if err != nil {
		t.Fatalf("marshal section schemas: %v", err)
	}
	reloaded, err := animedomain.ResolvedSectionsFromStored(effectiveJSON, versionsJSON, schemasJSON)
	if err != nil {
		t.Fatalf("reload stored sections: %v", err)
	}
	recomputed, err := reloaded.EffectiveDigest()
	if err != nil {
		t.Fatalf("digest reloaded sections: %v", err)
	}
	if recomputed != stored {
		t.Fatalf("effective digest changed across a JSON round trip: %s -> %s", stored, recomputed)
	}
}
