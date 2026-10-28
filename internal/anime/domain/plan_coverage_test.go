package domain

import (
	"reflect"
	"testing"

	"bansoko/internal/batchplan"
)

func TestApplyPlanCoverage(t *testing.T) {
	cases := []struct {
		name     string
		keys     []string
		episode  *float32
		coverage *ReleaseCoverage
	}{
		{name: "single tv key", keys: []string{"tv:e7"}, episode: ptrFloat(7)},
		{name: "single frozen v1 key", keys: []string{"tv:s1:e7"}, episode: ptrFloat(7)},
		{name: "movie keeps canonical zero", keys: []string{"movie:0"}, episode: ptrFloat(0)},
		{name: "several keys are a coverage", keys: []string{"tv:e1", "tv:e2", "tv:e3"}, coverage: &ReleaseCoverage{Episodes: []int{1, 2, 3}}},
		{name: "several frozen v1 keys are a coverage", keys: []string{"tv:s2:e1", "tv:s2:e2", "tv:s2:e3"}, coverage: &ReleaseCoverage{Episodes: []int{1, 2, 3}}},
		{name: "no key clears the title episode", keys: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title := float32(99)
			result := PreviewReleaseResult{ParsedFacts: &ParsedReleaseFacts{Episode: &title}}
			ApplyPlanCoverage(&result, &batchplan.Plan{CoverageKeys: tc.keys})
			got := result.ParsedFacts
			if (got.Episode == nil) != (tc.episode == nil) || (got.Episode != nil && *got.Episode != *tc.episode) {
				t.Fatalf("episode = %v, want %v", got.Episode, tc.episode)
			}
			if (got.Coverage == nil) != (tc.coverage == nil) {
				t.Fatalf("coverage = %+v, want %+v", got.Coverage, tc.coverage)
			}
			if tc.coverage != nil && !reflect.DeepEqual(got.Coverage.Episodes, tc.coverage.Episodes) {
				t.Fatalf("coverage = %+v, want %+v", got.Coverage, tc.coverage)
			}
		})
	}
}

func ptrFloat(value float32) *float32 { return &value }
