package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

func TestParserBracketEpisode_WhenEpisodeEqualsSeasonStillPreferStandaloneEpisodeBracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[爱恋字幕社][1月新番][金牌得主 第二季][Medalist][02][1080p][MP4][GB][简中]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalEpisode(res); got != "2" {
		t.Fatalf("episode=%q want=%q", got, "2")
	}
	if got := canonicalTitle(res); got != "金牌得主" {
		t.Fatalf("title=%q want=%q", got, "金牌得主")
	}
}
