package application

import (
	"testing"

	"bansoko/internal/filekinds"
)

// A local artifact path counts as selected only when it maps to a selected
// manifest path: through the exact entry, or through the torrent root when the
// client did not create the torrent folder. A rootless local name that also
// ends a deeper selected path is the unselected first-level file, not it.
func TestSelectedArtifactFileMapsThroughTheManifest(t *testing.T) {
	manifest := map[string]struct{}{"Pack/Show - 01.mkv": {}, "Pack/S2/Show - 01.mkv": {}, "Pack/S2/Show - 02.mkv": {}}
	selection := filekinds.NewSelection([]string{"Pack/S2/Show - 01.mkv"})
	for path, want := range map[string]bool{
		"Pack/S2/Show - 01.mkv": true,
		"S2/Show - 01.mkv":      true,
		`S2\Show - 01.mkv`:      true,
		"Show - 01.mkv":         false,
		"Pack/Show - 01.mkv":    false,
		"S2/Show - 02.mkv":      false,
		"stray.mkv":             false,
		"":                      false,
	} {
		if got := selectedArtifactFile(selection, manifest, path); got != want {
			t.Errorf("%q selected = %v, want %v", path, got, want)
		}
	}
	if selectedArtifactFile(nil, manifest, "Pack/S2/Show - 01.mkv") {
		t.Fatal("no selection selects nothing")
	}
}
