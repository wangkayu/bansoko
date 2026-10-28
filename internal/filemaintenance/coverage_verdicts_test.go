package filemaintenance

import (
	"reflect"
	"testing"

	"bansoko/internal/batchplan"
)

// The read-only dedup verdict per key: a present placement (retain/keep)
// covers it, a running acquisition of another release claims it, a better
// candidate with losers is a replacement, anything else stays open.
func TestCoverageVerdictsSingleCopy(t *testing.T) {
	keys := []string{"tv:e1", "tv:e2", "tv:e3", "tv:e4", "tv:e5"}
	pools := []PoolEvidence{
		{PoolKey: "tv:e1", Verdict: "retain"},
		{PoolKey: "tv:e2", Verdict: "replace", Losers: []PlacementRef{{PlacementID: "old", Version: 1}}},
		{PoolKey: "tv:e3", Verdict: "replace"},
		{PoolKey: "tv:e5", Verdict: "keep"},
	}
	claims := []inFlightClaim{{Group: "Other", Keys: []string{"tv:e4", "tv:e5"}}}
	got := coverageVerdicts(ModeSingleCopy, keys, "Mine", pools, claims)
	want := map[string]batchplan.Verdict{
		"tv:e1": batchplan.VerdictExisting,
		"tv:e2": batchplan.VerdictReplace,
		"tv:e3": batchplan.VerdictOpen,
		"tv:e4": batchplan.VerdictInFlight,
		"tv:e5": batchplan.VerdictExisting,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdicts = %v\nwant %v", got, want)
	}
}

// per_group pools carry the group in the key, and only a claim of the same
// group covers a key; a candidate without a group is never claimed.
func TestCoverageVerdictsPerGroup(t *testing.T) {
	keys := []string{"tv:e1", "tv:e2", "tv:e3"}
	pools := []PoolEvidence{{PoolKey: "tv:e1|Mine", Verdict: "keep"}}
	claims := []inFlightClaim{{Group: "Mine", Keys: []string{"tv:e2"}}, {Group: "Other", Keys: []string{"tv:e3"}}}
	got := coverageVerdicts(ModePerGroup, keys, "Mine", pools, claims)
	want := map[string]batchplan.Verdict{
		"tv:e1": batchplan.VerdictExisting,
		"tv:e2": batchplan.VerdictInFlight,
		"tv:e3": batchplan.VerdictOpen,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdicts = %v\nwant %v", got, want)
	}
	if got := coverageVerdicts(ModePerGroup, keys, "", nil, claims); got["tv:e2"] != batchplan.VerdictOpen {
		t.Fatalf("a group-less candidate was claimed: %v", got)
	}
}
