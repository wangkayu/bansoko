package buildinfo

import "testing"

func TestCurrentUsesLinkedBuildDirtyMarker(t *testing.T) {
	originalDirty, originalMarker := Dirty, BuildDirty
	t.Cleanup(func() {
		Dirty = originalDirty
		BuildDirty = originalMarker
	})

	Dirty = false
	BuildDirty = "false"
	if current := Current(); current.Dirty {
		t.Fatalf("clean linked build was reported dirty: %+v", current)
	}

	BuildDirty = "true"
	if current := Current(); !current.Dirty {
		t.Fatalf("dirty development build was reported clean: %+v", current)
	}
}
