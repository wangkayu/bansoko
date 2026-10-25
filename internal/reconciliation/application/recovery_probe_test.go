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

// fakeOccupancy records the asked path set; empty means free.
type fakeOccupancy struct {
	asked    [][]string
	occupied map[string]bool
}

func (f *fakeOccupancy) RecoveryTargetsOccupied(_ context.Context, paths []string, _ string) (bool, error) {
	f.asked = append(f.asked, paths)
	for _, path := range paths {
		if f.occupied[path] {
			return true, nil
		}
	}
	return false, nil
}

// probeService builds the minimal service for the DB-free probe tests: the
// three recovery ports are the whole read surface.
func probeService(renderer RecoveryTargetRenderer, observer FileObserver, occupancy RecoveryOccupancyChecker) *Service {
	return &Service{recoveryTargets: renderer, recoveryObserver: observer, recoveryOccupancy: occupancy}
}

// probeScene: one all-locators-missing placement with a subtitle companion,
// whose rendered video target exists at the exact rendered path with the
// placement's frozen expected size, and a live row version to freeze.
func probeScene() ([]ownedPlacement, *fakeRenderer, *fakeObserver, *fakeOccupancy) {
	placements := []ownedPlacement{{
		ID:                "plc-1",
		PhysicalPath:      "/library/Show/raw ep1.mkv",
		Version:           4,
		OperationSize:     4096,
		SourceFingerprint: "sha:source",
		Companions:        []ownedCompanion{{ID: "c-1", Path: "/library/Show/raw ep1.zh.ass", MediaKind: "subtitle", SizeBytes: 64, Version: 2}},
	}}
	renderer := &fakeRenderer{
		targets:    map[string]string{"plc-1": "/library/Show/Season 01/EP 01.mkv"},
		companions: map[string]string{"c-1": "/library/Show/Season 01/EP 01.zh.ass"},
	}
	observer := &fakeObserver{files: map[string]domain.FileObservation{
		"/library/Show/Season 01/EP 01.mkv":    {Fingerprint: "meta:size=4096", SizeBytes: 4096},
		"/library/Show/Season 01/EP 01.zh.ass": {Fingerprint: "meta:size=64", SizeBytes: 64},
	}}
	return placements, renderer, observer, &fakeOccupancy{occupied: map[string]bool{}}
}

// The probe stats only the exact rendered video and companion paths (no
// directory enumeration: the asked list is the whole stat surface) and freezes
// one candidate beside the unchanged missing disposition semantics: the row
// version, the frozen old path, the frozen expected size, the complete
// companion unit with its own versions, and the occupancy verdict.
func TestProbeRecoveryCandidatesStatsOnlyRenderedTargets(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
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
	if candidate.PlacementVersion != 4 {
		t.Fatalf("placement_version = %d, want the frozen row version 4", candidate.PlacementVersion)
	}
	if candidate.OldPhysicalPath != "/library/Show/raw ep1.mkv" {
		t.Fatalf("old_physical_path = %q, want the scan-time row path", candidate.OldPhysicalPath)
	}
	if len(candidate.Companions) != 1 {
		t.Fatalf("companions = %+v", candidate.Companions)
	}
	companion := candidate.Companions[0]
	if companion.ID != "c-1" || companion.Fingerprint != "meta:size=64" || companion.SizeBytes != 64 {
		t.Fatalf("companion fact = %+v", companion)
	}
	if companion.Version != 2 {
		t.Fatalf("companion version = %d, want the frozen row version 2", companion.Version)
	}
	if companion.TargetPath != "/library/Show/Season 01/EP 01.zh.ass" {
		t.Fatalf("companion target = %q", companion.TargetPath)
	}
	if len(observer.asked) != 2 {
		t.Fatalf("stat surface = %v, want exactly the rendered video + companion paths", observer.asked)
	}
	if len(occupancy.asked) != 1 {
		t.Fatalf("occupancy surface = %v, want one check over both unit paths", occupancy.asked)
	}
}

// A companion not found at its rendered path disqualifies the whole unit: the
// candidate is atomic, so an incomplete unit freezes nothing at all.
func TestProbeRecoveryCandidatesIncompleteCompanionsWriteNothing(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	delete(observer.files, "/library/Show/Season 01/EP 01.zh.ass")
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none for an incomplete unit", candidates)
	}
}

// A missing rendered video target freezes nothing: no observation at the exact
// path means no candidate (the disposition stays plain missing).
func TestProbeRecoveryCandidatesMissingTargetWritesNothing(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	delete(observer.files, "/library/Show/Season 01/EP 01.mkv")
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
}

// A size mismatch against the placement's frozen expected size (the
// calibration identity priority) disqualifies the file: it is not the
// placement's own content re-rendered under the new naming.
func TestProbeRecoveryCandidatesRejectsSizeMismatch(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	observer.files["/library/Show/Season 01/EP 01.mkv"] = domain.FileObservation{
		Fingerprint: "meta:size=999", SizeBytes: 999,
	}
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none on size mismatch", candidates)
	}
}

// A target that equals any of the placement's own locators is not a recovery
// (it is the file sitting where the row already points): no candidate.
func TestProbeRecoveryCandidatesRejectsLocatorTarget(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	renderer.targets["plc-1"] = placements[0].PhysicalPath
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none when the target is a locator", candidates)
	}
}

// An occupied unit (another active placement/companion or unfinished owner
// holds the target or any companion path) disqualifies the candidate: no
// second owner, ever.
func TestProbeRecoveryCandidatesRejectsOccupiedUnit(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	occupancy.occupied["/library/Show/Season 01/EP 01.mkv"] = true
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none when the unit is occupied", candidates)
	}
}

// A renderer failure degrades the whole probe this run (the next scan
// retries): no render result means no stats and no candidates, and the scan
// itself still lands its missing dispositions.
func TestProbeRecoveryCandidatesDegradeOnRenderFailure(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	renderer.err = errors.New("naming template broken")
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none on render failure", candidates)
	}
	if len(observer.asked) != 0 {
		t.Fatalf("stats after render failure = %v, want none", observer.asked)
	}
}

// An occupancy read failure degrades too: the ownership surface could not be
// read, so nothing may be frozen.
func TestProbeRecoveryCandidatesDegradeOnOccupancyFailure(t *testing.T) {
	placements, renderer, observer, _ := probeScene()
	candidates := probeService(renderer, observer, nil).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none when occupancy is unwired", candidates)
	}
}

// Unwired ports keep the probe inert; a cancelled context stops before any
// further work.
func TestProbeRecoveryCandidatesInertAndCancellable(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	if got := probeService(nil, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements); got != nil {
		t.Fatalf("candidates without renderer = %+v, want none", got)
	}
	if got := probeService(renderer, nil, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements); got != nil {
		t.Fatalf("candidates without observer = %+v, want none", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := probeService(renderer, observer, occupancy).probeRecoveryCandidates(ctx, "a1", 7, placements); got != nil {
		t.Fatalf("candidates on cancelled context = %+v, want none", got)
	}
}

// A companion already sitting at its rendered target is NOT a reason to
// refuse: the user moved the video into place and the companion was already
// correct, which is a legitimate complete unit. Presence at the target is
// decided by the no-follow stat, not by comparing the companion's recorded
// path.
func TestProbeRecoveryCandidatesAcceptsCompanionAlreadyAtTarget(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	placements[0].Companions[0].Path = "/library/Show/Season 01/EP 01.zh.ass"
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v, want 1 when the companion already sits at its target", candidates)
	}
	if len(candidates[0].Companions) != 1 || candidates[0].Companions[0].ID != "c-1" {
		t.Fatalf("companions = %+v, want the complete unit", candidates[0].Companions)
	}
	if candidates[0].Companions[0].TargetPath != "/library/Show/Season 01/EP 01.zh.ass" {
		t.Fatalf("companion target = %q", candidates[0].Companions[0].TargetPath)
	}
}

// A placement with nothing recording a size (no metadata fingerprint, no typed
// envelope, no operation size) has no known expected size: the observed file
// cannot be proven to be the placement's own content, so nothing is frozen.
func TestProbeRecoveryCandidatesRejectsUnknownFrozenSize(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	placements[0].OperationSize = -1
	placements[0].SourceFingerprint = "sha:source"
	placements[0].TargetFingerprint = ""
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none when no expected size was recorded", candidates)
	}
	if len(observer.asked) != 0 {
		t.Fatalf("stats = %v, want none without a frozen size", observer.asked)
	}
}

// One companion whose observed size disagrees with its frozen expected size
// disqualifies the WHOLE unit: the unit is atomic, never partially adopted.
func TestProbeRecoveryCandidatesRejectsCompanionSizeMismatch(t *testing.T) {
	placements, renderer, observer, occupancy := probeScene()
	observer.files["/library/Show/Season 01/EP 01.zh.ass"] = domain.FileObservation{
		Fingerprint: "meta:size=65", SizeBytes: 65,
	}
	candidates := probeService(renderer, observer, occupancy).probeRecoveryCandidates(context.Background(), "a1", 7, placements)
	if candidates != nil {
		t.Fatalf("candidates = %+v, want none on companion size mismatch", candidates)
	}
}
