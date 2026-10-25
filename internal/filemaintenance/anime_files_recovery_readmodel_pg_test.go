package filemaintenance

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/library/domain"
	naming "bansoko/internal/postprocessing/naming"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/workitems"
)

// This file drives the recovery read model over the REAL producer chain:
//
//	real scan handler (with the real recovery probe)
//	  -> reconciliation_files.facts carries the recovery_candidates ARRAY
//	  -> real calibration writes the disposition column and the coverage column
//	  -> real PathDiffService renders the path diff
//	  -> ListAnimeFilesApplication derives the offered actions
//
// The first version of these read-model assertions seeded the facts object by
// hand (seedRecoveryCandidate) and never called ListAnimeFilesApplication with a
// candidate present, which is why two defects survived a green PG suite:
//
//  1. the read selected rf.facts->'recovery_candidates' (an ARRAY) and decoded
//     it into the OBJECT-shaped struct, so the first candidate-bearing row a
//     user opened failed with "cannot unmarshal array into Go value of type
//     struct";
//  2. the liveness gate demanded calibration's ExpectedPath, but a real
//     missing row structurally has none (calibration sets coverage = [] for a
//     missing observation, so path_diff reports coverage_unresolved and a nil
//     ExpectedPath), so every real candidate was refused forever.
//
// Both are invisible to a hand-seeded fixture, so everything below is produced
// by the real producers.

// recoveryReadModelScene is one anime whose placement is missing at every
// locator while its rendered unit is already on disk: exactly the state the
// recovery probe freezes a candidate for.
type recoveryReadModelScene struct {
	db       *sql.DB
	fixture  *calibrationFixture
	animeID  string
	rules    naming.NamingRules
	chain    placedRenderChain
	renderer *NamingTargetRenderer
	// unitPath is where the CURRENT naming rules render the video, and where
	// the fake observer reports the frozen bytes.
	unitPath string
	// files is the metadata observation of the rendered unit (video only: this
	// scene carries no companion, so a companion registration cannot be the
	// reason for a refusal).
	files map[string]domain.FileObservation
}

// newRecoveryReadModelScene seeds one anime whose active placement is missing
// at every locator, then runs the REAL scan handler so the recovery probe
// freezes the candidate in the exact shape production writes.
func newRecoveryReadModelScene(t *testing.T) *recoveryReadModelScene {
	t.Helper()
	fixture := newCalibrationFixture(t)
	db := fixture.db
	const animeID = "anime-rm-0"
	rules := recoveryRules()

	// The completed file-operation chain is what lets the renderer produce a
	// target at all (a placement without lineage renders render_facts_unavailable).
	// The placement sits at its CURRENT path, which the scan will find empty.
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "rm-0", AnimeID: animeID, AnimeTitle: "Show",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle:  "[Group] Show - 1",
		PlacedPath:    "/library/Show/EP 01.mkv",
		DownloadName:  "EP 01.mkv",
		Rules:         rules,
		FrozenEpisode: "1",
		Coverage:      []string{"tv:s1:e1"},
	})
	if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state, version)
VALUES ('sub-rm-0', $1, 'custom', 'Read model', 'active', 1)
ON CONFLICT (managed_anime_id) DO NOTHING`, animeID); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	seedInstanceRetention(t, db, animeID, ModeAll, ``, 1)

	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules})
	targets, err := renderer.RenderPlacementTargets(context.Background(), animeID, []string{chain.PlacementID})
	if err != nil {
		t.Fatalf("render the unit target: %v", err)
	}
	unitPath, ok := targets[chain.PlacementID]
	if !ok {
		t.Fatalf("seeded placement did not render: %v", targets)
	}

	scene := &recoveryReadModelScene{
		db: db, fixture: fixture, animeID: animeID, rules: rules, chain: chain,
		renderer: renderer,
		unitPath: filepath.Clean(unitPath),
		files: map[string]domain.FileObservation{
			// The unit is already at its rendered target with the placement's
			// frozen size, which is what makes it adoptable rather than merely
			// co-located.
			filepath.Clean(unitPath): {Fingerprint: "meta:size=1024;dev=2;ino=7", SizeBytes: 1024},
		},
	}
	scene.runScan(t)
	return scene
}

// runScan submits a scan whose scanner reports nothing at the placement's
// locators (the file is "missing") and runs the REAL scan handler with the real
// recovery probe wired, so the candidate lands in reconciliation_files.facts.
func (s *recoveryReadModelScene) runScan(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := s.fixture.store
	service, err := reconciliationapplication.NewService(s.db, store, emptyScanner{})
	if err != nil {
		t.Fatal(err)
	}
	// The probe needs all three ports; the renderer is the SAME instance the
	// read model and the confirm use, and the observer is metadata-only.
	service.SetRecoveryProbe(s.renderer, staticMetadataObserver{files: s.files}, NewRecoveryOccupancy(s.db))
	treeTop := "/library"
	if _, err := service.SubmitScan(ctx, reconciliationapplication.ScanSubmitInput{
		ManagedAnimeID: s.animeID, TreeTop: &treeTop,
	}); err != nil {
		t.Fatalf("submit scan: %v", err)
	}
	claim, err := store.ClaimDue(ctx, "rm-worker", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatalf("claim scan: %v", err)
	}
	outcome, err := service.ScanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("run scan handler: %v", err)
	}
	if outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("scan outcome = %+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatalf("finish scan: %v", err)
	}
}

// calibrate runs the REAL calibration handler for the scanned generation, which
// is what writes the disposition column (the newest decided generation the read
// model and the confirm both gate on) and the coverage column path_diff reads.
func (s *recoveryReadModelScene) calibrate(t *testing.T) {
	t.Helper()
	s.fixture.runHandler(t, s.fixture.calibration.CalibrateHandler(), s.fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	// Calibration enqueues its two consumers in the same transaction, and the
	// read model reports a live alignment consumer as AlignmentLive — which
	// would refuse the recover action for the WRONG reason (alignment running
	// rather than the gate this file is about). Drain both so "nothing is in
	// flight" is true before the read.
	s.fixture.drainKind(t, s.fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	s.fixture.drainKind(t, s.fixture.calibration.ConsumeReplacementHandler(), workitems.KindReconciliationConsumeReplacement)
}

// service wires the files application exactly as the composition root does.
func (s *recoveryReadModelScene) service() *Service {
	service := NewService(nil, nil, nil, NewPlacementService(s.db, nil, nil), nil)
	service.SetPathDiff(NewPathDiffService(s.db, s.renderer, nil))
	return service
}

// candidateRow reads the persisted candidate facts straight out of the row the
// scan wrote, so a test can assert the REAL write shape instead of assuming it.
func (s *recoveryReadModelScene) candidateRow(t *testing.T) (isArray bool, placementID string, generation int64) {
	t.Helper()
	var kind string
	if err := s.db.QueryRow(`
SELECT jsonb_typeof(facts->'recovery_candidates'), COALESCE(facts->>'placement_id',''), generation
FROM reconciliation_files
WHERE managed_anime_id = $1 AND facts->'recovery_candidates' IS NOT NULL`, s.animeID).
		Scan(&kind, &placementID, &generation); err != nil {
		t.Fatalf("read the written candidate row: %v", err)
	}
	return kind == "array", placementID, generation
}

// emptyScanner reports no file at any requested locator: the placement is
// missing at every bounded locator, which is the precondition of a candidate.
type emptyScanner struct{}

func (emptyScanner) Scan(context.Context, domain.ScanRequest) (domain.ScanResult, error) {
	return domain.ScanResult{TreeTop: "/library", ScannerVersion: "test"}, nil
}

// staticMetadataObserver answers from a fixed table; any other path is absent.
type staticMetadataObserver struct {
	files map[string]domain.FileObservation
}

func (o staticMetadataObserver) ObserveMetadataFile(_ context.Context, path string) (domain.FileObservation, error) {
	if observation, ok := o.files[filepath.Clean(path)]; ok {
		return observation, nil
	}
	return domain.FileObservation{}, sql.ErrNoRows
}

// TestRecoveryReadModelOffersARealCandidate is the regression test for both
// defects: the read must decode the shape the scan actually writes (an ARRAY
// under the facts OBJECT) and the liveness gate must accept a real missing row,
// which never has a calibration ExpectedPath.
func TestRecoveryReadModelOffersARealCandidate(t *testing.T) {
	scene := newRecoveryReadModelScene(t)
	defer scene.db.Close()

	// The premise: the real scan wrote an ARRAY, and it names the placement.
	// (The previous read model selected exactly this array and decoded it into
	// the object-shaped struct.)
	isArray, placementID, generation := scene.candidateRow(t)
	if !isArray {
		t.Fatal("the scan must write recovery_candidates as a JSON array")
	}
	if placementID != scene.chain.PlacementID {
		t.Fatalf("candidate placement = %q, want %q", placementID, scene.chain.PlacementID)
	}

	scene.calibrate(t)
	if generation < 1 {
		t.Fatalf("scanned generation = %d", generation)
	}

	// Reading with a candidate present must not error: the old code failed here
	// with "cannot unmarshal array into Go value of type struct".
	view, err := scene.service().ListAnimeFilesApplication(context.Background(), scene.animeID)
	if err != nil {
		t.Fatalf("the files read must decode the real candidate shape: %v", err)
	}
	row, ok := readModelRow(view.Files, scene.chain.PlacementID)
	if !ok {
		t.Fatalf("no row for the placement: %+v", view.Files)
	}
	if row.Status != AnimeFileMissing {
		t.Fatalf("row status = %q, want missing", row.Status)
	}
	if row.Recovery == nil {
		t.Fatalf("the read model must surface the frozen candidate: %+v", row)
	}
	if filepath.Clean(row.Recovery.TargetPath) != scene.unitPath {
		t.Fatalf("candidate target = %q, want %q", row.Recovery.TargetPath, scene.unitPath)
	}
	if !row.Actions.Recover.Available {
		t.Fatalf("a real missing row with its unit on disk must offer recover, got %q",
			row.Actions.Recover.ReasonCode)
	}
	if row.Actions.Recover.ReasonCode != "" {
		t.Fatalf("available recover must carry no reason, got %q", row.Actions.Recover.ReasonCode)
	}
}

// TestRecoveryLiveReasonAcceptsARealMissingRow pins the gate itself: a real
// missing row has no calibration ExpectedPath (coverage is empty), so gating on
// the calibration projection refused every candidate forever. The gate compares
// the placement-frozen render instead.
func TestRecoveryLiveReasonAcceptsARealMissingRow(t *testing.T) {
	scene := newRecoveryReadModelScene(t)
	defer scene.db.Close()
	scene.calibrate(t)

	// The calibration projection really has no target for this row: this is the
	// structural fact the old gate tripped over, asserted rather than assumed.
	diffs, err := NewPathDiffService(scene.db, scene.renderer, nil).
		ListPathDiffs(context.Background(), scene.animeID, 1, 100, 0)
	if err != nil {
		t.Fatalf("path diff: %v", err)
	}
	missing, ok := diffForPlacement(diffs, scene.chain.PlacementID)
	if !ok {
		t.Fatalf("no path diff for the placement: %+v", diffs)
	}
	if missing.Disposition != DispositionMissing {
		t.Fatalf("disposition = %q, want missing", missing.Disposition)
	}
	if missing.ExpectedPath != nil && *missing.ExpectedPath != "" && missing.TargetUnavailable == "" {
		t.Fatalf("premise broken: the calibration projection unexpectedly renders a target (%q)", *missing.ExpectedPath)
	}

	view, err := scene.service().ListAnimeFilesApplication(context.Background(), scene.animeID)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := readModelRow(view.Files, scene.chain.PlacementID)
	if !ok || row.Recovery == nil {
		t.Fatalf("no candidate row: %+v", view.Files)
	}
	placement := scene.placement(t)
	if placement.Recovery == nil {
		t.Fatal("the loaded placement must carry the frozen candidate")
	}
	if reason := recoveryLiveReason(placement.Recovery, placement); reason != "" {
		t.Fatalf("a real missing row must not be refused, got %q", reason)
	}
}

// TestRecoveryReadModelRefusesChangedRender is the negative case: once the
// current naming rules render the unit elsewhere, the frozen candidate is stale
// and the action must be unavailable (a rescan is required), never a confirm
// button whose own transaction would refuse it.
func TestRecoveryReadModelRefusesChangedRender(t *testing.T) {
	scene := newRecoveryReadModelScene(t)
	defer scene.db.Close()
	scene.calibrate(t)

	// The read model renders through the rules the runner reports; a changed
	// template is exactly what a naming-rule edit produces.
	changed := scene.rules
	changed.TV.VideoFilenameTemplate = "renamed - E${pad(ep.display, 2)}.${file.ext}"
	view, err := scene.serviceWithRules(changed).ListAnimeFilesApplication(context.Background(), scene.animeID)
	if err != nil {
		t.Fatalf("the changed render must not fail the read: %v", err)
	}
	row, ok := readModelRow(view.Files, scene.chain.PlacementID)
	if !ok || row.Recovery == nil {
		t.Fatalf("no candidate row: %+v", view.Files)
	}
	if row.Actions.Recover.Available {
		t.Fatal("a unit that now renders elsewhere must not offer recover")
	}
	if row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
		t.Fatalf("reason = %q, want %q", row.Actions.Recover.ReasonCode, AnimeFilesReasonRescanRequired)
	}
}

// TestRecoveryReadModelRefusesUnrenderableUnit is the second negative case: the
// candidate is only offered while its rendered unit is actually on disk. The
// observer is not part of the read path, so this is expressed at the render
// layer: a placement whose frozen coverage is gone cannot render, which must
// fail closed rather than offer an unverifiable unit.
func TestRecoveryReadModelRefusesUnrenderableUnit(t *testing.T) {
	scene := newRecoveryReadModelScene(t)
	defer scene.db.Close()
	scene.calibrate(t)

	// Remove the frozen coverage the render needs: the placement can no longer
	// be rendered, so no frozen target can be verified.
	if _, err := scene.db.Exec(`DELETE FROM placement_coverage WHERE placement_id = $1`, scene.chain.PlacementID); err != nil {
		t.Fatal(err)
	}
	view, err := scene.service().ListAnimeFilesApplication(context.Background(), scene.animeID)
	if err != nil {
		t.Fatalf("an unrenderable unit must not fail the read: %v", err)
	}
	row, ok := readModelRow(view.Files, scene.chain.PlacementID)
	if !ok || row.Recovery == nil {
		t.Fatalf("no candidate row: %+v", view.Files)
	}
	if row.Actions.Recover.Available {
		t.Fatal("an unrenderable unit must not offer recover")
	}
	if row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
		t.Fatalf("reason = %q, want %q", row.Actions.Recover.ReasonCode, AnimeFilesReasonRescanRequired)
	}
}

func (s *recoveryReadModelScene) serviceWithRules(rules naming.NamingRules) *Service {
	service := NewService(nil, nil, nil, NewPlacementService(s.db, nil, nil), nil)
	service.SetPathDiff(NewPathDiffService(s.db, NewNamingTargetRenderer(s.db, fakeNamingRulesSource{rules: rules}), nil))
	return service
}

// placement loads the placement row the gate compares membership against.
func (s *recoveryReadModelScene) placement(t *testing.T) animeFilesPlacement {
	t.Helper()
	placements, err := loadAnimeFilesPlacements(context.Background(), s.db, s.animeID, NewPathDiffService(s.db, s.renderer, nil))
	if err != nil {
		t.Fatalf("load placements: %v", err)
	}
	for _, placement := range placements {
		if placement.ID == s.chain.PlacementID {
			return placement
		}
	}
	t.Fatalf("placement %q not loaded", s.chain.PlacementID)
	return animeFilesPlacement{}
}

func readModelRow(rows []AnimeFileView, placementID string) (AnimeFileView, bool) {
	for _, row := range rows {
		if row.PlacementID == placementID {
			return row, true
		}
	}
	return AnimeFileView{}, false
}

func diffForPlacement(diffs []PathDiffView, placementID string) (PathDiffView, bool) {
	for _, diff := range diffs {
		if diff.PlacementID == placementID {
			return diff, true
		}
	}
	return PathDiffView{}, false
}
