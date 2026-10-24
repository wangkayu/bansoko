package domain

import (
	"encoding/json"
	"testing"
)

// storedDefaultSections returns a complete stored section set, each section at
// its code default: what an instance stores after copying an unsaved Global.
func storedDefaultSections(t *testing.T) (OverrideValue, map[string]OverrideValue) {
	t.Helper()
	var subgroups OverrideValue
	rules := map[string]OverrideValue{}
	for _, section := range append(append([]string{}, EffectiveSectionOrder...), SectionTagFilters) {
		value, err := DefaultSectionValue(section)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if section == SectionSubgroups {
			subgroups = OverrideValue{Value: raw}
			continue
		}
		rules[section] = OverrideValue{Value: raw}
	}
	return subgroups, rules
}

// draftEnvelope wraps one section value in the draft's replace envelope.
func draftEnvelope(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := SectionEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	return wrapped
}

// completeDraftRules returns rules sections carrying every section at its
// code default, as a draft created from an unsaved Global does.
func completeDraftRules(t *testing.T) RulesSections {
	t.Helper()
	value := func(section string) json.RawMessage {
		v, err := DefaultSectionValue(section)
		if err != nil {
			t.Fatal(err)
		}
		return draftEnvelope(t, v)
	}
	return RulesSections{
		Parser:        value(SectionParser),
		ReleaseFilter: value(SectionReleaseRules),
		FileFilter:    value(SectionFileRules),
		Naming:        value(SectionNamingRules),
		FileEffect:    value(SectionFileEffect),
		TagFilters:    value(SectionTagFilters),
	}
}
