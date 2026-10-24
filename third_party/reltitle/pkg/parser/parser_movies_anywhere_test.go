package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserMoviesAnywhere(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, raw, platform, source string
	}{
		{"nest_movie", "[NEST] 剧场版 链锯人 蕾塞篇 / 劇場版 チェンソーマン レゼ篇 [MA WEB-DL 2160p HEVC DDP5.1 Atmos][简繁日内封]", "MA", "WEB-DL"},
		{"dot_separated", "[NEST] Sample.Movie.2160p.MA.WEB-DL.HEVC.DDP5.1.Atmos", "MA", "WEB-DL"},
		{"web_dl_spaces", "[NEST] Sample Movie [ma web dl 1080p AVC]", "MA", "WEB-DL"},
		{"webdl", "[NEST] Sample Movie [MA WEBDL 1080p AVC]", "MA", "WEB-DL"},
		{"webrip", "[NEST] Sample Movie [MA WEBRip 1080p AVC]", "MA", "WEBRip"},
		{"web_rip_spaces", "[NEST] Sample Movie [MA WEB Rip 1080p AVC]", "MA", "WEBRip"},
		{"web_rip_hyphen", "[NEST] Sample Movie [MA WEB-Rip 1080p AVC]", "MA", "WEBRip"},
		{"full_name", "[NEST] Sample Movie [Movies Anywhere WEB-DL 1080p AVC]", "MA", "WEB-DL"},
		{"master_audio_bluray", "[NEST] Sample Movie [BluRay 2160p DTS-HD MA 5.1 HEVC]", "", "BluRay"},
		{"master_audio_web", "[NEST] Sample Movie [WEB-DL 2160p DTS-HD MA 5.1 HEVC]", "", "WEB-DL"},
		{"master_audio_dots", "[NEST] Sample.Movie.2160p.BluRay.DTS-HD.MA.5.1.HEVC", "", "BluRay"},
		{"title_word", "[NEST] Ma - 01 [WEB-DL 1080p AVC]", "", "WEB-DL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := firstAttrValue(res, model.AttrPlatform); got != tc.platform {
				t.Fatalf("platform=%q want=%q", got, tc.platform)
			}
			if got := firstAttrValue(res, model.AttrSourceType); got != tc.source {
				t.Fatalf("source=%q want=%q", got, tc.source)
			}
			if tc.name == "nest_movie" {
				if res.ContentKind != model.ContentKindMovie {
					t.Fatalf("content kind=%q want MOVIE", res.ContentKind)
				}
				if got := firstAttrValue(res, model.AttrTitleAlias); got != "劇場版 チェンソーマン レゼ篇" {
					t.Fatalf("alias=%q", got)
				}
			}
		})
	}
}
