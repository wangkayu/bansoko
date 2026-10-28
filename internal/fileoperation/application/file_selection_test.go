package application

import (
	"testing"

	"bansoko/internal/filekinds"
)

func manifestInput(paths ...string) []byte {
	raw := `{"release_files":[`
	for index, path := range paths {
		if index > 0 {
			raw += ","
		}
		raw += `{"source_path":"` + path + `"}`
	}
	return []byte(raw + `]}`)
}

func TestFileSelectionReadsTheLevelFromTheManifest(t *testing.T) {
	selection := newFileSelection(manifestInput(
		"Show/01.mkv", "Show/01.chs.ass", "Show/cover.jpg", "Show/SPs/NCOP.mkv", "Show/SPs/NCOP.ass",
	), filekinds.Version, nil)
	for _, tc := range []struct{ path, kind, reason string }{
		{"Show/01.mkv", filekinds.KindVideo, ""},
		{"Show/01.chs.ass", filekinds.KindSubtitle, ""},
		{"Show/cover.jpg", filekinds.KindOther, filekinds.ReasonIgnoredKind},
		{"Show/SPs/NCOP.mkv", filekinds.KindVideo, filekinds.ReasonIgnoredNested},
		{"Show/SPs/NCOP.ass", filekinds.KindSubtitle, filekinds.ReasonIgnoredNested},
		// Not in the manifest: it cannot be mapped, so it fails closed.
		{"Show/stray.mkv", filekinds.KindVideo, filekinds.ReasonIgnoredNested},
	} {
		kind, reason := selection.classify(artifactFileContext{relativePath: tc.path})
		if kind != tc.kind || reason != tc.reason {
			t.Errorf("%s = %s/%q, want %s/%q", tc.path, kind, reason, tc.kind, tc.reason)
		}
	}
}

// The download client may omit the torrent folder; the level still comes from
// the manifest path, not from the segments the client produced.
func TestFileSelectionMapsLayoutsWithoutTheTorrentFolder(t *testing.T) {
	selection := newFileSelection(manifestInput("Show/01.mkv", "Show/SPs/NCOP.mkv"), filekinds.Version, nil)
	for path, wantReason := range map[string]string{
		"01.mkv":       "",                            // maps uniquely to Show/01.mkv
		"SPs/NCOP.mkv": filekinds.ReasonIgnoredNested, // maps to a nested entry
		"SPs/01.mkv":   filekinds.ReasonIgnoredNested,
	} {
		if _, reason := selection.classify(artifactFileContext{relativePath: path}); reason != wantReason {
			t.Errorf("%s reason = %q, want %q", path, reason, wantReason)
		}
	}
	// An entry that matches two manifest paths is ambiguous and fails closed.
	ambiguous := newFileSelection(manifestInput("Show/01.mkv", "Other/01.mkv"), filekinds.Version, nil)
	if _, reason := ambiguous.classify(artifactFileContext{relativePath: "01.mkv"}); reason != filekinds.ReasonIgnoredNested {
		t.Fatalf("ambiguous mapping reason = %q, want fail-closed", reason)
	}
}

func TestFileSelectionWithoutManifestReadsTheRelativePath(t *testing.T) {
	selection := newFileSelection(nil, filekinds.Version, nil)
	for path, wantReason := range map[string]string{
		"Single.mkv":      "",
		"Show/01.mkv":     "",
		"Show/SPs/01.mkv": filekinds.ReasonIgnoredNested,
		"Show/readme.nfo": filekinds.ReasonIgnoredKind,
	} {
		if _, reason := selection.classify(artifactFileContext{relativePath: path}); reason != wantReason {
			t.Errorf("%s reason = %q, want %q", path, reason, wantReason)
		}
	}
}

// Retry and recovery classify with the list frozen at evaluation: a version
// this build does not know processes nothing instead of guessing.
func TestFileSelectionUsesTheFrozenKindsVersion(t *testing.T) {
	selection := newFileSelection(manifestInput("Show/01.mkv"), "file-kinds-v999", nil)
	if kind, reason := selection.classify(artifactFileContext{relativePath: "Show/01.mkv"}); kind != filekinds.KindOther || reason != filekinds.ReasonIgnoredKind {
		t.Fatalf("unknown frozen version = %s/%q, want other/ignored_kind", kind, reason)
	}
}
