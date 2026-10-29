package filemaintenance

import (
	"strings"
	"testing"
)

func TestCompanionPathDerivesFromVideo(t *testing.T) {
	cases := []struct {
		video, suffix, ext, want string
	}{
		{"/lib/Show/Season 01/Show - S01E01.mkv", "zh-Hans", "ass", "/lib/Show/Season 01/Show - S01E01.zh-Hans.ass"},
		{"/lib/Show/Season 01/Show - S01E01.mkv", "", "nfo", "/lib/Show/Season 01/Show - S01E01.nfo"},
		{"/lib/Show/Show.v2.mp4", "sc", "srt", "/lib/Show/Show.v2.sc.srt"},
	}
	for _, c := range cases {
		if got := CompanionPath(c.video, c.suffix, c.ext); got != c.want {
			t.Fatalf("CompanionPath(%q,%q,%q)=%q want %q", c.video, c.suffix, c.ext, got, c.want)
		}
	}
}

func TestSplitCompanionNameRoundTrips(t *testing.T) {
	video := "/lib/Show/Season 01/Show - S01E01.mkv"
	cases := []struct {
		sidecar, suffix, ext string
		ok                   bool
	}{
		{"/lib/Show/Season 01/Show - S01E01.zh-Hans.ass", "zh-Hans", "ass", true},
		{"/lib/Show/Season 01/Show - S01E01.nfo", "", "nfo", true},
		{"/lib/Show/Season 01/Show - S01E01.tc.forced.srt", "tc.forced", "srt", true},
		{"/lib/Show/Season 02/Show - S01E01.ass", "", "", false},
		{"/lib/Show/Season 01/Other - S01E01.ass", "", "", false},
		{"/lib/Show/Season 01/Show - S01E01", "", "", false},
	}
	for _, c := range cases {
		suffix, ext, ok := splitCompanionName(video, c.sidecar)
		if ok != c.ok || suffix != c.suffix || ext != c.ext {
			t.Fatalf("splitCompanionName(%q)=(%q,%q,%v) want (%q,%q,%v)", c.sidecar, suffix, ext, ok, c.suffix, c.ext, c.ok)
		}
		if ok && CompanionPath(video, suffix, ext) != c.sidecar {
			t.Fatalf("split/compose must round-trip for %q", c.sidecar)
		}
	}
}

func TestCompanionFingerprintNormalizesStoredForms(t *testing.T) {
	const value = "meta:size=12;dev=3;ino=44"
	for _, input := range []string{
		value,
		" " + value + " ",
		`{"algorithm":"metadata","value":"` + value + `","size_bytes":12}`,
	} {
		if got := companionFingerprint(input); got != value {
			t.Fatalf("companionFingerprint(%q)=%q", input, got)
		}
	}
	if got := companionFingerprint(`{"algorithm":"metadata","value":"meta:size=12","size_bytes":12}`); got != "meta:size=12" {
		t.Fatalf("a size-only metadata fingerprint must normalize, got %q", got)
	}
	for _, input := range []string{
		strings.Repeat("ab", 32),
		`{"algorithm":"sha256","value":"` + strings.Repeat("ab", 32) + `","size_bytes":12}`,
		`{"algorithm":"md5","value":"x"}`,
		"meta:size=x",
		"",
	} {
		if got := companionFingerprint(input); got != "" {
			t.Fatalf("companionFingerprint(%q)=%q, want rejection", input, got)
		}
	}
	if companionID("op-1") == companionID("op-2") || !strings.HasPrefix(companionID("op-1"), "cmp-") {
		t.Fatal("companion ids must be stable and distinct per source ref")
	}
}
