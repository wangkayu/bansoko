package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"strings"
	"testing"
)

func TestParserRegulationRemovedVersion(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"規制解除版", "规制解除版"} {
		raw := "【悠哈璃羽字幕社】[腦洞學生會 / 學生會也有洞 / Seitokai ni mo Ana wa Aru][" + marker + "][01v2][1080p HEVC][CHT][繁體內嵌]"
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if firstAttrValue(res, model.AttrContentRating) != "UNCENSORED" || canonicalEpisode(res) != "1" || firstAttrValue(res, model.AttrVersion) != "v2" {
			t.Fatalf("result=%+v", res)
		}
		for _, a := range res.Attributes {
			if (a.Type == model.AttrTitle || a.Type == model.AttrTitleAlias) && strings.Contains(a.Normalized, marker) {
				t.Fatalf("marker leaked into title: %q", a.Normalized)
			}
		}
	}
}

func TestParserBaidongUnderscoreAliases(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	raw := "【百冬练习组】【Re: 从零开始的异世界的生活_Re꞉ Zero kara Hajimeru Isekai Seikatsu】[85][WebRip 1080p AVC AAC][简体]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.CanonicalTitle() != "Re: 从零开始的异世界的生活" {
		t.Fatalf("title=%q", res.CanonicalTitle())
	}
	aliases := canonicalAliases(res)
	if len(aliases) != 1 || aliases[0] != "Re꞉ Zero kara Hajimeru Isekai Seikatsu" {
		t.Fatalf("aliases=%v", aliases)
	}
	if canonicalEpisode(res) != "85" {
		t.Fatalf("episode=%q", canonicalEpisode(res))
	}
}
