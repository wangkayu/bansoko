package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserUnknownOutput_KeepOnlyGroupTitleAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AI-Raws] BEMANI SYMPHONY CONCERT 2022"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.ContentKind != "UNKNOWN" {
		t.Fatalf("content_kind=%q want UNKNOWN", res.ContentKind)
	}
	if res.TemplateID != "TF_UNKNOWN_DEFAULT" && res.TemplateID != "TF_UNKNOWN_G_T_ALIAS_MINIMAL_01" {
		t.Fatalf("template_id=%q want TF_UNKNOWN_DEFAULT|TF_UNKNOWN_G_T_ALIAS_MINIMAL_01", res.TemplateID)
	}
	if sem := canonicalEPSemantics(res); sem != nil {
		t.Fatalf("ep_semantics must be nil for UNKNOWN output, got=%+v", sem)
	}
	if res.AudioSlot != nil {
		t.Fatalf("audio_slot must be nil for UNKNOWN output, got=%+v", res.AudioSlot)
	}
	for _, a := range res.Attributes {
		if a.Type != model.AttrGroup && a.Type != model.AttrTitle && a.Type != model.AttrTitleAlias {
			t.Fatalf("unknown output leaked non-core attr: %+v", a)
		}
	}
}

func TestParserUnknownOutput_KeepTitleAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AngelEcho]Charlotte [Epilogue]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.ContentKind != "UNKNOWN" {
		t.Fatalf("content_kind=%q want UNKNOWN", res.ContentKind)
	}
	if got := canonicalGroup(res); got != "AngelEcho" {
		t.Fatalf("group=%q want AngelEcho", got)
	}
	if got := canonicalTitle(res); got != "Charlotte" {
		t.Fatalf("title=%q want Charlotte", got)
	}
	aliases := canonicalAliases(res)
	if len(aliases) != 1 || aliases[0] != "Epilogue" {
		t.Fatalf("aliases=%v want [Epilogue]", aliases)
	}
	for _, a := range res.Attributes {
		if a.Type != model.AttrGroup && a.Type != model.AttrTitle && a.Type != model.AttrTitleAlias {
			t.Fatalf("unknown output leaked non-core attr: %+v", a)
		}
	}
}

func TestParserUnknownOutput_GroupBoundaryPreservesRemovedSubsToken(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Inka-Subs] Armor Hunter Merowlink (Mellowlink)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if res.ContentKind != "UNKNOWN" {
		t.Fatalf("content_kind=%q want UNKNOWN", res.ContentKind)
	}
	if got := canonicalGroup(res); got != "Inka-Subs" {
		t.Fatalf("group=%q want Inka-Subs", got)
	}
}

func TestParserUnknownOutput_HeadParenGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "(NSSB) Slayers"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "NSSB" {
		t.Fatalf("group=%q want NSSB", got)
	}
	if got := canonicalTitle(res); got != "Slayers" {
		t.Fatalf("title=%q want Slayers", got)
	}
}

func TestParserUnknownOutput_HeadParenMetaNotGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "(Partial) Weekly K Manga Chapter Updates - Week 03 2026 (Digital) (Various)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "" {
		t.Fatalf("group=%q want empty", got)
	}
}

func TestParserUnknownOutput_HeadParenVersionNotGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "(V2)  [R-Archive - loroid] Arashi no Yoru ni | あらしのよるに | One Stormy Night [Multi-Track, 1080p WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got == "V2" {
		t.Fatalf("group should not use parenthesized version marker, got=%q", got)
	}
}

func TestParserHeadGroupFallback_AfterLeadingParenMeta(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "(V2) [R-Archive - loroid] Arashi no Yoru ni"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "R-Archive-loroid" {
		t.Fatalf("group=%q want=%q", got, "R-Archive-loroid")
	}
}

func TestParserUnknownOutput_DropsTechAndLangMetaAliases(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[PorterRAWS]去吧！稻中桌球社 / 去吧！稻中乒團/ Ping Pong Club[360P][VCDRip][粤语中字]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	aliases := canonicalAliases(res)
	for _, a := range aliases {
		if a == "360P" || a == "VCDRip" || a == "粤语中字" {
			t.Fatalf("alias should drop tech/lang meta token, aliases=%v", aliases)
		}
	}
	if !containsAlias(aliases, "去吧！稻中乒團") || !containsAlias(aliases, "Ping Pong Club") {
		t.Fatalf("expected title aliases retained, got=%v", aliases)
	}
}
