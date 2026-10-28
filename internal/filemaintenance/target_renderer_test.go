package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

type fakeNamingRulesSource struct {
	rules naming.NamingRules
	err   error
}

func (f fakeNamingRulesSource) EffectiveNamingRules(context.Context, string) (naming.NamingRules, error) {
	return f.rules, f.err
}

func seedAnimeForRenderer(t *testing.T, db *sql.DB, animeID, title string, tmdbMediaKind string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO managed_anime(id, title) VALUES ($1, $2)
ON CONFLICT (id) DO NOTHING`, animeID, title); err != nil {
		t.Fatal(err)
	}
	if tmdbMediaKind == "" {
		return
	}
	if _, err := db.Exec(`
INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season, version)
VALUES ($1, 'work-' || $1, $2, 1, 1)
ON CONFLICT (managed_anime_id) DO UPDATE SET tmdb_media_kind = EXCLUDED.tmdb_media_kind, tmdb_season = EXCLUDED.tmdb_season`,
		animeID, tmdbMediaKind); err != nil {
		t.Fatal(err)
	}
}

func rendererRules() naming.NamingRules {
	return naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "/library/${sanitize(anime.title)}/Season ${pad(tmdb.season, 2)}",
			VideoFilenameTemplate: "${sanitize(anime.title)} - E${pad(ep.display, 2)}.${file.ext}",
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     "/library/${sanitize(anime.title)}",
			VideoFilenameTemplate: "${sanitize(anime.title)}.${file.ext}",
		},
	}
}

func TestNamingTargetRendererTvBranch(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "render-tv", AnimeID: "anime-render-tv", AnimeTitle: "测试番组",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] 测试番组 - 03", PlacedPath: "/library/Anime/EP03.mkv",
		DownloadName: "EP03.mkv", Rules: rendererRules(), FrozenEpisode: "3",
	})
	freezeRenderFixtureBusiness(t, db, chain.OperationID)
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rendererRules()})

	target, err := renderer.RenderTarget(context.Background(), SnapshotObservation{
		AnimeID: "anime-render-tv", PlacementID: chain.PlacementID,
		FilePath: "/library/Anime/EP03.mkv", SizeBytes: 1024,
		Coverage: []string{"tv:e3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// TV branch: absolute title/season directory + frozen episode filename.
	// The naming template itself renders the deployment path; there is no
	// library-root anchor behind the renderer.
	want := "/library/测试番组/Season 01/测试番组 - E03.mkv"
	if target != want {
		t.Fatalf("tv target = %q, want %q", target, want)
	}
}

func TestNamingTargetRendererMovieBranch(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "render-movie", AnimeID: "anime-render-movie", AnimeTitle: "剧场版标题",
		TMDBMediaKind: "movie", SourceGroup: "Group",
		ReleaseTitle: "[Group] 剧场版标题", PlacedPath: "/library/Movie/MovieTitle.mkv",
		DownloadName: "MovieTitle.mkv", Rules: rendererRules(),
	})
	freezeRenderFixtureBusiness(t, db, chain.OperationID)
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rendererRules()})

	target, err := renderer.RenderTarget(context.Background(), SnapshotObservation{
		AnimeID: "anime-render-movie", PlacementID: chain.PlacementID,
		FilePath: "/library/Movie/MovieTitle.mkv", SizeBytes: 1024,
		Coverage: []string{"movie:0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if target != "/library/剧场版标题/剧场版标题.mkv" {
		t.Fatalf("movie target = %q, want movie branch without season", target)
	}
}

func TestNamingTargetRendererTitleOverrideAndErrors(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "render-override", AnimeID: "anime-render-override", AnimeTitle: "原始标题",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] 原始标题 - 01", PlacedPath: "/library/Anime/EP01.mkv",
		DownloadName: "EP01.mkv", Rules: rendererRules(), FrozenEpisode: "1",
	})
	// Title override wins over the base title.
	if _, err := db.Exec(`UPDATE managed_anime SET title_override = '覆盖标题' WHERE id = 'anime-render-override'`); err != nil {
		t.Fatal(err)
	}
	freezeRenderFixtureBusiness(t, db, chain.OperationID)
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rendererRules()})
	target, err := renderer.RenderTarget(context.Background(), SnapshotObservation{
		AnimeID: "anime-render-override", PlacementID: chain.PlacementID,
		FilePath: "/library/Anime/EP01.mkv", SizeBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(target, "/library/覆盖标题/") {
		t.Fatalf("title override must win: %q", target)
	}

	// Unknown anime/placement is a typed missing-facts outcome, not a panic or
	// an empty path.
	if _, err := renderer.RenderTarget(context.Background(), SnapshotObservation{
		AnimeID: "anime-missing", FilePath: "/library/Anime/EP01.mkv",
	}); !errors.Is(err, naming.ErrRenderFactsUnavailable) {
		t.Fatalf("unknown placement error = %v, want ErrRenderFactsUnavailable", err)
	}
}

// TestNamingTargetRendererRejectsPlacementWithoutLineage pins the fail-closed
// contract: a placement that does not descend from a completed file operation
// carries no frozen acquisition evidence, so the render is blocked instead of
// re-deriving facts from the current configuration or the file name.
func TestNamingTargetRendererRejectsPlacementWithoutLineage(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	seedAnimeForRenderer(t, db, "anime-render-nolineage", "无来源", "tv")
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, version)
VALUES ('plc-nolineage', '/library/Anime/EP01.mkv', 'anime-render-nolineage', 'reconciliation', 'plc-nolineage',
 '{}'::jsonb, '{}'::jsonb, NULL, 'facts-v1', 'verified', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rendererRules()})
	if _, err := renderer.RenderTarget(context.Background(), SnapshotObservation{
		AnimeID: "anime-render-nolineage", PlacementID: "plc-nolineage",
		FilePath: "/library/Anime/EP01.mkv", SizeBytes: 1024,
	}); !errors.Is(err, naming.ErrRenderFactsUnavailable) {
		t.Fatalf("lineage-less placement error = %v, want ErrRenderFactsUnavailable", err)
	}
}
