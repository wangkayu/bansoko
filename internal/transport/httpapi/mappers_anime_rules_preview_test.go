package httpapi

import (
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// The owner_draft read mode carries the unsaved Rules sections, so the input
// mapper is the surviving half of the retired flat rules preview mapper.
func TestMapRulesPreviewInputKeepsSections(t *testing.T) {
	sections := generated.RulesSections{}
	sections.Parser = &generated.ParserRulesOverride{}
	if err := sections.Parser.FromParserRulesReplace(generated.ParserRulesReplace{
		Mode:  generated.ParserRulesReplaceMode("replace"),
		Value: generated.ParserRules{EpisodePatterns: []string{`\[(?P<episode>\d{1,4})\]`}},
	}); err != nil {
		t.Fatal(err)
	}

	mapped, err := mapRulesPreviewInput(sections)
	if err != nil {
		t.Fatalf("map rules preview input: %v", err)
	}
	wantSections, err := json.Marshal(sections)
	if err != nil {
		t.Fatalf("marshal input sections: %v", err)
	}
	if string(mapped.Sections) != string(wantSections) {
		t.Fatalf("mapped sections = %s, want %s", mapped.Sections, wantSections)
	}
}

// mapRulesPreviewParsedFacts preserves episode and coverage independently of
// native source tags.
func TestMapRulesPreviewParsedFactsPreservesCoverage(t *testing.T) {
	episode := float32(3)
	mapped := mapRulesPreviewParsedFacts(&animedomain.ParsedReleaseFacts{
		Episode:  &episode,
		Coverage: &animedomain.ReleaseCoverage{Episodes: []int{3, 4}},
	})
	if mapped == nil || mapped.Episode == nil || *mapped.Episode != episode {
		t.Fatalf("mapped episode = %#v", mapped)
	}
	if mapped.Coverage == nil || len(mapped.Coverage.Episodes) != 2 || mapped.Coverage.Episodes[1] != 4 {
		t.Fatalf("mapped coverage = %#v", mapped.Coverage)
	}
	if mapped := mapRulesPreviewParsedFacts(&animedomain.ParsedReleaseFacts{}); mapped == nil || mapped.Episode != nil || mapped.Coverage != nil {
		t.Fatalf("zero facts changed shape: %#v", mapped)
	}
}
