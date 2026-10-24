package naming

import (
	"errors"
	"strings"
	"testing"
)

func TestNamingMissingValueRequiresExplicitFallback(t *testing.T) {
	e := NewEngine()
	// Naming reads an absent string as null, which a helper refuses;
	// file.name always has a value in a real naming render.
	withFile := NamingContext{File: FileNamingFacts{OriginalName: "show.mkv"}}
	for _, source := range []string{
		"${extract(file.name, '(never)')}",
		"${sanitize(null)}",
		"extract(file.name, '(never)')",
		"${coalesce(null, undefined)}",
		"${true ? extract(file.name, '(never)') : 'fallback'}",
		"${`nested ${extract(file.name, '(never)')}`}",
		"${extract('b', '(a)?b')}",
		"${extract(file.name) || 'fallback'}",
	} {
		c, err := e.Compile(KindDirectory, source)
		if err != nil {
			t.Fatal(err)
		}
		if value, err := e.Render(c, withFile); err == nil {
			t.Fatalf("missing became successful path %s => %q", source, value)
		}
	}
	for _, source := range []string{
		"/probe/${extract(file.name, '(never)') ?? 'chosen'}",
		"/probe/${defaultValue('chosen', extract(file.name, '(never)'))}",
		"/probe/${coalesce(extract(file.name, '(never)'), 'chosen')}",
		"/probe/${`前${extract(file.name, '(never)') ?? 'chosen'}`.slice(1)}",
	} {
		c, err := e.Compile(KindDirectory, source)
		if err != nil {
			t.Fatal(err)
		}
		if value, err := e.Render(c, withFile); err != nil || value != "/probe/chosen" {
			t.Fatalf("explicit fallback %s => %q %v", source, value, err)
		}
	}
	// An explicitly empty directory is not a null-fact failure, but it is not a
	// valid target either: the rendered directory must be absolute.
	c, err := e.Compile(KindDirectory, "${extract(file.name, '(never)') ?? ''}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Render(c, withFile); err == nil {
		t.Fatal("empty directory must be refused")
	} else {
		var namingErr *Error
		if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingTargetNotAbsolute {
			t.Fatalf("empty directory error = %v", err)
		}
	}
}

// goldenContext is a resolved TV/movie context with only a title, an episode
// and a file: no year, no TMDB/BGM binding (so no season and no ids).
func goldenContext(kind, episode string) NamingContext {
	ctx := NamingContext{
		Anime: AnimeNamingFacts{Title: "Show", TMDB: TMDBNamingFacts{MediaKind: kind}},
		File:  FileNamingFacts{OriginalName: "[Grp] Show - 05.mkv", Basename: "[Grp] Show - 05", Extension: "mkv"},
	}
	if episode != "" {
		ctx.Release.FrozenEpisode = &FrozenEpisodeNamingFacts{Raw: episode, Number: episode, Display: episode}
	}
	return DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
}

// Golden: the default TV and movie templates render valid normalized paths
// when the season and the work ids have no value — except that the default TV
// directory keeps `${sf}` as a folder level of its own, so a season-less TV
// episode refuses the render (fail-closed; the repair is setting the season),
// while a bound season, season 0 and the movie branch all render. `ny` reads
// the year inside its own conditional, so a missing year no longer renders an
// empty segment or a reported empty fact.
func TestDefaultTemplatesRenderWithMissingYearSeasonAndIDs(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.CompileRules(DefaultNamingRules())
	if err != nil {
		t.Fatal(err)
	}
	// Season-less TV refuses through the empty `${sf}` segment.
	var namingErr *Error
	if _, err := compiled.RenderVideoDetailed(engine, goldenContext("", "5")); !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactMissing {
		t.Fatalf("season-less tv = %v, want %s", err, ErrCodeNamingFactMissing)
	}
	if strings.Join(namingErr.Missing, ",") != "anime.season_folder" {
		t.Fatalf("season-less tv missing = %v, want anime.season_folder", namingErr.Missing)
	}
	// A bound season and a multi-episode file.
	multi := goldenContext("tv", "5")
	two := 2
	multi.Anime.Season = &two
	multi.Release.FrozenEpisode.NumberEnd = "6"
	tv, err := compiled.RenderVideoDetailed(engine, multi)
	if err != nil || tv.Directory != "/media/TV/Show/Season 02" || tv.Filename != "Show S02E05-E06.mkv" {
		t.Fatalf("bound tv = %+v, %v", tv, err)
	}
	// Season 0 kept for continuous specials.
	specials := goldenContext("tv", "1")
	zero := 0
	specials.Anime.Season = &zero
	tv, err = compiled.RenderVideoDetailed(engine, specials)
	if err != nil || tv.Directory != "/media/TV/Show/Season 00" || tv.Filename != "Show S00E01.mkv" {
		t.Fatalf("specials tv = %+v, %v", tv, err)
	}
	movie, err := compiled.RenderVideoDetailed(engine, goldenContext("movie", ""))
	if err != nil {
		t.Fatal(err)
	}
	if movie.Directory != "/media/Movie/Show" || movie.Filename != "[Grp] Show - 05.mkv" {
		t.Fatalf("movie = %q / %q", movie.Directory, movie.Filename)
	}
	if len(movie.EmptyFacts) != 0 {
		t.Fatalf("movie empty facts = %v", movie.EmptyFacts)
	}
	// A set season is used as is, and the year renders when known.
	bound := goldenContext("tv", "5")
	year := 2024
	bound.Anime.Season, bound.Anime.Year = &two, &year
	bound = DefaultRegistry().ResolveAll(ResolverInput{Facts: bound}).Context
	withSeason, err := compiled.RenderVideoDetailed(engine, bound)
	if err != nil || withSeason.Directory != "/media/TV/Show (2024)/Season 02" || withSeason.Filename != "Show S02E05.mkv" || len(withSeason.EmptyFacts) != 0 {
		t.Fatalf("bound = %+v, %v", withSeason, err)
	}
}

// Only an invalid normalized path fails, and the refusal names the facts that
// rendered empty: an empty filename stem (`-.mkv`) or an empty directory
// segment. Explicit fallbacks keep a fact from counting as empty.
func TestEmptyValueFailsOnlyWhenThePathIsInvalid(t *testing.T) {
	e := NewEngine()
	// A movie release with a title-tag block that carries no title: anime.year,
	// anime.season_folder and rt.title are defined and empty.
	ctx := NamingContext{File: FileNamingFacts{Extension: "mkv"}, Release: ReleaseNamingFacts{OriginalTitle: "[G] Show"}, Rt: map[string]string{}}
	ctx.Anime.TMDB.MediaKind = "movie"
	ctx.Derived.EffectiveMediaKind = "movie"
	cases := []struct {
		name, kind, source, missing string
	}{
		{"empty stem", "file", "${anime.year}-${rt.title}.${file.ext}", "anime.year,rt.title"},
		{"empty middle segment", "dir", "/library/${rt.title}/Season 01", "rt.title"},
		{"empty last segment", "dir", "/library/Show/${anime.season_folder}", "anime.season_folder"},
		{"root from an empty leading value", "dir", "${rt.title}/Show", "rt.title"},
		{"empty value read inside an expression", "file", "${anime.title + ' ' + rt.title}.${file.ext}", "anime.title,rt.title"},
		{"empty value passed to a helper", "file", "Show ${pad(anime.year, 4)}.${file.ext}", "anime.year"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind := KindVideoFilename
			if tc.kind == "dir" {
				kind = KindDirectory
			}
			c, err := e.Compile(kind, tc.source)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.Render(c, ctx)
			var namingErr *Error
			if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactMissing {
				t.Fatalf("render error = %v, want %s", err, ErrCodeNamingFactMissing)
			}
			if got := strings.Join(namingErr.Missing, ","); got != tc.missing {
				t.Fatalf("missing = %s, want %s", got, tc.missing)
			}
			if want := "naming_fact_missing_" + FactReasonSlug(strings.Split(tc.missing, ",")[0]); namingErr.ReasonCode() != want {
				t.Fatalf("reason = %s, want %s", namingErr.ReasonCode(), want)
			}
		})
	}
	// One empty value leaves a valid, distinct name, reported as empty (a
	// separator attached to a word is kept).
	c, err := e.Compile(KindVideoFilename, "${anime.year}-${anime.title}.${file.ext}")
	if err != nil {
		t.Fatal(err)
	}
	ctx.Anime.Title = "Show"
	ctx = DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
	if rendered, err := e.RenderDetailed(c, ctx); err != nil || rendered.Value != "-Show.mkv" || strings.Join(rendered.EmptyFacts, ",") != "anime.year" {
		t.Fatalf("one empty value: %+v %v", rendered, err)
	}
	year := 2024
	ctx.Anime.Year = &year
	if rendered, err := e.RenderDetailed(c, ctx); err != nil || rendered.Value != "2024-Show.mkv" || len(rendered.EmptyFacts) != 0 {
		t.Fatalf("owner year: %+v %v", rendered, err)
	}
	for _, source := range []string{
		"${tmdb.id ?? 'none'}.${file.ext}",
		"${tmdb.id || 'none'}.${file.ext}",
		"${defaultValue('none', tmdb.id)}.${file.ext}",
		"${tmdb.id ? tmdb.id : 'none'}.${file.ext}",
	} {
		c, err := e.Compile(KindVideoFilename, source)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := e.RenderDetailed(c, NamingContext{File: FileNamingFacts{Extension: "mkv"}})
		if err != nil || rendered.Value != "none.mkv" || len(rendered.EmptyFacts) != 0 {
			t.Fatalf("explicit fallback %s = %+v, %v", source, rendered, err)
		}
	}
}

// A variable whose namespace has no source in this render is undefined: the
// render fails naming it, whatever the path would have been, and an empty
// value never stands in for it.
func TestUndefinedVariableFailsNamingTheVariable(t *testing.T) {
	e := NewEngine()
	bare := NamingContext{File: FileNamingFacts{Extension: "mkv"}}
	for _, tc := range []struct {
		name, source, undefined string
	}{
		{"no bangumi or tmdb binding", "Show ${bgm.id}-${tmdb.id}.${file.ext}", "bgm.id,tmdb.id"},
		{"no release for the title tags", "Show ${rt.title}.${file.ext}", "rt.title"},
		{"not a mikan release", "Show ${mikan.groupid}.${file.ext}", "mikan.groupid"},
		{"no episode identity", "Show ${ep.number}.${file.ext}", "ep.number"},
		{"not an anibt release", "Show ${anibt.language}.${file.ext}", "anibt.language"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := e.Compile(KindVideoFilename, tc.source)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.Render(c, bare)
			var namingErr *Error
			if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactUndefined {
				t.Fatalf("render error = %v, want %s", err, ErrCodeNamingFactUndefined)
			}
			if got := strings.Join(namingErr.Missing, ","); got != tc.undefined {
				t.Fatalf("undefined = %s, want %s", got, tc.undefined)
			}
			if want := "naming_fact_undefined_" + FactReasonSlug(strings.Split(tc.undefined, ",")[0]); namingErr.ReasonCode() != want {
				t.Fatalf("reason = %s, want %s", namingErr.ReasonCode(), want)
			}
		})
	}
	// Bound, the same template renders.
	bound := bare
	bound.Anime.BGM.WorkID, bound.Anime.TMDB.WorkID = "1", "2"
	c, err := e.Compile(KindVideoFilename, "Show ${bgm.id}-${tmdb.id}.${file.ext}")
	if err != nil {
		t.Fatal(err)
	}
	if rendered, err := e.RenderDetailed(c, bound); err != nil || rendered.Value != "Show 1-2.mkv" {
		t.Fatalf("bound ids: %+v %v", rendered, err)
	}
}

// NormalizePathSegment removes only what an empty value leaves behind.
func TestNormalizePathSegment(t *testing.T) {
	for in, want := range map[string]string{
		"Show 2024":                    "Show 2024",
		"Show ":                        "Show",
		"  Show   S01E05 ":             "Show S01E05",
		"Show ()":                      "Show",
		"Show [] - S01E05":             "Show - S01E05",
		"Show （）【 】":                   "Show",
		"Show -":                       "Show",
		"Show - ":                      "Show",
		"- 05":                         "05",
		"Show -  - 05":                 "Show - - 05",
		"-":                            "",
		" - ":                          "",
		"()":                           "",
		"Kaguya-sama -Ultra Romantic-": "Kaguya-sama -Ultra Romantic-",
		"Re:Zero - Starting Life":      "Re:Zero - Starting Life",
		"[Grp] Show - 05":              "[Grp] Show - 05",
	} {
		if got := NormalizePathSegment(in); got != want {
			t.Errorf("NormalizePathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

// A frozen snapshot replays the semantics it was rendered with: without a
// recorded version (naming-render-1) a missing fact refuses, nothing is
// normalized and no TV season default applies. An unknown version is refused.
func TestForRendererReplaysRecordedSemantics(t *testing.T) {
	legacy, err := NewEngine().ForRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewEngine().ForRenderer(RendererVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewEngine().ForRenderer("naming-render-99"); err == nil {
		t.Fatal("unknown renderer version must be refused")
	}
	c, err := legacy.Compile(KindVideoFilename, "${anime.title}  ${anime.year}.${file.ext}")
	if err != nil {
		t.Fatal(err)
	}
	ctx := goldenContext("", "5")
	var namingErr *Error
	if _, err := legacy.Render(c, ctx); !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactMissing {
		t.Fatalf("legacy render of a missing year = %v", err)
	}
	if got, err := current.Render(c, ctx); err != nil || got != "Show.mkv" {
		t.Fatalf("current render = %q, %v", got, err)
	}
	year := 2024
	ctx.Anime.TMDB.Year = &year
	if got, err := legacy.Render(c, ctx); err != nil || got != "Show  2024.mkv" {
		t.Fatalf("legacy render must not normalize: %q, %v", got, err)
	}
	season, err := legacy.Compile(KindDirectory, "/library/${anime.season_folder}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Render(season, ctx); err == nil {
		t.Fatal("legacy render must not default the TV season")
	}
	render2, err := NewEngine().ForRenderer("naming-render-2")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := render2.Render(season, ctx); err != nil || got != "/library/Season 01" {
		t.Fatalf("naming-render-2 season default = %q, %v", got, err)
	}
	// naming-render-3 has no unbound season default: the empty folder level
	// makes the path invalid and names the fact.
	if _, err := current.Render(season, ctx); !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactMissing || strings.Join(namingErr.Missing, ",") != "anime.season_folder" {
		t.Fatalf("current unbound season = %v", err)
	}
}
