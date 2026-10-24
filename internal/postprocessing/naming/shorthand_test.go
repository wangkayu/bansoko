package naming

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// shorthandContext builds a minimal naming context: an owner season that may
// be absent (seasonPresent false), an owner year that may be absent
// (yearPresent false) and one episode. The TMDB binding carries the same
// season, which the current renderer does not read.
func shorthandContext(season int, seasonPresent bool, episode string) NamingContext {
	year := 2026
	return shorthandContextYear(season, seasonPresent, &year, episode)
}

func shorthandContextYear(season int, seasonPresent bool, year *int, episode string) NamingContext {
	var owner *int
	if seasonPresent {
		owner = &season
	}
	return NamingContext{
		Anime: AnimeNamingFacts{Title: "Show", Season: owner, Year: year, TMDB: TMDBNamingFacts{MediaKind: "tv", Season: season, SeasonPresent: seasonPresent}},
		// The media-probe leaves, scroll group and effective kind let every
		// registered shorthand render its catalog example (media.* only render
		// those values they probed in a real run).
		File:   FileNamingFacts{OriginalName: "Show - 12.mkv", Extension: "mkv", Media: MediaNamingFacts{Resolution: "1080p", VideoCodec: "HEVC", AudioCodec: "AAC", Container: "mkv"}},
		Source: SourceNamingFacts{GroupName: "示例字幕组"},
		Parsed: ParsedNamingVariables{Episode: EpisodeNamingVariables{Raw: episode, Display: episode}},
		Release: ReleaseNamingFacts{
			FrozenEpisode: &FrozenEpisodeNamingFacts{Raw: episode, Number: episode, Display: episode},
		},
		Derived: DerivedNamingVariables{EffectiveMediaKind: "tv", EffectiveTitle: "Show"},
	}
}

// Every registered shorthand renders its documented result, so the catalog the
// editor reads cannot drift from what the compiler expands.
func TestNamingShorthandsRenderTheirCatalogExample(t *testing.T) {
	engine := NewEngine()
	ctx := shorthandContext(1, true, "12")
	if len(Shorthands) != 20 {
		t.Fatalf("shorthand table has %d entries, want the 20 approved ones", len(Shorthands))
	}
	seen := map[string]bool{}
	for _, shorthand := range Shorthands {
		if seen[shorthand.Name] {
			t.Fatalf("shorthand %s is registered twice", shorthand.Name)
		}
		seen[shorthand.Name] = true
	}
	for _, shorthand := range Shorthands {
		// sp already carries its leading slash: a directory template appends it
		// to a parent folder, never to a separator.
		source := "/${" + shorthand.Name + "}"
		want := "/" + shorthand.Example
		if shorthand.Name == "sp" {
			source = "/library${sp}"
			want = "/library" + shorthand.Example
		}
		compiled, err := engine.Compile(KindDirectory, source)
		if err != nil {
			t.Fatalf("%s compile: %v", shorthand.Name, err)
		}
		got, err := engine.Render(compiled, ctx)
		if err != nil {
			t.Fatalf("%s render: %v", shorthand.Name, err)
		}
		if got != want {
			t.Fatalf("%s rendered %q, catalog example %q", shorthand.Name, got, shorthand.Example)
		}
	}
}

// The owner year disappearing (y renders "") leaves the leftover separator
// spaces in the template text; segment normalization collapses them, so
// "name ${y} [tmdbid=42]" reads back "name [tmdbid=42]" instead of failing or
// doubling a space.
func TestNamingShorthandYCollapsesToNothingWithItsNeighborText(t *testing.T) {
	engine := NewEngine()
	ctx := shorthandContextYear(1, true, nil, "12")
	compiled, err := engine.Compile(KindDirectory, "/name ${y} [tmdbid=42]")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got, err := engine.Render(compiled, ctx)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "/name [tmdbid=42]" {
		t.Fatalf("rendered %q, want /name [tmdbid=42]", got)
	}
}

// A shorthand is a whole interpolation: arithmetic, surrounding whitespace and
// unregistered names stay ordinary expressions and keep their existing refusal
// instead of being silently expanded.
func TestNamingShorthandRecognitionIsExact(t *testing.T) {
	engine := NewEngine()
	for _, source := range []string{
		"${s00e00 + 1}",
		"${ s00e00 }",
		"${s00e00 }",
		"${ s00e00}",
		"${s00e001}",
		"${S00E00}",
	} {
		if _, err := engine.Compile(KindDirectory, source); err == nil {
			t.Fatalf("%s compiled; only a whole registered shorthand may expand", source)
		}
	}
	// The same text inside a string literal is not an interpolation.
	compiled, err := engine.Compile(KindDirectory, "/${'${s00e00}'}")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := engine.Render(compiled, shorthandContext(1, true, "12")); err != nil || got != "/${s00e00}" {
		t.Fatalf("quoted shorthand rendered %q %v, want the literal text", got, err)
	}
}

// An unknown season makes the guarded season shorthands (sxe, s00, s) render
// the empty string: the template produced an incomplete name for target
// validation to refuse, and it never degrades to an episode-only name. s00e00
// no longer guards: it is the ep.s00e00 composite, so a season-less episode
// renders the episode pair alone.
func TestNamingShorthandWithUnknownSeasonRendersEmpty(t *testing.T) {
	engine := NewEngine()
	absent := shorthandContext(0, false, "12")
	for _, name := range []string{"sxe", "s00", "s"} {
		compiled, err := engine.Compile(KindDirectory, "/A${"+name+"}B")
		if err != nil {
			t.Fatalf("%s compile: %v", name, err)
		}
		got, err := engine.Render(compiled, absent)
		if err != nil {
			t.Fatalf("%s with no season: %v", name, err)
		}
		if got != "/AB" {
			t.Fatalf("%s with no season rendered %q, want the empty string (no episode-only fallback)", name, got)
		}
	}
	// The composite s00e00 does not depend on the season either: a season-less
	// TV episode renders the episode pair alone ("E12").
	compiled, err := engine.Compile(KindDirectory, "/A${s00e00}B")
	if err != nil {
		t.Fatalf("s00e00 compile: %v", err)
	}
	if got, err := engine.Render(compiled, absent); err != nil || got != "/AE12B" {
		t.Fatalf("season-less s00e00 rendered %q %v, want /AE12B", got, err)
	}
	// The episode-only shorthands do not depend on the season.
	for source, want := range map[string]string{"/A${e00}B": "/A12B", "/A${e000}B": "/A012B", "/A${e}B": "/A12B"} {
		compiled, err := engine.Compile(KindDirectory, source)
		if err != nil {
			t.Fatalf("%s compile: %v", source, err)
		}
		if got, err := engine.Render(compiled, absent); err != nil || got != want {
			t.Fatalf("%s with no season rendered %q %v, want %q", source, got, err, want)
		}
	}
}

// An owner season 0 (specials) is a known season with a zero value. The
// replayed renderers read the TMDB binding instead: a preserved season 0
// (continuous SP) is a season, a bound season 0 that was not preserved is no
// season (AnimeSeason), and naming-render-2 replays the unbound default 01.
func TestNamingShorthandSeasonZeroIsPresent(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.Compile(KindDirectory, "/${s00}")
	if err != nil {
		t.Fatal(err)
	}
	owner := shorthandContext(0, true, "12")
	if got, err := engine.Render(compiled, owner); err != nil || got != "/00" {
		t.Fatalf("owner season 00 rendered %q %v, want 00", got, err)
	}
	render3, err := engine.ForRenderer("naming-render-3")
	if err != nil {
		t.Fatal(err)
	}
	preserved := shorthandContext(0, false, "12")
	preserved.Anime.TMDB.SeasonPresent, preserved.Anime.TMDB.SeasonBound = true, true
	if got, err := render3.Render(compiled, preserved); err != nil || got != "/00" {
		t.Fatalf("naming-render-3 preserved season 00 rendered %q %v, want 00", got, err)
	}
	unpreserved := shorthandContext(0, false, "12")
	unpreserved.Anime.TMDB.SeasonPresent = true
	unpreserved.Derived.EffectiveMediaKind = "tv"
	if got, err := render3.Render(compiled, unpreserved); err != nil || got != "/" {
		t.Fatalf("naming-render-3 unpreserved season 0 rendered %q %v, want empty", got, err)
	}
	if got, err := engine.Render(compiled, unpreserved); err != nil || got != "/" {
		t.Fatalf("unset owner season rendered %q %v, want empty", got, err)
	}
	render2, err := engine.ForRenderer("naming-render-2")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := render2.Render(compiled, unpreserved); err != nil || got != "/01" {
		t.Fatalf("naming-render-2 unpreserved season 0 rendered %q %v, want the unbound default 01", got, err)
	}
}

// The guard keeps only the season optional: the episode stays a required fact,
// so a missing episode number is still the ordinary missing-fact refusal.
func TestNamingShorthandKeepsTheEpisodeRequired(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.Compile(KindDirectory, "/${s00e00}")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(compiled.RequiredFacts, "anime.season") {
		t.Fatalf("anime.season must be optional for a shorthand: %v", compiled.RequiredFacts)
	}
	if !slices.Contains(compiled.RequiredFacts, "ep.number") {
		t.Fatalf("ep.number must stay required: %v", compiled.RequiredFacts)
	}
	_, err = engine.Render(compiled, shorthandContext(1, true, ""))
	var namingErr *Error
	if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactUndefined {
		t.Fatalf("missing episode rendered: %v", err)
	}
	if strings.Join(namingErr.Missing, ",") != "ep.number" {
		t.Fatalf("missing=%v, want only ep.number", namingErr.Missing)
	}
	// The hand-written equivalent refuses an unknown season instead of rendering
	// the empty string: that difference is the approved shorthand semantics.
	handWritten, err := engine.Compile(KindDirectory, "/${\"S\" + pad(anime.season, 2) + \"E\" + pad(ep.number, 2)}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Render(handWritten, shorthandContext(0, false, "12")); !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactMissing {
		t.Fatalf("hand-written equivalent with no season: %v", err)
	}
}

// The shorthand survives the whole rules pipeline: save-time compilation and the
// variant render used for real targets.
func TestNamingShorthandRendersThroughRules(t *testing.T) {
	engine := NewEngine()
	rules := DefaultNamingRules()
	rules.TV.DirectoryTemplate = "/library/${sanitize(anime.title)}/Season ${s00}"
	rules.TV.VideoFilenameTemplate = "${sanitize(anime.title)} - ${s00e00}.${file.ext}"
	if err := ValidateSavableRules(rules); err != nil {
		t.Fatal(err)
	}
	compiled, err := engine.CompileRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	directory, filename, err := compiled.RenderVideo(engine, shorthandContext(1, true, "12"))
	if err != nil {
		t.Fatal(err)
	}
	if directory != "/library/Show/Season 01" || filename != "Show - S01E12.mkv" {
		t.Fatalf("rendered %q %q", directory, filename)
	}
}
