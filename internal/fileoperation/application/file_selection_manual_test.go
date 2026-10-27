package application

import (
	"testing"

	"bansoko/internal/filekinds"
)

// Under a frozen manual selection the selection replaces the first-level gate:
// a chosen deep video and its subtitle are placed, an unchosen video or
// subtitle is ignored_unselected, and a file of another kind stays ignored by
// kind even when chosen.
func TestFileSelectionManualSelectionPlacesChosenDeepFiles(t *testing.T) {
	manifest := manifestInput("Pack/Show - 01.mkv", "Pack/S2/Show - 01.mkv", "Pack/S2/Show - 01.chs.ass", "Pack/S2/cover.jpg")
	selected := filekinds.NewSelection([]string{"Pack/S2/Show - 01.mkv", "Pack/S2/Show - 01.chs.ass", "Pack/S2/cover.jpg"})
	selection := newFileSelection(manifest, filekinds.Version, selected)
	for _, tc := range []struct{ path, kind, reason string }{
		{"Pack/S2/Show - 01.mkv", filekinds.KindVideo, ""},
		{"Pack/S2/Show - 01.chs.ass", filekinds.KindSubtitle, ""},
		{"Pack/Show - 01.mkv", filekinds.KindVideo, filekinds.ReasonIgnoredUnselected},
		{"Pack/S2/cover.jpg", filekinds.KindOther, filekinds.ReasonIgnoredKind},
		// A client that did not create the torrent folder: the local path maps
		// back to the manifest through the torrent root.
		{"S2/Show - 01.mkv", filekinds.KindVideo, ""},
		{"S2/Show - 01.chs.ass", filekinds.KindSubtitle, ""},
		{"Show - 01.mkv", filekinds.KindVideo, filekinds.ReasonIgnoredUnselected},
		// Not in the manifest: fails closed.
		{"Pack/S2/stray.mkv", filekinds.KindVideo, filekinds.ReasonIgnoredUnselected},
	} {
		kind, reason := selection.classify(artifactFileContext{relativePath: tc.path})
		if kind != tc.kind || reason != tc.reason {
			t.Errorf("%s = %s/%q, want %s/%q", tc.path, kind, reason, tc.kind, tc.reason)
		}
	}
}

// A local path both a first-level and a deep manifest entry end with maps to
// the torrent root's own entry, not to an ambiguous suffix match.
func TestFileSelectionMapsThroughTheTorrentRootBeforeTheSuffix(t *testing.T) {
	manifest := manifestInput("Pack/Show - 01.mkv", "Pack/S2/Show - 01.mkv")
	defaults := newFileSelection(manifest, filekinds.Version, nil)
	if path, ok := defaults.manifestPath("Show - 01.mkv"); !ok || path != "Pack/Show - 01.mkv" {
		t.Fatalf("Show - 01.mkv maps to %q (%v), want the root entry", path, ok)
	}
	if _, reason := defaults.classify(artifactFileContext{relativePath: "Show - 01.mkv"}); reason != "" {
		t.Fatalf("the first-level file without the torrent folder = %q, want eligible", reason)
	}
	deepOnly := newFileSelection(manifest, filekinds.Version, filekinds.NewSelection([]string{"Pack/S2/Show - 01.mkv"}))
	if _, reason := deepOnly.classify(artifactFileContext{relativePath: "Show - 01.mkv"}); reason != filekinds.ReasonIgnoredUnselected {
		t.Fatalf("the unchosen first-level file = %q, want ignored_unselected", reason)
	}
	if _, reason := deepOnly.classify(artifactFileContext{relativePath: "S2/Show - 01.mkv"}); reason != "" {
		t.Fatalf("the chosen deep file = %q, want eligible", reason)
	}
	// Without a shared root the suffix is the only mapping, and two matches
	// still fail closed.
	rootless := newFileSelection(manifestInput("A/Show - 01.mkv", "B/Show - 01.mkv"), filekinds.Version, nil)
	if _, ok := rootless.manifestPath("Show - 01.mkv"); ok {
		t.Fatal("an ambiguous suffix mapped")
	}
}

// A subtitle binds only to a video of its own torrent folder.
func TestSubtitleBindingStaysInItsFolder(t *testing.T) {
	video := fileBindingFacts(artifactFileContext{fileRef: "v", relativePath: "Pack/A/Show - 01.mkv", sourcePath: "/downloads/Pack/A/Show - 01.mkv"})
	same := fileBindingFacts(artifactFileContext{fileRef: "s1", relativePath: "Pack/A/Show - 01.chs.ass", sourcePath: "/downloads/Pack/A/Show - 01.chs.ass"})
	other := fileBindingFacts(artifactFileContext{fileRef: "s2", relativePath: "Pack/B/Show - 01.chs.ass", sourcePath: "/downloads/Pack/B/Show - 01.chs.ass"})
	if decision := decideSubtitleBinding(same, []bindingFileFacts{video}); decision.state != bindingStateBound || decision.boundRef != "v" {
		t.Fatalf("same-folder subtitle = %+v, want bound to v", decision)
	}
	if decision := decideSubtitleBinding(other, []bindingFileFacts{video}); decision.state != bindingStateMissing {
		t.Fatalf("other-folder subtitle = %+v, want unbound", decision)
	}
}
