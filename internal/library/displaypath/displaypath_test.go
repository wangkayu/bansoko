package displaypath

import "testing"

func TestRootKeepsAnimeAndSeasonFolders(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		root  string
	}{
		{"none", nil, ""},
		{"single file", []string{"/media/anime/Title/Season 01/Title - 01.mkv"}, "/media/anime"},
		{"one season", []string{"/media/anime/Title/Season 01/a.mkv", "/media/anime/Title/Season 01/b.mkv"}, "/media/anime"},
		{"two seasons", []string{"/media/anime/Title/Season 01/a.mkv", "/media/anime/Title/Season 02/b.mkv"}, "/media/anime"},
		{"renamed anime folder", []string{"/lib/T 2021/Season 02/a.mkv", "/lib/T （2021）/Season 02/a.mkv"}, "/lib"},
		{"deeper library", []string{"/lib/anime/T/S1/a.mkv", "/lib/anime/T/S1/sub/b.mkv"}, "/lib/anime"},
		{"shallow path", []string{"/a/b.mkv"}, "/"},
		{"different libraries", []string{"/x/T/S/a.mkv", "/y/T/S/a.mkv"}, "/"},
		{"blank ignored", []string{"", "/m/T/S/a.mkv"}, "/m"},
	}
	for _, tc := range cases {
		if got := Root(tc.paths); got != tc.root {
			t.Errorf("%s: Root = %q, want %q", tc.name, got, tc.root)
		}
	}
}

func TestRelative(t *testing.T) {
	cases := []struct{ root, path, want string }{
		{"/media/anime", "/media/anime/Title/Season 01/a.mkv", "Title/Season 01/a.mkv"},
		{"/", "/a/b.mkv", "a/b.mkv"},
		{"/media/anime", "/elsewhere/deep/Title/Season 01/a.mkv", "Title/Season 01/a.mkv"},
		{"", "/elsewhere/Title/Season 01/a.mkv", "Title/Season 01/a.mkv"},
		{"/media/anime/", "/media/anime/T/S/a.mkv", "T/S/a.mkv"},
	}
	for _, tc := range cases {
		if got := Relative(tc.root, tc.path); got != tc.want {
			t.Errorf("Relative(%q, %q) = %q, want %q", tc.root, tc.path, got, tc.want)
		}
	}
}
