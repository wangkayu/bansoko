// Package naming implements the js-naming engine (js-naming-1) and its method-free
// naming context, resolver catalog, and language-independent target checks.
package naming

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

// MaxTemplateBytes bounds every naming template source. It lives here because
// both the engine and persisted-config validation share the limit.
const MaxTemplateBytes = 4096

// MediaKind identifies the naming branch selected for a verified artifact.
// The provider may call a release a video, but the persisted media facts are
// what decide whether the TV or movie rule is rendered.
type MediaKind string

const (
	MediaKindTV    MediaKind = "tv"
	MediaKindMovie MediaKind = "movie"
)

// NamingVariant is the media-kind-specific pair of JavaScript template sources.
// The directory template may render empty to place files at the library root; the
// video filename template must render a non-empty bare filename.
type NamingVariant struct {
	DirectoryTemplate     string `json:"directory_template"`
	VideoFilenameTemplate string `json:"video_filename_template"`
}

// NamingRules is the persisted, media-type-aware naming configuration. TV and
// movie each own a directory and a video filename template. The subtitle
// suffix template only appends a suffix to the bound video basename; it can
// never replace directory, basename, or extension. Preset is a convenience
// reference to the built-in preset this configuration was derived from; it
// never changes evaluation because the concrete templates are authoritative.
type NamingRules struct {
	TV                     NamingVariant `json:"tv"`
	Unknown                NamingVariant `json:"unknown"`
	Movie                  NamingVariant `json:"movie"`
	SubtitleSuffixTemplate string        `json:"subtitle_suffix_template"`
	Preset                 string        `json:"preset,omitempty"`
	Language               string        `json:"language,omitempty"`
}

// defaultUnknownVariant is the ordinary default of the unknown naming
// variant: an unrecognized video lands under `<work>/Extras` with its own
// filename (`fn` — the full original name including the extension, kept
// verbatim). The variant is edited like tv and movie; there is no built-in
// fallback behind it.
func defaultUnknownVariant() NamingVariant {
	return NamingVariant{
		DirectoryTemplate:     "/media/${kind}/${ny}/Extras",
		VideoFilenameTemplate: "${fn}",
	}
}

// DefaultNamingRules returns the built-in defaults. Directory templates are
// absolute literal paths; `/media` matches the bundled development mount.
// A deployment that mounts the library elsewhere edits the templates once —
// the rendered target never depends on a hidden global anchor.
//
// A template value is the body of one JavaScript template literal: the text as
// it reads between the backticks, with `${...}` interpolations. The backticks
// are the engine's, never the owner's, so a folder named `Show (2024)` needs no
// escaping.
//
// The defaults are the all-shorthand forms (2026-10-22 owner ruling): `kind`
// renders the folder word TV/Movie, `n`/`ny` the title and the FileBot-style
// "Title (Year)" name, `fn` the file's own full name, `s00e00` and `sf` the
// season/episode composites. A fact with no value is not substituted here.
// `${sf}` is a path segment of its own, so a TV episode with no owner season
// renders an empty folder level and the render refuses with
// NAMING_FACT_MISSING naming anime.season_folder — fail-closed; the repair is
// setting the season on the anime (the episode-only `E07` fallback of the old
// defaults is deliberately not provided). `${s00e00}` still renders the
// episode alone when the season is missing, keeping the filename fact from
// silently repairing the path. Any other invalid result fails target
// validation, which is the intended fail-closed behaviour. The subtitle
// suffix is each subtitle's own one BCP 47 tag (sub.lang_tag: its filename
// tag first, else the release title), so subtitles of one video in different
// languages land on distinct targets; no language renders no suffix.
func DefaultNamingRules() NamingRules {
	return NamingRules{
		Language: Language,
		TV: NamingVariant{
			DirectoryTemplate:     "/media/${kind}/${ny}/${sf}",
			VideoFilenameTemplate: "${n} ${s00e00}.${file.ext}",
		},
		Unknown: defaultUnknownVariant(),
		Movie: NamingVariant{
			DirectoryTemplate:     "/media/${kind}/${ny}",
			VideoFilenameTemplate: "${fn}",
		},
		SubtitleSuffixTemplate: "${sub.lang_tag}",
	}
}

// sanitize replaces filesystem-unsafe and control characters with underscores
// and trims surrounding whitespace.
func sanitize(value string) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || strings.ContainsRune(`/\\:*?"<>|`, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// validateRenderedTarget enforces target hygiene on one rendered segment:
// no NUL, no traversal, and (for filenames) no separators. A rendered
// directory must additionally be absolute; Render enforces that shape and
// reports NAMING_TARGET_NOT_ABSOLUTE.
func validateRenderedTarget(value string, directory bool) error {
	if strings.IndexByte(value, 0) >= 0 {
		return errors.New("path contains NUL")
	}
	if strings.HasPrefix(value, "~") {
		return errors.New("path must not use a home-directory prefix")
	}
	clean := filepath.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("path contains traversal segments")
	}
	if !directory && strings.ContainsAny(value, `/\\`) {
		return errors.New("filename must not contain a directory separator")
	}
	return nil
}
