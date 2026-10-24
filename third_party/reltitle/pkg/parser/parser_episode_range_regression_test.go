package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

func TestParserEpisodeRange_PlainDashAfterTitleDelimiter(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] Crayon Shin-chan - 241-248 (CS-EX2 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalEpisodeRange(res); got != "241-248" {
		t.Fatalf("episode_range=%q want=%q", got, "241-248")
	}
	if got := canonicalTitle(res); got != "Crayon Shin-chan" {
		t.Fatalf("title=%q want=%q", got, "Crayon Shin-chan")
	}
	if got := canonicalGroup(res); got != "shincaps" {
		t.Fatalf("group=%q want=%q", got, "shincaps")
	}
}

func TestParserEpisodeRange_SpacedTildeBatchAfterEpisodePreconsume(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Erai-raws] Prism Rondo - 01 ~ 20 [1080p NF WEBRip HEVC EAC3][MultiSub] [BATCH]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalEpisodeRange(res); got != "1-20" {
		t.Fatalf("episode_range=%q want=%q", got, "1-20")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty when range detected", got)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
}

func TestParserEpisodeRange_SharedPrefixSxxExxRightDigits(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "A Couple of Cuckoos S02E07-09 MULTi 1080p WEB x264 AAC -Tsundere-Raws (CR) (VF, FRENCH, VOSTFR, Kakkou no Iinazuke Season 2)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalEpisodeRange(res); got != "7-9" {
		t.Fatalf("episode_range=%q want=%q", got, "7-9")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty when shared-prefix range detected", got)
	}
	if got := canonicalEPKind(res); got != "RANGE" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "RANGE")
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
}

func TestParserEpisodeRange_SharedPrefixSxxExxRightPrefixedEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[crane0922] Food Wars - S00E04-E05 (BD 1080p HEVC Opus)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "0" {
		t.Fatalf("season=%q want=%q", got, "0")
	}
	if got := canonicalEpisodeRange(res); got != "4-5" {
		t.Fatalf("episode_range=%q want=%q", got, "4-5")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty when shared-prefix range detected", got)
	}
	if got := canonicalEPKind(res); got != "RANGE" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "RANGE")
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
}

func TestParserEpisodeRange_AliasSeasonEchoDoesNotBecomeBatchRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[黒ネズミたち] 一人之下 第6季 / The Outcast 6 - 14 (CR 2600x1080 AVC AAC MKV)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "6" {
		t.Fatalf("season=%q want=%q", got, "6")
	}
	if got := canonicalEpisode(res); got != "14" {
		t.Fatalf("episode=%q want=%q", got, "14")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := canonicalEPKind(res); got != "EP" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "EP")
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalTitle(res); got != "一人之下" {
		t.Fatalf("title=%q want=%q", got, "一人之下")
	}
	if got := canonicalAliases(res); len(got) != 1 || got[0] != "The Outcast" {
		t.Fatalf("aliases=%v want=[The Outcast]", got)
	}
}

func TestParserEpisodeRange_DashSeasonAliasEchoDoesNotBecomeBatchRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[FSP] Douluo Dalu II - Soul Land 2 - 147 [1080p]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalEpisode(res); got != "147" {
		t.Fatalf("episode=%q want=%q", got, "147")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := canonicalEPKind(res); got != "EP" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "EP")
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
}

func TestParserEpisodeRange_OfTotalProgressBatchUsesOneToCurrent(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Sousou no Frieren TV-2 | Фрирен, провожающая в последний путь [ТВ-2] | Frieren: Beyond Journey's End 2 [2026] [07 of 10] [WEBRip] [1080p] [RUS + JAP]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalEpisodeRange(res); got != "1-7" {
		t.Fatalf("episode_range=%q want=%q", got, "1-7")
	}
	if got := canonicalSeason(res); got != "2" {
		t.Fatalf("season=%q want=%q", got, "2")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty for of-total batch progress", got)
	}
	if got := canonicalEPKind(res); got != "RANGE" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "RANGE")
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
}
