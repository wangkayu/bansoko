package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/library/domain"
)

// fakeRenderer renders every requested placement to its stat-able target (or a
// fixed error when the scene says the naming rules cannot render). Like the
// real NamingTargetRenderer it also emits each companion of a rendered
// placement under the companion's own id.
type fakeRenderer struct {
	targets    map[string]string
	companions map[string]string // companion id -> rendered path, keyed by owner placement id
	err        error
	calls      int
}

func (f *fakeRenderer) RenderPlacementTargets(_ context.Context, _ string, placementIDs []string) (map[string]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]string, len(placementIDs))
	for _, id := range placementIDs {
		if path, ok := f.targets[id]; ok {
			out[id] = path
		}
	}
	for companionID, path := range f.companions {
		if _, ok := out[companionOwner(companionID)]; ok {
			out[companionID] = path
		}
	}
	return out, nil
}

// companionOwner maps the fixture's companion ids to their placement; the
// fixtures use a single placement so every companion belongs to plc-1.
func companionOwner(string) string { return "plc-1" }

// fakeObserver stats from a fixed table; paths not listed fail like a missing
// file. The interface itself carries no content read, so recording the asked
// paths is the whole observable surface.
type fakeObserver struct {
	files map[string]domain.FileObservation
	asked []string
}

func (f *fakeObserver) ObserveFile(_ context.Context, path string) (domain.FileObservation, error) {
	f.asked = append(f.asked, path)
	if observation, ok := f.files[path]; ok {
		return observation, nil
	}
	return domain.FileObservation{}, errors.New("not found")
}

// probeService builds the minimal service for the DB-free probe tests: only
// the two recovery ports are read.
func probeService(renderer RecoveryTargetRenderer, observer FileObserver) *Service {
	return &Service{recoveryTargets: renderer, recoveryObserver: observer}
}

// probeScene: one all-locators-missing placement with a subtitle companion,
// whose rendered video target exists at the exact rendered path.
func probeScene() ([]ownedPlacement, *fakeRenderer, *fakeObserver) {
	placements := []ownedPlacement{{
		ID:           "plc-1",
		PhysicalPath: "/library/Show/raw ep1.mkv",
		Companions:   []ownedCompanion{{ID: "c-1", Path: "/library/Show/raw ep1.zh.ass", MediaKind: "subtitle"}},
	}}
	renderer := &fakeRenderer{
		targets:    map[string]string{"plc-1": "/library/Show/Season 01/EP 01.mkv"},
		companions: map[string]string{"c-1": "/library/Show/Season 01/EP 01.zh.ass"},
	}
	observer := &fakeObserver{files: map[string]domain.FileObservation{
		"/library/Show/Season 01/EP 01.mkv":    {Fingerprint: "meta:size=4096", SizeBytes: 4096},
		"/library/Show/Season 01/EP 01.zh.ass": {Fingerprint: "meta:size=64", SizeBytes: 64},
	}}
	return placements, renderer, observer
}

// The probe stats only the exact rendered video and companion paths (no
// directory enumeration: the asked list is the whole stat surface) and freezes
// one candidate beside the unchanged missing disposition semantics.
func TestProbeRecoveryCandidatesStatsOnlyRenderedTargets(t *testing.T) {
	placements, renderer, observer := probeScene()
	candidates := probeService(renderer, observer).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.PlacementID != "plc-1" || candidate.TargetPath != "/library/Show/Season 01/EP 01.mkv" {
		t.Fatalf("candidate = %+v", candidate)
	}
	if candidate.Generation != 7 || candidate.SizeBytes != 4096 || candidate.TargetFingerprint != "meta:size=4096" {
		t.Fatalf("candidate facts = %+v", candidate)
	}
	if len(candidate.Companions) != 1 {
		t.Fatalf("companions = %+v", candidate.Companions)
	}
	companion := candidate.Companions[0]
	if companion.ID != "c-1" || companion.State != "present" || companion.Fingerprint == nil || companion.SizeBytes != 64 {
		t.Fatalf("companion fact = %+v", companion)
	}
	if !candidate.CompanionsComplete {
		t.Fatal("companions_complete = false, want true")
	}
	if len(observer.asked) != 2 {
		t.Fatalf("stat surface = %v, want exactly the rendered video + companion paths", observer.asked)
	}
}

// A companion not found at its rendered path freezes a null observation and
// marks the candidate incomplete: the confirm still recovers the video but the
// gap stays visible.
func TestProbeRecoveryCandidatesIncompleteCompanions(t *testing.T) {
	placements, renderer, observer := probeScene()
	delete(observer.files, "/library/Show/Season 01/EP 01.zh.ass")
	candidates := probeService(renderer, observer).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if len(candidates) != 1 || candidates[0].CompanionsComplete {
		t.Fatalf("candidates = %+v, want one incomplete candidate", candidates)
	}
	companion := candidates[0].Companions[0]
	if companion.State != "missing" || companion.Fingerprint != nil {
		t.Fatalf("companion fact = %+v, want a missing null-observation", companion)
	}
}

// A missing rendered video target freezes nothing: no observation at the exact
// path means no candidate (the disposition stays plain missing).
func TestProbeRecoveryCandidatesMissingTargetWritesNothing(t *testing.T) {
	placements, renderer, observer := probeScene()
	delete(observer.files, "/library/Show/Season 01/EP 01.mkv")
	candidates := probeService(renderer, observer).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
}

// A renderer failure degrades the whole probe this run (the next scan
// retries): no render result means no stats and no candidates, and the scan
// itself still lands its missing dispositions.
func TestProbeRecoveryCandidatesDegradeOnRenderFailure(t *testing.T) {
	placements, renderer, observer := probeScene()
	renderer.err = errors.New("naming template broken")
	candidates := probeService(renderer, observer).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none on render failure", candidates)
	}
	if len(observer.asked) != 0 {
		t.Fatalf("stats after render failure = %v, want none", observer.asked)
	}
}

// Unwired ports keep the probe inert; a cancelled context stops before any
// further work.
func TestProbeRecoveryCandidatesInertAndCancellable(t *testing.T) {
	placements, renderer, observer := probeScene()
	if got := probeService(nil, observer).probeRecoveryCandidates(context.Background(), "a1", 7, placements); got != nil {
		t.Fatalf("candidates without renderer = %+v, want none", got)
	}
	if got := probeService(renderer, nil).probeRecoveryCandidates(context.Background(), "a1", 7, placements); got != nil {
		t.Fatalf("candidates without observer = %+v, want none", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := probeService(renderer, observer).probeRecoveryCandidates(ctx, "a1", 7, placements); got != nil {
		t.Fatalf("candidates on cancelled context = %+v, want none", got)
	}
}
