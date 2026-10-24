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

// mapRulesPreviewParsedFacts maps the parsed facts of every RSS list item row.
func TestMapRulesPreviewParsedFactsPreservesLanguageSliceShape(t *testing.T) {
	var nilLanguages *[]string
	emptyLanguages := []string{}
	nonEmptyLanguages := []string{"ja"}

	cases := []struct {
		name    string
		in      *[]string
		wantNil bool
		wantLen int
	}{
		{name: "nil", in: nilLanguages, wantNil: true},
		{name: "empty", in: &emptyLanguages, wantLen: 0},
		{name: "non-empty", in: &nonEmptyLanguages, wantLen: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := mapRulesPreviewParsedFacts(&animedomain.ParsedReleaseFacts{Languages: tc.in})
			if mapped == nil {
				t.Fatal("mapped parsed facts is nil")
			}
			if (mapped.Languages == nil) != tc.wantNil {
				t.Fatalf("languages nil = %v, want %v", mapped.Languages == nil, tc.wantNil)
			}
			if mapped.Languages != nil && len(*mapped.Languages) != tc.wantLen {
				t.Fatalf("languages = %#v, want length %d", *mapped.Languages, tc.wantLen)
			}
		})
	}
}
