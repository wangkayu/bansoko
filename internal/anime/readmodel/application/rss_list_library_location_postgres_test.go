package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

type fakePlacementTargets map[string]string

func (f fakePlacementTargets) RenderPlacementTargets(_ context.Context, _ string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if target, ok := f[id]; ok {
			out[id] = target
		}
	}
	return out, nil
}

// An imported file of an owner release shows where its placement is now,
// relative to the library, and the current-rules path when that differs; a
// file without a placement keeps only its rendered target.
func TestRSSListOwnerRowShowsLibraryLocation(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	seedFailedRecordRoot(t, db, "record-located")
	// The fixture's file operation fo-failed placed file-a.mkv (frozen target
	// /library/Failed Show/file-a.mkv); the file now sits elsewhere.
	if _, err := disposabledb.Owner(t).ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, verification_state, state)
VALUES ('plc-located', '/library/Failed Show 2021/Season 01/file-a.mkv', 'anime-failed', 'file_operation', 'fo-failed',
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

	if err := service.applyLibraryLocations(ctx, "anime-failed", &view); err != nil {
		t.Fatal(err)
	}
	files := view.Groups[0].Items[0].Files
	if files[0].LibraryPath == nil || *files[0].LibraryPath != "Failed Show 2021/Season 01/file-a.mkv" || files[0].LibraryNewPath != nil {
		t.Fatalf("located file = %+v", files[0])
	}
	if files[1].LibraryPath != nil || files[1].LibraryNewPath != nil {
		t.Fatalf("a file not in the library keeps its target only: %+v", files[1])
	}

	service.placementTargets = fakePlacementTargets{"plc-located": "/library/Failed Show （2021）/Season 01/Failed Show - S01E01.mkv"}
	view.Groups[0].Items[0].Files[0].LibraryPath = nil
	if err := service.applyLibraryLocations(ctx, "anime-failed", &view); err != nil {
		t.Fatal(err)
	}
	located := view.Groups[0].Items[0].Files[0]
	if located.LibraryPath == nil || *located.LibraryPath != "Failed Show 2021/Season 01/file-a.mkv" ||
		located.LibraryNewPath == nil || *located.LibraryNewPath != "Failed Show （2021）/Season 01/Failed Show - S01E01.mkv" {
		t.Fatalf("pending alignment = %+v", located)
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
	if err := service.applyLibraryLocations(ctx, "anime-failed", &view); err != nil {
		t.Fatal(err)
	}
	item = view.Groups[0].Items[0]
	moved, other := item.Files[0], item.Files[1]
	if moved.LibraryPath == nil || moved.InLibrary == nil || !*moved.InLibrary {
		t.Fatalf("a moved placement reads in the library: %+v", moved)
	}
	if other.InLibrary == nil || *other.InLibrary || other.LibraryPath != nil {
		t.Fatalf("a file with no placement stays not held: %+v", other)
	}
	overlap := item.LibraryOverlap
	if overlap.Verdict != RSSListLibraryOverlapPresent || overlap.PresentFiles == nil || *overlap.PresentFiles != 1 ||
		overlap.PendingFiles == nil || *overlap.PendingFiles != 1 {
		t.Fatalf("overlap counts follow the held file: %+v", overlap)
	}
}
