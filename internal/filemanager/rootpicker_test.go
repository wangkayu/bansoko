package filemanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRootPickerWalksWithoutAnyRoot is the picker's whole point: it reads
// outside the library roots, so the owner can choose the first one, but it
// still refuses the refusal set and never follows a symbolic link.
func TestRootPickerWalksWithoutAnyRoot(t *testing.T) {
	base := canonicalTemp(t)
	for _, name := range []string{"library", "Photos", "Videos"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(base, "notes.txt"), 1)
	if err := os.Symlink(filepath.Join(base, "library"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(base, "appdata")
	if err := os.MkdirAll(filepath.Join(denied, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No roots at all: a picker bounded by them could never add the first one.
	service := newTestService(t, &fakeStore{}, NewDenialZone(denied))
	ctx := context.Background()

	listing, err := service.RootPicker(ctx, base, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if listing.Path != base || listing.Truncated {
		t.Fatalf("listing = %+v; want the directory itself, untruncated", listing)
	}
	var names []string
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
		if entry.Path != filepath.Join(base, entry.Name) {
			t.Fatalf("entry %q path = %q", entry.Name, entry.Path)
		}
	}
	// Only directories, byte-wise by name: the file, the symlink and the
	// refused directory never appear.
	want := []string{"Photos", "Videos", "library"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v; want %v", names, want)
	}
	for index, name := range want {
		if names[index] != name {
			t.Fatalf("entries = %v; want %v", names, want)
		}
	}

	// The filesystem root itself is a valid starting point.
	root, err := service.RootPicker(ctx, "/", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if root.Path != string(filepath.Separator) {
		t.Fatalf("root path = %q", root.Path)
	}

	// A refused path is refused before it is probed, and never offered.
	_, err = service.RootPicker(ctx, denied, "", 0)
	mustCode(t, err, CodePathDenied)
	_, err = service.RootPicker(ctx, filepath.Join(base, "notes.txt"), "", 0)
	mustCode(t, err, CodeNotADirectory)
	_, err = service.RootPicker(ctx, filepath.Join(base, "missing"), "", 0)
	mustCode(t, err, CodePathNotFound)
	_, err = service.RootPicker(ctx, filepath.Join(base, "alias"), "", 0)
	mustCode(t, err, CodeSymlinkNotFollowed)
	_, err = service.RootPicker(ctx, filepath.Join(base, "alias", "Show"), "", 0)
	mustCode(t, err, CodeSymlinkNotFollowed)
	_, err = service.RootPicker(ctx, "relative/x", "", 0)
	mustCode(t, err, CodePathInvalid)
	_, err = service.RootPicker(ctx, "/a\x00b", "", 0)
	mustCode(t, err, CodePathInvalid)
}

// TestRootPickerPagesByDirectoryName walks every page with a tiny limit and
// checks a cursor only works for the directory it was issued for.
func TestRootPickerPagesByDirectoryName(t *testing.T) {
	base := canonicalTemp(t)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(base, "b")
	service := newTestService(t, &fakeStore{}, NewDenialZone())
	ctx := context.Background()

	var names []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > len(names)+2 {
			t.Fatal("pagination did not terminate")
		}
		page, err := service.RootPicker(ctx, base, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Entries {
			names = append(names, entry.Name)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	want := []string{"a", "b", "c", "d", "e"}
	if len(names) != len(want) {
		t.Fatalf("paged entries = %v; want %v", names, want)
	}
	for index, name := range want {
		if names[index] != name {
			t.Fatalf("paged entries = %v; want %v", names, want)
		}
	}

	first, err := service.RootPicker(ctx, base, "", 1)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	_, err = service.RootPicker(ctx, other, first.NextCursor, 1)
	mustCode(t, err, CodeCursorInvalid)
}
