package filemaintenance

import (
	"bansoko/internal/testutil/disposabledb"
	"context"
	"testing"
)

func TestAlignmentAndPreviewUseMigratedPlacementCoverage(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "offset-alignment", AnimeID: "anime-offset-alignment", AnimeTitle: "Show",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] Show - 01", PlacedPath: "/library/Drift/03.mkv",
		DownloadName: "01.mkv", Rules: rendererRules(), FrozenEpisode: "1",
	})
	freezeRenderFixtureBusiness(t, db, chain.OperationID)
	// Admission identity remains 1; a completed offset migration owns new coverage.
	if _, err := db.Exec(`UPDATE placement_coverage SET observation_key='tv:s1:e3' WHERE placement_id=$1`, chain.PlacementID); err != nil {
		t.Fatal(err)
	}
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rendererRules()})
	observation := SnapshotObservation{AnimeID: "anime-offset-alignment", PlacementID: chain.PlacementID, FilePath: "/library/Drift/03.mkv", SizeBytes: 1024, Coverage: []string{"tv:s1:e3"}}
	planned, err := renderer.RenderTarget(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := renderer.RenderCalibratedTarget(context.Background(), observation.AnimeID, observation.FilePath, 1024, CalibratedRenderFacts{PlacementID: chain.PlacementID, EpisodeDisplay: "3"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "/library/Show/Season 01/Show - E03.mkv"
	if planned != want || preview != want {
		t.Fatalf("planning=%s preview=%s want literal %s", planned, preview, want)
	}
}
