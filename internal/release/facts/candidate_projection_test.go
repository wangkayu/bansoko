package facts

import (
	"reflect"
	"testing"

	"bansoko/internal/attrrules"
)

func projectionRules() attrrules.Rules {
	return attrrules.Rules{
		Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web-dl"},
		Language: []string{"chs"}, SpecialMarkers: []string{"special", "ncop", "nced"},
		GroupBrackets: []string{"[", "]"},
	}
}

// Candidate coverage is a projection of the frozen BatchPlan coverage keys. It
// never re-parses the title: a title naming another episode cannot move it, a
// batch has no scalar episode, and no key is unresolved coverage.
func TestCandidateFromCoverageNeverInventsCoverage(t *testing.T) {
	tests := []struct {
		name         string
		title        string
		keys         []string
		wantCoverage []string
		wantEpisode  string
		wantKind     string
	}{
		{name: "single key projects the episode", title: "[Group] Show - 99 [1080p]", keys: []string{"tv:e12"},
			wantCoverage: []string{"tv:e12"}, wantEpisode: "12", wantKind: "video"},
		{name: "a frozen v1 key projects the episode", title: "[Group] Show - 99 [1080p]", keys: []string{"tv:s1:e12"},
			wantCoverage: []string{"tv:s1:e12"}, wantEpisode: "12", wantKind: "video"},
		{name: "batch keys are deduplicated and sorted without a scalar episode", title: "[Group] Show 01-04 [1080p]",
			keys: []string{"tv:s1:e3", "tv:s1:e1", "tv:s1:e3", "tv:s1:e2"}, wantCoverage: []string{"tv:s1:e1", "tv:s1:e2", "tv:s1:e3"}, wantKind: "video"},
		{name: "movie key is a movie", title: "[Group] Movie [1080p]", keys: []string{"movie:0"},
			wantCoverage: []string{"movie:0"}, wantKind: "movie"},
		{name: "no key is unresolved", title: "[Group] Show - 05 [1080p]", keys: nil,
			wantCoverage: []string{CoverageUnresolved}, wantKind: "video"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := CandidateFromCoverage(test.title, projectionRules(), test.keys)
			if !reflect.DeepEqual(candidate.Coverage, test.wantCoverage) {
				t.Fatalf("coverage = %v, want %v", candidate.Coverage, test.wantCoverage)
			}
			if candidate.CoverageUnresolved != (len(test.keys) == 0) {
				t.Fatalf("unresolved = %v for keys %v", candidate.CoverageUnresolved, test.keys)
			}
			if candidate.Episode != test.wantEpisode || candidate.MediaKind != test.wantKind {
				t.Fatalf("candidate = %+v", candidate)
			}
			if candidate.Resolution != "1080p" {
				t.Fatalf("quality dimensions still come from the title: %+v", candidate)
			}
		})
	}
}
