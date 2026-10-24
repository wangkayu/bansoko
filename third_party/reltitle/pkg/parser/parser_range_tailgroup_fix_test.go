package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserEpisodeRange_FrenchConnectorAndTailBracketGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Galaxy Express 999 - 038 à 045 [VOSTFR][1080p][SnC-Fansub]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalEpisodeRange(res); got != "38-45" {
		t.Fatalf("episode_range=%q want=%q", got, "38-45")
	}
	if got := canonicalGroup(res); got != "SnC-Fansub" {
		t.Fatalf("group=%q want=%q", got, "SnC-Fansub")
	}
	if got := firstAttrValue(res, model.AttrTailGroup); got != "SnC-Fansub" {
		t.Fatalf("tail_group=%q want=%q", got, "SnC-Fansub")
	}
	if got := canonicalTitle(res); got != "Galaxy Express 999" {
		t.Fatalf("title=%q want=%q", got, "Galaxy Express 999")
	}
}
