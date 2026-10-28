package httpapi

import (
	"errors"
	"net/http"
	"testing"
	"time"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapAnimeFilesRecoverErrorMapsPlacementHandoff(t *testing.T) {
	mapped := mapAnimeFilesRecoverError(&filemaintenance.AnimeFilesRecoverRefusal{
		Code: filemaintenance.RecoveryReasonPlacementHandoff,
	})
	var apiErr *APIError
	if !errors.As(mapped, &apiErr) {
		t.Fatalf("mapped refusal has type %T, want *APIError", mapped)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != "ANIME_FILES_RECOVER_REFUSED" ||
		len(apiErr.Details) != 1 || apiErr.Details[0].Code != filemaintenance.RecoveryReasonPlacementHandoff ||
		apiErr.Details[0].Message == "" {
		t.Fatalf("mapped placement handoff refusal = %+v", apiErr)
	}
}

func TestMapPlacementViewsPreservesShapeAndFields(t *testing.T) {
	createdAt := time.Unix(123, 0).UTC()
	values := []filemaintenance.PlacementView{{
		CreatedAt:          createdAt,
		FactsVersion:       "facts-v1",
		ID:                 "placement-1",
		ManagedAnimeID:     "anime-1",
		PhysicalPath:       "/library/show/episode.mkv",
		SourceEvidenceType: "file_operation",
		SourceOwnerRef:     "operation-1",
		State:              "active",
		TargetFingerprint:  "sha256:target",
		VerificationState:  "verified",
		Version:            4,
	}}

	mapped := mapPlacementViews(values)
	if len(mapped) != 1 {
		t.Fatalf("mapped %d placements, want 1", len(mapped))
	}
	got := mapped[0]
	if got.Id != "placement-1" || got.ManagedAnimeId != "anime-1" ||
		got.SourceEvidenceType != generated.PlacementViewSourceEvidenceTypeFileOperation ||
		got.State != generated.PlacementViewStateActive ||
		got.VerificationState != generated.PlacementViewVerificationStateVerified ||
		got.Version != 4 || !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("mapped placement = %+v", got)
	}

	got.Id = "changed"
	if values[0].ID != "placement-1" {
		t.Fatal("mapped placement aliases application value")
	}
}

func TestMapPlacementViewsPreservesNilAndEmptyShape(t *testing.T) {
	if mapped := mapPlacementViews(nil); mapped != nil {
		t.Fatal("nil placement list became non-nil")
	}
	if mapped := mapPlacementViews([]filemaintenance.PlacementView{}); mapped == nil || len(mapped) != 0 {
		t.Fatalf("empty placement list = %#v", mapped)
	}
}

func TestMapReplacementDecisionViewsPreservesNestedFieldsAndShape(t *testing.T) {
	candidatePlacement := "placement-1"
	inputDigest := "digest-1"
	snapshotGeneration := 4
	subscriptionVersion := 7
	staleReason := "subscription_changed"
	poolReason := "same_group"
	losers := []filemaintenance.ReplacementPlacementRef{{Fingerprint: "sha256:loser", PlacementID: "loser-1", Version: 2}}
	pools := []filemaintenance.ReplacementPoolEvidence{{
		Losers:  &losers,
		PoolKey: "group-a",
		Reason:  &poolReason,
		Verdict: "replace",
		Winner:  &filemaintenance.ReplacementPlacementRef{Fingerprint: "sha256:winner", PlacementID: "winner-1", Version: 3},
	}}
	values := []filemaintenance.ReplacementDecisionView{{
		AdmissionMode:        "single_copy",
		CandidatePlacementID: &candidatePlacement,
		CandidateReleaseID:   "release-1",
		ID:                   "decision-1",
		InputDigest:          &inputDigest,
		ManagedAnimeID:       "anime-1",
		PolicyVersion:        8,
		Pools:                &pools,
		ProfileVersion:       "profile-1",
		SnapshotGeneration:   &snapshotGeneration,
		StaleReason:          &staleReason,
		State:                "admitted",
		SubscriptionVersion:  &subscriptionVersion,
		Verdict:              "replace",
		Version:              9,
	}}
	mapped := mapReplacementDecisionViews(values)
	if len(mapped) != 1 {
		t.Fatalf("mapped %d decisions, want 1", len(mapped))
	}
	got := mapped[0]
	if got.AdmissionMode != generated.ReplacementDecisionViewAdmissionModeSingleCopy ||
		got.CandidatePlacementId == nil || *got.CandidatePlacementId != candidatePlacement ||
		got.CandidateReleaseId != "release-1" || got.Id != "decision-1" ||
		got.ManagedAnimeId != "anime-1" || got.PolicyVersion != 8 || got.ProfileVersion != "profile-1" ||
		got.SnapshotGeneration == nil || *got.SnapshotGeneration != snapshotGeneration ||
		got.State != generated.ReplacementDecisionViewStateAdmitted ||
		got.SubscriptionVersion == nil || *got.SubscriptionVersion != subscriptionVersion ||
		got.Verdict != generated.ReplacementDecisionViewVerdictReplace || got.Version != 9 {
		t.Fatalf("mapped decision = %+v", got)
	}
	if got.InputDigest == nil || *got.InputDigest != inputDigest || got.StaleReason == nil || *got.StaleReason != staleReason {
		t.Fatalf("mapped decision pointers = %#v %#v", got.InputDigest, got.StaleReason)
	}
	if got.Pools == nil || len(*got.Pools) != 1 || (*got.Pools)[0].Winner == nil || (*got.Pools)[0].Losers == nil ||
		(*got.Pools)[0].PoolKey != "group-a" || (*got.Pools)[0].Reason == nil || *(*got.Pools)[0].Reason != poolReason ||
		(*got.Pools)[0].Verdict != generated.ReplacementPoolEvidenceVerdictReplace ||
		(*got.Pools)[0].Winner.PlacementId != "winner-1" || len(*(*got.Pools)[0].Losers) != 1 ||
		(*(*got.Pools)[0].Losers)[0].PlacementId != "loser-1" {
		t.Fatalf("mapped pools = %+v", *got.Pools)
	}

	*got.CandidatePlacementId = "changed"
	*got.SnapshotGeneration = 99
	*got.Pools = (*got.Pools)[:0]
	if candidatePlacement != "placement-1" || snapshotGeneration != 4 || len(pools) != 1 {
		t.Fatal("mapped decision aliases application values")
	}
}

func TestMapReplacementDecisionViewsPreservesNilAndEmptyShape(t *testing.T) {
	if mapped := mapReplacementDecisionViews(nil); mapped != nil {
		t.Fatal("nil decision list became non-nil")
	}
	if mapped := mapReplacementDecisionViews([]filemaintenance.ReplacementDecisionView{}); mapped == nil || len(mapped) != 0 {
		t.Fatalf("empty decision list = %#v", mapped)
	}
	pools := []filemaintenance.ReplacementPoolEvidence{}
	mapped := mapReplacementDecisionView(filemaintenance.ReplacementDecisionView{Pools: &pools})
	if mapped.Pools == nil || len(*mapped.Pools) != 0 {
		t.Fatalf("empty pools shape = %#v", mapped.Pools)
	}
}

func TestMapPlacementCoverageViewsPreservesNilAndEmptyShape(t *testing.T) {
	if mapped := mapPlacementCoverageViews(nil); mapped != nil {
		t.Fatal("nil coverage list became non-nil")
	}
	if mapped := mapPlacementCoverageViews([]filemaintenance.PlacementCoverageView{}); mapped == nil || len(mapped) != 0 {
		t.Fatalf("empty coverage list = %#v", mapped)
	}

	mapped := mapPlacementCoverageViews([]filemaintenance.PlacementCoverageView{{
		CoverageOrdinal: 2,
		EvidenceRef:     "observation-2",
		FactsVersion:    "facts-v1",
		ObservationKey:  "tv:e2",
	}})
	if mapped[0].CoverageOrdinal != 2 || mapped[0].EvidenceRef != "observation-2" ||
		mapped[0].FactsVersion != "facts-v1" || mapped[0].ObservationKey != "tv:e2" {
		t.Fatalf("mapped coverage = %+v", mapped[0])
	}
}
