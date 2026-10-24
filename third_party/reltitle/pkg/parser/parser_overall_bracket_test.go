package parser

import (
	"fmt"
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserOverallBracketEpisodes(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, traditional := range []bool{false, true} {
		group, title, marker := "晚街与灯", "Re：从零开始的异世界生活 第四季", "总第"
		if traditional {
			group, title, marker = "晚街與燈", "Re：從零開始的異世界生活 第四季", "總第"
		}
		for ep := 12; ep <= 19; ep++ {
			raw := fmt.Sprintf("[%s][%s / Re:Zero kara Hajimeru Isekai Seikatsu 4th Season][%d - %s%d][WebRip][1080P_AVC_AAC][简日双语内嵌]", group, title, ep, marker, ep+66)
			t.Run(raw, func(t *testing.T) {
				res, err := p.Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				if canonicalSeason(res) != "4" || canonicalEpisode(res) != fmt.Sprint(ep) || res.ContentKind != model.ContentKindTVEP {
					t.Fatalf("primary=%+v", res.EPSemantics)
				}
				alts := canonicalEPAlternates(res)
				if len(alts) != 1 || alts[0].Axis != model.EPAxisABS || alts[0].Episode != fmt.Sprint(ep+66) || alts[0].Relation != "overall" || alts[0].Season != "" {
					t.Fatalf("alternates=%+v", alts)
				}
			})
		}
	}
}

func TestParserOverallBracketRange(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ group, marker, source, subs string }{
		{"晚街与灯", "总第", "WEB-DL Remux", "简繁日内封PGS"},
		{"晚街与灯", "总第", "WebRip", "简日双语内嵌"},
		{"晚街與燈", "總第", "WebRip", "繁日雙語內嵌"},
	} {
		raw := "[" + tc.group + "][Re：从零开始的异世界生活 第四季丧失篇合集 / Re:Zero kara Hajimeru Isekai Seikatsu 4th - 01~11][" + tc.marker + "67~77][" + tc.source + "][1080P_AVC_AAC][" + tc.subs + "]"
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if canonicalEpisodeRange(res) != "1-11" || canonicalSeason(res) != "4" || res.ContentKind != model.ContentKindBatch {
			t.Fatalf("primary=%+v", res.EPSemantics)
		}
		if res.CanonicalTitle() != "Re:从零开始的异世界生活" {
			t.Fatalf("title=%q", res.CanonicalTitle())
		}
		aliases := canonicalAliases(res)
		if len(aliases) != 1 || aliases[0] != "Re:Zero kara Hajimeru Isekai Seikatsu 4th" {
			t.Fatalf("aliases=%v", aliases)
		}
		if res.TemplateID != "TF_BATCH_RANGE_WGJD_OVERALL_01" {
			t.Fatalf("template=%q", res.TemplateID)
		}
		alts := canonicalEPAlternates(res)
		if len(alts) != 1 || alts[0].Kind != model.EPKindRange || alts[0].Axis != model.EPAxisABS || alts[0].RangeStart != "67" || alts[0].RangeEnd != "77" {
			t.Fatalf("alternates=%+v", alts)
		}
	}
}

func TestOverallBracketGuards(t *testing.T) {
	primary := &model.EPReference{Kind: model.EPKindEP, Axis: model.EPAxisSE, Season: "4", Episode: "19"}
	for _, raw := range []string{"[18 - 总第85]", "[19 - 总第0]", "[总第85的冒险]", "[说明 总第85]", "总第85", "[总第67~77]"} {
		if ref, ok := detectOverallBracketAlternate(raw, primary); ok {
			t.Fatalf("%q: unexpected %+v", raw, ref)
		}
	}
	rangePrimary := &model.EPReference{Kind: model.EPKindRange, Axis: model.EPAxisSE, Season: "4", RangeStart: "1", RangeEnd: "11"}
	for _, raw := range []string{"[总第77~67]", "[总第67~78]", "[19 - 总第85]"} {
		if ref, ok := detectOverallBracketAlternate(raw, rangePrimary); ok {
			t.Fatalf("%q: unexpected %+v", raw, ref)
		}
	}
	for _, raw := range []string{
		"[Other][标题 合集 / Title 4th - 01~11][总第67~77][WebRip]",
		"[晚街与灯][标题 / Title 4th - 01~11][总第67~77][WebRip]",
		"[晚街与灯][标题 合集 / Title 4th - 01~11][WebRip][总第67~77]",
		"[晚街与灯][标题 合集 / Title 4th - 01~11][总第67~78][WebRip]",
	} {
		if ref, ok := detectOverallBracketAlternate(raw, rangePrimary); ok {
			t.Fatalf("invalid batch slot %q: %+v", raw, ref)
		}
	}
}
