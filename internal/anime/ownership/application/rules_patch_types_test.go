package application

import (
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

func TestRulesSectionsFromDomainOverridesWrapsStoredValues(t *testing.T) {
	sections := rulesSectionsFromDomainOverrides(map[string]animedomain.OverrideValue{
		animedomain.SectionParser:      {Value: json.RawMessage(`{"episode_patterns":["S(\\d+)"]}`)},
		animedomain.SectionNamingRules: {Value: json.RawMessage(`{"language":"js-naming-1"}`)},
	})
	var parser struct {
		Mode  string          `json:"mode"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(sections.Parser, &parser); err != nil {
		t.Fatal(err)
	}
	if parser.Mode != "replace" || string(parser.Value) != `{"episode_patterns":["S(\\d+)"]}` {
		t.Fatalf("parser union = %s, want replace wrapper", sections.Parser)
	}
	var naming map[string]any
	if err := json.Unmarshal(sections.Naming, &naming); err != nil {
		t.Fatal(err)
	}
	if naming["mode"] != "replace" {
		t.Fatalf("naming union = %s, want the stored value as replace", sections.Naming)
	}
}
