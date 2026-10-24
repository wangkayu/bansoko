package naming

import "testing"

// CompanionSidecarFilename is the one composition rule for a subtitle that
// follows a video; the placement stage and the RSS preview both call it, so
// these cases are the shared contract: the suffix only joins when the rules
// rendered one, and the subtitle's extension always replaces the video's.
func TestCompanionSidecarFilename(t *testing.T) {
	for _, tc := range []struct {
		name          string
		videoBasename string
		suffix        string
		subtitleExt   string
		want          string
	}{
		{name: "rendered suffix", videoBasename: "Show - E01", suffix: "sc", subtitleExt: "ass", want: "Show - E01.sc.ass"},
		{name: "empty suffix adds no separator", videoBasename: "Show - E01", suffix: "", subtitleExt: "ass", want: "Show - E01.ass"},
		{name: "subtitle extension replaces the video's", videoBasename: "Show - E01", suffix: "und", subtitleExt: "mkv", want: "Show - E01.und.mkv"},
		{name: "multi-part suffix", videoBasename: "Show - E01 [1080p]@Group", suffix: "chs&jpn", subtitleExt: "ass", want: "Show - E01 [1080p]@Group.chs&jpn.ass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompanionSidecarFilename(tc.videoBasename, tc.suffix, tc.subtitleExt); got != tc.want {
				t.Fatalf("CompanionSidecarFilename(%q, %q, %q) = %q, want %q", tc.videoBasename, tc.suffix, tc.subtitleExt, got, tc.want)
			}
		})
	}
}
