package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

func TestParserEpisodeDecimal_BahaSeasonDashDecimalEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] 杖與劍的魔劍譚 Season 2 - 12.5 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalEpisode(res); got != "12.5" {
		t.Fatalf("episode=%q want=%q", got, "12.5")
	}
	if got := canonicalEPKind(res); got != "EP" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "EP")
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
}

func TestParserEpisodeDecimal_AliasSeasonEchoStillUsesDecimalEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[黒ネズミたち] 杖与剑的魔剑谭 Season 2 / Tsue to Tsurugi no Wistoria Season 2 - 12.5 (Baha 1920x1080 AVC AAC MP4)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalEpisode(res); got != "12.5" {
		t.Fatalf("episode=%q want=%q", got, "12.5")
	}
	if got := canonicalEPKind(res); got != "EP" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "EP")
	}
	if got := canonicalTitle(res); got != "杖与剑的魔剑谭" {
		t.Fatalf("title=%q want=%q", got, "杖与剑的魔剑谭")
	}
	aliases := canonicalAliases(res)
	if len(aliases) != 1 || aliases[0] != "Tsue to Tsurugi no Wistoria" {
		t.Fatalf("aliases=%v want=[Tsue to Tsurugi no Wistoria]", aliases)
	}
}

func TestParserEpisodeZero_AliasSeasonEchoStillUsesEpisodeZero(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[黒ネズミたち] 杖与剑的魔剑谭 Season 2 / Tsue to Tsurugi no Wistoria Season 2 - 0 (CR 1920x1080 AVC AAC MKV)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalEpisode(res); got != "0" {
		t.Fatalf("episode=%q want=%q", got, "0")
	}
	if got := canonicalEPKind(res); got != "EP" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "EP")
	}
}
