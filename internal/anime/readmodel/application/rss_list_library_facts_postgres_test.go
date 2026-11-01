package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

// An imported file of an owner release reads in_library wherever its placement
// sits now; the row carries no location, because this view renders the seed's
// result and the file's state. A file with no placement stays not held.
func TestRSSListOwnerRowReadsHeldFile(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	seedFailedRecordRoot(t, db, "record-held")
	// The fixture's file operation fo-failed placed file-a.mkv (frozen target
	// /library/Failed Show/file-a.mkv); it now sits elsewhere.
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, verification_state, state)
VALUES ('plc-held', '/library/Failed Show 2021/Season 01/file-a.mkv', 'anime-failed', 'file_operation', 'fo-failed',
 'sha256:a', 'sha256:a', 'parser-v1', 'verified', 'active')`); err != nil {
		t.Fatal(err)
	}
	service := readmodelNewTestService(t, db, nil)
	view := RSSListView{Groups: []RSSListGroup{{Items: []RSSListItem{{
		SnapshotID: "release:rel-failed",
		Files: []RSSListFile{
			{SourcePath: "file-a.mkv", RenderedTarget: "/library/Failed Show/file-a.mkv"},
			{SourcePath: "file-b.mkv", RenderedTarget: "/library/Failed Show/file-b.mkv"},
		},
	}}}}}

	if err := service.applyLibraryFacts(ctx, "anime-failed", &view); err != nil {
		t.Fatal(err)
	}
	files := view.Groups[0].Items[0].Files
	if files[0].InLibrary == nil || !*files[0].InLibrary {
		t.Fatalf("held file = %+v", files[0])
	}
	if files[0].PlacementEnd != nil {
		t.Fatalf("a held file has no ended placement: %+v", files[0].PlacementEnd)
	}
	if files[1].InLibrary != nil || files[1].PlacementEnd != nil {
		t.Fatalf("a file with no placement stays not held: %+v", files[1])
	}
}

// A file alignment moved away from its frozen target is still in the library:
// the overlap alone compares the frozen target and reads it not held, and the
// library location read, which matches through the file operation to the
// active placement, turns it held and moves the seed's counts with it. A file
// with no placement stays not held.
func TestRSSListOwnerRowMovedPlacementReadsInLibrary(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	seedFailedRecordRoot(t, db, "record-moved")
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, verification_state, state)
VALUES ('plc-moved', '/library/Failed Show （2021）/Season 01/Failed Show - S01E01.mkv', 'anime-failed', 'file_operation', 'fo-failed',
 'sha256:a', 'sha256:a', 'parser-v1', 'verified', 'active')`); err != nil {
		t.Fatal(err)
	}
	service := readmodelNewTestService(t, db, nil)
	view := RSSListView{Groups: []RSSListGroup{{Items: []RSSListItem{{
		SnapshotID: "release:rel-failed",
		Evaluation: RSSListEvaluation{State: RSSListEvaluationEvaluated},
		Files: []RSSListFile{
			{SourcePath: "file-a.mkv", RenderedTarget: "/library/Failed Show/file-a.mkv", Included: true, Safety: rssListFileSafetyValid},
			{SourcePath: "file-b.mkv", RenderedTarget: "/library/Failed Show/file-b.mkv", Included: true, Safety: rssListFileSafetyValid},
		},
	}}}}}

	if err := service.applyLibraryOverlap(ctx, &view); err != nil {
		t.Fatal(err)
	}
	item := view.Groups[0].Items[0]
	if item.LibraryOverlap.Verdict != RSSListLibraryOverlapAbsent || item.Files[0].InLibrary == nil || *item.Files[0].InLibrary {
		t.Fatalf("frozen-target overlap of a moved file = %+v / %+v", item.LibraryOverlap, item.Files[0])
	}
	if err := service.applyLibraryFacts(ctx, "anime-failed", &view); err != nil {
		t.Fatal(err)
	}
	item = view.Groups[0].Items[0]
	moved, other := item.Files[0], item.Files[1]
	if moved.InLibrary == nil || !*moved.InLibrary {
		t.Fatalf("a moved placement reads in the library: %+v", moved)
	}
	if other.InLibrary == nil || *other.InLibrary {
		t.Fatalf("a file with no placement stays not held: %+v", other)
	}
	overlap := item.LibraryOverlap
	if overlap.Verdict != RSSListLibraryOverlapPresent || overlap.PresentFiles == nil || *overlap.PresentFiles != 1 ||
		overlap.PendingFiles == nil || *overlap.PendingFiles != 1 {
		t.Fatalf("overlap counts follow the held file: %+v", overlap)
	}
}

// A file whose placement has ended is in no library, so it can no longer read
// "waiting to import": the row carries that one file's own outcome, keeps the
// target this seed rendered, and never points at whichever file holds the
// episode now.
func TestRSSListOwnerRowStatesEndedPlacement(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	seedFailedRecordRoot(t, db, "record-ended")
	service := readmodelNewTestService(t, db, nil)
	cases := []struct {
		name  string
		state string
		cause string
	}{
		{"superseded", RSSListPlacementEndSuperseded, ""},
		{"displaced overwrite", RSSListPlacementEndDisplaced, RSSListPlacementEndCauseOverwrite},
		{"displaced redownload", RSSListPlacementEndDisplaced, RSSListPlacementEndCauseRedownload},
		{"released", RSSListPlacementEndReleased, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := disposabledb.Owner(t).ExecContext(ctx, `DELETE FROM placements WHERE managed_anime_id = 'anime-failed'`); err != nil {
				t.Fatal(err)
			}
			// A terminal state is entered from active only (placements_release_terminal
			// rejects an insert in any of them), so seed the holder and then take the exit.
			if _, err := disposabledb.Owner(t).ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, verification_state, state)
VALUES ('plc-ended', '/library/Failed Show/file-a.mkv', 'anime-failed', 'file_operation', 'fo-failed',
 'sha256:a', 'sha256:a', 'parser-v1', 'verified', 'active')`); err != nil {
				t.Fatal(err)
			}
			var exit string
			switch c.state {
			case RSSListPlacementEndDisplaced:
				exit = `
UPDATE placements SET state = 'displaced', displaced_at = CURRENT_TIMESTAMP, displaced_cause = $1,
 displaced_by_placement_id = 'plc-successor', displaced_by_anime_id = 'anime-failed', displaced_by_ref = 'fixture'
WHERE id = 'plc-ended'`
			case RSSListPlacementEndReleased:
				exit = `
UPDATE placements SET state = 'released', released_at = CURRENT_TIMESTAMP, released_by = 'user:placement_issue',
 release_request_key = 'fixture-release-ended'
WHERE id = 'plc-ended'`
			default:
				exit = `UPDATE placements SET state = 'superseded' WHERE id = 'plc-ended'`
			}
			args := []any{}
			if c.state == RSSListPlacementEndDisplaced {
				args = append(args, c.cause)
			}
			if _, err := disposabledb.Owner(t).ExecContext(ctx, exit, args...); err != nil {
				t.Fatal(err)
			}
			view := RSSListView{Groups: []RSSListGroup{{Items: []RSSListItem{{
				SnapshotID: "release:rel-failed",
				Files: []RSSListFile{
					{SourcePath: "file-a.mkv", RenderedTarget: "/library/Failed Show/file-a.mkv"},
					{SourcePath: "file-b.mkv", RenderedTarget: "/library/Failed Show/file-b.mkv"},
				},
			}}}}}
			if err := service.applyLibraryFacts(ctx, "anime-failed", &view); err != nil {
				t.Fatal(err)
			}
			file := view.Groups[0].Items[0].Files[0]
			if file.PlacementEnd == nil || file.PlacementEnd.State != c.state || file.PlacementEnd.Cause != c.cause {
				t.Fatalf("ended placement = %+v", file.PlacementEnd)
			}
			if file.InLibrary != nil {
				t.Fatalf("an ended file is in no library: %+v", file)
			}
			if other := view.Groups[0].Items[0].Files[1]; other.PlacementEnd != nil {
				t.Fatalf("a file this release never placed has no outcome: %+v", other.PlacementEnd)
			}
		})
	}
}
