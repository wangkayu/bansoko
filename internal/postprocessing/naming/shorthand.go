package naming

import "strings"

// This file implements the shorthand layer of js-naming-1: a fixed set of whole
// interpolation tokens that expand to ordinary expressions at compile time. It
// is preprocessing, not a namespace — the fact registry, the renderer and the
// frozen render snapshot never see a shorthand, and the language identifier
// stays js-naming-1.
//
// Recognition is exact: an interpolation expands only when the text between its
// `${` and `}` is exactly the registered name. `${s00e00 + 1}` and `${ s00e00 }`
// stay ordinary expressions — the first is the user's own arithmetic, the second
// keeps its existing unknown-identifier refusal — so no template that compiles
// today changes meaning. An unregistered name is left alone and reported by the
// ordinary analysis instead of being silently swallowed.
//
// s00e00 is the whole ep.s00e00 composite (season.go SeasonEpisode, driven
// through the deprecated seasonEpisode helper): a bound TV season renders the
// SxxEyy pair, a season-less TV episode renders the episode pair alone ("E12"),
// and a movie renders empty. sxe, s00 and s keep their null-guard: they render
// the empty string when anime.season is null. The fact is nil exactly when no
// season is known (unified_facts.go), and an empty string makes an incomplete
// template produce an incomplete name for target validation to refuse, instead
// of silently dropping the season and emitting an episode-only name (the
// FileBot single-season E-only fallback is deliberately not implemented). The
// guard is also required mechanically: without it the interpolation helper
// throws on the null value rather than rendering anything.
//
// The guard is written so that only the season sits in the conditional's test:
// every other fact an expansion reads (ep.number) stays a required fact, exactly
// as in the equivalent hand-written expression, so a missing episode number is
// still a refusal instead of an empty name.
//
// The shorthand expansions themselves are unchanged per token across time so a
// frozen snapshot replays byte for byte: a token's expansion is fixed at
// registration and never rewritten in place.

// Shorthand documents one registered shorthand: the token written inside
// ${...}, the js-naming-1 expression it expands to, and what that expansion
// renders for the documented sample season 1 / episode 12 (which is what the
// catalog shows the editor).
type Shorthand struct {
	Name      string `json:"name"`
	Expansion string `json:"expansion"`
	Example   string `json:"example"`
}

// Shorthands is the single source of truth for the shorthand set. The compiler
// expands exactly this table (shorthandExpansions) and the generated naming
// catalog documents exactly this table, so editor help cannot drift from
// execution.
var Shorthands = []Shorthand{
	// s00e00 composes exactly like the ep.s00e00 fact (SeasonEpisode): the
	// season argument is guard-preprocessed because the helper takes a string,
	// and ep.number_end is conditional so a multi-episode range stays optional.
	// ep.number stays required.
	{Name: "s00e00", Expansion: `seasonEpisode(anime.kind, anime.season === null ? "" : anime.season, ep.number, ep.number_end === null ? "" : ep.number_end)`, Example: "S01E12"},
	{Name: "sxe", Expansion: `anime.season === null ? "" : anime.season + "x" + pad(ep.number, 2)`, Example: "1x12"},
	{Name: "s00", Expansion: `anime.season === null ? "" : pad(anime.season, 2)`, Example: "01"},
	{Name: "e00", Expansion: `pad(ep.number, 2)`, Example: "12"},
	{Name: "e000", Expansion: `pad(ep.number, 3)`, Example: "012"},
	{Name: "e", Expansion: `ep.number`, Example: "12"},
	{Name: "s", Expansion: `anime.season === null ? "" : anime.season`, Example: "1"},
	// y renders the frozen input year (anime.year, the owner-edited value) and
	// disappears whole when the year is unset: a segment like "name ${y}"
	// normalizes back to "name" because whole-interpolation empties render ""
	// and NormalizePathSegment collapses the double space afterward.
	{Name: "y", Expansion: `anime.year === null ? "" : anime.year`, Example: "2026"},
	// n is the effective title (anime.title); ny composes it with the frozen
	// input year in the FileBot bracket style, falling back to the bare title
	// when the year is unset ("Show", "Show (2026)").
	{Name: "n", Expansion: `anime.title`, Example: "Show"},
	{Name: "ny", Expansion: `anime.year === null ? anime.title : anime.title + " (" + anime.year + ")"`, Example: "Show (2026)"},
	// fn replays the source file's own complete name (file.name, the listing
	// entry the artifact record freezes), the FileBot {fn} semantics: a whole
	// value returning, not a composite.
	{Name: "fn", Expansion: `file.name`, Example: "Show - 12.mkv"},
	// ep is the bare episode number; res/vc/ac/cf replay the file's own media
	// probe facts and g the scroll group. Each is a whole value returning, so
	// an empty fact renders an empty segment that NormalizePathSegment cleans.
	{Name: "ep", Expansion: `ep.number`, Example: "12"},
	{Name: "res", Expansion: `media.resolution`, Example: "1080p"},
	{Name: "vc", Expansion: `media.video_codec`, Example: "HEVC"},
	{Name: "ac", Expansion: `media.audio_codec`, Example: "AAC"},
	{Name: "cf", Expansion: `media.container`, Example: "mkv"},
	{Name: "g", Expansion: `anime.group`, Example: "示例字幕组"},
	// sf and sp replay the season folder level (Season 01 under the movie gate
	// for sf, the '/Season 01' root-relative composite for sp), the same
	// empties the ep.s00e00 composites fall back to whole segments for.
	{Name: "sf", Expansion: `anime.season_folder`, Example: "Season 01"},
	{Name: "sp", Expansion: `anime.season_path`, Example: "/Season 01"},
	// kind replays the effective media kind the composites gate on.
	{Name: "kind", Expansion: `anime.kind === "tv" ? "TV" : "Movie"`, Example: "TV"},
}

// shorthandExpansions indexes Shorthands for the compile-time pre-pass.
var shorthandExpansions = func() map[string]string {
	index := make(map[string]string, len(Shorthands))
	for _, shorthand := range Shorthands {
		index[shorthand.Name] = shorthand.Expansion
	}
	return index
}()

// expandShorthands returns expression with every whole-interpolation shorthand
// replaced by its expansion. Spans come from the same parser the analyser uses
// — in a pass that tolerates the shorthand identifiers — so recognition follows
// template syntax rather than a text scan: `${s00e00}` written inside a string
// literal or a comment is not an interpolation and stays untouched. A parse
// failure leaves the expression as it is, and the ordinary analysis reports it.
func expandShorthands(expression string) string {
	if !strings.Contains(expression, "${") {
		return expression
	}
	spans, err := interpolationSpans(expression)
	if err != nil {
		return expression
	}
	// Rewriting right to left keeps the offsets of the remaining spans valid.
	for i := len(spans) - 1; i >= 0; i-- {
		span := spans[i]
		if span[0] < 2 || span[1] >= len(expression) {
			continue
		}
		if expression[span[0]-2:span[0]] != "${" || expression[span[1]:span[1]+1] != "}" {
			continue
		}
		expansion, ok := shorthandExpansions[expression[span[0]:span[1]]]
		if !ok {
			continue
		}
		expression = expression[:span[0]] + expansion + expression[span[1]:]
	}
	return expression
}

// interpolationSpans lists the source span of every interpolation expression in
// one template expression, in source order and in the coordinates of
// `expression` itself. It parses the same string the analyser parses, but it
// accepts registered shorthand names as identifiers: the pre-pass runs before
// the expansion and never validates a template, because the validating analysis
// runs on the expanded expression.
func interpolationSpans(expression string) ([][2]int, error) {
	analyzer := newExpressionAnalyzer(languageProfileOf(), KindDirectory, nil)
	analyzer.allowShorthands = true
	if err := analyzer.analyze(expression); err != nil {
		return nil, err
	}
	return analyzer.interpolations, nil
}
