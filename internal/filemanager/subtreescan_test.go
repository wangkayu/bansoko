package filemanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestListMarksManagedOnlyDirectories: a listing that asks for it marks each
// directory by whether a hide-managed browse has anything to show inside it —
// every file below managed or in progress, at any depth, hides the folder
// itself; one detached file anywhere below keeps it.
func TestListMarksManagedOnlyDirectories(t *testing.T) {
	fixture := newStateFixture(t)
	root := fixture.entriesManaged(t, "")
	// Season holds one managed file; Busy holds one in-progress file below a
	// nested directory: both have nothing a hide-managed browse would show.
	for name := range map[string]bool{"Season": true, "Busy": true} {
		if got := root[name].ManagedOnly; got == nil || !*got {
			t.Fatalf("%s: managed_only = %v, want true", name, root[name].ManagedOnly)
		}
	}
	// Show holds detached files (a retired anime, a superseded placement, a
	// released one in extras); OneShot and Loose hold only detached files.
	for _, name := range []string{"Show", "OneShot", "Loose"} {
		if got := root[name].ManagedOnly; got == nil || *got {
			t.Fatalf("%s: managed_only = %v, want false", name, root[name].ManagedOnly)
		}
	}
	// An empty directory is not managed: it stays visible.
	if root["Empty"].ManagedOnly != nil {
		t.Fatalf("Empty: managed_only = %v, want unset", root["Empty"].ManagedOnly)
	}
	// Files never carry the flag, and a directory holding a released file is
	// not managed only.
	show := fixture.entriesManaged(t, "Show")
	if show["managed.mkv"].ManagedOnly != nil || show["busy.mkv"].ManagedOnly != nil {
		t.Fatalf("files carry no flag: %+v", show["managed.mkv"])
	}
	if got := show["extras"].ManagedOnly; got == nil || *got {
		t.Fatalf("extras: managed_only = %v, want false", show["extras"].ManagedOnly)
	}
	// A listing that did not ask leaves directory flags unset too.
	plain := fixture.entries(t, "")
	if plain["Season"].ManagedOnly != nil || plain["Show"].ManagedOnly != nil {
		t.Fatalf("a plain listing carries no managed-only flags")
	}
}

// TestManagedOnlyScanFailsOpenBeyondItsCap: a subtree larger than the scan
// bound keeps the folder visible instead of walking it all.
func TestManagedOnlyScanFailsOpenBeyondItsCap(t *testing.T) {
	base := canonicalTemp(t)
	library := filepath.Join(base, "library")
	var placements []PlacementFact
	for _, name := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		writeFile(t, filepath.Join(library, "Season", name), 2)
		placements = append(placements, activePlacement("p-"+name, filepath.Join(library, "Season", name), "anime-1"))
	}
	service := newTestService(t, &fakeStore{placements: placements}, NewDenialZone(filepath.Join(library, "appdata")), library)
	service.subtreeScanLimit = 2
	entries, err := service.List(context.Background(), library, "", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if entries.Entries[0].Name != "Season" || entries.Entries[0].ManagedOnly != nil {
		t.Fatalf("an over-cap subtree stays visible: %+v", entries.Entries[0])
	}
	// Below the cap the same subtree is decided: every file is managed.
	service.subtreeScanLimit = 10
	entries, err = service.List(context.Background(), library, "", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := entries.Entries[0].ManagedOnly; got == nil || !*got {
		t.Fatalf("managed_only = %v, want true", entries.Entries[0].ManagedOnly)
	}
}

// TestManagedOnlyScanTreatsASymlinkAsOneLeaf: a link is never descended into,
// and it is judged by the facts of its own path and its target, exactly as its
// row would be.
func TestManagedOnlyScanTreatsASymlinkAsOneLeaf(t *testing.T) {
	base := canonicalTemp(t)
	library := filepath.Join(base, "library")
	writeFile(t, filepath.Join(library, "Season", "ep01.mkv"), 2)
	writeFile(t, filepath.Join(library, "Loose", "note.txt"), 2)
	if err := os.Symlink(filepath.Join(library, "Season", "ep01.mkv"), filepath.Join(library, "Season", "link.mkv")); err != nil {
		t.Fatal(err)
	}
	season := activePlacement("p-1", filepath.Join(library, "Season", "ep01.mkv"), "anime-1")
	service := newTestService(t, &fakeStore{placements: []PlacementFact{season}}, NewDenialZone(filepath.Join(library, "appdata")), library)
	entries, err := service.List(context.Background(), library, "", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Entry{}
	for _, entry := range entries.Entries {
		byName[entry.Name] = entry
	}
	// The link's row would be locked (the facts of its target manage it), so
	// Season has nothing to show: managed only.
	if got := byName["Season"].ManagedOnly; got == nil || !*got {
		t.Fatalf("Season: managed_only = %v, want true", byName["Season"].ManagedOnly)
	}
	// A plain note keeps its folder visible.
	if got := byName["Loose"].ManagedOnly; got == nil || *got {
		t.Fatalf("Loose: managed_only = %v, want false", byName["Loose"].ManagedOnly)
	}
}
