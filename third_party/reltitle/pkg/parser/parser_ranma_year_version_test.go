package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"strings"
	"testing"
)

func TestParserRanmaYearVersion(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"[喵萌奶茶屋&LoliHouse] 乱马 1/2 2024年版 / Ranma ½ / Ranma 1/2 (2024) - 25 [WebRip 1080p HEVC-10bit AAC][简繁日内封字幕]",
		"【喵萌奶茶屋】★10月新番★[乱马 1/2 2024年版 / Ranma ½ / Ranma 1/2 (2024)][25][1080p][简日双语][招募翻译]",
	} {
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if res.CanonicalTitle() != "乱马1/2" || canonicalEpisode(res) != "25" || firstAttrValue(res, model.AttrYear) != "2024" {
			t.Fatalf("title=%q ep=%q year=%q", res.CanonicalTitle(), canonicalEpisode(res), firstAttrValue(res, model.AttrYear))
		}
		aliases := canonicalAliases(res)
		if len(aliases) != 2 || aliases[0] != "Ranma ½" || aliases[1] != "Ranma 1/2" {
			t.Fatalf("aliases=%v", aliases)
		}
		for _, a := range res.Attributes {
			if (a.Type == model.AttrTitle || a.Type == model.AttrTitleAlias) && strings.Contains(a.Normalized, "年版") {
				t.Fatalf("year marker leaked: %q", a.Normalized)
			}
		}
	}
}

func TestExtractYearVersionMarker(t *testing.T) {
	tokens := []model.Token{{Text: "作品"}, {Text: "2024年版"}}
	attrs := []model.Attribute{}
	extractYearAttrsAndRemoveFromTokens(tokens, &attrs, false)
	if len(attrs) != 1 || attrs[0].Normalized != "2024" || !tokens[1].Removed {
		t.Fatalf("tokens=%v attrs=%v", tokens, attrs)
	}
}
