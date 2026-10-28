package naming

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"bansoko/internal/templatefacts"
)

// RendererVersion identifies the render semantics a target was produced with.
// Every new render uses it and the file effect records it in the frozen render
// snapshot (`naming_renderer`), so a later re-render of a placed file replays
// the semantics the file was placed with (Engine.ForRenderer).
//
//   - naming-render-1 (recorded as absent): a nullable fact read outside a
//     fallback refuses the render (NAMING_FACT_MISSING); no path
//     normalization; no TV season default.
//   - naming-render-2: a value with no value renders "" and the facts that did
//     are reported (EmptyFacts); the rendered path is normalized
//     (NormalizePathSegment); the named season comes from AnimeSeason, where
//     an unbound TV season reads 1; only an invalid normalized path fails,
//     naming the empty facts that contributed.
//   - naming-render-3: as naming-render-2, except that an unbound TV season is
//     empty (no implicit default), the composite facts anime.season_path and
//     ep.s00e00 exist (ep.s00e00 is E07 for an unbound season), and path normalization also removes control
//     characters, leading dots and trailing dots of a segment and refuses a
//     segment longer than 255 bytes (sanitizeSegment).
//   - naming-render-4: as naming-render-3, except that the named season
//     (anime.season, anime.season_folder, anime.season_path, ep.s00e00) is the
//     owner-edited season (AnimeNamingFacts.Season) and never the TMDB
//     binding; tmdb.season stays the raw binding. An unset season is empty.
//     A frozen input that predates the owner season maps to the season
//     naming-render-3 named (FrozenAnimeSeason), so its render is unchanged.
//   - naming-render-5: as naming-render-4; the engine semantics (season
//     source, empty reporting, normalization) are unchanged. The built-in
//     default template bodies changed shape (the all-shorthand forms with the
//     /media root), so a render run against the unsaved defaults produces new
//     targets — including the season-less TV refusal the ${sf} segment now
//     implies.
//   - naming-render-6: as naming-render-5 for the engine semantics; the
//     subtitle-mode swap is the render change: release.subtitle_mode now
//     derives from the release's parsed subtitle flags (rt.sub_flag) instead
//     of the retired hand-rolled title token scan, and the new lang_code /
//     lang_name / sub.lang display facts and the chain helper join the fact
//     and helper catalogs every render at this floor reads. Frozen snapshots
//     of naming-render-5 and earlier replay the retired token scan
//     (legacyRenderSubtitleMode) so an older target re-renders unchanged.
const RendererVersion = "naming-render-6"

// renderer5Version, renderer4Version, renderer3Version and renderer2Version
// are previous semantics, replayed for snapshots that recorded them.
const (
	renderer5Version = "naming-render-5"
	renderer4Version = "naming-render-4"
	renderer3Version = "naming-render-3"
	renderer2Version = "naming-render-2"
)

// LegacyRendererVersion is the semantics of a snapshot that records no
// renderer version.
const LegacyRendererVersion = "naming-render-1"

// ForRenderer returns an engine that renders with the recorded semantics of
// version. An empty version is a snapshot written before the field existed,
// which is LegacyRendererVersion. An unknown version is an error: a frozen
// target is never replayed with guessed semantics. naming-render-6's only
// render change is the subtitle-mode derivation, so every pre-6 version
// replays at its own replay level (see renderFacts); renderer versions 4
// through 1 keep their season semantics through the same levels.
func (e *Engine) ForRenderer(version string) (*Engine, error) {
	replay := *e
	switch strings.TrimSpace(version) {
	case RendererVersion:
		replay.replay = 0
	case renderer5Version, renderer4Version:
		replay.replay = 4
	case renderer3Version:
		replay.replay = 3
	case renderer2Version:
		replay.replay = 2
	case "", LegacyRendererVersion:
		replay.replay = 1
	default:
		return nil, fmt.Errorf("unknown naming renderer version %q", version)
	}
	return &replay, nil
}

// CurrentConfigRenderer is the renderer version a maintenance render of the
// current configuration uses for a placement that recorded version. The owner
// season and year are always read now, so the pre-owner-season semantics
// (naming-render-1 to 3, which name the TMDB binding's season) have no replay
// there; they render with naming-render-4, the first owner-season version.
// naming-render-4 to 6 keep their own subtitle-mode derivation.
func CurrentConfigRenderer(recorded string) string {
	switch version := strings.TrimSpace(recorded); version {
	case RendererVersion, renderer5Version, renderer4Version:
		return version
	}
	return renderer4Version
}

// Legacy reports whether the engine replays naming-render-1.
func (e *Engine) Legacy() bool { return e.legacy() }

// MissingRequiredFacts is MissingRequiredFacts under this engine's renderer
// semantics.
func (e *Engine) MissingRequiredFacts(required []string, ctx NamingContext) []string {
	if len(required) == 0 {
		return nil
	}
	facts := renderFacts(ctx, e.replay)
	return sortedMissing(languageProfileOf().missing(required, &facts, false))
}

// renderFacts projects ctx onto the fact set one render reads under the
// renderer semantics replay (0 = current, see Engine.replay). Under the
// current renderer anime.season and anime.season_folder are the owner-edited
// season (UnifiedFacts). naming-render-2 and naming-render-3 replay the
// season of the TMDB binding through AnimeSeason; naming-render-1 replays
// the raw binding projection it read; naming-render-4/5 keep the current
// season semantics. tmdb.season stays the raw binding in every version. The
// composites (anime.season_path, ep.s00e00) derive from the same season.
// release.subtitle_mode keeps the retired token-scan semantics for every
// pre-6 replay, so an older snapshot re-renders its recorded value.
func renderFacts(ctx NamingContext, replay int) templatefacts.FactSet {
	facts := UnifiedFacts(ctx)
	switch replay {
	case 1:
		// naming-render-1 read the provider year projection (TMDB first,
		// then BGM) and the raw TMDB season with the derived season folder.
		facts.Anime.Year = ""
		if year, _ := templatefacts.EffectiveYear(ctx.Anime.TMDB.Year, ctx.Anime.BGM.Year); year != 0 {
			facts.Anime.Year = strconv.Itoa(year)
		}
		facts.Anime.Season = legacyTMDBSeason(ctx.Anime.TMDB)
		facts.Anime.SeasonFolder = ctx.Derived.SeasonFolder
	case 2, 3:
		facts.Anime.Season = AnimeSeason(facts.Anime.Kind, ctx.Anime.TMDB, replay == 2)
		facts.Anime.SeasonFolder = templatefacts.SeasonFolder(facts.Anime.Season)
	}
	if replay != 0 {
		facts.Release.SubtitleMode = legacyRenderSubtitleMode(ctx.Release.OriginalTitle)
	}
	facts.Anime.SeasonPath = SeasonPath(facts.Anime.Kind, facts.Anime.Season)
	facts.Ep.S00E00 = SeasonEpisode(facts.Anime.Kind, facts.Anime.Season, facts.Ep.Number, facts.Ep.NumberEnd)
	return facts
}

// legacyRenderSubtitleMode is the release.subtitle_mode of naming-render-5
// and earlier: the hand-rolled title token scan the rt.sub_flag swap retired.
// Frozen snapshots of those renderer versions replay it so an older target
// re-renders byte for byte; it is dead for every new render.
func legacyRenderSubtitleMode(title string) string {
	tokens := map[string]string{
		"内封": "embedded", "內封": "embedded", "内嵌": "embedded",
		"外挂": "external",
	}
	matched := map[string]bool{}
	for token, mode := range tokens {
		if strings.Contains(title, token) {
			matched[mode] = true
		}
	}
	switch {
	case matched["embedded"] && matched["external"]:
		return "mixed"
	case matched["embedded"]:
		return "embedded"
	case matched["external"]:
		return "external"
	}
	return "unknown"
}

var (
	spaceRun          = regexp.MustCompile(` {2,}`)
	emptyBracketPair  = regexp.MustCompile(`\(\s*\)|\[\s*\]|\{\s*\}|（\s*）|【\s*】|「\s*」`)
	trailingDebris    = regexp.MustCompile(`(\s+[-–—_~·・|,，、+&]+)+$`)
	leadingDebris     = regexp.MustCompile(`^([-–—_~·・|,，、+&]+\s+)+`)
	debrisOnly        = regexp.MustCompile(`^[-–—_~·・|,，、+&\s]*$`)
	errEmptySegment   = fmt.Errorf("rendered directory has an empty path segment")
	errEmptyStem      = fmt.Errorf("rendered filename has an empty name")
	errEmptyDirectory = fmt.Errorf("rendered directory is empty; the target must be absolute")
)

// NormalizePathSegment is the naming-render-2 cleanup of one rendered path
// segment (a directory name or a filename stem). It is deterministic and only
// removes what an empty value leaves behind:
//
//   - leading/trailing whitespace is trimmed and runs of spaces collapse to one;
//   - a bracket pair holding nothing but whitespace is removed: `()`, `[]`,
//     `{}`, `（）`, `【】`, `「」`;
//   - a separator run at an edge that is set apart by whitespace is removed
//     (`Show -` → `Show`, `- 01` → `01`); a separator attached to a word
//     (`-Ultra Romantic-`) is part of the name and stays;
//   - a segment of nothing but separators and spaces is empty.
//
// It never drops a segment: the caller decides that an empty one is invalid.
// The current renderer applies sanitizeSegment on top (normalizeSegment).
func NormalizePathSegment(segment string) string {
	value := strings.TrimSpace(spaceRun.ReplaceAllString(segment, " "))
	for {
		next := emptyBracketPair.ReplaceAllString(value, "")
		if next == value {
			break
		}
		value = next
	}
	value = strings.TrimSpace(spaceRun.ReplaceAllString(value, " "))
	value = strings.TrimSpace(trailingDebris.ReplaceAllString(value, ""))
	value = strings.TrimSpace(leadingDebris.ReplaceAllString(value, ""))
	if debrisOnly.MatchString(value) {
		return ""
	}
	return value
}

// maxSegmentBytes is the longest path segment the library filesystems accept
// (NAME_MAX on ext4, btrfs, xfs and SMB shares).
const maxSegmentBytes = 255

var errSegmentTooLong = fmt.Errorf("rendered path has a segment longer than %d bytes", maxSegmentBytes)

// sanitizeSegment is the naming-render-3 cleanup applied after
// NormalizePathSegment, compared against FileBot's file name validation:
//
//   - control characters (tab, CR, LF, ...) are removed; FileBot strips them;
//   - leading dots are removed (a dot file is hidden from media servers);
//     FileBot strips them since 4.9.3;
//   - trailing dots and spaces are removed (Windows and SMB shares refuse a
//     name ending in either); FileBot strips both;
//   - the result is normalized again, so removing a character never leaves
//     debris behind.
//
// FileBot also removes \ / : * ? " < > | from every name unless run with
// -unixfs. This renderer does not: a rendered value keeps them (sanitize() is
// the explicit opt-in) and a separator in a filename stays a target error.
func sanitizeSegment(segment string) string {
	value := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, segment)
	for {
		next := strings.TrimRight(strings.TrimLeft(NormalizePathSegment(value), "."), ". ")
		if next == value {
			return value
		}
		value = next
	}
}

// normalizeSegment is the per-segment normalization of one renderer.
func normalizeSegment(segment string, render2 bool) (string, error) {
	if render2 {
		return NormalizePathSegment(segment), nil
	}
	value := sanitizeSegment(segment)
	if len(value) > maxSegmentBytes {
		return "", errSegmentTooLong
	}
	return value, nil
}

// normalizeDirectory normalizes every segment of a rendered directory. An
// empty segment anywhere after the leading root — `a//b`, or a trailing `/`
// left by an empty last value — is an invalid path, never collapsed. A
// relative value is returned normalized; Render reports it as not absolute.
func normalizeDirectory(value string, render2 bool) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errEmptyDirectory
	}
	if value == "/" {
		return value, nil
	}
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if index == 0 && segment == "" {
			continue
		}
		normalized, err := normalizeSegment(segment, render2)
		if err != nil {
			return "", err
		}
		if normalized == "" {
			return "", errEmptySegment
		}
		segments[index] = normalized
	}
	return strings.Join(segments, "/"), nil
}

// normalizeDirectoryAfterRoot is the current renderer's directory
// normalization when the template opens with literal directory text: root (the
// owner's own path, ending in "/") is kept as written — a mount such as
// `/srv/.media/` keeps its dot directory — and only the segments rendered after
// it are normalized. An empty segment after the root is invalid as in
// normalizeDirectory; a root of "/" alone is normalizeDirectory's case.
func normalizeDirectoryAfterRoot(root, rest string) (string, error) {
	if rest == "" {
		if root == "/" {
			return root, nil
		}
		return "", errEmptySegment
	}
	segments := strings.Split(rest, "/")
	for index, segment := range segments {
		normalized, err := normalizeSegment(segment, false)
		if err != nil {
			return "", err
		}
		if normalized == "" {
			return "", errEmptySegment
		}
		segments[index] = normalized
	}
	return root + strings.Join(segments, "/"), nil
}

// normalizeFilename normalizes the stem of a rendered filename (the text
// before its last dot) and trims the extension. An empty stem — `.mkv`, or
// `-.mkv` once separator debris is removed — is invalid.
func normalizeFilename(value string, render2 bool) (string, error) {
	if strings.ContainsAny(value, `/\`) {
		// A separator is refused by target validation with its own reason.
		return value, nil
	}
	stem, extension, dotted := value, "", false
	if index := strings.LastIndex(value, "."); index >= 0 {
		stem, extension, dotted = value[:index], strings.TrimSpace(value[index+1:]), true
	}
	if render2 {
		stem = NormalizePathSegment(stem)
	} else {
		stem = sanitizeSegment(stem)
		extension = sanitizeSegment(extension)
	}
	if stem == "" {
		return "", errEmptyStem
	}
	name := stem
	if dotted && (render2 || extension != "") {
		// The current renderer drops the dot of an empty extension: a
		// trailing dot is removed like any other.
		name = stem + "." + extension
	}
	if !render2 && len(name) > maxSegmentBytes {
		return "", errSegmentTooLong
	}
	return name, nil
}

// interpolationShape classifies the required facts of an analysed template
// expression. A fact whose every occurrence is a whole interpolation
// (`${anime.year}`) renders "" when empty; any other read is strict (see
// CompiledTemplate.strictFacts). A text occurrence inside a string literal or
// comment also counts as a non-whole read, which only ever makes the template
// stricter.
func interpolationShape(expression string, spans [][2]int, required []string) (strict []string, leading string) {
	whole := map[string]int{}
	for _, span := range spans {
		if span[0] < 0 || span[1] > len(expression) || span[0] >= span[1] {
			continue
		}
		text := strings.TrimSpace(expression[span[0]:span[1]])
		whole[text]++
		if span[0] == 3 && strings.HasPrefix(expression, "`${") && expression[span[0]:span[1]] == text {
			leading = text
		}
	}
	for _, path := range required {
		occurrences := len(factOccurrence(path).FindAllStringIndex(expression, -1))
		if occurrences > whole[path] {
			strict = append(strict, path)
		}
	}
	isRequired := false
	for _, path := range required {
		if path == leading {
			isRequired = true
		}
	}
	if !isRequired {
		leading = ""
	}
	return strict, leading
}

// factOccurrence matches path as a whole member chain: not preceded by an
// identifier character or a dot, and not followed by one.
func factOccurrence(path string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_$.])` + regexp.QuoteMeta(path) + `($|[^A-Za-z0-9_$])`)
}

// strictEmptyFacts returns the empty facts a template cannot render as "":
// those read outside a whole interpolation, and a directory's leading fact.
func strictEmptyFacts(t CompiledTemplate, empty []string) []string {
	if len(empty) == 0 {
		return nil
	}
	refused := []string{}
	for _, fact := range empty {
		if fact == t.leadingFact && t.Kind == KindDirectory {
			refused = append(refused, fact)
			continue
		}
		for _, strict := range t.strictFacts {
			if strict == fact {
				refused = append(refused, fact)
				break
			}
		}
	}
	return refused
}
