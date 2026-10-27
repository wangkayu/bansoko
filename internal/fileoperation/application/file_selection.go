package application

import (
	"encoding/json"
	"strings"

	"bansoko/internal/filekinds"
)

// fileSelection is the product-owned first decision about every file of one
// artifact, made before any user rule: which files exist for planning at all.
// Only first-level videos and subtitles do. A file below the first level, or
// of any other kind, is read-only: never parsed, planned, bound or placed.
//
// The level comes from the torrent manifest frozen on the download task, never
// from counting the download client's local path segments (the client may or
// may not create the torrent folder). The kind list is the version frozen on
// the release snapshot, so retry and recovery classify like the first plan.
type fileSelection struct {
	version  string
	manifest map[string]struct{} // normalized manifest paths; nil when none was frozen
	// selected is the batch plan's frozen manual file selection (one-shot);
	// nil is the product default (first-level files only).
	selected *filekinds.Selection
}

func newFileSelection(frozenInput []byte, version string, selected *filekinds.Selection) fileSelection {
	selection := fileSelection{version: version, selected: selected}
	var input struct {
		ReleaseFiles []struct {
			SourcePath string `json:"source_path"`
		} `json:"release_files"`
	}
	if len(frozenInput) == 0 || json.Unmarshal(frozenInput, &input) != nil || len(input.ReleaseFiles) == 0 {
		return selection
	}
	selection.manifest = make(map[string]struct{}, len(input.ReleaseFiles))
	for _, file := range input.ReleaseFiles {
		selection.manifest[normalizeManifestPath(file.SourcePath)] = struct{}{}
	}
	return selection
}

func normalizeManifestPath(value string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/"), "/")
}

// classify returns the file's kind under the frozen list and the typed reason
// it is ignored (empty when it is an eligible first-level video or subtitle).
// Under a frozen manual selection the selection replaces the first-level
// gate: a selected video or subtitle at any depth is eligible, every other
// one is ignored_unselected, and a file that maps to no single manifest entry
// is unselected (fail closed).
func (s fileSelection) classify(file artifactFileContext) (kind, reason string) {
	kind = filekinds.Classify(s.version, file.relativePath)
	if s.selected != nil {
		if kind == filekinds.KindOther {
			return kind, filekinds.ReasonIgnoredKind
		}
		if path, ok := s.manifestPath(file.relativePath); !ok || !s.selected.Has(path) {
			return kind, filekinds.ReasonIgnoredUnselected
		}
		return kind, ""
	}
	switch {
	case !s.firstLevel(file.relativePath):
		return kind, filekinds.ReasonIgnoredNested
	case kind == filekinds.KindOther:
		return kind, filekinds.ReasonIgnoredKind
	default:
		return kind, ""
	}
}

// firstLevel maps the artifact file to its manifest path and reads the level
// there. A file that maps to no single manifest entry fails closed.
func (s fileSelection) firstLevel(relativePath string) bool {
	path, ok := s.manifestPath(relativePath)
	return ok && filekinds.FirstLevel(path)
}

// manifestPath maps an artifact file to its torrent manifest path, the key of
// the frozen batch plan (filekinds.MapManifestPath: exact entry, then the
// torrent root joined with the local path, then a unique suffix). Without a
// frozen manifest the relative path is the manifest path.
func (s fileSelection) manifestPath(relativePath string) (string, bool) {
	if s.manifest == nil {
		return normalizeManifestPath(relativePath), true
	}
	return filekinds.MapManifestPath(s.manifest, relativePath)
}
