package filemaintenance

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

// matchRules names the deployment mount explicitly: a rendered target is
// always absolute, so a relative directory template stays fail-closed and the
// expectations below carry the whole path.
func matchRules(filename string) naming.NamingRules {
	return naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "/library/${sanitize(anime.title)}/Season ${pad(tmdb.season, 2)}",
			VideoFilenameTemplate: filename,
		},
		Movie: naming.NamingVariant{DirectoryTemplate: "/library/${sanitize(anime.title)}", VideoFilenameTemplate: "${sanitize(anime.title)}.${file.ext}"},
	}
}

// The match render over real rows: the chosen season and episode are final
// (no offset, ep.source manual), release-level facts come from the file's own
// lineage and are absent without one.
func TestRenderMatchOverRealRows(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "match", AnimeID: "anime-match", AnimeTitle: "测试番组", TMDBMediaKind: "tv", TMDBSeason: 1,
		SourceGroup: "Group", ReleaseTitle: "[Group] 测试番组 - 03 special", PlacedPath: "/library/Anime/EP03.mkv",
		DownloadName: "EP03.mkv", Rules: rendererRules(), FrozenEpisode: "3",
	})
	freezeRenderFixtureBusiness(t, db, chain.OperationID)
	seedAnimeForRenderer(t, db, "anime-nolineage", "无出处番", "tv")
	seedAnimeForRenderer(t, db, "anime-movie", "剧场番", "movie")

	render := func(rules naming.NamingRules, in MatchRenderInput) (MatchRender, error) {
		return NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules}).RenderMatch(ctx, in)
	}
	input := MatchRenderInput{ManagedAnimeID: "anime-match", LineagePlacementID: chain.PlacementID, FileName: "EP03.mkv", SizeBytes: 1024, Season: 2, Episode: 7}
	grouped := matchRules("${sanitize(anime.title)} - E${pad(ep.display, 2)} [${ep.source}] ${anime.group}.${file.ext}")

	// With lineage: the season is the chosen one, not the bound one; the
	// episode is the chosen one, not the frozen one; the group is the file's.
	got, err := render(grouped, input)
	if err != nil || got.Path != "/library/测试番组/Season 02/测试番组 - E07 [manual] Group.mkv" || !got.Lineage {
		t.Fatalf("with lineage = %+v, %v", got, err)
	}
	// Season 0 is the specials folder.
	specials := input
	specials.Season = 0
	if got, err = render(grouped, specials); err != nil || got.Path != "/library/测试番组/Season 00/测试番组 - E07 [manual] Group.mkv" {
		t.Fatalf("season 0 = %+v, %v", got, err)
	}

	// Without lineage only system facts exist: the display group is a system
	// fact and reads empty, while lineage-only release facts stay missing,
	// named.
	got, err = render(grouped, MatchRenderInput{ManagedAnimeID: "anime-nolineage", FileName: "EP03.mkv", Season: 1, Episode: 3})
	if err != nil || got.Path != "/library/无出处番/Season 01/无出处番 - E03 [manual].mkv" || got.Lineage {
		t.Fatalf("without lineage group reads empty = %+v, %v", got, err)
	}
	release := matchRules("${sanitize(anime.title)} - E${pad(ep.display, 2)} ${release.title}.${file.ext}")
	_, err = render(release, MatchRenderInput{ManagedAnimeID: "anime-nolineage", FileName: "EP03.mkv", Season: 1, Episode: 3})
	var failure *MatchRenderError
	if !errors.As(err, &failure) || failure.Code != MatchRenderFactsMissing || len(failure.Facts) != 1 || failure.Facts[0] != "release.title" {
		t.Fatalf("without lineage = %v", err)
	}
	plain := matchRules("${sanitize(anime.title)} - E${pad(ep.display, 2)}.${file.ext}")
	if got, err = render(plain, MatchRenderInput{ManagedAnimeID: "anime-nolineage", FileName: "EP03.mkv", Season: 1, Episode: 3}); err != nil || got.Path != "/library/无出处番/Season 01/无出处番 - E03.mkv" || got.Lineage {
		t.Fatalf("system facts only = %+v, %v", got, err)
	}

	// The Vars output frozen on the file's acquisition reaches the template;
	// without one the value does not exist.
	vars := matchRules("${vars.actual.size}.${file.ext}")
	if got, err = render(vars, input); err != nil || got.Path != "/library/测试番组/Season 02/1024.mkv" {
		t.Fatalf("frozen vars = %+v, %v", got, err)
	}
	if _, err = render(vars, MatchRenderInput{ManagedAnimeID: "anime-nolineage", FileName: "EP03.mkv", Season: 1, Episode: 3}); !errors.As(err, &failure) || failure.Code != MatchRenderFactsMissing {
		t.Fatalf("vars without lineage = %v", err)
	}

	// A TMDB work bound as a movie names no season of a show.
	tmdb := matchRules("${sanitize(anime.title)} - ${tmdb.id}.${file.ext}")
	if _, err = render(tmdb, MatchRenderInput{ManagedAnimeID: "anime-movie", FileName: "EP03.mkv", Season: 1, Episode: 3}); !errors.As(err, &failure) || failure.Code != MatchRenderFactsMissing {
		t.Fatalf("a movie binding = %v", err)
	}

	// Refusals before any render.
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: grouped})
	for _, bad := range []MatchRenderInput{
		{FileName: "a.mkv", Season: 1, Episode: 1},
		{ManagedAnimeID: "anime-match", Season: 1, Episode: 1},
		{ManagedAnimeID: "anime-match", FileName: "a.mkv", Season: -1, Episode: 1},
		{ManagedAnimeID: "anime-match", FileName: "a.mkv", Season: 1, Episode: 0},
	} {
		if _, err := renderer.RenderMatch(ctx, bad); err == nil {
			t.Fatalf("%+v must be refused", bad)
		}
	}
}

// A file with no placement still carries its own lineage. The exact path of
// the completed operation that published it (or, for a file left at its
// download source, the artifact file record there) supplies the release facts,
// the rt values, the source group name and the frozen Vars output. A path with no
// lineage stays facts_missing, and a placement lineage wins when both exist.
func TestRenderMatchPathLineage(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	unplaced := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "unplaced", AnimeID: "anime-path", AnimeTitle: "路径番", TMDBMediaKind: "tv", TMDBSeason: 1,
		SourceGroup: "PathGroup", ReleaseTitle: "[PathGroup] 路径番 - 05", PlacedPath: "/library/路径番/EP05.mkv",
		DownloadName: "EP05.mkv", Rules: rendererRules(), FrozenEpisode: "5", NoPlacement: true,
	})
	freezeRenderFixtureBusiness(t, db, unplaced.OperationID)
	placed := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "placed", AnimeID: "anime-path", AnimeTitle: "路径番", TMDBMediaKind: "tv", TMDBSeason: 1,
		SourceGroup: "PlacementGroup", ReleaseTitle: "[PlacementGroup] 路径番 - 06", PlacedPath: "/library/路径番/EP06.mkv",
		DownloadName: "EP06.mkv", Rules: rendererRules(), FrozenEpisode: "6",
	})
	freezeRenderFixtureBusiness(t, db, placed.OperationID)
	sourceFacts := `{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"test","tags":["resolution:1080p"]}}`
	for _, releaseID := range []string{"unplaced-rel", "placed-rel"} {
		if _, err := db.ExecContext(ctx, `UPDATE releases SET source_facts=$2::jsonb WHERE id=$1`, releaseID, sourceFacts); err != nil {
			t.Fatal(err)
		}
	}
	render := func(rules naming.NamingRules, in MatchRenderInput) (MatchRender, error) {
		return NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules}).RenderMatch(ctx, in)
	}
	grouped := matchRules("${sanitize(anime.title)} - E${pad(ep.display, 2)} [${rt.resolution}] [${vars.actual.size}] ${anime.group}.${file.ext}")
	// The completed operation target names the file's own artifact lineage.
	in := MatchRenderInput{
		ManagedAnimeID: "anime-path", LineagePath: "/library/路径番/EP05.mkv", FileName: "EP05.mkv",
		SizeBytes: 1024, Season: 2, Episode: 7,
	}
	got, err := render(grouped, in)
	if err != nil || got.Path != "/library/路径番/Season 02/路径番 - E07 [1080p] [1024] PathGroup.mkv" || !got.Lineage {
		t.Fatalf("path lineage = %+v, %v", got, err)
	}
	// The artifact file record at the download source path is the fallback for
	// a file the acquisition never published (the skip route leaves it there).
	sourceOnly := in
	sourceOnly.LineagePath = "/downloads/EP05.mkv"
	got, err = render(matchRules("${sanitize(anime.title)} - [${rt.resolution}] ${anime.group}.${file.ext}"), sourceOnly)
	if err != nil || got.Path != "/library/路径番/Season 02/路径番 - [1080p] PathGroup.mkv" || !got.Lineage {
		t.Fatalf("source path lineage = %+v, %v", got, err)
	}
	// A path with no lineage is missing, never guessed.
	none := in
	none.LineagePath = "/downloads/none.mkv"
	var failure *MatchRenderError
	if _, err = render(grouped, none); !errors.As(err, &failure) || failure.Code != MatchRenderFactsMissing {
		t.Fatalf("unmatched path = %v", err)
	}
	// The placement lineage is authoritative when both exist.
	both := MatchRenderInput{
		ManagedAnimeID: "anime-path", LineagePlacementID: placed.PlacementID, LineagePath: "/library/路径番/EP05.mkv",
		FileName: "EP06.mkv", SizeBytes: 1024, Season: 2, Episode: 7,
	}
	if got, err = render(grouped, both); err != nil || got.Path != "/library/路径番/Season 02/路径番 - E07 [1080p] [1024] PlacementGroup.mkv" {
		t.Fatalf("placement wins = %+v, %v", got, err)
	}
}
