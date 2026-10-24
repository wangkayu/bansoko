package filemaintenance

import (
	"reflect"
	"testing"

	"bansoko/internal/qualityprofile"
)

func TestFrozenReplacementComparisonKeepsEvaluatedInputs(t *testing.T) {
	candidate := released(Facts{Resolution: "1080p", Group: "G"})
	incumbent := released(Facts{Resolution: "720p", Group: "G"})
	ref := PlacementRef{PlacementID: "incumbent", Version: 2, Fingerprint: "fingerprint"}
	pools := []PoolEvidence{{Verdict: "replace", Losers: []PlacementRef{ref}}, {Verdict: "retain", Losers: []PlacementRef{ref}}}
	profile := Profile{Version: qualityprofile.Version, Items: []qualityprofile.Item{
		{Key: qualityprofile.KeyReleaseResolution, Ranks: [][]string{{"1080p"}, {"720p"}}},
		{Key: qualityprofile.KeyReleaseLang, Ranks: [][]string{}},
	}}
	freezeReplacementComparisons(pools, candidate, []PlacementFacts{{PlacementID: ref.PlacementID, Version: ref.Version, Fingerprint: ref.Fingerprint, Facts: incumbent, FactsOK: true}}, profile)
	if len(pools[0].Comparisons) != 1 || len(pools[1].Comparisons) != 0 {
		t.Fatal("incorrect comparison admission")
	}
	frozen := pools[0].Comparisons[0]
	if !reflect.DeepEqual(frozen.Candidate, candidate) || !reflect.DeepEqual(frozen.Existing, incumbent) || frozen.Placement != ref || !reflect.DeepEqual(frozen.Dimensions, []string{"languages", "resolution", "size"}) {
		t.Fatalf("input changed %+v", frozen)
	}
}

func TestFrozenReplacementComparisonRejectsFalsePoolApproval(t *testing.T) {
	ref := PlacementRef{PlacementID: "incumbent", Version: 2, Fingerprint: "fingerprint"}
	profile := resolutionProfile("1080p", "720p")
	for _, existing := range []PlacementFacts{
		{PlacementID: ref.PlacementID, Version: 1, Fingerprint: ref.Fingerprint, Facts: released(Facts{Resolution: "720p"}), FactsOK: true},
		{PlacementID: ref.PlacementID, Version: 2, Fingerprint: "foreign", Facts: released(Facts{Resolution: "720p"}), FactsOK: true},
		{PlacementID: ref.PlacementID, Version: 2, Fingerprint: ref.Fingerprint, Facts: released(Facts{Resolution: "1080p"}), FactsOK: true},
	} {
		pools := []PoolEvidence{{Verdict: "replace", Losers: []PlacementRef{ref}}}
		freezeReplacementComparisons(pools, released(Facts{Resolution: "1080p"}), []PlacementFacts{existing}, profile)
		if len(pools[0].Comparisons) != 0 {
			t.Fatalf("false approval frozen: %+v", existing)
		}
	}
}
