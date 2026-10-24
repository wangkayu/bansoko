package application

import "testing"

func TestFrozenSubtitleTargetBesideVideo(t *testing.T) {
	video := plannedVideoTarget{directory: "/library/Show/Season 01", filename: "Show - E01.mkv"}
	relative := plannedVideoTarget{directory: "Show/Season 01", filename: "Show - E01.mkv"}
	for _, tc := range []struct {
		name   string
		target string
		video  plannedVideoTarget
		want   bool
	}{
		{"beside video", "/library/Show/Season 01/Show - E01.ass", video, true},
		{"with language suffix", "/library/Show/Season 01/Show - E01.chs.ass", video, true},
		{"relative video has no absolute target", "/library/Show/Season 01/Show - E01.ass", relative, false},
		{"other directory", "/library/Show/Show - E01.ass", video, false},
		{"other basename", "/library/Show/Season 01/Show - E02.ass", video, false},
		{"basename prefix only", "/library/Show/Season 01/Show - E010.ass", video, false},
		{"traversal back into directory", "/library/Show/Season 01/../Season 01/Show - E01.ass", video, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := frozenSubtitleTargetBesideVideo(tc.target, tc.video, "Show - E01"); got != tc.want {
				t.Fatalf("frozenSubtitleTargetBesideVideo(%q) = %t, want %t", tc.target, got, tc.want)
			}
		})
	}
}
