package filemaintenance

import (
	"context"
	"testing"

	"bansoko/internal/testutil/disposabledb"
)

// TestPlannerRealSnapshotChain drives the full dry-run chain with the real
// reconciliation snapshot reader and naming target renderer: completed scan
// generation -> observations -> coverage -> target rendering. It proves the
// production wiring shape (cmd/bansoko-server) works end to end without panics.
func TestPlannerRealSnapshotChain(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()

	seedAnimeForRenderer(t, db, "anime-plan-tv", "规划番组", "tv")
	seedScanOwner(t, db, "anime-plan-tv", "completed", 1)
	seedObservationCoverage(t, db, "anime-plan-tv", 1, 0, "/library/Anime/S01E01.mkv", "fp-1", 100, "tv:s1:e1")
	seedObservationCoverage(t, db, "anime-plan-tv", 1, 1, "/library/Anime/S01E02.mkv", "fp-2", 200, "tv:s1:e2")
	// Each observed file descends from its own completed file operation, so the
	// planner renders it from the frozen evidence of that placement.
	first := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "plan-e1", AnimeID: "anime-plan-tv", AnimeTitle: "规划番组",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] 规划番组 - 01", PlacedPath: "/library/Anime/S01E01.mkv",
		DownloadName: "S01E01.mkv", Rules: rendererRules(), FrozenEpisode: "1",
		Coverage: []string{"tv:s1:e1"},
	})
	second := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "plan-e2", AnimeID: "anime-plan-tv", AnimeTitle: "规划番组",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] 规划番组 - 02", PlacedPath: "/library/Anime/S01E02.mkv",
		DownloadName: "S01E02.mkv", Rules: rendererRules(), FrozenEpisode: "2",
		Coverage: []string{"tv:s1:e2"},
	})
	for batch, placementID := range map[int64]string{0: first.PlacementID, 1: second.PlacementID} {
		if _, err := db.Exec(`UPDATE reconciliation_files SET facts = facts || jsonb_build_object('placement_id', $1::text) WHERE managed_anime_id = 'anime-plan-tv' AND generation = 1 AND batch_index = $2`, placementID, batch); err != nil {
			t.Fatal(err)
		}
	}

	reader := NewReconciliationSnapshotReader(db)
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rendererRules()})
	plan, err := NewPlanner(reader, renderer).Plan(ctx, "anime-plan-tv", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AnimeID != "anime-plan-tv" || plan.Generation != 1 {
		t.Fatalf("unexpected plan header: %+v", plan)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(plan.Candidates))
	}
	seen := map[string]bool{}
	for _, candidate := range plan.Candidates {
		if candidate.Excluded {
			t.Fatalf("candidate %s must not be excluded: %s", candidate.SourcePath, candidate.ExcludeReason)
		}
		if len(candidate.Coverage) != 1 {
			t.Fatalf("candidate %s coverage = %v", candidate.SourcePath, candidate.Coverage)
		}
		seen[candidate.Coverage[0]] = true
		if candidate.TargetPath == "" {
			t.Fatalf("candidate %s must render a target path", candidate.SourcePath)
		}
	}
	if !seen["tv:s1:e1"] || !seen["tv:s1:e2"] {
		t.Fatalf("coverage keys = %v, want tv:s1:e1 + tv:s1:e2", seen)
	}
}

// TestPlannerRealSnapshotChainBlocksUnresolvedAndUnusable ensures unresolved
// coverage and non-completed generations fail closed instead of guessing.
func TestPlannerRealSnapshotChainBlocksUnresolvedAndUnusable(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()

	reader := NewReconciliationSnapshotReader(db)
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rendererRules()})
	planner := NewPlanner(reader, renderer)

	// Running generation is not consumable.
	seedAnimeForRenderer(t, db, "anime-plan-run", "运行中", "")
	seedScanOwner(t, db, "anime-plan-run", "running", 1)
	if _, err := planner.Plan(ctx, "anime-plan-run", 1, 1); err == nil {
		t.Fatal("non-completed generation must block replan")
	}

	// Unresolvable coverage stays excluded with a typed reason.
	seedAnimeForRenderer(t, db, "anime-plan-unk", "无法解析", "")
	seedScanOwner(t, db, "anime-plan-unk", "completed", 1)
	seedObservation(t, db, "anime-plan-unk", 1, 0, "/library/Anime/NoEpisode.mkv", "fp-u", 100)
	plan, err := planner.Plan(ctx, "anime-plan-unk", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || !plan.Candidates[0].Excluded || plan.Candidates[0].ExcludeReasonCode != "coverage_unresolved" {
		t.Fatalf("unresolved coverage must be excluded: %+v", plan.Candidates)
	}
}
