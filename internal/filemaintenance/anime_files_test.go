package filemaintenance

import (
	"testing"
	"time"
)

func animeFilesBase() animeFilesFacts {
	return animeFilesFacts{
		AnimeID:    "a1",
		Owner:      animeFilesOwner{Lifecycle: "active", SubscriptionState: "stopped"},
		Scan:       &animeFilesScanOwner{State: "completed", Generation: 3},
		Generation: 3,
		Placements: []animeFilesPlacement{{
			ID: "p1", Path: "/lib/Show/raw ep1.mkv", ReleaseID: "r1", Coverage: []string{"tv:s1:e1"},
			Companions: []animeFilesCompanion{{ID: "c1", MediaKind: "subtitle", Path: "/lib/Show/raw ep1.zh.ass", SizeBytes: 10}},
		}},
	}
}

func TestDeriveAnimeFilesMisalignedWithCompanion(t *testing.T) {
	facts := animeFilesBase()
	facts.Diffs = []PathDiffView{{
		PlacementID: "p1", FilePath: "/lib/Show/raw ep1.mkv", Disposition: DispositionPresent, SizeBytes: 100,
		ExpectedPath: strPtr("/lib/Show/Season 01/Show - E01.mkv"), Differs: true,
		Companions: []CompanionPathDiffView{{ID: "c1", FilePath: "/lib/Show/raw ep1.zh.ass", State: "present", Differs: true, ExpectedPath: strPtr("/lib/Show/Season 01/Show - E01.zh.ass")}},
	}}
	view := deriveAnimeFiles(facts)
	if len(view.Files) != 1 {
		t.Fatalf("files = %d", len(view.Files))
	}
	row := view.Files[0]
	if row.Status != AnimeFileMisaligned || row.TargetPath == nil || *row.TargetPath != "/lib/Show/Season 01/Show - E01.mkv" {
		t.Fatalf("row = %+v", row)
	}
	// The display root keeps anime folder / season folder / file below it.
	if view.Root != "/" || row.RelativePath != "lib/Show/raw ep1.mkv" || *row.TargetRelativePath != "lib/Show/Season 01/Show - E01.mkv" {
		t.Fatalf("root=%q rel=%q target=%q", view.Root, row.RelativePath, *row.TargetRelativePath)
	}
	if row.MediaKind != "tv" || row.Season == nil || *row.Season != 1 || len(row.Episodes) != 1 || row.Episodes[0] != "1" {
		t.Fatalf("episode facts = %+v", row)
	}
	if row.SizeBytes == nil || *row.SizeBytes != 100 {
		t.Fatalf("size = %v", row.SizeBytes)
	}
	if !row.Actions.Align.Available || !view.AlignAllAction.Available {
		t.Fatalf("align must be available: %+v / %+v", row.Actions.Align, view.AlignAllAction)
	}
	if row.Actions.Rerun.Available || row.Actions.Rerun.ReasonCode != AnimeFilesReasonFileNotMissing {
		t.Fatalf("rerun = %+v", row.Actions.Rerun)
	}
	if len(row.Companions) != 1 || row.Companions[0].Status != AnimeFileMisaligned {
		t.Fatalf("companion = %+v", row.Companions)
	}
}

func TestDeriveAnimeFilesUnscannedBeforeAnyScan(t *testing.T) {
	facts := animeFilesBase()
	facts.Scan = nil
	facts.Generation = 0
	view := deriveAnimeFiles(facts)
	row := view.Files[0]
	if row.Status != AnimeFileUnscanned || row.Companions[0].Status != AnimeFileUnscanned {
		t.Fatalf("row = %+v", row)
	}
	if view.Scan.State != "idle" || view.Scan.Generation != 0 {
		t.Fatalf("scan = %+v", view.Scan)
	}
	if view.AlignAllAction.Available || view.AlignAllAction.ReasonCode != AnimeFilesReasonScanRequired {
		t.Fatalf("align all = %+v", view.AlignAllAction)
	}
	if !view.ScanAction.Available {
		t.Fatalf("scan action = %+v", view.ScanAction)
	}
}

func TestDeriveAnimeFilesMissingOffersRerunAndRelease(t *testing.T) {
	facts := animeFilesBase()
	facts.Diffs = []PathDiffView{{PlacementID: "p1", FilePath: "/lib/Show/raw ep1.mkv", Disposition: DispositionMissing}}
	row := deriveAnimeFiles(facts).Files[0]
	if row.Status != AnimeFileMissing || !row.Actions.Rerun.Available || !row.Actions.Release.Available || row.Actions.Align.Available {
		t.Fatalf("row = %+v", row)
	}
	facts.Placements[0].ReleaseID = ""
	row = deriveAnimeFiles(facts).Files[0]
	if row.Actions.Rerun.Available || row.Actions.Rerun.ReasonCode != AnimeFilesReasonSourceUnavailable {
		t.Fatalf("rerun without release = %+v", row.Actions.Rerun)
	}
}

func TestDeriveAnimeFilesChangedAndNoTarget(t *testing.T) {
	facts := animeFilesBase()
	facts.Diffs = []PathDiffView{{PlacementID: "p1", FilePath: "/lib/Show/raw ep1.mkv", Disposition: DispositionFingerprintConflict}}
	if row := deriveAnimeFiles(facts).Files[0]; row.Status != AnimeFileChanged {
		t.Fatalf("row = %+v", row)
	}
	facts.Diffs = []PathDiffView{{PlacementID: "p1", FilePath: "/lib/Show/raw ep1.mkv", Disposition: DispositionPresent, ExpectedPath: strPtr("/lib/Show/raw ep1.mkv"), TargetUnavailable: "render_facts_unavailable"}}
	row := deriveAnimeFiles(facts).Files[0]
	if row.Status != AnimeFileNoTarget || row.StatusReason != "render_facts_unavailable" || row.Actions.Align.Available {
		t.Fatalf("row = %+v", row)
	}
}

func TestDeriveAnimeFilesMovedAfterScan(t *testing.T) {
	facts := animeFilesBase()
	facts.Placements[0].Path = "/lib/Show/Season 01/Show - E01.mkv"
	facts.Diffs = []PathDiffView{{PlacementID: "p1", FilePath: "/lib/Show/raw ep1.mkv", Disposition: DispositionPresent, ExpectedPath: strPtr("/lib/Show/Season 01/Show - E01.mkv"), Differs: true}}
	if row := deriveAnimeFiles(facts).Files[0]; row.Status != AnimeFileAligned {
		t.Fatalf("moved to its target must be aligned: %+v", row)
	}
	facts.Placements[0].Path = "/elsewhere/x.mkv"
	if row := deriveAnimeFiles(facts).Files[0]; row.Status != AnimeFileUnscanned {
		t.Fatalf("moved elsewhere must wait for a scan: %+v", row)
	}
}

func TestDeriveAnimeFilesActionGates(t *testing.T) {
	misaligned := func(facts animeFilesFacts) animeFilesFacts {
		facts.Diffs = []PathDiffView{{PlacementID: "p1", FilePath: "/lib/Show/raw ep1.mkv", Disposition: DispositionPresent, ExpectedPath: strPtr("/lib/Show/E01.mkv"), Differs: true}}
		return facts
	}
	cases := []struct {
		name   string
		mutate func(*animeFilesFacts)
		reason string
	}{
		{"retired", func(f *animeFilesFacts) { f.Owner.Lifecycle = "retired" }, AnimeFilesReasonOwnerRetired},
		{"pending", func(f *animeFilesFacts) { f.Owner.SubscriptionState = "pending" }, AnimeFilesReasonNotStarted},
		{"scan running", func(f *animeFilesFacts) { f.Scan.State = "running" }, AnimeFilesReasonScanRunning},
		{"calibrating", func(f *animeFilesFacts) { f.CalibratePending = true }, AnimeFilesReasonScanRunning},
		{"in progress", func(f *animeFilesFacts) {
			f.Alignments = []animeFilesAlignmentOp{{ObservationRef: "a1:3:/lib/Show/raw ep1.mkv", State: "ready"}}
		}, AnimeFilesReasonAlignmentRunning},
		{"settled", func(f *animeFilesFacts) {
			f.Alignments = []animeFilesAlignmentOp{{ObservationRef: "a1:3:/lib/Show/raw ep1.mkv", State: "blocked", FailureCode: "target_occupied"}}
		}, AnimeFilesReasonRescanRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := misaligned(animeFilesBase())
			tc.mutate(&facts)
			row := deriveAnimeFiles(facts).Files[0]
			if row.Actions.Align.Available || row.Actions.Align.ReasonCode != tc.reason {
				t.Fatalf("align = %+v, want %s", row.Actions.Align, tc.reason)
			}
		})
	}
	facts := misaligned(animeFilesBase())
	facts.Alignments = []animeFilesAlignmentOp{{ObservationRef: "a1:3:/lib/Show/raw ep1.mkv", State: "blocked", FailureCode: "target_occupied"}}
	row := deriveAnimeFiles(facts).Files[0]
	if row.Alignment == nil || row.Alignment.State != "failed" || row.Alignment.ReasonCode != "target_occupied" {
		t.Fatalf("alignment = %+v", row.Alignment)
	}
}

func TestAnimeFilesScanStateAndTime(t *testing.T) {
	completed := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	facts := animeFilesBase()
	facts.Scan.CompletedAt = &completed
	if view := facts.scanState(); view.State != "idle" || view.ScannedAt == nil || !view.ScannedAt.Equal(completed) {
		t.Fatalf("scan = %+v", view)
	}
	facts.Scan.State = "failed"
	facts.Scan.FailureCode = strPtr("SCAN_ROOT_MISSING")
	if view := facts.scanState(); view.State != "failed" || view.FailureCode == nil {
		t.Fatalf("scan = %+v", view)
	}
}

func TestOneOffAlignmentAdmission(t *testing.T) {
	completed := &animeFilesScanOwner{State: "completed", Generation: 4}
	cases := []struct {
		name        string
		scan        *animeFilesScanOwner
		calibrating bool
		latest      int64
		requested   int64
		want        string
	}{
		{"admitted", completed, false, 4, 4, ""},
		{"stale request", completed, false, 4, 3, AnimeFilesReasonScanChanged},
		{"never scanned", nil, false, 0, 1, AnimeFilesReasonScanRequired},
		{"running", &animeFilesScanOwner{State: "running", Generation: 4}, false, 4, 4, AnimeFilesReasonScanRunning},
		{"calibrating", completed, true, 3, 3, AnimeFilesReasonScanRunning},
		{"newer uncalibrated", &animeFilesScanOwner{State: "completed", Generation: 5}, false, 4, 4, AnimeFilesReasonScanChanged},
		{"failed scan keeps last", &animeFilesScanOwner{State: "failed", Generation: 5}, false, 4, 4, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := oneOffAlignmentAdmission(tc.scan, tc.calibrating, tc.latest, tc.requested); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestAnimeFilesRelativePaths(t *testing.T) {
	if got := relativeTo("/", "/a/x.mkv"); got != "a/x.mkv" {
		t.Fatalf("got %q", got)
	}
	if got := relativeTo("/a/b", "/a/bc/x.mkv"); got != "a/bc/x.mkv" {
		t.Fatalf("prefix must stop at a directory boundary: %q", got)
	}
}

func TestDeriveAnimeFilesCarriesGroupAndRelease(t *testing.T) {
	facts := animeFilesBase()
	facts.Placements[0].Path = "/media/anime/Show/Season 01/raw ep1.mkv"
	facts.Placements[0].Companions = nil
	facts.Placements[0].GroupName = "SampleGroup"
	facts.Placements[0].ReleaseTitle = "[SampleGroup] Show - 01"
	view := deriveAnimeFiles(facts)
	row := view.Files[0]
	if row.GroupName != "SampleGroup" || row.ReleaseTitle != "[SampleGroup] Show - 01" {
		t.Fatalf("group = %q title = %q", row.GroupName, row.ReleaseTitle)
	}
	if view.Root != "/media/anime" || row.RelativePath != "Show/Season 01/raw ep1.mkv" {
		t.Fatalf("root = %q rel = %q", view.Root, row.RelativePath)
	}
}
