package filemaintenance

import (
	"testing"

	"bansoko/internal/library/domain"
	"bansoko/internal/qualityprofile"
)

func TestRouteAlignmentSameKeyByModeAndPolicy(t *testing.T) {
	policyRoute := map[domain.CollisionPolicy]routeAction{
		domain.CollisionBlock:     routeSkip,
		domain.CollisionVersion:   routeVersion,
		domain.CollisionOverwrite: routeOverwrite,
	}
	type modeCase struct {
		name       string
		mode       AdmissionMode
		sameGroup  bool
		comparable bool
	}
	modes := []modeCase{
		{"all", ModeAll, true, false},
		{"per_group same group", ModePerGroup, true, true},
		{"per_group other group", ModePerGroup, false, false},
		{"single_copy", ModeSingleCopy, false, true},
	}
	for _, mode := range modes {
		for policy, kept := range policyRoute {
			for _, retention := range []retentionOutcome{retentionKeptBoth, retentionOccupantLost, retentionMoverLost, retentionUnsettled} {
				occupancy := targetOccupancy{Occupied: true, SameInstance: true, SharesKey: true, SameGroup: mode.sameGroup, Retention: retention}
				got := routeAlignment(mode.mode, policy, occupancy)
				want := alignmentRoute{Action: kept}
				if kept == routeSkip {
					want.Reason = "target_occupied"
				}
				if mode.comparable {
					switch retention {
					case retentionOccupantLost:
						want = alignmentRoute{Action: routeHold, Reason: ReasonRetentionPending}
					case retentionMoverLost:
						want = alignmentRoute{Action: routeDrop, Reason: "placement_not_active"}
					case retentionUnsettled:
						want = alignmentRoute{Action: routeHold, Reason: ReasonRetentionPending, Settle: true}
					}
				}
				if got != want {
					t.Fatalf("%s %s retention=%s: route=%+v want %+v", mode.name, policy, retention, got, want)
				}
			}
		}
	}
}

func TestRouteAlignmentOtherOccupants(t *testing.T) {
	for _, policy := range []domain.CollisionPolicy{domain.CollisionBlock, domain.CollisionVersion, domain.CollisionOverwrite} {
		byPolicy := map[domain.CollisionPolicy]routeAction{domain.CollisionBlock: routeSkip, domain.CollisionVersion: routeVersion, domain.CollisionOverwrite: routeOverwrite}[policy]
		for _, mode := range []AdmissionMode{ModeAll, ModePerGroup, ModeSingleCopy} {
			cases := []struct {
				name      string
				occupancy targetOccupancy
				want      routeAction
			}{
				{"free", targetOccupancy{}, routeProceed},
				{"cross-instance", targetOccupancy{Occupied: true}, byPolicy},
				{"unmanaged", targetOccupancy{Occupied: true}, byPolicy},
				{"same instance other key", targetOccupancy{Occupied: true, SameInstance: true}, byPolicy},
				{"loser deletion in flight", targetOccupancy{Occupied: true, SameInstance: true, LoserPendingDeletion: true}, routeHold},
				{"occupant moving away", targetOccupancy{Occupied: true, SameInstance: true, MovingAway: true}, routeWait},
				{"publication in flight", targetOccupancy{Occupied: true, SameInstance: true, InFlight: true}, map[bool]routeAction{true: routeProceed, false: routeHold}[mode == ModeAll]},
			}
			for _, tc := range cases {
				if got := routeAlignment(mode, policy, tc.occupancy); got.Action != tc.want {
					t.Fatalf("%s %s %s: action=%s want %s", mode, policy, tc.name, got.Action, tc.want)
				}
			}
		}
	}
}

func TestRetentionComparable(t *testing.T) {
	for _, tc := range []struct {
		mode      AdmissionMode
		sameGroup bool
		want      bool
	}{
		{ModeAll, true, false},
		{ModePerGroup, true, true},
		{ModePerGroup, false, false},
		{ModeSingleCopy, false, true},
	} {
		if got := retentionComparable(tc.mode, tc.sameGroup); got != tc.want {
			t.Fatalf("%s sameGroup=%v: %v want %v", tc.mode, tc.sameGroup, got, tc.want)
		}
	}
}

func TestRetentionContendersFindTheStrictWinner(t *testing.T) {
	profile := Profile{Version: qualityprofile.Version, Items: []qualityprofile.Item{{Key: qualityprofile.KeySubgroupScore, Ranks: [][]string{}}}}
	high, low := int64(9), int64(1)
	better := PlacementFacts{PlacementID: "p-better", Version: 1, Coverage: []string{"tv:e1"}, FactsOK: true, Facts: Facts{Score: &high}}
	worse := PlacementFacts{PlacementID: "p-worse", Version: 1, Coverage: []string{"tv:e1"}, FactsOK: true, Facts: Facts{Score: &low}}
	other := PlacementFacts{PlacementID: "p-other", Version: 1, Coverage: []string{"tv:e2"}, FactsOK: true, Facts: Facts{Score: &low}}
	placements := []PlacementFacts{worse, better, other}
	keys := map[string]bool{"tv:e1": true, "tv:e2": true}
	if got := retentionContenders(ModeAll, profile, placements, keys); len(got) != 0 {
		t.Fatalf("all mode contenders = %+v, want none", got)
	}
	got := retentionContenders(ModeSingleCopy, profile, placements, keys)
	if len(got) != 1 || got[0].placementID != "p-better" || len(got[0].keys) != 1 || got[0].keys[0] != "tv:e1" {
		t.Fatalf("single_copy contenders = %+v, want p-better on tv:e1", got)
	}
	tie := PlacementFacts{PlacementID: "p-tie", Version: 1, Coverage: []string{"tv:e1"}, FactsOK: true, Facts: Facts{Score: &high}}
	if got := retentionContenders(ModeSingleCopy, profile, []PlacementFacts{better, tie}, keys); len(got) != 0 {
		t.Fatalf("tied contenders = %+v, want none (retention keeps both)", got)
	}
}
