package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestTitleKnowledgeCombinesNo6FutureCityAsCanonicalTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	res, err := New(cfg).Parse("【黑白映画字幕社】★7月新番[No.6] [未来都市][第10话][GB][848x480][RMVB]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := res.CanonicalTitle(); got != "No.6 未来都市" {
		t.Fatalf("title=%q", got)
	}
	if containsAlias(res.CanonicalTitleAliases(), "未来都市") {
		t.Fatalf("compound title component must not be repeated as alias: %v", res.CanonicalTitleAliases())
	}
	if got := res.CanonicalEpisode(); got != "10" {
		t.Fatalf("episode=%q", got)
	}
}

func TestKnownHeadGroupWinsOverAnnouncementTailBlock(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	raw := "[三明治摆烂组] 才女的侍从 在满是高岭之花的贵族学校暗中照顾（毫无生活自理能力的）学院第一大小姐 / Saijo no Osewa / 才女のお世話 - 04 - [繁日内嵌][AVC 8bit 1080P][關於本番字幕更新時間調整說明]"
	res, err := New(cfg).Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := res.CanonicalGroup(); got != "三明治摆烂组" {
		t.Fatalf("group=%q", got)
	}
	for _, attr := range res.Attributes {
		if attr.Type == model.AttrTailGroup && strings.Contains(attr.Normalized, "調整說明") {
			t.Fatalf("announcement parsed as tail group: %+v", attr)
		}
	}
	if strings.Contains(res.CanonicalTitle(), "調整說明") {
		t.Fatalf("announcement leaked into title: %q", res.CanonicalTitle())
	}
}

func TestExplicitUnknownHeadGroupWinsOverAnnouncementTailBlock(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	raw := "[临时摆烂组] 示例标题 - 04 - [1080P][關於本番字幕更新時間調整說明]"
	res, err := New(cfg).Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := res.CanonicalGroup(); got != "临时摆烂组" {
		t.Fatalf("group=%q", got)
	}
	for _, attr := range res.Attributes {
		if attr.Type == model.AttrTailGroup && strings.Contains(attr.Normalized, "調整說明") {
			t.Fatalf("announcement parsed as tail group: %+v", attr)
		}
	}
}
