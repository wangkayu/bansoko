package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// TestRSSListViewReadIsPureAndReportsTheHold covers the read model invariants
// the retired flat preview pinned: an owner read writes no product row, and an
// incomplete metadata binding is reported as a hold instead of blocking the
// read. The flat preview's per-source identity DTO, its live custom-source
// fetch and its render/offset assertions are gone with the route; the evaluator
// half of those assertions survives in the domain package
// (TestPreviewRenderErrorCarriesMessageAtPathPosition there).
func TestRSSListViewReadIsPureAndReportsTheHold(t *testing.T) {
	db := animetest.OpenTestDB(t)
	readmodel, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	animetest.SeedQbConnection(t, db)

	draft := readmodelProviderDraftWithGroups(t, generated.SubscriptionOriginMikan, "70021", "678")
	draft.Metadata.Bgm = nil
	draft.Metadata.Tmdb = nil
	candidate := saveCandidate(t, root, draft)
	if candidate.CurrentRevision == nil || *candidate.CurrentRevision != 1 {
		t.Fatalf("saved candidate revision = %v, want 1", candidate.CurrentRevision)
	}

	before := animetest.ProductRowCounts(t, db)
	view, err := readmodel.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
		InputSource: "candidate_revision",
		CandidateID: string(candidate.Id),
		Revision:    1,
	})
	if err != nil {
		t.Fatalf("read RSS list view: %v", err)
	}
	after := animetest.ProductRowCounts(t, db)
	for table, count := range before {
		if after[table] != count {
			t.Fatalf("the read wrote to %s: before=%d after=%d", table, count, after[table])
		}
	}
	if !view.MetadataHold.Held {
		t.Fatalf("an unbound revision must report a metadata hold: %+v", view.MetadataHold)
	}
	if len(view.MetadataHold.Missing) != 2 {
		t.Fatalf("hold missing = %+v, want bgm and tmdb", view.MetadataHold.Missing)
	}
	// The selected group has no persisted snapshot identity here, so the read
	// reports what is missing instead of fetching or fabricating a row.
	if len(view.Groups) != 1 || view.Groups[0].Feed.State != RSSListFeedSnapshotRequired {
		t.Fatalf("group feed = %+v, want snapshot_required", view.Groups)
	}
	if len(view.Groups[0].Items) != 0 {
		t.Fatalf("a snapshot_required group must not fabricate items: %+v", view.Groups[0].Items)
	}
	if view.ConfigIdentity == "" || view.Versions.Parser == "" || view.Versions.Evaluator == "" || view.Versions.Renderer == "" {
		t.Fatalf("read must report its configuration identity and versions: %+v / %+v", view.ConfigIdentity, view.Versions)
	}
}
