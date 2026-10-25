package filemaintenance

import "testing"

// recoveryBase returns a missing placement with a frozen candidate at its own
// rendered target: the shape the scan leaves behind for an all-locator-missing
// row whose COMPLETE rendered unit it observed at the rendered paths.
func recoveryBase() animeFilesFacts {
	facts := animeFilesBase()
	facts.Diffs = []PathDiffView{{
		PlacementID: "p1", FilePath: "/lib/Show/raw ep1.mkv", Disposition: DispositionMissing,
		ExpectedPath: strPtr("/lib/Show/Season 01/Show - E01.mkv"),
		Companions: []CompanionPathDiffView{{
			ID: "c1", FilePath: "/lib/Show/raw ep1.zh.ass", MediaKind: "subtitle",
			ExpectedPath: strPtr("/lib/Show/Season 01/Show - E01.zh.ass"),
		}},
	}}
	facts.Placements[0].Recovery = &animeFilesRecoveryCandidate{
		PlacementID: "p1", TargetPath: "/lib/Show/Season 01/Show - E01.mkv", Generation: 3,
		SizeBytes: 4096, PlacementVersion: 7, Fingerprint: "meta:size=4096", Companions: 1,
		CompanionTargets: []animeFilesRecoveryCompanionTarget{{
			ID: "c1", TargetPath: "/lib/Show/Season 01/Show - E01.zh.ass",
		}},
	}
	facts.RecoveryReady = true
	return facts
}

// The confirm rides on the row: candidate present, row missing, read current
// and nothing in flight — otherwise the action refuses with the gate reason
// and no candidate ever means nothing_to_recover. The view exposes the frozen
// tokens (generation, target, fingerprint, placement version) the confirm
// echoes back.
func TestDeriveAnimeFilesRecoveryActionGates(t *testing.T) {
	t.Run("available with candidate on a current missing row", func(t *testing.T) {
		row := deriveAnimeFiles(recoveryBase()).Files[0]
		if !row.Actions.Recover.Available {
			t.Fatalf("recover = %+v, want available", row.Actions.Recover)
		}
		view := row.Recovery
		if view == nil || view.Generation != 3 || view.SizeBytes != 4096 {
			t.Fatalf("recovery view = %+v", view)
		}
		if view.PlacementVersion != 7 || view.Fingerprint != "meta:size=4096" || view.Companions != 1 {
			t.Fatalf("recovery tokens = %+v", view)
		}
	})
	t.Run("not missing", func(t *testing.T) {
		facts := recoveryBase()
		facts.Diffs[0].Disposition = DispositionPresent
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonRecoveryGate {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonRecoveryGate)
		}
	})
	t.Run("owner retired", func(t *testing.T) {
		facts := recoveryBase()
		facts.Owner.Lifecycle = "retired"
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonOwnerRetired {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonOwnerRetired)
		}
	})
	t.Run("scan running", func(t *testing.T) {
		facts := recoveryBase()
		facts.Scan.State = "running"
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonScanRunning {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonScanRunning)
		}
	})
	t.Run("alignment in flight", func(t *testing.T) {
		facts := recoveryBase()
		facts.AlignmentLive = true
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonAlignmentRunning {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonAlignmentRunning)
		}
	})
	t.Run("stale read behind a newer generation", func(t *testing.T) {
		facts := recoveryBase()
		facts.RecoveryReady = false
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonScanChanged {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonScanChanged)
		}
	})
	t.Run("no candidate", func(t *testing.T) {
		facts := recoveryBase()
		facts.Placements[0].Recovery = nil
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != "nothing_to_recover" {
			t.Fatalf("recover = %+v, want nothing_to_recover", row.Actions.Recover)
		}
		if row.Recovery != nil {
			t.Fatalf("recovery view = %+v, want absent", row.Recovery)
		}
	})
}

// A read-only retired owner hides nothing the row already decided: the action
// still carries its reason, the view still carries the frozen candidate.
func TestDeriveAnimeFilesRecoveryViewSurvivesRetirement(t *testing.T) {
	facts := recoveryBase()
	facts.Owner.Lifecycle = "retired"
	row := deriveAnimeFiles(facts).Files[0]
	if row.Recovery == nil || row.Recovery.TargetPath != "/lib/Show/Season 01/Show - E01.mkv" {
		t.Fatalf("recovery = %+v", row.Recovery)
	}
}

// The read model gates the offered action on the CURRENT render, not just on
// generation liveness: the confirm re-renders under the anime's live naming
// rules and refuses a unit that moved, so the button must be unavailable with
// a rescan reason instead of promising a confirm that would be refused.
func TestDeriveAnimeFilesRecoveryRequiresCurrentRender(t *testing.T) {
	t.Run("video target re-rendered by a rule change", func(t *testing.T) {
		facts := recoveryBase()
		facts.Diffs[0].ExpectedPath = strPtr("/lib/Show/Season 01/Show - E01v2.mkv")
		facts.Diffs[0].Differs = true
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonRescanRequired)
		}
	})
	t.Run("video target no longer renders", func(t *testing.T) {
		facts := recoveryBase()
		facts.Diffs[0].ExpectedPath = nil
		facts.Diffs[0].TargetUnavailable = "coverage_unresolved"
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonRescanRequired)
		}
	})
	t.Run("render failed", func(t *testing.T) {
		facts := recoveryBase()
		facts.Diffs[0].TargetUnavailable = "target_render_failed"
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonRescanRequired)
		}
	})
	t.Run("companion membership changed since the scan", func(t *testing.T) {
		facts := recoveryBase()
		// The frozen candidate counts one companion; a second active
		// companion registered after the scan means the unit is no longer
		// the one the confirm would re-render.
		facts.Placements[0].Companions = append(facts.Placements[0].Companions,
			animeFilesCompanion{ID: "c2", MediaKind: "subtitle", Path: "/lib/Show/raw ep1.en.ass", SizeBytes: 48})
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonRescanRequired)
		}
	})
	t.Run("companion no longer renders", func(t *testing.T) {
		facts := recoveryBase()
		facts.Placements[0].Companions = []animeFilesCompanion{{ID: "c1", MediaKind: "subtitle", Path: "/lib/Show/raw ep1.zh.ass", SizeBytes: 64}}
		facts.Diffs[0].Companions = []CompanionPathDiffView{{ID: "c1", FilePath: "/lib/Show/raw ep1.zh.ass", MediaKind: "subtitle"}}
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
			t.Fatalf("recover = %+v, want %s", row.Actions.Recover, AnimeFilesReasonRescanRequired)
		}
	})
	t.Run("companion target moved by a rule change", func(t *testing.T) {
		// The defect this case pins: the same companion COUNT, but the
		// companion now renders somewhere else. A count-only gate offered the
		// confirm button here, and the confirm's own transaction then refused
		// companion_missing — the UI promised an action it had to reject.
		facts := recoveryBase()
		facts.Placements[0].Companions = []animeFilesCompanion{{ID: "c1", MediaKind: "subtitle", Path: "/lib/Show/raw ep1.zh.ass", SizeBytes: 64}}
		movedTarget := "/lib/Show/Season 01/Show - E01.zh.forced.ass"
		facts.Diffs[0].Companions = []CompanionPathDiffView{{
			ID: "c1", FilePath: "/lib/Show/raw ep1.zh.ass", MediaKind: "subtitle",
			ExpectedPath: &movedTarget,
		}}
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
			t.Fatalf("recover = %+v, want %s for a moved companion target",
				row.Actions.Recover, AnimeFilesReasonRescanRequired)
		}
	})
	t.Run("frozen companion targets missing from the candidate", func(t *testing.T) {
		// A candidate that claims companions but carries no frozen targets
		// cannot be verified: fail closed instead of offering it.
		facts := recoveryBase()
		facts.Placements[0].Recovery.CompanionTargets = nil
		facts.Placements[0].Companions = []animeFilesCompanion{{ID: "c1", MediaKind: "subtitle", Path: "/lib/Show/raw ep1.zh.ass", SizeBytes: 64}}
		row := deriveAnimeFiles(facts).Files[0]
		if row.Actions.Recover.Available || row.Actions.Recover.ReasonCode != AnimeFilesReasonRescanRequired {
			t.Fatalf("recover = %+v, want %s for an unverifiable unit",
				row.Actions.Recover, AnimeFilesReasonRescanRequired)
		}
	})
	t.Run("whole unit still renders to the frozen paths", func(t *testing.T) {
		facts := recoveryBase()
		facts.Placements[0].Companions = []animeFilesCompanion{{ID: "c1", MediaKind: "subtitle", Path: "/lib/Show/raw ep1.zh.ass", SizeBytes: 64}}
		companionTarget := "/lib/Show/Season 01/Show - E01.zh.ass"
		facts.Diffs[0].Companions = []CompanionPathDiffView{{
			ID: "c1", FilePath: "/lib/Show/raw ep1.zh.ass", MediaKind: "subtitle",
			ExpectedPath: &companionTarget,
		}}
		row := deriveAnimeFiles(facts).Files[0]
		if !row.Actions.Recover.Available {
			t.Fatalf("recover = %+v, want available for a live unit", row.Actions.Recover)
		}
	})
}
