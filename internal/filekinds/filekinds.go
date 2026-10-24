// Package filekinds owns the product-fixed file classification of a torrent
// manifest: which extensions are video or subtitle, and which manifest paths
// are first level. It is a pure leaf package. The lists are product
// decisions, not user configuration; a release freezes the list version it
// was accepted with, so retry, recovery and preview classify with the frozen
// list even after the product list changes.
package filekinds

import (
	"path"
	"strings"
)

// Kinds a file can be classified as.
const (
	KindVideo    = "video"
	KindSubtitle = "subtitle"
	KindOther    = "other"
)

// Typed reasons a manifest file is read-only and never parsed, planned or
// placed.
const (
	// ReasonIgnoredNested marks a file below the first level of the torrent.
	ReasonIgnoredNested = "ignored_nested"
	// ReasonIgnoredKind marks a file that is neither video nor subtitle.
	ReasonIgnoredKind = "ignored_kind"
)

// VersionV1 is the first (and current) extension list.
const VersionV1 = "file-kinds-v1"

// Version is the list version written to a new parse snapshot.
const Version = VersionV1

type list struct {
	video    map[string]struct{}
	subtitle map[string]struct{}
}

func newList(video, subtitle []string) list {
	result := list{video: map[string]struct{}{}, subtitle: map[string]struct{}{}}
	for _, extension := range video {
		result.video[extension] = struct{}{}
	}
	for _, extension := range subtitle {
		result.subtitle[extension] = struct{}{}
	}
	return result
}

var lists = map[string]list{
	VersionV1: newList(
		[]string{".mkv", ".mp4", ".avi", ".mov", ".m4v", ".webm", ".ts"},
		[]string{".ass", ".ssa", ".srt", ".sub", ".vtt"},
	),
}

// Known reports whether version names a list this build can classify with.
func Known(version string) bool {
	_, ok := lists[version]
	return ok
}

// Classify returns the kind of filename under the frozen list version. An
// empty version means the current list (a snapshot written before the version
// was frozen); an unknown non-empty version classifies everything as other so
// an unreadable frozen list fails closed instead of guessing.
func Classify(version, filename string) string {
	if version == "" {
		version = Version
	}
	frozen, ok := lists[version]
	if !ok {
		return KindOther
	}
	extension := strings.ToLower(path.Ext(strings.ReplaceAll(filename, `\`, "/")))
	if _, ok := frozen.video[extension]; ok {
		return KindVideo
	}
	if _, ok := frozen.subtitle[extension]; ok {
		return KindSubtitle
	}
	return KindOther
}

// ClassifyCurrent classifies filename with the current list.
func ClassifyCurrent(filename string) string { return Classify(Version, filename) }

// FirstLevel reports whether a manifest path is first level: a multi-file
// torrent file is exactly `<torrent.name>/<filename>` (two segments) and a
// single-file torrent file is `<torrent.name>` itself (one segment). The
// torrent parser builds every manifest path from the torrent name, so the
// segment count of a manifest path is the torrent-relative depth; callers
// must pass the manifest path, never a download-client local path.
func FirstLevel(manifestPath string) bool {
	normalized := strings.Trim(strings.ReplaceAll(manifestPath, `\`, "/"), "/")
	if normalized == "" {
		return false
	}
	return strings.Count(normalized, "/") <= 1
}

// Eligibility is the product-owned disposition of one manifest file before any
// user rule runs: eligible files are first-level videos and subtitles. The
// reason is empty for an eligible file.
func Eligibility(version, manifestPath string) (kind, reason string) {
	kind = Classify(version, manifestPath)
	switch {
	case !FirstLevel(manifestPath):
		return kind, ReasonIgnoredNested
	case kind == KindOther:
		return kind, ReasonIgnoredKind
	default:
		return kind, ""
	}
}

// BindSubtitle is the one subtitle-to-video rule: the subtitle filename starts
// with a video filename without its extension plus a dot, so `abcd.mp4` owns
// `abcd.ass` and `abcd.forced.ass` but not `abcde.ass`. Several matches resolve
// to the longest video name, and a remaining tie to the first video; callers
// pass videoBasenames in manifest path order so the result never depends on
// arrival order. It returns the index of the bound video, or -1.
func BindSubtitle(subtitleFilename string, videoBasenames []string) int {
	match := -1
	for index, basename := range videoBasenames {
		if basename == "" || !strings.HasPrefix(subtitleFilename, basename+".") {
			continue
		}
		if match < 0 || len(basename) > len(videoBasenames[match]) {
			match = index
		}
	}
	return match
}
