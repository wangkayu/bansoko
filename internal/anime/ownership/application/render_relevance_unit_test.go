package application

import (
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

func TestMetadataChangeRenderRelevance(t *testing.T) {
	cases := []struct {
		name                       string
		change                     metadataChange
		renders, reevaluates, held bool
	}{
		{"nothing", metadataChange{}, false, false, false},
		{"title only", metadataChange{title: true}, true, false, false},
		{"bindings", metadataChange{bindings: true}, true, true, true},
		{"total only", metadataChange{total: true}, false, true, true},
		{"media kind", metadataChange{mediaKind: true}, true, true, true},
		{"year", metadataChange{year: true}, true, true, false},
		{"season", metadataChange{season: true}, true, true, false},
	}
	for _, tc := range cases {
		if got := tc.change.rendersFiles(); got != tc.renders {
			t.Errorf("%s: rendersFiles=%v, want %v", tc.name, got, tc.renders)
		}
		if got := tc.change.reevaluatesReleases(); got != tc.reevaluates {
			t.Errorf("%s: reevaluatesReleases=%v, want %v", tc.name, got, tc.reevaluates)
		}
		if got := tc.change.bindingsOrKindOrTotal(); got != tc.held {
			t.Errorf("%s: bindingsOrKindOrTotal=%v, want %v", tc.name, got, tc.held)
		}
	}
}

func TestRulesRenderChangedOnlyForNamingRules(t *testing.T) {
	if rulesRenderChanged(nil) {
		t.Fatal("no changed section must not request a scan")
	}
	if rulesRenderChanged([]string{animedomain.SectionFileRules, animedomain.SectionReleaseRules}) {
		t.Fatal("file and release rules never reach a rendered path")
	}
	if !rulesRenderChanged([]string{animedomain.SectionFileRules, animedomain.SectionNamingRules}) {
		t.Fatal("a naming-rules change must request a scan")
	}
}
