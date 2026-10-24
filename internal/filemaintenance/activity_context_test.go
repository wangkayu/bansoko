package filemaintenance

import (
	"reflect"
	"testing"
)

func TestReplacementResultOf(t *testing.T) {
	ref := func(id string) *PlacementRef { return &PlacementRef{PlacementID: id} }
	for name, tc := range map[string]struct {
		verdict   string
		pools     []PoolEvidence
		candidate string
		want      replacementResult
	}{
		"replace lists losers once, never the candidate": {
			verdict: "replace",
			pools: []PoolEvidence{
				{Verdict: "replace", Winner: ref("cand"), Losers: []PlacementRef{{PlacementID: "old-1"}, {PlacementID: "cand"}}},
				{Verdict: "replace", Losers: []PlacementRef{{PlacementID: "old-1"}, {PlacementID: "old-2"}}},
				{Verdict: "retain", Losers: []PlacementRef{{PlacementID: "ignored"}}},
			},
			candidate: "cand",
			want:      replacementResult{replaced: []string{"old-1", "old-2"}},
		},
		"retain names the kept version": {
			verdict:   "retain",
			pools:     []PoolEvidence{{Verdict: "retain", Winner: ref("old-1")}},
			candidate: "cand",
			want:      replacementResult{kept: "old-1"},
		},
		"keep without another winner counts versions when placed": {
			verdict:   "keep",
			pools:     []PoolEvidence{{Verdict: "keep", Winner: ref("cand")}},
			candidate: "cand",
			want:      replacementResult{countVersions: true},
		},
		"queue counts versions when placed": {
			verdict:   "queue",
			candidate: "cand",
			want:      replacementResult{countVersions: true},
		},
		"queue before placement counts nothing": {
			verdict: "queue",
			want:    replacementResult{},
		},
		"blocked reports nothing": {
			verdict:   "blocked",
			pools:     []PoolEvidence{{Verdict: "blocked", Losers: []PlacementRef{{PlacementID: "old-1"}}}},
			candidate: "cand",
			want:      replacementResult{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := replacementResultOf(tc.verdict, tc.pools, tc.candidate); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("replacementResultOf = %#v, want %#v", got, tc.want)
			}
		})
	}
}
