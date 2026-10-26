package filemaintenance

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/workitems"
)

// TestOneOffAlignmentRunsWithAutomaticAlignmentOff drives the files read and
// the one-off admission over a real calibrated generation while the anime's
// automatic alignment is off: the consumer settles alignment_disabled, the
// read reports the file misaligned with its rendered target, and the one-off
// admission creates the same ready operation and execute claim the automatic
// consumer would. A replay admits nothing; a stale generation is refused.
func TestOneOffAlignmentRunsWithAutomaticAlignmentOff(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	ctx := context.Background()
	const animeID = "anime-oneoff-1"
	seedAnimeSubscription(t, fixture.db, animeID, "sub-oneoff-1", "stopped")
	seedAnimeForRenderer(t, fixture.db, animeID, "Show", "tv")
	seedInstanceReplacement(t, fixture.db, animeID, ModeAll, ``, 1)
	seedCompletedGenerationWithRenderChains(t, fixture.db, animeID, 1, map[string]string{
		"/library/EP 01.mkv": "fp-1",
	}, rendererRules())
	// The seeded observation carries the placement's own bytes (a present,
	// unchanged file), so the only difference is the path.
	if _, err := fixture.db.Exec(`
UPDATE reconciliation_files rf SET fingerprint = (p.source_fingerprint::jsonb)->>'value'
FROM placements p
WHERE p.id = rf.facts->>'placement_id' AND rf.managed_anime_id = $1`, animeID); err != nil {
		t.Fatal(err)
	}

	fixture.calibration.SetAlignmentConsumer(NewAlignmentService(fixture.db))
	snapshot := NewReconciliationSnapshotReader(fixture.db)
	renderer := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: rendererRules()})
	fixture.calibration.planner = NewPlanner(snapshot, renderer)

	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	outcomes := fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	if len(outcomes) != 1 || outcomes[0].OwnerResult.ReasonCode != "alignment_disabled" {
		t.Fatalf("automatic alignment must stay off: %+v", outcomes)
	}

	service := NewService(fixture.calibration.policies, nil, fixture.calibration.planner, fixture.calibration.placements, nil)
	service.SetOwnerTruthWires(fixture.calibration, nil, fixture.calibration.alignment, nil)
	service.SetPathDiff(NewPathDiffService(fixture.db, renderer, nil))

	view, err := service.ListAnimeFilesApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := chainedFile(view.Files)
	if !ok {
		t.Fatalf("files = %+v", view.Files)
	}
	if row.Status != AnimeFileMisaligned || row.TargetPath == nil || *row.TargetPath != "/library/Show/Season 01/Show - E01.mkv" {
		t.Fatalf("row = %+v", row)
	}
	if !row.Actions.Align.Available || view.Scan.Generation != 1 {
		t.Fatalf("align must be available with the toggle off: %+v scan=%+v", row.Actions.Align, view.Scan)
	}

	// A template change on the anime moves the target of the existing
	// placement: renders read the anime's current rules, not the frozen ones.
	changed := rendererRules()
	changed.TV.DirectoryTemplate = "/library/${sanitize(anime.title)}/S${pad(tmdb.season, 2)}"
	changed.TV.VideoFilenameTemplate = "E${pad(ep.display, 2)}.${file.ext}"
	current := NewService(fixture.calibration.policies, nil, fixture.calibration.planner, fixture.calibration.placements, nil)
	current.SetOwnerTruthWires(fixture.calibration, nil, fixture.calibration.alignment, nil)
	current.SetPathDiff(NewPathDiffService(fixture.db, NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: changed}), nil))
	changedView, err := current.ListAnimeFilesApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := chainedFile(changedView.Files); got.Status != AnimeFileMisaligned || got.TargetPath == nil || *got.TargetPath != "/library/Show/S01/E01.mkv" {
		t.Fatalf("a template change must show the current template's path: %+v", got)
	}
	// The RSS row's new path is the same render without a scan.
	placementTargets, err := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: changed}).RenderPlacementTargets(ctx, animeID, []string{row.PlacementID, "missing-placement"})
	if err != nil || len(placementTargets) != 1 || placementTargets[row.PlacementID] != "/library/Show/S01/E01.mkv" {
		t.Fatalf("placement targets = %+v %v", placementTargets, err)
	}
	if row.GroupName == "" {
		t.Fatalf("the chained row must name its release's group: %+v", row)
	}

	if _, err := service.AlignAnimeFilesApplication(ctx, animeID, 2, nil); !isAlignDenial(err, AnimeFilesReasonScanChanged) {
		t.Fatalf("stale generation must be refused, got %v", err)
	}
	result, err := service.AlignAnimeFilesApplication(ctx, animeID, 1, []string{row.PlacementID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Admitted != 1 || result.Generation != 1 {
		t.Fatalf("result = %+v", result)
	}
	var ready, items int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM alignment_operations WHERE managed_anime_id = $1 AND state = 'ready' AND target_path = '/library/Show/Season 01/Show - E01.mkv'`, animeID).Scan(&ready); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE kind = 'alignment_execute' AND owner_id IN (SELECT id FROM alignment_operations WHERE managed_anime_id = $1)`, animeID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if ready != 1 || items != 1 {
		t.Fatalf("one ready operation with one execute claim: ops=%d items=%d", ready, items)
	}
	replay, err := service.AlignAnimeFilesApplication(ctx, animeID, 1, nil)
	if err != nil || replay.Admitted != 0 {
		t.Fatalf("replay must admit nothing: %+v %v", replay, err)
	}
	view, err = service.ListAnimeFilesApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	aligned, _ := chainedFile(view.Files)
	if got := aligned.Actions.Align; got.Available || got.ReasonCode != AnimeFilesReasonAlignmentRunning || !view.Aligning {
		t.Fatalf("an admitted alignment is in progress: %+v aligning=%v", got, view.Aligning)
	}

	if _, err := fixture.db.Exec(`UPDATE subscriptions SET state = 'pending' WHERE managed_anime_id = $1`, animeID); err == nil {
		if _, err := service.AlignAnimeFilesApplication(ctx, animeID, 1, nil); !isAlignDenial(err, AnimeFilesReasonNotStarted) {
			t.Fatalf("a pending subscription admits no manual alignment, got %v", err)
		}
	}
}

// chainedFile is the row of the placement seeded with a full render chain (the
// fixture's generation also seeds a bare placement without lineage).
func chainedFile(files []AnimeFileView) (AnimeFileView, bool) {
	for _, file := range files {
		if file.ReleaseID != "" {
			return file, true
		}
	}
	return AnimeFileView{}, false
}

func isAlignDenial(err error, reason string) bool {
	var denial *AnimeFilesAlignError
	return errors.As(err, &denial) && denial.Reason == reason
}

// TestFilesReadAfterAlignmentNeedsRescan covers the read that follows a
// completed alignment. The scan behind it observed the pre-move path, so the
// row only reads as aligned once the preview renders the placement's target
// from the placement id. The scan view then carries the durable rescan fact:
// the browser cannot see an alignment that both started and settled between
// two polls, and without the fact it never rescans and the row keeps showing
// the status of the pre-move scan.
func TestFilesReadAfterAlignmentNeedsRescan(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	ctx := context.Background()
	const (
		animeID   = "anime-moved-read"
		observed  = "/library/EP 01.mkv"
		movedPath = "/library/Show/Season 01/Show - E01.mkv"
	)
	seedAnimeSubscription(t, fixture.db, animeID, "sub-moved-read", "stopped")
	seedAnimeForRenderer(t, fixture.db, animeID, "Show", "tv")
	seedInstanceReplacement(t, fixture.db, animeID, ModeAll, ``, 1)
	seedCompletedGenerationWithRenderChains(t, fixture.db, animeID, 1, map[string]string{
		observed: "fp-1",
	}, rendererRules())
	// The seeded observation carries the placement's own bytes (a present,
	// unchanged file), so the only difference is the path.
	if _, err := fixture.db.Exec(`
UPDATE reconciliation_files rf SET fingerprint = (p.source_fingerprint::jsonb)->>'value'
FROM placements p
WHERE p.id = rf.facts->>'placement_id' AND rf.managed_anime_id = $1`, animeID); err != nil {
		t.Fatal(err)
	}
	fixture.calibration.SetAlignmentConsumer(NewAlignmentService(fixture.db))
	renderer := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: rendererRules()})
	fixture.calibration.planner = NewPlanner(NewReconciliationSnapshotReader(fixture.db), renderer)
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	// Automatic alignment stays off here, so the consumer settles without
	// admitting anything: the only operation this anime ever gets is the
	// one-off seeded below, which keeps the read free of in-flight work.
	fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)

	service := NewService(fixture.calibration.policies, nil, nil, fixture.calibration.placements, nil)
	service.SetOwnerTruthWires(fixture.calibration, nil, fixture.calibration.alignment, nil)
	service.SetPathDiff(NewPathDiffService(fixture.db, renderer, nil))

	view, err := service.ListAnimeFilesApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Scan.RescanRequired {
		t.Fatal("a settled generation without a completed alignment needs no rescan")
	}
	before, ok := chainedFile(view.Files)
	if !ok || before.Status != AnimeFileMisaligned || before.TargetPath == nil || *before.TargetPath != movedPath {
		t.Fatalf("pre-move row = %+v", before)
	}

	// The alignment moved the file onto its target and settled. Only the
	// placement moves: the calibration row keeps the path the scan observed.
	if _, err := fixture.db.Exec(`UPDATE placements SET physical_path = $2 WHERE id = $1`, before.PlacementID, movedPath); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`
INSERT INTO alignment_operations
(id, idempotency_key, managed_anime_id, scan_generation, observation_ref, candidate_ref, policy_version,
 metadata_version, rules_version, naming_version, source_path, source_fingerprint, target_path,
 target_contract, dry_run_result, state, version, created_at, completed_at, updated_at)
VALUES ('alg-moved-read', 'align:moved-read', $1, 1, $1 || ':1:' || $2, 'c', 1, 1, 1, 1,
 $2, 'fp-1', $3, '{}', '{}', 'completed', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		animeID, observed, movedPath); err != nil {
		t.Fatal(err)
	}

	moved, err := service.ListAnimeFilesApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := chainedFile(moved.Files)
	if !ok {
		t.Fatalf("files = %+v", moved.Files)
	}
	// The pre-move observation still renders the target, so the file the
	// alignment just placed reads as aligned instead of unscanned.
	if row.Status != AnimeFileAligned {
		t.Fatalf("post-move row status = %q, want %q: %+v", row.Status, AnimeFileAligned, row)
	}
	if !moved.Scan.RescanRequired {
		t.Fatal("a completed alignment of the current generation must ask for a rescan")
	}
	if moved.Aligning {
		t.Fatal("a settled alignment is not in flight")
	}

	// A pending operation is in flight, not a settled move.
	if _, err := fixture.db.Exec(`UPDATE alignment_operations SET state = 'ready', completed_at = NULL WHERE id = 'alg-moved-read'`); err != nil {
		t.Fatal(err)
	}
	live, err := service.ListAnimeFilesApplication(ctx, animeID)
	if err != nil {
		t.Fatal(err)
	}
	if live.Scan.RescanRequired || !live.Aligning {
		t.Fatalf("in-flight alignment: rescan_required=%v aligning=%v", live.Scan.RescanRequired, live.Aligning)
	}
}
