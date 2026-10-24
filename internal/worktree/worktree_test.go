package worktree

import "testing"

// The tree top is the anchor every consumer expresses a work against (the
// scan's scope, the delivery route's drop directory, the base an
// unclassified import is placed under), so it is derived from placement
// evidence alone and never defaulted.
func TestTopTakesTheDeepestCommonDirectory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		want  string
		ok    bool
	}{
		{name: "one file", paths: []string{"/media/Show/a.mkv"}, want: "/media/Show", ok: true},
		{
			name:  "seasons under one work",
			paths: []string{"/media/Show/Season 01/a.mkv", "/media/Show/Season 02/b.mkv"},
			want:  "/media/Show", ok: true,
		},
		{
			name:  "segments, not string prefixes",
			paths: []string{"/media/Show/a.mkv", "/media/ShowB/b.mkv"},
			want:  "/media", ok: true,
		},
		{
			name:  "a sibling moved away narrows the tree",
			paths: []string{"/media/Show/a.mkv", "/media/Show/Extras/b.mkv", "/media/Show/Extras/c.mkv"},
			want:  "/media/Show", ok: true,
		},
		{
			name:  "only the filesystem root is shared",
			paths: []string{"/media/a.mkv", "/other/b.mkv"},
			ok:    false,
		},
		{
			name:  "top-level files share only the filesystem root",
			paths: []string{"/a.mkv", "/b.mkv"},
			ok:    false,
		},
		{name: "no evidence", paths: nil, ok: false},
		{name: "empty evidence", paths: []string{""}, ok: false},
		{name: "relative paths are not evidence", paths: []string{"Show/a.mkv"}, ok: false},
		{
			name:  "relative evidence is ignored, absolute evidence decides",
			paths: []string{"Show/a.mkv", "/media/Show/b.mkv"},
			want:  "/media/Show", ok: true,
		},
		{
			name:  "an embedded NUL is refused",
			paths: []string{"/media/Show/a\x00.mkv"},
			ok:    false,
		},
		{
			name:  "the path is cleaned before it is compared",
			paths: []string{"/media/Show/../Other/a.mkv"},
			want:  "/media/Other", ok: true,
		},
		{
			name:  "a traversing segment cannot escape the root",
			paths: []string{"/../media/Show/a.mkv"},
			want:  "/media/Show", ok: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Top(tc.paths)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("Top(%v)=%q,%v want %q,%v", tc.paths, got, ok, tc.want, tc.ok)
			}
		})
	}
}
