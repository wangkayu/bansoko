package filemaintenance

import (
	"testing"
	"time"

	"bansoko/internal/qualityprofile"
)

func scoredFacts(groupRef string, score *int64) Facts {
	return Facts{Group: groupRef, Subgroup: &qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: groupRef}, Score: score}
}

// A pool that compared with subgroup.score freezes the score of every source
// it compared (candidate and members), sorted and once each.
func TestEvaluatePoolsFreezesTheSubgroupScoresItCompared(t *testing.T) {
	members := []PlacementFacts{
		{PlacementID: "plc-a", Coverage: []string{"tv:s1:e1"}, Facts: scoredFacts("group-z", int64Ptr(10)), FactsOK: true, CreatedAt: time.Unix(1, 0)},
		{PlacementID: "plc-b", Coverage: []string{"tv:s1:e1"}, Facts: scoredFacts("group-a", nil), FactsOK: true, CreatedAt: time.Unix(2, 0)},
	}
	candidate := scoredFacts("group-m", int64Ptr(90))
	pools := EvaluatePools(ModeSingleCopy, []string{"tv:s1:e1"}, candidate, PlacementRef{}, members, qualityprofile.Default(), nil)
	if len(pools) != 1 || pools[0].Verdict != "replace" {
		t.Fatalf("the higher score must replace: %+v", pools)
	}
	got := pools[0].SubgroupScores
	if len(got) != 3 || got[0].GroupRef != "group-a" || got[0].Score != nil ||
		got[1].GroupRef != "group-m" || *got[1].Score != 90 || got[2].GroupRef != "group-z" || *got[2].Score != 10 {
		t.Fatalf("frozen scores = %+v", got)
	}

	// No subgroup.score item, or an empty pool: nothing compared a score.
	if pools := EvaluatePools(ModeSingleCopy, []string{"tv:s1:e1"}, candidate, PlacementRef{}, members, resolutionProfile("1080p"), nil); pools[0].SubgroupScores != nil {
		t.Fatalf("a profile without subgroup.score freezes no scores: %+v", pools[0].SubgroupScores)
	}
	if pools := EvaluatePools(ModeSingleCopy, []string{"tv:s1:e2"}, candidate, PlacementRef{}, members, qualityprofile.Default(), nil); pools[0].SubgroupScores != nil {
		t.Fatalf("an empty pool compares nothing: %+v", pools[0].SubgroupScores)
	}
}

func TestSubgroupScoresChangedComparesEveryFrozenSource(t *testing.T) {
	frozen := []FrozenSubgroupScore{
		{Provider: "anibt", GroupRef: "g-1", Score: int64Ptr(70)},
		{Provider: "anibt", GroupRef: "g-2"},
	}
	g1 := qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "g-1"}
	g2 := qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "g-2"}
	for name, tc := range map[string]struct {
		current SubgroupScores
		changed bool
	}{
		"same":            {SubgroupScores{g1: 70}, false},
		"unrelated added": {SubgroupScores{g1: 70, {Provider: "anibt", GroupRef: "g-3"}: 1}, false},
		"value changed":   {SubgroupScores{g1: 71}, true},
		"score removed":   {SubgroupScores{}, true},
		"score added":     {SubgroupScores{g1: 70, g2: 0}, true},
	} {
		if got := subgroupScoresChanged(frozen, tc.current); got != tc.changed {
			t.Errorf("%s: changed = %v, want %v", name, got, tc.changed)
		}
	}
}
