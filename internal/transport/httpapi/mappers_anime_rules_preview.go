package httpapi

import (
	"encoding/json"

	animedomain "bansoko/internal/anime/domain"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

// mapRulesPreviewInput serializes the unsaved owner rules sections the
// RSSListView owner_draft read mode carries. The retired flat rules preview
// route is gone; this is the surviving owner_draft request path.
func mapRulesPreviewInput(value generated.RulesSections) (readmodelapplication.RulesPreviewInput, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return readmodelapplication.RulesPreviewInput{}, err
	}
	return readmodelapplication.RulesPreviewInput{Sections: animedomain.RulesPreviewSections(raw)}, nil
}

func mapRulesPreviewParsedFacts(value *animedomain.ParsedReleaseFacts) *generated.ParsedReleaseFacts {
	if value == nil {
		return nil
	}
	mapped := &generated.ParsedReleaseFacts{
		Episode:   cloneRulesPreviewFloat32(value.Episode),
		GroupName: cloneRulesPreviewString(value.GroupName),
		Languages: cloneRulesPreviewStrings(value.Languages),
		Quality:   cloneRulesPreviewString(value.Quality),
		Special:   cloneRulesPreviewBool(value.Special),
	}
	if value.Coverage != nil {
		mapped.Coverage = &generated.ReleaseCoverage{Episodes: append([]int(nil), value.Coverage.Episodes...)}
	}
	return mapped
}

func cloneRulesPreviewInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRulesPreviewString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRulesPreviewFloat32(value *float32) *float32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRulesPreviewBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRulesPreviewStrings(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	cloned := make([]string, len(*value))
	copy(cloned, *value)
	return &cloned
}
