package application

import (
	"context"
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

// The library overlap is a read-only fact about the library, never a plan: it
// reads the placements the library already holds, creates nothing, and keeps
// "cannot decide" strictly apart from "not held".

// libraryOverlapView is a composed view of the fixture seed, with the two file
// rows the exact manifest renders.
func libraryOverlapView(t *testing.T) (RSSListView, *Service) {
	t.Helper()
	draft := previewBoundDraft()
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: manifestPreviewSnapshot(t, "12")}}
	seedPreviewBusiness(t, service, &draft)
	setDraftOwnerYear(&draft)
	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("groups/items = %+v, want one group with one seed", view.Groups)
	}
	if len(view.Groups[0].Items[0].Files) != 2 {
		t.Fatalf("files = %+v, want the manifest's two rendered rows", view.Groups[0].Items[0].Files)
	}
	return view, service
}

// placeLibraryPath records one placement of the library, which is the only
// thing that makes a path "held". A released placement is born active and then
// released: the placement trigger refuses an insert that starts terminal, so
// the fixture takes the same transition the owner path takes.
func placeLibraryPath(t *testing.T, service *Service, id, path, verification, state string) {
	t.Helper()
	insertState := state
	if state == "released" {
		insertState = "active"
	}
	if _, err := service.db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, verification_state, state, version, created_at, updated_at)
VALUES ($1, $2, 'anime-library', 'reconciliation', $3, $4, $4, 'parser-v1', $5, $6, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		id, path, "obs-"+id, "sha256:"+path, verification, insertState); err != nil {
		t.Fatal(err)
	}
	if state == "released" {
		if _, err := service.db.Exec(`
UPDATE placements SET state = 'released', released_at = CURRENT_TIMESTAMP,
 released_by = 'owner', release_request_key = $2, version = version + 1,
 updated_at = CURRENT_TIMESTAMP
WHERE id = $1`, id, "req-"+id); err != nil {
			t.Fatal(err)
		}
	}
}

func libraryOverlapCounts(t *testing.T, overlap LibraryOverlap) (int, int) {
	t.Helper()
	if overlap.PresentFiles == nil || overlap.PendingFiles == nil {
		t.Fatalf("overlap %+v = %q without counts, want both counts for a decidable verdict", overlap, overlap.Verdict)
	}
	return *overlap.PresentFiles, *overlap.PendingFiles
}

// refreshLibraryOverlap recomputes the overlap over the same composed view.
// Composing again would rebuild the disposable schema (the business fixture
// opens it), wiping the library state this test is building up; the overlap is
// the only thing that has to move between placements.
func refreshLibraryOverlap(t *testing.T, service *Service, view *RSSListView) {
	t.Helper()
	if err := service.applyLibraryOverlap(context.Background(), view); err != nil {
		t.Fatalf("apply overlap: %v", err)
	}
}

// A seed whose write set is not yet held is absent with every file counted as
// pending; a held target turns the seed present and moves exactly that file out
// of the pending count.
func TestRSSListViewLibraryOverlapFollowsTheHeldPlacements(t *testing.T) {
	view, service := libraryOverlapView(t)
	group := view.Groups[0]
	item := group.Items[0]
	videoPath := group.Items[0].Files[0].RenderedTarget
	subtitlePath := group.Items[0].Files[1].RenderedTarget

	// An empty library is "absent", not "undetermined": every write-set file has
	// a decidable target and none of them is held.
	if item.LibraryOverlap.Verdict != RSSListLibraryOverlapAbsent {
		t.Fatalf("verdict = %q with an empty library, want absent", item.LibraryOverlap.Verdict)
	}
	present, pending := libraryOverlapCounts(t, item.LibraryOverlap)
	if present != 0 || pending != 2 {
		t.Fatalf("counts = %d/%d, want 0/2", present, pending)
	}
	if item.LibraryOverlap.ReasonCode != nil {
		t.Fatalf("reason = %q, want none for a decidable verdict", *item.LibraryOverlap.ReasonCode)
	}
	for index, file := range item.Files {
		if file.InLibrary == nil || *file.InLibrary {
			t.Fatalf("file %d in_library = %v, want an explicit false", index, file.InLibrary)
		}
	}

	// A released placement is not what the library holds now, and an unverified
	// one is not verified truth: neither makes a file held.
	placeLibraryPath(t, service, "plc-released-video", videoPath, "verified", "released")
	placeLibraryPath(t, service, "plc-unverified-subtitle", subtitlePath, "unverified", "active")
	refreshLibraryOverlap(t, service, &view)
	if got := view.Groups[0].Items[0].LibraryOverlap.Verdict; got != RSSListLibraryOverlapAbsent {
		t.Fatalf("verdict = %q with a released and an unverified placement, want absent", got)
	}

	// One verified active placement is a held file: the verdict flips to present
	// and only that file leaves the pending count.
	placeLibraryPath(t, service, "plc-held-video", videoPath, "verified", "active")
	refreshLibraryOverlap(t, service, &view)
	item = view.Groups[0].Items[0]
	if item.LibraryOverlap.Verdict != RSSListLibraryOverlapPresent {
		t.Fatalf("verdict = %q with a held video target, want present", item.LibraryOverlap.Verdict)
	}
	if present, pending = libraryOverlapCounts(t, item.LibraryOverlap); present != 1 || pending != 1 {
		t.Fatalf("counts = %d/%d, want 1/1", present, pending)
	}
	if item.Files[0].InLibrary == nil || !*item.Files[0].InLibrary {
		t.Fatalf("held video in_library = %v, want true", item.Files[0].InLibrary)
	}
	if item.Files[1].InLibrary == nil || *item.Files[1].InLibrary {
		t.Fatalf("unheld subtitle in_library = %v, want an explicit false", item.Files[1].InLibrary)
	}

	// The whole write set held is still one present verdict with nothing
	// pending: the library owes no write.
	placeLibraryPath(t, service, "plc-held-subtitle", subtitlePath, "verified", "active")
	refreshLibraryOverlap(t, service, &view)
	item = view.Groups[0].Items[0]
	if present, pending = libraryOverlapCounts(t, item.LibraryOverlap); present != 2 || pending != 0 {
		t.Fatalf("counts = %d/%d with every target held, want 2/0", present, pending)
	}
}

// The overlap never writes: reading it neither creates a placement nor a
// WorkItem, so a preview cannot plant library truth.
func TestRSSListViewLibraryOverlapWritesNothing(t *testing.T) {
	_, service := libraryOverlapView(t)
	count := func(table string) int {
		t.Helper()
		var rows int
		if err := service.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	placements, workItems := count("placements"), count("work_items")
	if _, err := service.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
		InputSource: "current_input", Draft: func() *animedomain.AnimeDraft { draft := previewBoundDraft(); return &draft }(),
	}); err == nil {
		t.Fatal("expected the draft read without a business snapshot to fail closed")
	}
	if after := count("placements"); after != placements {
		t.Fatalf("placements = %d -> %d, the overlap read must not place anything", placements, after)
	}
	if after := count("work_items"); after != workItems {
		t.Fatalf("work items = %d -> %d, the overlap read must not enqueue anything", workItems, after)
	}
}

// An unavailable torrent has no structure, so the seed's write set is unknown:
// the verdict is undetermined with no counts, never "absent" and never 0/0.
func TestRSSListViewLibraryOverlapIsUndeterminedWithoutAStructure(t *testing.T) {
	snapshot := manifestPreviewSnapshot(t, "12")
	snapshot.Seeds[0].Manifest = nil
	snapshot.Seeds[0].TorrentState = "unavailable"
	snapshot.Seeds[0].TorrentErrorCode = "torrent_metadata_unavailable"
	snapshot.Seeds[0].TorrentErrorReason = "the torrent payload could not be parsed"
	draft := previewBoundDraft()
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: snapshot}}
	seedPreviewBusiness(t, service, &draft)
	setDraftOwnerYear(&draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	item := view.Groups[0].Items[0]
	if len(item.Files) != 0 {
		t.Fatalf("files = %+v, want none without an acquired structure", item.Files)
	}
	if item.LibraryOverlap.Verdict != RSSListLibraryOverlapUndetermined {
		t.Fatalf("verdict = %q, want undetermined rather than %q", item.LibraryOverlap.Verdict, RSSListLibraryOverlapAbsent)
	}
	if item.LibraryOverlap.PresentFiles != nil || item.LibraryOverlap.PendingFiles != nil {
		t.Fatalf("counts = %v/%v, want both null for an undecided overlap", item.LibraryOverlap.PresentFiles, item.LibraryOverlap.PendingFiles)
	}
	if item.LibraryOverlap.ReasonCode == nil || *item.LibraryOverlap.ReasonCode != RSSListLibraryOverlapReasonNoTarget {
		t.Fatalf("reason = %v, want %q", item.LibraryOverlap.ReasonCode, RSSListLibraryOverlapReasonNoTarget)
	}
}

// A read model with no library to read answers undetermined, not absent, and
// leaves every file fact null rather than false. This is the pure unit-level
// pin of the rule the PostgreSQL cases above exercise end to end.
func TestRSSListViewLibraryOverlapWithoutALibraryStaysUndetermined(t *testing.T) {
	view := RSSListView{Groups: []RSSListGroup{{Items: []RSSListItem{{
		Evaluation: RSSListEvaluation{State: RSSListEvaluationEvaluated},
		Files: []RSSListFile{
			{Included: true, Safety: "valid", RenderedTarget: "/library/Show - 01.mkv"},
			{Included: false, Safety: "valid", RenderedTarget: "/library/Show - 01.zh.ass"},
		},
	}}}}}
	if err := (&Service{}).applyLibraryOverlap(context.Background(), &view); err != nil {
		t.Fatalf("overlap: %v", err)
	}
	item := view.Groups[0].Items[0]
	if item.LibraryOverlap.Verdict != RSSListLibraryOverlapUndetermined {
		t.Fatalf("verdict = %q without a library, want undetermined", item.LibraryOverlap.Verdict)
	}
	if item.LibraryOverlap.PresentFiles != nil || item.LibraryOverlap.PendingFiles != nil {
		t.Fatalf("counts = %v/%v, want both null", item.LibraryOverlap.PresentFiles, item.LibraryOverlap.PendingFiles)
	}
	if item.LibraryOverlap.ReasonCode == nil || *item.LibraryOverlap.ReasonCode != RSSListLibraryOverlapReasonUnavailable {
		t.Fatalf("reason = %v, want %q", item.LibraryOverlap.ReasonCode, RSSListLibraryOverlapReasonUnavailable)
	}
	// Every file fact is withheld, including the excluded file's: an undecidable
	// seed must not report any file as "not held".
	for index, file := range item.Files {
		if file.InLibrary != nil {
			t.Fatalf("file %d in_library = %v, want null", index, *file.InLibrary)
		}
	}

	// The same write set against a reading library is decidable, so the
	// undetermined verdict above is the missing library, not the fixture.
	if item.LibraryOverlap.ReasonCode == nil || *item.LibraryOverlap.ReasonCode == RSSListLibraryOverlapReasonNoTarget {
		t.Fatal("a rendered target must not be reported as a missing target")
	}
}
