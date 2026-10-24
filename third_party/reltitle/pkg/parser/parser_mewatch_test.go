package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserMewatch(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"MW", "meWATCH", "mw"} {
		raw := "[ToonsHub] Magic Knight Rayearth 2026 S01E01 1080p " + platform + " WEB-DL AAC2.0 H.264 (Multi-Subs)"
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if firstAttrValue(res, model.AttrPlatform) != "MW" || firstAttrValue(res, model.AttrSourceType) != "WEB-DL" {
			t.Fatalf("platform/source: %v", res.Attributes)
		}
		if canonicalSeason(res) != "1" || canonicalEpisode(res) != "1" {
			t.Fatalf("episode semantics: %+v", res.EPSemantics)
		}
		if res.CanonicalTitle() != "Magic Knight Rayearth" {
			t.Fatalf("title=%q", res.CanonicalTitle())
		}
	}
}
