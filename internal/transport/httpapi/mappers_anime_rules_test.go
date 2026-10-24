package httpapi

import (
	"encoding/json"
	"reflect"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/transport/httpapi/generated"
)

func assertRulesJSONEqual(t *testing.T, left, right any) {
	t.Helper()
	leftRaw, err := json.Marshal(left)
	if err != nil {
		t.Fatal(err)
	}
	rightRaw, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	var leftValue, rightValue any
	if err := json.Unmarshal(leftRaw, &leftValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rightRaw, &rightValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leftValue, rightValue) {
		t.Fatalf("JSON differs:\nleft:  %s\nright: %s", leftRaw, rightRaw)
	}
}

func TestRulesPatchHTTPMappersPreserveInputAndResultShape(t *testing.T) {
	sections := generated.RulesSections{}
	sections.Parser = &generated.ParserRulesOverride{}
	if err := sections.Parser.FromParserRulesReplace(generated.ParserRulesReplace{
		Mode: generated.ParserRulesReplaceModeReplace,
		Value: generated.ParserRules{
			EpisodePatterns: []string{"S(\\d+)"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	sections.ReleaseFilter = &generated.ReleaseFilterRulesOverride{}
	if err := sections.ReleaseFilter.FromReleaseFilterRulesReplace(generated.ReleaseFilterRulesReplace{
		Mode: generated.ReleaseFilterRulesReplaceModeReplace,
		Value: generated.ReleaseFilterRules{
			MustContain: "",
			NotIn:       "cam",
		},
	}); err != nil {
		t.Fatal(err)
	}
	sections.FileFilter = &generated.FileFilterRulesOverride{}
	if err := sections.FileFilter.FromFileFilterRulesReplace(generated.FileFilterRulesReplace{
		Mode:  generated.FileFilterRulesReplaceModeReplace,
		Value: generated.FileFilterRules{MustContain: "", NotIn: ""},
	}); err != nil {
		t.Fatal(err)
	}
	sections.FileEffect = &generated.FileEffectRulesOverride{}
	if err := sections.FileEffect.FromFileEffectRulesReplace(generated.FileEffectRulesReplace{
		Mode: generated.FileEffectRulesReplaceModeReplace,
		Value: generated.FileEffectRules{
			Operation:       generated.FileEffectRulesOperationCopy,
			CollisionPolicy: generated.CollisionPolicyFail,
		},
	}); err != nil {
		t.Fatal(err)
	}

	input, err := mapRulesPatchInput(generated.RulesPatch{ExpectedVersion: 7, Sections: sections})
	if err != nil {
		t.Fatal(err)
	}
	if input.ExpectedVersion != 7 {
		t.Fatalf("expected version = %d, want 7", input.ExpectedVersion)
	}
	mappedSections, err := animetransport.RulesSectionsToHTTP(input.Sections)
	if err != nil {
		t.Fatal(err)
	}
	assertRulesJSONEqual(t, sections, mappedSections)

	effective := animedomain.EffectiveRules{}
	result := ownershipapplication.RulesPatchResult{
		Reevaluation: ownershipapplication.RulesPatchReevaluation{
			Accepted:       true,
			ManagedAnimeID: "anime-7",
			ScheduledCount: 3,
		},
		Rules: ownershipapplication.RulesOwner{
			ManagedAnimeID: "anime-7",
			Sections:       input.Sections,
			Effective:      effective,
			Version:        8,
		},
	}
	mapped, err := mapRulesPatchResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Reevaluation.ManagedAnimeId != "anime-7" || mapped.Reevaluation.ScheduledCount != 3 {
		t.Fatalf("reevaluation = %+v", mapped.Reevaluation)
	}
	if mapped.Rules.Version != 8 || mapped.Rules.ManagedAnimeId != "anime-7" {
		t.Fatalf("rules owner = %+v", mapped.Rules)
	}
	assertRulesJSONEqual(t, sections, mapped.Rules.Sections)
}

func stringSlicePtr(value []string) *[]string { return &value }
