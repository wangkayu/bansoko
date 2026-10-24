package transport

import (
	"testing"
	"time"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func TestReplacementEvaluateInputMapsIdentities(t *testing.T) {
	input := ReplacementEvaluateInput(generated.ReplacementEvaluateInput{
		ManagedAnimeId:     generated.ID("anime-1"),
		CandidateReleaseId: generated.ID("release-1"),
	})
	if input.AnimeID != "anime-1" || input.CandidateRelease != "release-1" {
		t.Fatalf("mapped input = %+v", input)
	}
}

func TestReplacementDecisionViewMapsNestedEvidenceAndIsolatesValues(t *testing.T) {
	createdAt := time.Unix(123, 0).UTC()
	candidatePlacement := "candidate-1"
	inputDigest := "digest"
	snapshotGeneration := 4
	subscriptionVersion := 7
	reason := "same group"
	losers := []filemaintenance.ReplacementPlacementRef{{Fingerprint: "loser-fp", PlacementID: "loser-1", Version: 2}}
	pools := []filemaintenance.ReplacementPoolEvidence{{
		PoolKey: "group-a",
		Reason:  &reason,
		Verdict: "replace",
		Winner:  &filemaintenance.ReplacementPlacementRef{Fingerprint: "winner-fp", PlacementID: "winner-1", Version: 3},
		Losers:  &losers,
	}}
	value := filemaintenance.ReplacementDecisionView{
		AdmissionMode:        "single_copy",
		CandidatePlacementID: &candidatePlacement,
		CandidateReleaseID:   "release-1",
		CreatedAt:            createdAt,
		ID:                   "decision-1",
		InputDigest:          &inputDigest,
		ManagedAnimeID:       "anime-1",
		PolicyVersion:        8,
		Pools:                &pools,
		ProfileVersion:       "profile-1",
		SnapshotGeneration:   &snapshotGeneration,
		StaleReason:          &reason,
		State:                "admitted",
		SubscriptionVersion:  &subscriptionVersion,
		Verdict:              "replace",
		Version:              9,
	}
	mapped := ReplacementDecisionView(value)
	if mapped.AdmissionMode != generated.ReplacementDecisionViewAdmissionModeSingleCopy ||
		mapped.CandidatePlacementId == nil || *mapped.CandidatePlacementId != candidatePlacement ||
		mapped.CandidateReleaseId != "release-1" || mapped.CreatedAt != createdAt || mapped.Id != "decision-1" ||
		mapped.InputDigest == nil || *mapped.InputDigest != inputDigest || mapped.ManagedAnimeId != "anime-1" ||
		mapped.PolicyVersion != 8 || mapped.ProfileVersion != "profile-1" ||
		mapped.SnapshotGeneration == nil || *mapped.SnapshotGeneration != snapshotGeneration ||
		mapped.State != generated.ReplacementDecisionViewStateAdmitted ||
		mapped.SubscriptionVersion == nil || *mapped.SubscriptionVersion != subscriptionVersion ||
		mapped.Verdict != generated.ReplacementDecisionViewVerdictReplace || mapped.Version != 9 {
		t.Fatalf("mapped decision = %+v", mapped)
	}
	if mapped.Pools == nil || len(*mapped.Pools) != 1 || (*mapped.Pools)[0].Winner == nil || (*mapped.Pools)[0].Losers == nil ||
		(*mapped.Pools)[0].Reason == nil || *(*mapped.Pools)[0].Reason != reason ||
		(*mapped.Pools)[0].Winner.PlacementId != "winner-1" || (*mapped.Pools)[0].Winner.Version != 3 ||
		len(*(*mapped.Pools)[0].Losers) != 1 || (*(*mapped.Pools)[0].Losers)[0].PlacementId != "loser-1" {
		t.Fatalf("mapped pools = %+v", *mapped.Pools)
	}
	*mapped.CandidatePlacementId = "changed"
	*mapped.Pools = (*mapped.Pools)[:0]
	if candidatePlacement != "candidate-1" || len(pools) != 1 {
		t.Fatal("mapped decision aliases application values")
	}
}

func TestReplacementDecisionViewPreservesNilAndEmptyShapes(t *testing.T) {
	mapped := ReplacementDecisionView(filemaintenance.ReplacementDecisionView{})
	if mapped.Pools != nil {
		t.Fatal("nil pools became non-nil")
	}
	pools := []filemaintenance.ReplacementPoolEvidence{}
	mapped = ReplacementDecisionView(filemaintenance.ReplacementDecisionView{Pools: &pools})
	if mapped.Pools == nil || len(*mapped.Pools) != 0 {
		t.Fatalf("empty pools shape = %#v", mapped.Pools)
	}
}
