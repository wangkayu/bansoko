package filemaintenance

import (
	"context"
	"database/sql"
	"testing"

	"bansoko/internal/testutil/disposabledb"
)

// seedMovieCoverageRow seeds one anime with a single placed movie file whose
// frozen coverage is the legitimate movie key and one reconciliation row that
// points at that placement. It returns the placement id, the observed path and
// the target the frozen evidence renders.
func seedMovieCoverageRow(t *testing.T, db *sql.DB) (placementID, filePath, target string) {
	t.Helper()
	const (
		animeID   = "anime-movie-coverage"
		animeName = "Movie Anime"
	)
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "movie-coverage", AnimeID: animeID, AnimeTitle: animeName,
		TMDBMediaKind: "movie", SourceGroup: "Group",
		ReleaseTitle: "[Group] Movie Anime", PlacedPath: "/library/Movie Anime/Movie Anime.mkv",
		DownloadName: "Movie Anime.mkv", Rules: frozenRulesForTest(),
		Coverage: []string{"movie:0"},
	})
	if _, err := db.Exec(`
INSERT INTO reconciliation_files
(managed_anime_id, generation, file_path, batch_index, fingerprint, size_bytes, facts, observed_at,
 disposition, coverage, parser_version, facts_fingerprint, version, created_at, updated_at)
VALUES ($1, 1, $2, 0, 'fp-movie', 1024, jsonb_build_object('placement_id', $3::text), CURRENT_TIMESTAMP,
        'present', '["movie:0"]'::jsonb, 'v1', 'ff-movie', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		animeID, "/library/Movie Anime/Movie Anime.mkv", chain.PlacementID); err != nil {
		t.Fatalf("seed movie reconciliation row: %v", err)
	}
	return chain.PlacementID, "/library/Movie Anime/Movie Anime.mkv", "/library/Movie Anime/Movie Anime.mkv"
}

// TestPathDiffRendersMovieCoverageTarget is the regression for the preview that
// read a legitimate movie coverage key as "no episode": it reported
// coverage_unresolved instead of the movie target the same facts render.
func TestPathDiffRendersMovieCoverageTarget(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	logger := &capturingLogger{}
	placementID, filePath, want := seedMovieCoverageRow(t, db)

	service := NewPathDiffService(db, NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: frozenRulesForTest()}), logger)
	views, err := service.ListPathDiffs(context.Background(), "anime-movie-coverage", 1, 100, 0)
	if err != nil {
		t.Fatalf("list path diffs: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %+v, want the one movie row", views)
	}
	view := views[0]
	if view.TargetUnavailable != "" {
		t.Fatalf("movie coverage must resolve, got target_unavailable=%q", view.TargetUnavailable)
	}
	if view.PlacementID != placementID {
		t.Fatalf("placement id = %q, want %q", view.PlacementID, placementID)
	}
	if view.ExpectedPath == nil || *view.ExpectedPath != want {
		t.Fatalf("expected=%v, want the rendered movie target %q", view.ExpectedPath, want)
	}
	if view.Differs {
		t.Fatalf("the placed movie sits at its target %q, differs must be false", filePath)
	}
	if len(logger.errors) != 0 {
		t.Fatalf("error events=%+v, want none", logger.errors)
	}
}

// TestPathDiffUnresolvedCoverageStaysUnavailable pins the other half of the
// contract: a coverage list that names neither a tv episode nor a movie has no
// target identity and keeps reporting coverage_unresolved.
func TestPathDiffUnresolvedCoverageStaysUnavailable(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedPathDiffRows(t, db, "skip")
	unrecognizedPath := "/library/Diff Anime/Season 01/Extra.mkv"
	if _, err := db.ExecContext(ctx, `
INSERT INTO reconciliation_files
(managed_anime_id, generation, file_path, batch_index, fingerprint, size_bytes, facts, observed_at,
 disposition, coverage, parser_version, facts_fingerprint, version, created_at, updated_at)
VALUES ('anime-diff', 1, $1, 0, 'fp-extra', 1024, '{}'::jsonb, CURRENT_TIMESTAMP,
        'present', '["unknown:0"]'::jsonb, 'v1', 'ff-extra', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		unrecognizedPath); err != nil {
		t.Fatalf("seed unrecognized coverage row: %v", err)
	}

	service := NewPathDiffService(db, NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: frozenRulesForTest()}), &capturingLogger{})
	views, err := service.ListPathDiffs(ctx, "anime-diff", 1, 100, 0)
	if err != nil {
		t.Fatalf("list path diffs: %v", err)
	}
	unresolved := 0
	for _, view := range views {
		switch view.FilePath {
		case unrecognizedPath:
			unresolved++
			if view.TargetUnavailable != "coverage_unresolved" || view.ExpectedPath != nil {
				t.Fatalf("unrecognized coverage row = %+v, want coverage_unresolved without a target", view)
			}
		}
	}
	if unresolved != 1 {
		t.Fatalf("unrecognized coverage rows = %d, want 1 of %+v", unresolved, views)
	}
	// The empty coverage row seeded by seedPathDiffRows keeps the same verdict.
	for _, view := range views {
		if view.Disposition == "unresolved" {
			if view.TargetUnavailable != "coverage_unresolved" || view.ExpectedPath != nil {
				t.Fatalf("empty coverage row = %+v, want coverage_unresolved without a target", view)
			}
		}
	}
}

// TestRenderPlacementTargetsRendersMovieCoverage covers the files dialog's
// render: a movie placement renders its movie target instead of being skipped
// as an episode-less placement, while a placement whose coverage names neither
// a tv episode nor a movie stays absent.
func TestRenderPlacementTargetsRendersMovieCoverage(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	moviePlacement, _, movieTarget := seedMovieCoverageRow(t, db)
	empty := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "movie-no-coverage", AnimeID: "anime-movie-no-coverage", AnimeTitle: "No Coverage",
		TMDBMediaKind: "movie", SourceGroup: "Group",
		ReleaseTitle: "[Group] No Coverage", PlacedPath: "/library/No Coverage/No Coverage.mkv",
		DownloadName: "No Coverage.mkv", Rules: frozenRulesForTest(),
	})
	unrecognized := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "movie-unknown-coverage", AnimeID: "anime-movie-unknown-coverage", AnimeTitle: "Unknown Coverage",
		TMDBMediaKind: "movie", SourceGroup: "Group",
		ReleaseTitle: "[Group] Unknown Coverage", PlacedPath: "/library/Unknown Coverage/Unknown Coverage.mkv",
		DownloadName: "Unknown Coverage.mkv", Rules: frozenRulesForTest(),
		Coverage: []string{"unknown:0"},
	})

	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: frozenRulesForTest()})
	targets, err := renderer.RenderPlacementTargets(context.Background(), "anime-movie-coverage", []string{moviePlacement})
	if err != nil {
		t.Fatalf("render movie placement targets: %v", err)
	}
	if got, ok := targets[moviePlacement]; !ok || got != movieTarget {
		t.Fatalf("movie placement target = %q (present=%t), want %q", got, ok, movieTarget)
	}

	for _, tc := range []struct{ animeID, placementID string }{
		{"anime-movie-no-coverage", empty.PlacementID},
		{"anime-movie-unknown-coverage", unrecognized.PlacementID},
	} {
		targets, err := renderer.RenderPlacementTargets(context.Background(), tc.animeID, []string{tc.placementID})
		if err != nil {
			t.Fatalf("render %s targets: %v", tc.animeID, err)
		}
		if _, ok := targets[tc.placementID]; ok {
			t.Fatalf("%s rendered a target for a placement without a coverage identity: %+v", tc.animeID, targets)
		}
	}
}

// TestRenderCalibratedTargetKeepsParsedEpisode pins the render contract the
// movie pass-through relies on: a movie coverage renders the movie branch with
// the empty episode display, and an empty display never clears the parsed
// episode the frozen identity carries.
func TestRenderCalibratedTargetKeepsParsedEpisode(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	moviePlacement, moviePath, movieTarget := seedMovieCoverageRow(t, db)
	const tvPath = "/library/Calibrated Anime/Calibrated Anime - 03.mkv"
	tv := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "calibrated-tv", AnimeID: "anime-calibrated-tv", AnimeTitle: "Calibrated Anime",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] Calibrated Anime - 03", PlacedPath: tvPath,
		DownloadName: "Calibrated Anime - 03.mkv", Rules: frozenRulesForTest(), FrozenEpisode: "3",
		Coverage: []string{"tv:e3"},
	})
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: frozenRulesForTest()})

	movie, err := renderer.RenderCalibratedTarget(ctx, "anime-movie-coverage", moviePath, 1024, CalibratedRenderFacts{PlacementID: moviePlacement})
	if err != nil {
		t.Fatalf("render movie calibrated target: %v", err)
	}
	if movie != movieTarget {
		t.Fatalf("movie calibrated target = %q, want %q", movie, movieTarget)
	}

	// The frozen tv episode must survive an empty coverage display: the render
	// is the same one the coverage episode produces.
	frozenEpisode, err := renderer.RenderCalibratedTarget(ctx, "anime-calibrated-tv", tvPath, 1024, CalibratedRenderFacts{
		EpisodeDisplay: "3", PlacementID: tv.PlacementID,
	})
	if err != nil {
		t.Fatalf("render tv calibrated target: %v", err)
	}
	withoutDisplay, err := renderer.RenderCalibratedTarget(ctx, "anime-calibrated-tv", tvPath, 1024, CalibratedRenderFacts{
		PlacementID: tv.PlacementID,
	})
	if err != nil {
		t.Fatalf("render tv calibrated target without a display: %v", err)
	}
	if withoutDisplay != frozenEpisode {
		t.Fatalf("empty display render = %q, want the frozen episode render %q", withoutDisplay, frozenEpisode)
	}
}
