package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserSubtitleLanguageTagDoesNotBecomeTitleAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	result, err := New(cfg).Parse("【喵萌奶茶屋】★劇場版★[Virgin Punk Clockwork Girl][BDRip][1080p][繁體][v2]")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	for _, alias := range result.CanonicalTitleAliases() {
		if strings.EqualFold(alias, "繁體") || strings.EqualFold(alias, "繁体") {
			t.Fatalf("subtitle language leaked into aliases: %v", result.CanonicalTitleAliases())
		}
	}
	if got := result.CanonicalTitle(); got != "Virgin Punk Clockwork Girl" {
		t.Fatalf("title=%q", got)
	}
	if got := attrValues(result, model.AttrSubLang); len(got) != 1 || got[0] != "TC" {
		t.Fatalf("sub_lang=%v", got)
	}
}

func TestParserContentRatingDoesNotBecomeTitleAlias(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	result, err := New(cfg).Parse("[ToonsHub] Dara-san of the Reiwa Era S01E05 1080p CR WEB-DL AAC2.0 H.264 (Reiwa no Dara-san, Multi-Subs, Uncensored)")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	aliases := result.CanonicalTitleAliases()
	if len(aliases) != 1 || aliases[0] != "Reiwa no Dara-san" {
		t.Fatalf("aliases=%v", aliases)
	}
	if got := attrValues(result, model.AttrContentRating); len(got) != 1 || got[0] != "UNCENSORED" {
		t.Fatalf("content_rating=%v", got)
	}
}
