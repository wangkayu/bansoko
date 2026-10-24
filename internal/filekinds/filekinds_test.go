package filekinds

import "testing"

func TestClassifyUsesTheFrozenList(t *testing.T) {
	for name, want := range map[string]string{
		"Show - 01.MKV": KindVideo, "a.mp4": KindVideo, "a.webm": KindVideo, "a.ts": KindVideo,
		"a.chs&cht.ass": KindSubtitle, "a.srt": KindSubtitle, "a.VTT": KindSubtitle,
		"a.nfo": KindOther, "cover.jpg": KindOther, "a.mka": KindOther, "Show.mkv.torrent": KindOther, "noext": KindOther,
	} {
		if got := Classify(VersionV1, name); got != want {
			t.Errorf("Classify(%q) = %q, want %q", name, got, want)
		}
	}
	// A version this build does not know cannot be trusted: nothing is a
	// video or subtitle under it.
	if got := Classify("file-kinds-v999", "a.mkv"); got != KindOther {
		t.Fatalf("unknown frozen version classified a.mkv as %q", got)
	}
	if Known("file-kinds-v999") || !Known(Version) {
		t.Fatal("Known must accept only registered versions")
	}
	if Classify("", "a.mkv") != KindVideo {
		t.Fatal("an empty version reads the current list")
	}
}

func TestFirstLevel(t *testing.T) {
	for path, want := range map[string]bool{
		"Show.mkv":                true,  // single-file torrent: torrent.name itself
		"Show/Show - 01.mkv":      true,  // multi-file torrent: torrent.name/<file>
		"Show/SPs/NCOP.mkv":       false, // nested
		"Show/Season 1/ep/01.mkv": false,
		`Show\Show - 01.mkv`:      true,
		`Show\SPs\NCOP.mkv`:       false,
		"":                        false,
	} {
		if got := FirstLevel(path); got != want {
			t.Errorf("FirstLevel(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestEligibility(t *testing.T) {
	for _, tc := range []struct{ path, kind, reason string }{
		{"Show/Show - 01.mkv", KindVideo, ""},
		{"Show/Show - 01.chs.ass", KindSubtitle, ""},
		{"Show/cover.jpg", KindOther, ReasonIgnoredKind},
		{"Show/SPs/NCOP.mkv", KindVideo, ReasonIgnoredNested},
		{"Show/SPs/cover.jpg", KindOther, ReasonIgnoredNested},
		{"Single.mkv", KindVideo, ""},
	} {
		kind, reason := Eligibility(Version, tc.path)
		if kind != tc.kind || reason != tc.reason {
			t.Errorf("Eligibility(%q) = %q,%q want %q,%q", tc.path, kind, reason, tc.kind, tc.reason)
		}
	}
}

func TestBindSubtitleLongestPrefixThenFirst(t *testing.T) {
	videos := []string{"Show - 01", "Show - 01.v2", "Show - 02"}
	cases := map[string]int{
		"Show - 01.zh.ass":    0,
		"Show - 01.v2.zh.ass": 1,
		"Show - 02.ass":       2,
		"Show - 011.ass":      -1,
		"notes.ass":           -1,
	}
	for name, want := range cases {
		if got := BindSubtitle(name, videos); got != want {
			t.Fatalf("BindSubtitle(%q) = %d, want %d", name, got, want)
		}
	}
	if got := BindSubtitle("a.ass", []string{"a", "a"}); got != 0 {
		t.Fatalf("a tie follows the first video, got %d", got)
	}
}
