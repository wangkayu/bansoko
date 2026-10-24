package application

import (
	"bansoko/internal/attrrules"
	"testing"
)

// Valid independent producer rules, not retention quality ranks.
func businessProgramRulesFixture() attrrules.Rules {
	return attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web-dl"}, Language: []string{"jpn"}, SpecialMarkers: []string{"special"}, GroupBrackets: []string{"[", "]"}}
}
func TestBusinessProgramFixtureMeetsProducerContract(t *testing.T) {
	rules := businessProgramRulesFixture()
	if err := rules.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := attrrules.Fingerprint(rules); err != nil {
		t.Fatal(err)
	}
	rules.Resolution = nil
	if err := rules.Validate(); err == nil {
		t.Fatal("empty resolution must remain invalid")
	}
}
