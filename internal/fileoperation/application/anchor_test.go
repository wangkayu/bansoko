package application

import (
	"errors"
	"fmt"
	"testing"
)

// Naming templates render absolute targets, so a relative directory is a
// genuinely invalid target rather than a missing deployment anchor.
func TestRenderedTargetRejectsRelativeDirectory(t *testing.T) {
	_, err := renderedTarget("Anime/Show", "episode.mkv")
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("expected ErrInvalidTarget for a relative rendered directory, got %v", err)
	}
	if code := failureCode(err); code != "target_invalid" {
		t.Fatalf("direct error code = %q, want target_invalid", code)
	}
	wrapped := fmt.Errorf("plan rendered target for file %s: %w", "file-1", err)
	if code := failureCode(wrapped); code != "target_invalid" {
		t.Fatalf("wrapped error code = %q, want target_invalid", code)
	}
}

// An absolute rendered directory is composed as-is; there is no global anchor
// to join.
func TestRenderedTargetComposesAbsoluteDirectory(t *testing.T) {
	target, err := renderedTarget("/library/Anime/Show", "episode.mkv")
	if err != nil {
		t.Fatalf("absolute directory refused: %v", err)
	}
	if want := "/library/Anime/Show/episode.mkv"; target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
}
