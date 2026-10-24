package naming

import (
	"strings"
	"testing"
)

// renderSuffix compiles source as a subtitle suffix and renders it against
// ctx, giving a direct view of helper output through the real engine.
func renderSuffix(t *testing.T, source string, ctx NamingContext) string {
	t.Helper()
	engine := NewEngine()
	compiled, err := engine.Compile(KindSubtitleSuffix, source)
	if err != nil {
		t.Fatalf("compile %q: %v", source, err)
	}
	value, err := engine.Render(compiled, ctx)
	if err != nil {
		t.Fatalf("render %q: %v", source, err)
	}
	return value
}

func TestDefaultAndCoalesce(t *testing.T) {
	if got := renderSuffix(t, "${defaultValue('fb', anime.season_folder)}", NamingContext{}); got != "fb" {
		t.Fatalf("default on empty = %q", got)
	}
	ctx := NamingContext{}
	two := 2
	ctx.Anime.Season = &two
	// season_dir is defined only for a TV item: the derived kind must be set,
	// a movie context leaves the fact empty and the default wins.
	ctx.Derived.EffectiveMediaKind = string(MediaKindTV)
	if got := renderSuffix(t, "${defaultValue('fb', anime.season_folder)}", ctx); got != "Season 02" {
		t.Fatalf("default on value = %q", got)
	}
	ctx.Derived.EffectiveMediaKind = "movie"
	if got := renderSuffix(t, "${defaultValue('fb', anime.season_folder)}", ctx); got != "fb" {
		t.Fatalf("movie leaves season_dir empty = %q", got)
	}
	if got := renderSuffix(t, "${coalesce(release.lang, anime.group, 'x')}", ctx); got != "x" {
		t.Fatalf("coalesce all empty = %q", got)
	}
	ctx.Source.GroupName = "Group"
	ctx.Parsed.Language.Code = "CHS"
	if got := renderSuffix(t, "${coalesce(release.lang, anime.group, 'x')}", ctx); got != "CHS" {
		t.Fatalf("coalesce first value = %q", got)
	}
	ctx.Parsed.Language.Code = ""
	if got := renderSuffix(t, "${coalesce(release.lang, anime.group, 'x')}", ctx); got != "Group" {
		t.Fatalf("coalesce second value = %q", got)
	}
}

func TestPadHelpers(t *testing.T) {
	ctx := NamingContext{}
	ctx.Parsed.Episode.Display = "3"
	if got := renderSuffix(t, "${pad(ep.display, 2)}", ctx); got != "03" {
		t.Fatalf("pad = %q", got)
	}
	ctx.Parsed.Episode.Display = "123"
	if got := renderSuffix(t, "${pad(ep.display, 2)}", ctx); got != "123" {
		t.Fatalf("pad overwide = %q", got)
	}
	if got := renderSuffix(t, "${pad(ep.display, 0)}", ctx); got != "123" {
		t.Fatalf("pad zero width = %q", got)
	}
	ctx.Parsed.Episode.Display = "7"
	if got := renderSuffix(t, "${pad(ep.display, 3)}", ctx); got != "007" {
		t.Fatalf("pad3 = %q", got)
	}
	if got := renderSuffix(t, "${padLeft(ep.display, 3, '*')}", ctx); got != "**7" {
		t.Fatalf("padLeft = %q", got)
	}
	if got := renderSuffix(t, "${padRight(ep.display, 3, '*')}", ctx); got != "7**" {
		t.Fatalf("padRight = %q", got)
	}
	if got := renderSuffix(t, "${padLeft(ep.display, 3, '')}", ctx); got != "  7" {
		t.Fatalf("padLeft empty fill = %q", got)
	}
}

func TestReplaceAndRegexHelpers(t *testing.T) {
	ctx := NamingContext{}
	ctx.File.OriginalName = "Show - 01 [WEB DL].mkv"
	if got := renderSuffix(t, "${replace(file.name, ' ', '_')}", ctx); got != "Show_-_01_[WEB_DL].mkv" {
		t.Fatalf("replace = %q", got)
	}
	if got := renderSuffix(t, "${replaceRegex(file.name, '\\\\s+', '_')}", ctx); got != "Show_-_01_[WEB_DL].mkv" {
		t.Fatalf("replaceRegex = %q", got)
	}
	if got := renderSuffix(t, "${extract(file.name, '0(\\\\d)')}", ctx); got != "1" {
		t.Fatalf("extract default group = %q", got)
	}
	if got := renderSuffix(t, "${extract(file.name, '0(\\\\d)', 0)}", ctx); got != "01" {
		t.Fatalf("extract group 0 = %q", got)
	}
	engine := NewEngine()
	// An out-of-range capture index is a malformed call and fails; a missing
	// capture is null, which an interpolation renders as "" (the rendered path
	// is what is judged), and an empty suffix is valid.
	outOfRange, err := engine.Compile(KindSubtitleSuffix, "${extract(file.name, '0(\\\\d)', 5)}")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := engine.Render(outOfRange, ctx); err == nil {
		t.Fatalf("out-of-range capture became successful: %q", got)
	}
	noMatch, err := engine.Compile(KindSubtitleSuffix, "${extract(file.name, 'ZZZ(\\\\d)')}")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := engine.Render(noMatch, ctx); err != nil || got != "" {
		t.Fatalf("missing capture = %q, %v; want an empty suffix", got, err)
	}
	if got := renderSuffix(t, "${match(file.name, '(?i)web[- ]?dl')}", ctx); got != "true" {
		t.Fatalf("match = %q", got)
	}
	if got := renderSuffix(t, "${match(file.name, /BDRip/)}", ctx); got != "false" {
		t.Fatalf("match negative = %q", got)
	}
}

func TestRegexHelperErrors(t *testing.T) {
	engine := NewEngine()
	ctx := NamingContext{}
	// A JS regex literal that does not parse is a template syntax error; a
	// string-literal pattern is a static constant, so its bounds are checked
	// while the template is validated. A pattern that is not a literal is left
	// to the helper at render, where the same bounds apply.
	if _, err := engine.Compile(KindSubtitleSuffix, "${replaceRegex(file.name, /[/, 'x')}"); err == nil {
		t.Fatal("invalid replaceRegex pattern was accepted")
	}
	if _, err := engine.Compile(KindSubtitleSuffix, "${extract(file.name, /[/)}"); err == nil {
		t.Fatal("invalid extract pattern was accepted")
	}
	if _, err := engine.Compile(KindSubtitleSuffix, "${match(file.name, /[/)}"); err == nil {
		t.Fatal("invalid match pattern was accepted")
	}
	oversize := strings.Repeat("a", MaxRegexBytes+1)
	if _, err := engine.Compile(KindSubtitleSuffix, `${match(File.OriginalName, "`+oversize+`")}`); err == nil {
		t.Fatal("oversize regex pattern was accepted")
	}
	_ = ctx
}

func TestCaseTrimAndPredicateHelpers(t *testing.T) {
	ctx := NamingContext{}
	ctx.Parsed.Language.Code = " chs "
	if got := renderSuffix(t, "${upper(trim(release.lang))}", ctx); got != "CHS" {
		t.Fatalf("upper/trim = %q", got)
	}
	if got := renderSuffix(t, "${lower('CHS')}", ctx); got != "chs" {
		t.Fatalf("lower = %q", got)
	}
	ctx.File.OriginalName = "[ANi] Show.mkv"
	if got := renderSuffix(t, "${contains(file.name, 'Show')}", ctx); got != "true" {
		t.Fatalf("contains = %q", got)
	}
	if got := renderSuffix(t, "${hasPrefix(file.name, '[ANi]')}", ctx); got != "true" {
		t.Fatalf("hasPrefix = %q", got)
	}
	if got := renderSuffix(t, "${hasSuffix(file.name, '.mkv')}", ctx); got != "true" {
		t.Fatalf("hasSuffix = %q", got)
	}
}

func TestSplitJoinSanitizeToIntPrintf(t *testing.T) {
	ctx := NamingContext{}
	ctx.File.Basename = "a b c"
	if got := renderSuffix(t, "${join(file.stem.split(' '), '-')}", ctx); got != "a-b-c" {
		t.Fatalf("split/join = %q", got)
	}
	ctx.Anime.Title = `a/b:c*?d`
	if got := renderSuffix(t, "${sanitize(anime.title)}", ctx); got != "a_b_c__d" {
		t.Fatalf("sanitize = %q", got)
	}
	ctx.Parsed.Episode.Raw = "07"
	if got := renderSuffix(t, "${toInt(ep.raw)}", ctx); got != "7" {
		t.Fatalf("toInt = %q", got)
	}
	ctx.Parsed.Episode.Raw = "abc"
	if got := renderSuffix(t, "${toInt(ep.raw)}", ctx); got != "0" {
		t.Fatalf("toInt non-numeric = %q", got)
	}
	ctx.Anime.TMDB.Season = 2
	if got := renderSuffix(t, "S${String(tmdb.season).padStart(2, '0')}", ctx); got != "S02" {
		t.Fatalf("printf = %q", got)
	}
}
