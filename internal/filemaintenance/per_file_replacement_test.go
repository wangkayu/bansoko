package filemaintenance

import (
	"reflect"
	"testing"

	"bansoko/internal/batchplan"
)

func perFileProfile() Profile {
	return resolutionProfile("2160p", "1080p", "720p", "480p")
}

func present(id string, resolution string, keys ...string) PlacementFacts {
	return PlacementFacts{PlacementID: id, Version: 1, Fingerprint: "fp-" + id, Coverage: keys, Facts: released(Facts{Resolution: resolution}), FactsOK: true}
}

func verdictsOf(pools []PoolEvidence) map[string]string {
	out := map[string]string{}
	for _, pool := range pools {
		out[pool.PoolKey] = pool.Verdict + "/" + pool.Reason
	}
	return out
}

// A batch replaces per file: every single-episode file is its own unit, so a
// better E1 replaces the present E1 while E2 (absent) is filled.
func TestEvaluatePoolsBatchReplacesPerFile(t *testing.T) {
	keys := []string{"tv:s1:e1", "tv:s1:e2"}
	pools := EvaluatePools(ModeSingleCopy, keys, released(Facts{Resolution: "1080p"}), poolRef("release:r", 1, "fp"),
		[]PlacementFacts{present("old-e1", "720p", "tv:s1:e1")}, perFileProfile(),
		map[string][]string{"tv:s1:e1": {"tv:s1:e1"}, "tv:s1:e2": {"tv:s1:e2"}})
	if len(pools) != 2 || pools[0].Verdict != "replace" || len(pools[0].Losers) != 1 || pools[1].Verdict != "replace" || len(pools[1].Losers) != 0 {
		t.Fatalf("pools = %+v", pools)
	}
}

// A single-episode candidate never replaces a present multi-episode file: it
// would drop the other episode (replacement_partial_coverage).
func TestEvaluatePoolsPartialCoverageKeepsMultiEpisodeFile(t *testing.T) {
	pools := EvaluatePools(ModeSingleCopy, []string{"tv:s1:e1"}, released(Facts{Resolution: "1080p"}), poolRef("release:r", 1, "fp"),
		[]PlacementFacts{present("old-e1e2", "720p", "tv:s1:e1", "tv:s1:e2")}, perFileProfile(), nil)
	if len(pools) != 1 || pools[0].Verdict != "keep" || pools[0].Reason != ReasonReplacementPartialCoverage || pools[0].Winner == nil || pools[0].Winner.PlacementID != "old-e1e2" || len(pools[0].Losers) != 0 {
		t.Fatalf("pools = %+v", pools)
	}
	// A multi-episode candidate covering both keys replaces it.
	units := map[string][]string{"tv:s1:e1": {"tv:s1:e1", "tv:s1:e2"}, "tv:s1:e2": {"tv:s1:e1", "tv:s1:e2"}}
	pools = EvaluatePools(ModeSingleCopy, []string{"tv:s1:e1", "tv:s1:e2"}, released(Facts{Resolution: "1080p"}), poolRef("release:r", 1, "fp"),
		[]PlacementFacts{present("old-e1e2", "720p", "tv:s1:e1", "tv:s1:e2")}, perFileProfile(), units)
	if got := verdictsOf(pools); got["tv:s1:e1"][:7] != "replace" || got["tv:s1:e2"][:7] != "replace" {
		t.Fatalf("multi-episode candidate verdicts = %v", got)
	}
}

// A multi-episode candidate replaces only when both keys would: a better E1
// but a worse-or-equal E2 keeps both present files (replacement_unit_kept);
// an empty key of the unit is still filled.
func TestEvaluatePoolsMultiEpisodeUnitReplacesOnlyAsAWhole(t *testing.T) {
	units := map[string][]string{"tv:s1:e1": {"tv:s1:e1", "tv:s1:e2"}, "tv:s1:e2": {"tv:s1:e1", "tv:s1:e2"}}
	current := []PlacementFacts{present("old-e1", "720p", "tv:s1:e1"), present("old-e2", "2160p", "tv:s1:e2")}
	pools := EvaluatePools(ModeSingleCopy, []string{"tv:s1:e1", "tv:s1:e2"}, released(Facts{Resolution: "1080p"}), poolRef("release:r", 1, "fp"),
		current, perFileProfile(), units)
	got := verdictsOf(pools)
	if got["tv:s1:e1"] != "keep/"+ReasonReplacementUnitKept || pools[0].Winner == nil || pools[0].Winner.PlacementID != "old-e1" || len(pools[0].Losers) != 0 {
		t.Fatalf("unit-kept pool = %+v", pools[0])
	}
	if pools[1].Verdict != "retain" {
		t.Fatalf("E2 pool = %+v", pools[1])
	}
	// E2 absent: the unit has nothing to keep, E1 is replaced and E2 filled.
	pools = EvaluatePools(ModeSingleCopy, []string{"tv:s1:e1", "tv:s1:e2"}, released(Facts{Resolution: "1080p"}), poolRef("release:r", 1, "fp"),
		current[:1], perFileProfile(), units)
	if pools[0].Verdict != "replace" || len(pools[0].Losers) != 1 || pools[1].Verdict != "replace" {
		t.Fatalf("pools = %+v", pools)
	}
}

// per_group units compare inside the group pools; the unit rule strips the
// group suffix to find the sibling key.
func TestEvaluatePoolsUnitRulePerGroup(t *testing.T) {
	units := map[string][]string{"tv:s1:e1": {"tv:s1:e1", "tv:s1:e2"}, "tv:s1:e2": {"tv:s1:e1", "tv:s1:e2"}}
	mine := func(id, resolution, key string) PlacementFacts {
		p := present(id, resolution, key)
		p.Facts.Group = "G"
		return p
	}
	pools := EvaluatePools(ModePerGroup, []string{"tv:s1:e1", "tv:s1:e2"}, released(Facts{Resolution: "1080p", Group: "G"}), poolRef("release:r", 1, "fp"),
		[]PlacementFacts{mine("old-e1", "720p", "tv:s1:e1"), mine("old-e2", "2160p", "tv:s1:e2")}, perFileProfile(), units)
	if got := verdictsOf(pools); got["tv:s1:e1|G"] != "keep/"+ReasonReplacementUnitKept {
		t.Fatalf("verdicts = %v", got)
	}
}

// The plan-side verdict of a pool kept for partial coverage is
// replace_partial; a unit-kept pool is an ordinary existing key.
func TestCoverageVerdictsPartialAndUnitKept(t *testing.T) {
	keys := []string{"tv:s1:e1", "tv:s1:e2"}
	pools := []PoolEvidence{
		{PoolKey: "tv:s1:e1", Verdict: "keep", Reason: ReasonReplacementPartialCoverage},
		{PoolKey: "tv:s1:e2", Verdict: "keep", Reason: ReasonReplacementUnitKept},
	}
	got := coverageVerdicts(ModeSingleCopy, keys, "", pools, nil)
	want := map[string]batchplan.Verdict{"tv:s1:e1": batchplan.VerdictReplacePartial, "tv:s1:e2": batchplan.VerdictExisting}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdicts = %v, want %v", got, want)
	}
}

// Each key's decision has its own bounded id and idempotency key; the same
// inputs give the same identity.
func TestDecisionIdentityPerKey(t *testing.T) {
	keyA, idA := decisionIdentity("anime", "release", "tv:s1:e1", "0123456789abcdef0123456789abcdef0123")
	keyB, idB := decisionIdentity("anime", "release", "tv:s1:e2", "0123456789abcdef0123456789abcdef0123")
	if keyA == keyB || idA == idB {
		t.Fatalf("keys share an identity: %s %s", keyA, keyB)
	}
	if again, id := decisionIdentity("anime", "release", "tv:s1:e1", "0123456789abcdef0123456789abcdef0123"); again != keyA || id != idA {
		t.Fatal("identity is not deterministic")
	}
	if len(idA) != len("dec-")+40 {
		t.Fatalf("id length = %d", len(idA))
	}
	pools := poolsForKey([]PoolEvidence{{PoolKey: "tv:s1:e1|G"}, {PoolKey: "tv:s1:e2|G"}, {PoolKey: "all"}}, "tv:s1:e2", ModePerGroup)
	if len(pools) != 2 || pools[0].PoolKey != "tv:s1:e2|G" || pools[1].PoolKey != "all" {
		t.Fatalf("poolsForKey = %+v", pools)
	}
}
