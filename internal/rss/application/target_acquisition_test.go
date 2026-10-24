package application

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveTargetPathCanonicalizesRenderedAbsoluteTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "Season 01", "episode.mkv")

	got, err := resolveTargetPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Clean(target); got != want {
		t.Fatalf("resolved target = %q, want canonical %q", got, want)
	}
}

func TestResolveTargetPathRelativeTargetFailsClosed(t *testing.T) {
	if _, err := resolveTargetPath("Season 01/episode.mkv"); !errors.Is(err, errTargetNotAbsolute) {
		t.Fatalf("resolveTargetPath error = %v, want errTargetNotAbsolute", err)
	}
}

func TestResolveTargetPathEmptyTargetFailsClosed(t *testing.T) {
	absolute := filepath.Join(string(filepath.Separator), "library", "episode.mkv")
	if _, err := resolveTargetPath(""); err == nil {
		t.Fatal("an empty rendered target must fail closed")
	}
	if got, err := resolveTargetPath(absolute); err != nil || got != absolute {
		t.Fatalf("absolute target = %q, err = %v, want clean pass-through", got, err)
	}
}
