package application

import "testing"

// File rules match the filename of a first-level video, never the relative
// path, so a directory or the torrent name can neither satisfy must_contain nor
// trip not_in.
func TestOrganizationFilterMatchesFilenameOnly(t *testing.T) {
	rules := fileOperationFileFilterRules{MustContain: `^01`, NotIn: `sample`}
	for _, c := range []struct {
		relative, absolute string
		allowed            bool
	}{
		{"Show/01.mkv", "/downloads/extras/01.mkv", true},
		{"Show/02.mkv", "/downloads/main/01.mkv", false},
		{"Show/01 sample.mkv", "/downloads/sample.mkv", false},
		{"sample Show/01.mkv", "/downloads/sample/01.mkv", true},
		{"../01.mkv", "/downloads/01.mkv", false},
	} {
		if got := fileAllowedByFilter(artifactFileContext{relativePath: c.relative, sourcePath: c.absolute}, rules); got != c.allowed {
			t.Errorf("%s allowed=%v want %v", c.relative, got, c.allowed)
		}
	}
}
