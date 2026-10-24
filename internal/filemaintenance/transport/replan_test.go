package transport

import (
	"testing"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func TestReplanInputMapsIdentityAndGeneration(t *testing.T) {
	input := ReplanInput(generated.FileMaintenanceReplanInput{
		ManagedAnimeId: generated.ID("anime-1"),
		Generation:     7,
	})
	if input.ManagedAnimeID != "anime-1" || input.Generation != 7 {
		t.Fatalf("mapped input = %+v", input)
	}
}

func TestReplanResultMapsCandidatesAndIsolatesSlices(t *testing.T) {
	target := "/library/Show/episode.mkv"
	reason := "coverage unresolved"
	code := "coverage_unresolved"
	coverage := []string{"tv:s1:e1"}
	result := ReplanResult(filemaintenance.PlanResult{
		AnimeID:          "anime-1",
		Generation:       3,
		PolicyVersion:    4,
		InputFingerprint: "digest",
		Candidates: []filemaintenance.PlanCandidate{{
			ObservationRef:    "anime-1:3:file.mkv",
			SourcePath:        "file.mkv",
			Fingerprint:       "fp",
			SizeBytes:         123,
			Coverage:          coverage,
			TargetPath:        target,
			Excluded:          true,
			ExcludeReason:     reason,
			ExcludeReasonCode: code,
		}},
	})
	if result.AnimeId != "anime-1" || result.Generation != 3 || result.PolicyVersion != 4 || result.InputFingerprint != "digest" {
		t.Fatalf("mapped result = %+v", result)
	}
	candidate := result.Candidates[0]
	if candidate.TargetPath == nil || *candidate.TargetPath != target || candidate.Excluded == nil || !*candidate.Excluded || candidate.ExcludeReason == nil || *candidate.ExcludeReasonCode != code {
		t.Fatalf("mapped candidate = %+v", candidate)
	}
	coverage[0] = "changed"
	if candidate.Coverage[0] != "tv:s1:e1" {
		t.Fatal("mapped coverage aliases application slice")
	}
	*candidate.TargetPath = "changed"
	if target != "/library/Show/episode.mkv" {
		t.Fatal("mapped target aliases application value")
	}
}

func TestReplanResultPreservesNilAndEmptyCandidateShapes(t *testing.T) {
	emptyCoverage := []string{}
	result := ReplanResult(filemaintenance.PlanResult{Candidates: []filemaintenance.PlanCandidate{
		{Coverage: nil},
		{Coverage: emptyCoverage},
	}})
	if result.Candidates[0].Coverage != nil {
		t.Fatal("nil coverage must remain nil")
	}
	if result.Candidates[1].Coverage == nil || len(result.Candidates[1].Coverage) != 0 {
		t.Fatal("empty coverage must remain non-nil")
	}
}
