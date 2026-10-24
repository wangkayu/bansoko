// Package worktree derives a work's tree top: the deepest directory that
// contains every file the work's own active placements put on disk, as the
// placement evidence records it.
//
// The tree top is the anchor the product expresses a work against — the
// directory a maintenance scan walks, the root a delivery route drops new
// files into, and the base an unclassified import is placed under. Those
// callers share this derivation so they cannot disagree about where a work
// lives, and none of them substitutes a configured root of its own.
//
// A work with no placement evidence has no tree top. Callers must not invent
// one: no evidence means unknown, not a default.
package worktree

import (
	"path/filepath"
	"strings"
)

// Top returns the deepest directory containing every path in paths, where the
// paths are the work's own files.
//
// ok=false means paths held no usable absolute file path, or the only
// directory common to them is the filesystem root. Either way the work has no
// tree of its own and the caller must not substitute one.
func Top(paths []string) (string, bool) {
	var common []string
	matched := false
	for _, path := range paths {
		cleaned, ok := cleanAbsolute(path)
		if !ok {
			continue
		}
		segments := splitAbsolute(filepath.Dir(cleaned))
		if !matched {
			common, matched = segments, true
			continue
		}
		common = commonPrefix(common, segments)
	}
	if !matched || len(common) == 0 {
		return "", false
	}
	return absoluteFromSegments(common), true
}

// cleanAbsolute validates and cleans an absolute path without touching the
// filesystem: an empty value, an embedded NUL and a relative path are refused,
// and the result is Clean'ed so ".." can only move the path toward the root.
func cleanAbsolute(value string) (string, bool) {
	if value == "" || strings.IndexByte(value, 0) >= 0 || !filepath.IsAbs(value) {
		return "", false
	}
	return filepath.Clean(value), true
}

// splitAbsolute splits a Clean'ed absolute path into its segment names.
func splitAbsolute(cleaned string) []string {
	volume := filepath.VolumeName(cleaned)
	rest := strings.TrimPrefix(cleaned[len(volume):], string(filepath.Separator))
	if rest == "" {
		return nil
	}
	return strings.Split(rest, string(filepath.Separator))
}

func absoluteFromSegments(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	return string(filepath.Separator) + strings.Join(segments, string(filepath.Separator))
}

func commonPrefix(left, right []string) []string {
	length := 0
	for length < len(left) && length < len(right) && left[length] == right[length] {
		length++
	}
	return left[:length]
}
