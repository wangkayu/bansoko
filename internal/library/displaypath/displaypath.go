// Package displaypath projects absolute library paths onto the relative form
// the product shows. The system has no configured
// library root, so the root is derived from the paths themselves: the deepest
// directory holding every given path, raised until each path keeps at least
// its last MinSegments segments (anime folder / season folder / file) below
// it. A single file, or files of one season folder, therefore still show
// their anime and season folders instead of a bare file name. It is pure: no
// filesystem access.
package displaypath

import (
	"path/filepath"
	"strings"
)

// MinSegments is the number of trailing path segments every relative path
// keeps when the path has that many: anime folder / season folder / file.
const MinSegments = 3

// Root is the display root of the given absolute paths ("" when there are
// none; "/" when they share nothing else).
func Root(paths []string) string {
	var common []string
	seen := false
	shortest := -1
	for _, path := range paths {
		parts := segments(path)
		if len(parts) == 0 {
			continue
		}
		dir := parts[:len(parts)-1]
		if !seen {
			common = append([]string(nil), dir...)
			seen = true
		} else {
			n := 0
			for n < len(common) && n < len(dir) && common[n] == dir[n] {
				n++
			}
			common = common[:n]
		}
		// The root may sit at most len(parts)-MinSegments segments deep so
		// this path keeps MinSegments segments below it.
		limit := len(parts) - MinSegments
		if limit < 0 {
			limit = 0
		}
		if shortest < 0 || limit < shortest {
			shortest = limit
		}
	}
	if !seen {
		return ""
	}
	if len(common) > shortest {
		common = common[:shortest]
	}
	return "/" + strings.Join(common, "/")
}

// Relative is path relative to root. A path outside root keeps its last
// MinSegments segments, so no absolute path is ever returned.
func Relative(root, path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	switch {
	case root == "/":
		return strings.TrimPrefix(clean, "/")
	case root != "":
		if rel, ok := strings.CutPrefix(clean, strings.TrimSuffix(root, "/")+"/"); ok && rel != "" {
			return rel
		}
	}
	parts := segments(clean)
	if len(parts) > MinSegments {
		parts = parts[len(parts)-MinSegments:]
	}
	return strings.Join(parts, "/")
}

func segments(path string) []string {
	trimmed := strings.Trim(filepath.Clean(strings.TrimSpace(path)), "/")
	if strings.TrimSpace(path) == "" || trimmed == "" || trimmed == "." {
		return nil
	}
	return strings.Split(trimmed, "/")
}
