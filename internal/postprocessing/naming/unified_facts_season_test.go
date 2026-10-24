package naming

import (
	"errors"
	"strings"
	"testing"

	"bansoko/internal/templatefacts"
)

func seasonPtr(value int) *int { return &value }

// seasonContext is one TV episode 7 of "Show" with an owner season and a TMDB
// binding that may name a different season.
func seasonContext(owner *int, tmdb TMDBNamingFacts) NamingContext {
	ctx := NamingContext{
		Anime: AnimeNamingFacts{Title: "Show", Season: owner, TMDB: tmdb},
		File:  FileNamingFacts{OriginalName: "[Grp] Show - 07.mkv", Basename: "[Grp] Show - 07", Extension: "mkv"},
		Release: ReleaseNamingFacts{
			FrozenEpisode: &FrozenEpisodeNamingFacts{Raw: "07", Number: "7", Display: "7"},
		},
	}
	return DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
}

// TestSeasonIsTheOwnerValue pins anime.season (and its folder and composites)
// to the owner-edited season: the TMDB binding's season is tmdb.season only,
// and an unset owner season is empty whatever the binding says.
func TestSeasonIsTheOwnerValue(t *testing.T) {
	bound := TMDBNamingFacts{WorkID: "100", MediaKind: "tv", Season: 3, SeasonPresent: true}
	for _, tc := range []struct {
		name       string
		owner      *int
		tmdb       TMDBNamingFacts
		wantSeason any
		wantDir    string
		wantPath   string
		wantSxxExx string
	}{
		{"owner season without TMDB", seasonPtr(2), TMDBNamingFacts{}, int64(2), "Season 02", "/Season 02", "S02E07"},
		{"owner season wins over a different TMDB season", seasonPtr(2), bound, int64(2), "Season 02", "/Season 02", "S02E07"},
		{"owner specials season", seasonPtr(0), bound, int64(0), "Season 00", "/Season 00", "S00E07"},
		{"unset is empty despite a TMDB season", nil, bound, nil, "", "", "E07"},
		{"unset without TMDB", nil, TMDBNamingFacts{}, nil, "", "", "E07"},
		// A movie keeps no season folder at all, even with a set owner
		// season: season_dir, its path composite and s00e00 all gate on the
		// kind (the season value itself stays the owner value).
		{"movie with an owner season renders empty dir",
			seasonPtr(2), TMDBNamingFacts{WorkID: "42", MediaKind: "movie"}, int64(2), "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := renderFacts(seasonContext(tc.owner, tc.tmdb), 0)
			var got any
			if facts.Anime.Season != nil {
				got = *facts.Anime.Season
			}
			if got != tc.wantSeason || facts.Anime.SeasonFolder != tc.wantDir || facts.Anime.SeasonPath != tc.wantPath || facts.Ep.S00E00 != tc.wantSxxExx {
				t.Fatalf("season=%v dir=%q path=%q s00e00=%q", got, facts.Anime.SeasonFolder, facts.Anime.SeasonPath, facts.Ep.S00E00)
			}
			// tmdb.season stays the raw binding.
			if tc.tmdb.SeasonPresent && (facts.Tmdb.Season == nil || *facts.Tmdb.Season != 3) {
				t.Fatalf("tmdb.season = %v; want the bound 3", facts.Tmdb.Season)
			}
		})
	}
}

// TestDefaultTemplatesNameTheOwnerSeason renders the default TV template: the
// owner season names the folder and file, and an unset season refuses the
// render (the default directory keeps `${sf}` as its own folder level) even
// with a TMDB season bound.
func TestDefaultTemplatesNameTheOwnerSeason(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.CompileRules(DefaultNamingRules())
	if err != nil {
		t.Fatal(err)
	}
	bound := TMDBNamingFacts{WorkID: "100", MediaKind: "tv", Season: 3, SeasonPresent: true}
	withSeason, err := compiled.RenderVideoDetailed(engine, seasonContext(seasonPtr(2), bound))
	if err != nil || withSeason.Directory != "/media/TV/Show/Season 02" || withSeason.Filename != "Show S02E07.mkv" {
		t.Fatalf("owner season = %+v, %v", withSeason, err)
	}
	var namingErr *Error
	if _, err := compiled.RenderVideoDetailed(engine, seasonContext(nil, bound)); !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactMissing || strings.Join(namingErr.Missing, ",") != "anime.season_folder" {
		t.Fatalf("unset season = %v, want %s naming anime.season_folder", err, ErrCodeNamingFactMissing)
	}
}

// TestReplayedRenderersKeepTheTMDBSeason proves a frozen naming-render-3 (and
// older) snapshot replays exactly as it rendered: those renderers name the
// TMDB binding's season and never read the owner season.
func TestReplayedRenderersKeepTheTMDBSeason(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.CompileRules(DefaultNamingRules())
	if err != nil {
		t.Fatal(err)
	}
	bound := TMDBNamingFacts{WorkID: "100", MediaKind: "tv", Season: 3, SeasonPresent: true}
	for _, tc := range []struct {
		version   string
		directory string
		filename  string
	}{
		{"naming-render-3", "/media/TV/Show/Season 03", "Show S03E07.mkv"},
		{"naming-render-2", "/media/TV/Show/Season 03", "Show S03E07.mkv"},
	} {
		replay, err := engine.ForRenderer(tc.version)
		if err != nil {
			t.Fatal(err)
		}
		// A different owner season does not move a replayed render.
		got, err := compiled.RenderVideoDetailed(replay, seasonContext(seasonPtr(5), bound))
		if err != nil || got.Directory != tc.directory || got.Filename != tc.filename {
			t.Fatalf("%s = %+v, %v", tc.version, got, err)
		}
	}
	// naming-render-3 named no season for an unbound TV item; with the current
	// default templates that empty season folder is now a refused render for
	// every renderer (naming-render-2 replays its season-1 default instead,
	// already covered above by the season-present case).
	render3, _ := engine.ForRenderer("naming-render-3")
	var replayErr *Error
	if _, err := compiled.RenderVideoDetailed(render3, seasonContext(seasonPtr(5), TMDBNamingFacts{MediaKind: "tv"})); !errors.As(err, &replayErr) || replayErr.Code != ErrCodeNamingFactMissing || strings.Join(replayErr.Missing, ",") != "anime.season_folder" {
		t.Fatalf("naming-render-3 unbound = %v, want %s naming anime.season_folder", err, ErrCodeNamingFactMissing)
	}
}

// TestFrozenInputSeason maps the season of a frozen Vars input: a recorded
// owner season (or null) is read as is; an input frozen before the owner
// season existed names the season its TMDB binding named under
// naming-render-3, so the current renderer reproduces the old target.
func TestFrozenInputSeason(t *testing.T) {
	fs := templatefacts.FactSet{Tmdb: templatefacts.TmdbFacts{ID: "100", Kind: "tv", Season: templatefacts.Int64(3)}}
	current := templatefacts.VarsInputObject(&fs)
	if _, ok := current["anime"].(map[string]any)["season"]; !ok {
		t.Fatalf("a new Vars input must carry anime.season")
	}
	ctx := WithFrozenReleaseFacts(NamingContext{}, current)
	if ctx.Anime.Season != nil {
		t.Fatalf("recorded null season = %v; want unset (the TMDB season never stands in)", *ctx.Anime.Season)
	}
	fs.Anime.Season = templatefacts.Int64(1)
	ctx = WithFrozenReleaseFacts(NamingContext{}, templatefacts.VarsInputObject(&fs))
	if ctx.Anime.Season == nil || *ctx.Anime.Season != 1 {
		t.Fatalf("recorded season = %v; want 1", ctx.Anime.Season)
	}

	legacy := templatefacts.VarsInputObject(&fs)
	delete(legacy["anime"].(map[string]any), "season")
	ctx = WithFrozenReleaseFacts(NamingContext{}, legacy)
	if ctx.Anime.Season == nil || *ctx.Anime.Season != 3 {
		t.Fatalf("legacy input season = %v; want the TMDB season 3 naming-render-3 named", ctx.Anime.Season)
	}
	// An older input bound to season 0 without the continuous-SP flag named
	// no season, and still names none.
	fs.Tmdb.Season = templatefacts.Int64(0)
	legacy = templatefacts.VarsInputObject(&fs)
	delete(legacy["anime"].(map[string]any), "season")
	if season := FrozenAnimeSeason(legacy, WithFrozenReleaseFacts(NamingContext{}, legacy).Anime.TMDB); season != nil {
		t.Fatalf("legacy unpreserved season 0 = %d; want none", *season)
	}
}

// TestLegacyFrozenInputRendersAsBefore renders the default templates from an
// input frozen before the owner season: with a bound season the current
// renderer gives the target naming-render-3 gave from the same input, and
// without one both renderers refuse the season-less render through the same
// empty `${sf}` segment of the current default templates.
func TestLegacyFrozenInputRendersAsBefore(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.CompileRules(DefaultNamingRules())
	if err != nil {
		t.Fatal(err)
	}
	render3, _ := engine.ForRenderer("naming-render-3")
	for _, season := range []*int64{templatefacts.Int64(2), nil} {
		fs := templatefacts.FactSet{Anime: templatefacts.AnimeFacts{TitleBase: "Show"}}
		if season != nil {
			fs.Tmdb = templatefacts.TmdbFacts{ID: "100", Kind: "tv", Season: season}
		}
		frozen := templatefacts.VarsInputObject(&fs)
		delete(frozen["anime"].(map[string]any), "season")
		base := NamingContext{
			File:    FileNamingFacts{OriginalName: "a.mkv", Basename: "a", Extension: "mkv"},
			Release: ReleaseNamingFacts{FrozenEpisode: &FrozenEpisodeNamingFacts{Raw: "7", Number: "7", Display: "7"}},
		}
		ctx := DefaultRegistry().ResolveAll(ResolverInput{Facts: WithFrozenReleaseFacts(base, frozen)}).Context
		now, nowErr := compiled.RenderVideoDetailed(engine, ctx)
		before, beforeErr := compiled.RenderVideoDetailed(render3, ctx)
		if season == nil {
			var nowErrTyped, beforeErrTyped *Error
			if !errors.As(nowErr, &nowErrTyped) || !errors.As(beforeErr, &beforeErrTyped) ||
				nowErrTyped.Code != ErrCodeNamingFactMissing || beforeErrTyped.Code != ErrCodeNamingFactMissing ||
				strings.Join(nowErrTyped.Missing, ",") != "anime.season_folder" ||
				strings.Join(beforeErrTyped.Missing, ",") != "anime.season_folder" {
				t.Fatalf("season-less legacy input: current %v, naming-render-3 %v", nowErr, beforeErr)
			}
			continue
		}
		if nowErr != nil {
			t.Fatalf("season %v: %v", season, nowErr)
		}
		if beforeErr != nil {
			t.Fatalf("season %v: %v", season, beforeErr)
		}
		if now.Directory != before.Directory || now.Filename != before.Filename {
			t.Fatalf("season %v: current %q/%q, naming-render-3 %q/%q", season, now.Directory, now.Filename, before.Directory, before.Filename)
		}
	}
}
