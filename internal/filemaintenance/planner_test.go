package filemaintenance

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/postprocessing/naming"
)

type fakeSnapshotReader struct {
	usable bool
	obs    []SnapshotObservation
}

func (f *fakeSnapshotReader) GenerationUsable(context.Context, string, int64) (bool, error) {
	return f.usable, nil
}

func (f *fakeSnapshotReader) ListObservations(context.Context, string, int64) ([]SnapshotObservation, error) {
	return f.obs, nil
}

type fakeRenderer struct{}

func (fakeRenderer) RenderTarget(_ context.Context, observation SnapshotObservation) (string, error) {
	if observation.FilePath == "" {
		return "", errors.New("empty path")
	}
	return "/library/Show/Season 01/" + observation.FilePath, nil
}

func TestPlanRequiresCompletedImmutableGeneration(t *testing.T) {
	planner := NewPlanner(&fakeSnapshotReader{usable: false}, fakeRenderer{})
	if _, err := planner.Plan(context.Background(), "anime-1", 1, 1); err == nil {
		t.Fatal("partial/failed generation must block replan")
	}
}

func TestPlanKeepsAllCandidatesAndExclusions(t *testing.T) {
	reader := &fakeSnapshotReader{
		usable: true,
		obs: []SnapshotObservation{
			{FilePath: "EP01.mkv", Fingerprint: "fp-1", SizeBytes: 100, Coverage: []string{"tv:e1"}},
			{FilePath: "SPECIAL.mkv", Fingerprint: "fp-2", SizeBytes: 50, Coverage: []string{"unresolved"}},
		},
	}
	planner := NewPlanner(reader, fakeRenderer{})
	result, err := planner.Plan(context.Background(), "anime-1", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("plan must keep ALL candidates (no winner pre-filter), got %d", len(result.Candidates))
	}
	byPath := map[string]PlanCandidate{}
	for _, candidate := range result.Candidates {
		byPath[candidate.SourcePath] = candidate
	}
	resolved := byPath["EP01.mkv"]
	if resolved.Excluded || resolved.TargetPath == "" {
		t.Fatalf("resolved candidate must render a target: %+v", resolved)
	}
	unresolved := byPath["SPECIAL.mkv"]
	if !unresolved.Excluded || unresolved.ExcludeReasonCode != "coverage_unresolved" {
		t.Fatalf("unresolved coverage must be excluded with reason: %+v", unresolved)
	}
}

type missingFactsRenderer struct{}

func (missingFactsRenderer) RenderTarget(context.Context, SnapshotObservation) (string, error) {
	return "", naming.ErrRenderFactsUnavailable
}

func TestPlanExcludesMissingRenderFactsWithOperationalCode(t *testing.T) {
	reader := &fakeSnapshotReader{usable: true, obs: []SnapshotObservation{{FilePath: "EP01.mkv", Fingerprint: "fp", Coverage: []string{"tv:e1"}}}}
	result, err := NewPlanner(reader, missingFactsRenderer{}).Plan(context.Background(), "anime-1", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || !result.Candidates[0].Excluded || result.Candidates[0].ExcludeReasonCode != "render_facts_unavailable" || result.Candidates[0].TargetPath != "" {
		t.Fatalf("missing durable render facts must exclude without admitting a target: %+v", result.Candidates)
	}
}

func TestPlanDeterministicFingerprint(t *testing.T) {
	reader := &fakeSnapshotReader{
		usable: true,
		obs: []SnapshotObservation{
			{FilePath: "EP01.mkv", Fingerprint: "fp-1", SizeBytes: 100, Coverage: []string{"tv:e1"}},
		},
	}
	planner := NewPlanner(reader, fakeRenderer{})
	first, err := planner.Plan(context.Background(), "anime-1", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), "anime-1", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.InputFingerprint != second.InputFingerprint {
		t.Fatal("plan fingerprint must be deterministic")
	}
	if first.Candidates[0].TargetPath != second.Candidates[0].TargetPath {
		t.Fatal("plan output must be deterministic")
	}
}
