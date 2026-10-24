package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserGroupRouteOverride_APTX4869ConanBracketEpisodeTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[APTX4869][CONAN][名侦探柯南 1044 猪肉味增汤是舍命留下的暗号][HDTV][1080P][简体MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "APTX4869" {
		t.Fatalf("group=%q want=%q", got, "APTX4869")
	}
	if got := canonicalTitle(res); got != "名侦探柯南" {
		t.Fatalf("title=%q want=%q", got, "名侦探柯南")
	}
	if got := canonicalEpisode(res); got != "1044" {
		t.Fatalf("episode=%q want=%q", got, "1044")
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if aliases := canonicalAliases(res); !containsAlias(aliases, "CONAN") {
		t.Fatalf("missing alias CONAN, aliases=%v", aliases)
	}
	if notes := attrValues(res, model.AttrNote); !containsAlias(notes, "猪肉味增汤是舍命留下的暗号") {
		t.Fatalf("missing episode title note, notes=%v", notes)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("ep_semantics should not be nil")
	}
	if got := canonicalEPSource(res); got != "group_route_override" {
		t.Fatalf("ep_semantics.source=%q want=%q", got, "group_route_override")
	}
}
