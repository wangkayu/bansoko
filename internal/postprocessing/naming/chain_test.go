package naming

import (
	"testing"
)

// chainSubtitleCtx is the fixed context behind the chain matrix: a TV episode
// with a subtitle file on board, the media probe values the tech chain reads.
func chainSubtitleCtx(season *int) NamingContext {
	ctx := NamingContext{}
	ctx.Anime = AnimeNamingFacts{Title: "Show"}
	if season != nil {
		ctx.Anime.Season = season
	}
	ctx.Derived.EffectiveMediaKind = "tv"
	ctx.Parsed.Episode = EpisodeNamingVariables{Raw: "07", Display: "7", Number: 7}
	ctx.Release.FrozenEpisode = &FrozenEpisodeNamingFacts{Raw: "07", Number: "7", Display: "7"}
	ctx.File = FileNamingFacts{
		OriginalName: "show.chs.ass", Basename: "show.chs", Extension: "mkv",
		Kind: "subtitle", Media: MediaNamingFacts{Resolution: "1080p", VideoCodec: "x265", AudioCodec: "aac"},
	}
	ctx.Parsed.Subtitle.TagLanguages = []string{"SC", "JPN"}
	return ctx
}

// chainFull is the PRD's tech-chain probe, in the guarded form the language
// actually offers: a fact that carries no value for a render reads null, so
// every fact read inside a part needs a ?? ” guard (the shorthands do not
// expand inside an argument — the whole-interpolation form ${s00e00} is the
// fact compose route, ep.s00e00 is the expression one).
const chainFull = "Show${chain(' ' + (ep.s00e00 ?? ''), '.' + (media.resolution ?? ''), '.' + (sub.lang_code ?? ''), '.' + (media.video_codec ?? '') + (media.audio_codec ?? ''), '.' + file.ext)}"

// TestChainHelperMatrix pins the chain helper's contract on the real engine:
// every part is an expression carrying its own separator prefix, an empty
// part is dropped whole (prefix included), kept parts concatenate verbatim,
// and all parts empty render "" (the extension dot stays the caller's).
func TestChainHelperMatrix(t *testing.T) {
	season := 1
	if got := renderSuffix(t, chainFull, chainSubtitleCtx(&season)); got != "Show S01E07.1080p.jpsc.x265aac.mkv" {
		t.Fatalf("full chain = %q", got)
	}
	// ep.s00e00 never guards a season: a season-less episode renders the
	// episode pair alone and the chain joins it verbatim.
	if got := renderSuffix(t, chainFull, chainSubtitleCtx(nil)); got != "Show E07.1080p.jpsc.x265aac.mkv" {
		t.Fatalf("season-less chain joins the episode pair verbatim = %q", got)
	}
	// An episode that resolves to nothing makes the part " " — dropped whole,
	// prefix included (the empty middle part loses it the same way).
	noEpisode := chainSubtitleCtx(nil)
	noEpisode.Parsed.Episode = EpisodeNamingVariables{}
	if got := renderSuffix(t, chainFull, noEpisode); got != "Show.1080p.jpsc.x265aac.mkv" {
		t.Fatalf("empty part drops its space prefix whole = %q", got)
	}
	emptyResolution := chainSubtitleCtx(&season)
	emptyResolution.File.Media.Resolution = ""
	if got := renderSuffix(t, chainFull, emptyResolution); got != "Show S01E07.jpsc.x265aac.mkv" {
		t.Fatalf("empty middle part never prints its dot = %q", got)
	}
	allEmpty := "Show${chain(' ' + (ep.s00e00 ?? ''), '.' + (media.resolution ?? ''), '.' + (sub.lang_code ?? ''))}.${file.ext}"
	nothing := chainSubtitleCtx(nil)
	nothing.Parsed.Episode = EpisodeNamingVariables{}
	nothing.Parsed.Subtitle.TagLanguages = nil
	nothing.File.Media = MediaNamingFacts{}
	if got := renderSuffix(t, allEmpty, nothing); got != "Show.mkv" {
		t.Fatalf("all non-ext parts empty = %q", got)
	}
	if got := renderSuffix(t, "${chain(sub.lang_code, media.resolution)}", NamingContext{}); got != "" {
		t.Fatalf("every part empty = %q", got)
	}
	if got := renderSuffix(t, "a${chain(' X ', 'y')}b", NamingContext{}); got != "a X yb" {
		t.Fatalf("kept parts concatenate verbatim = %q", got)
	}
}

// TestChainEngineMatrix pins the same contract on the filename renderer,
// where the path normalization cuts in: a space prefix drops with its part,
// the kept dots join, and the degenerate all-empty tail collapses.
func TestChainEngineMatrix(t *testing.T) {
	season := 1
	ctx := chainSubtitleCtx(&season)
	ctx.File.Kind = "video"
	got, err := renderNamespaceTemplate(t, "Show${chain(' ' + (ep.s00e00 ?? ''), '.' + (media.resolution ?? ''), '.' + file.ext)}", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Show S01E07.1080p.mkv" {
		t.Fatalf("full chain on the filename = %q", got)
	}
	seasonLess := chainSubtitleCtx(nil)
	seasonLess.File.Kind = "video"
	seasonLess.Parsed.Episode = EpisodeNamingVariables{}
	seasonLess.Release.FrozenEpisode = nil
	got, err = renderNamespaceTemplate(t, "Show${chain(' ' + (ep.s00e00 ?? ''), '.' + (media.resolution ?? ''), '.' + file.ext)}", seasonLess)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Show.1080p.mkv" {
		t.Fatalf("empty part drops its space prefix on the filename = %q", got)
	}
	// Only the media factors empty: the S01E07 part keeps its value, the dot
	// collapses with it.
	noMedia := chainSubtitleCtx(&season)
	noMedia.File.Media = MediaNamingFacts{}
	got, err = renderNamespaceTemplate(t, "Show${chain(' ' + (ep.s00e00 ?? ''), '.' + (media.resolution ?? ''))}.${file.ext}", noMedia)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Show S01E07.mkv" {
		t.Fatalf("kept parts join on the filename = %q", got)
	}
	// Every non-ext part empty: the whole tail collapses.
	allEmpty := chainSubtitleCtx(nil)
	allEmpty.Parsed.Episode = EpisodeNamingVariables{}
	allEmpty.Release.FrozenEpisode = nil
	allEmpty.File.Media = MediaNamingFacts{}
	got, err = renderNamespaceTemplate(t, "Show${chain(' ' + (ep.s00e00 ?? ''), '.' + (media.resolution ?? ''))}.${file.ext}", allEmpty)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Show.mkv" {
		t.Fatalf("all non-ext parts empty on the filename = %q", got)
	}
}
