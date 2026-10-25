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
		Companions:   []CompanionPathDiffView{},
	}}
	facts.Placements[0].Recovery = &animeFilesRecoveryCandidate{
		PlacementID: "p1", TargetPath: "/lib/Show/Season 01/Show - E01.mkv", Generation: 3,
		SizeBytes: 4096, PlacementVersion: 7, Fingerprint: "meta:size=4096", Companions: 1,
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
