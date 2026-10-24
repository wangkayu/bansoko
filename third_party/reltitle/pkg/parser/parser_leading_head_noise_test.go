package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

func TestParserNoiseWrapper_SkipsLeadingRelayNoiseBracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "simplified",
			raw:  "[代发][羊圈个人译制] 主播女孩重度依赖 NEEDY GIRL OVERDOES - 01 [WebRip 1080P AVC 8bit AAC MP4][简日内嵌]",
		},
		{
			name: "traditional",
			raw:  "[代發][羊圈个人译制] 主播女孩重度依赖 NEEDY GIRL OVERDOES - 01 [WebRip 1080P AVC 8bit AAC MP4][简日内嵌]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if got := canonicalGroup(res); got != "羊圈个人译制" {
				t.Fatalf("group=%q want=%q", got, "羊圈个人译制")
			}
			if got := canonicalTitle(res); strings.Contains(got, "代发") || strings.Contains(got, "代發") {
				t.Fatalf("title leaked relay noise: %q", got)
			}
			if got := canonicalTitle(res); strings.Contains(got, "羊圈个人译制") {
				t.Fatalf("title should not retain real group text: %q", got)
			}
			if got := canonicalEpisode(res); got != "1" {
				t.Fatalf("episode=%q want=%q", got, "1")
			}
			if aliases := canonicalAliases(res); !containsAlias(aliases, "NEEDY GIRL OVERDOES") {
				t.Fatalf("expected english alias preserved, got=%v", aliases)
			}
		})
	}
}
