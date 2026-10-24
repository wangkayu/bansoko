package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

// A release version after the episode ("01v2") must not stop "Season 3 - 01v2" from being
// recognized as a repeated season marker followed by an episode. It used to be read as the
// episode range 3-1 (BATCH) when the title also carried "第三季".
func TestParserSeasonEchoWithVersionSuffix(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	tests := []struct {
		raw     string
		episode string
	}{
		{"[ToonsHub] 药屋少女的呢喃 第三季 / The Apothecary Diaries Season 3 - 01v2 [CR][1080P][MKV]", "1"},
		{"[G] 药屋 第三季 / Apo Season 3 - 05v2 [1080P]", "5"},
		{"[G] 药屋 第三季 / Apo Season 3 - 05 [1080P]", "5"},
	}
	for _, tc := range tests {
		res, err := p.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := res.ContentKind; got != "TV_EP" {
			t.Errorf("%q: content_kind=%q want TV_EP", tc.raw, got)
		}
		if got := canonicalSeason(res); got != "3" {
			t.Errorf("%q: season=%q want 3", tc.raw, got)
		}
		if got := canonicalEpisode(res); got != tc.episode {
			t.Errorf("%q: episode=%q want %q", tc.raw, got, tc.episode)
		}
	}
}

// A real range with a version suffix on the right is still a range.
func TestParserRangeWithVersionSuffixStaysRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	res, err := New(cfg).Parse("[G] Show 01-12v2 [1080P]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want 1-12", got)
	}
}
