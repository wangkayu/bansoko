package transport

import (
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

func TestRulesSectionsTagFiltersRoundTrip(t *testing.T) {
	section := func(name string) json.RawMessage {
		value, err := animedomain.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		wrapped, err := animedomain.SectionEnvelope(raw)
		if err != nil {
			t.Fatal(err)
		}
		return wrapped
	}
	domain := animedomain.RulesSections{
		Parser: section(animedomain.SectionParser), ReleaseFilter: section(animedomain.SectionReleaseRules),
		FileFilter: section(animedomain.SectionFileRules), Naming: section(animedomain.SectionNamingRules),
		FileEffect: section(animedomain.SectionFileEffect),
	}
	wire, err := RulesSectionsToHTTP(domain)
	if err != nil {
		t.Fatal(err)
	}
	if wire.TagFilters != nil {
		t.Fatal("absent tag_filters must stay absent on the wire")
	}
	back, err := RulesSections(wire)
	if err != nil || back.TagFilters != nil {
		t.Fatalf("absent round trip = %s, %v", back.TagFilters, err)
	}
	domain.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":[],"exclude":["tag:HEVC"]},"title":{"require":[],"exclude":[]}}}`)
	wire, err = RulesSectionsToHTTP(domain)
	if err != nil || wire.TagFilters == nil {
		t.Fatalf("replace to wire = %+v, %v", wire.TagFilters, err)
	}
	replace, err := wire.TagFilters.AsTagFilterRulesReplace()
	if err != nil || replace.Value.Anibt.Exclude[0] != "codec:HEVC" || len(replace.Value.Anibt.Require) != 0 {
		t.Fatalf("replace value = %+v, %v", replace, err)
	}
	back, err = RulesSections(wire)
	if err != nil {
		t.Fatal(err)
	}
	overrides, violations := animedomain.RulesSectionOverrides(back)
	if len(violations) != 0 || len(overrides[animedomain.SectionTagFilters].Value) == 0 {
		t.Fatalf("round trip overrides = %+v, %+v", overrides, violations)
	}
	effective := TagFilterRulesToHTTP(animedomain.DefaultTagFilterRules())
	if effective.Anibt.Require == nil || effective.Anibt.Exclude == nil {
		t.Fatalf("effective = %+v", effective)
	}
}
