package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserBatchSourceType_BDRipPack(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "(五一劳动节快乐)[肥猫压制][FatCatRAW][假面骑士Stronger][BDBOX][BDRIP][1080P]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := firstAttrValue(res, model.AttrSourceType); got != "BDRip" {
		t.Fatalf("source_type=%q want=%q", got, "BDRip")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "bdrip" {
		t.Fatalf("pack_kind=%q want=%q", got, "bdrip")
	}
	if got := canonicalGroup(res); got != "FatCatRAW" {
		t.Fatalf("group=%q want=%q", got, "FatCatRAW")
	}
	for _, a := range canonicalAliases(res) {
		la := strings.ToLower(strings.TrimSpace(a))
		if strings.Contains(a, "节快乐") || strings.Contains(a, "壓制") || strings.Contains(a, "压制") || strings.Contains(la, "fatcatraw") || strings.EqualFold(strings.TrimSpace(a), "BDBOX") {
			t.Fatalf("alias should drop obvious noise token, got=%q aliases=%v", a, canonicalAliases(res))
		}
	}
}

func TestParserBatchSourceType_WEBRipRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "29-sai Dokushin Chuuken Boukensha no Nichijou | An Adventurer`s Daily Grind at Age 29 | Повседневная жизнь одинокого 29-летнего авантюриста [2026] [01 of 12] [WEBRip] [1080p] [RUS + JAP]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := firstAttrValue(res, model.AttrSourceType); got != "WEBRip" {
		t.Fatalf("source_type=%q want=%q", got, "WEBRip")
	}
	if got := canonicalEpisodeRange(res); got != "1-1" {
		t.Fatalf("episode_range=%q want=%q", got, "1-1")
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("expected ep semantics for explicit WEBRip range case")
	}
}

func TestParserBatchSourceType_DVDRipPack(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "City Hunter 1-3 and 91 and films dvdrip (JPN audio ENG sub)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := firstAttrValue(res, model.AttrSourceType); got != "DVDRip" {
		t.Fatalf("source_type=%q want=%q", got, "DVDRip")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "dvdrip" {
		t.Fatalf("pack_kind=%q want=%q", got, "dvdrip")
	}
	if sem := canonicalEPSemantics(res); sem != nil {
		t.Fatalf("ambiguous batch digits should not keep ep semantics, got=%+v", sem)
	}
}

func TestParserBatchSeasonRange_FromSeasonKeywordAndConnector(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AI-Raws] Tomorrow's Joe あしたのジョー TV Season 1&2 BDRip 1080p MKV"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := canonicalSeasonRange(res); got != "S1-S2" {
		t.Fatalf("season_range=%q want=%q", got, "S1-S2")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := canonicalEpisodeList(res); len(got) != 0 {
		t.Fatalf("episode_list=%v want empty", got)
	}
}

func TestParserBatchSeasonRange_FrenchSaisonRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[MECHAPOULPE] Solo Leveling Henshū saison 1 & 2"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := canonicalSeasonRange(res); got != "S1-S2" {
		t.Fatalf("season_range=%q want=%q", got, "S1-S2")
	}
}

func TestParserBatchMovieKeyword_TVMoviePackStaysBatch(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AI-Raws] SPACE WARRIOR BALDIOS 宇宙戦士バルディオス TV+Movie BDRip 1080p MKV"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	contents := attrValues(res, model.AttrContents)
	if len(contents) == 0 {
		t.Fatalf("expected contents attrs for TV+Movie pack")
	}
}

func TestParserBatchSourceType_FallbackFromPackKindDVDISO(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Dofus.livre.1.Julith.2016.DVDISO.FRENCH-MaitreAnonyme.iso"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "dvdiso" {
		t.Fatalf("pack_kind=%q want=%q", got, "dvdiso")
	}
	if got := firstAttrValue(res, model.AttrSourceType); got != "DVD" {
		t.Fatalf("source_type=%q want=%q", got, "DVD")
	}
}

func TestParserBatchTemplateRangePack_WithTech(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[LoliHouse] 2.5-jigen no Ririsa / 2.5次元的诱惑 [01-24 合集][WebRip 1080p HEVC-10bit AAC][简繁内封字幕][Fin]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := res.TemplateID; got != "TF_BATCH_RANGE_PACK_01" {
		t.Fatalf("template_id=%q want=%q", got, "TF_BATCH_RANGE_PACK_01")
	}
	if got := canonicalEpisodeRange(res); got != "1-24" {
		t.Fatalf("episode_range=%q want=%q", got, "1-24")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "合集" {
		t.Fatalf("pack_kind=%q want=%q", got, "合集")
	}
}

func TestParserBatchTemplateRangePack_NoTechWithCollectionKeyword(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[LoliHouse] Sample Title [01-12][Collection]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := res.TemplateID; got != "TF_BATCH_RANGE_PACK_02" {
		t.Fatalf("template_id=%q want=%q", got, "TF_BATCH_RANGE_PACK_02")
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=%q", got, "1-12")
	}
	if got := firstAttrValue(res, model.AttrPackKind); !strings.EqualFold(got, "collection") {
		t.Fatalf("pack_kind=%q want=%q", got, "collection")
	}
}

func TestParserBatchTemplateRangePack_NoGroupWithPackAndTech(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "BLEACH (2004) - COMPLETE (001-366) - JP BD + R2J DVD [Dual Audio][FLAC] [V2]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := res.TemplateID; got != "TF_BATCH_RANGE_PACK_NOGROUP_01" {
		t.Fatalf("template_id=%q want=%q", got, "TF_BATCH_RANGE_PACK_NOGROUP_01")
	}
	if got := canonicalEpisodeRange(res); got != "1-366" {
		t.Fatalf("episode_range=%q want=%q", got, "1-366")
	}
	if got := firstAttrValue(res, model.AttrPackKind); !strings.EqualFold(got, "complete") {
		t.Fatalf("pack_kind=%q want=%q", got, "complete")
	}
	regions := attrValues(res, model.AttrPlatformRegion)
	hasJPRegion := false
	for _, v := range regions {
		if strings.EqualFold(strings.TrimSpace(v), "JP") {
			hasJPRegion = true
			break
		}
	}
	if !hasJPRegion {
		t.Fatalf("platform_region should include JP, got=%v", regions)
	}
	srcTypes := attrValues(res, model.AttrSourceType)
	hasDVD := false
	for _, v := range srcTypes {
		if strings.EqualFold(strings.TrimSpace(v), "DVD") {
			hasDVD = true
			break
		}
	}
	if !hasDVD {
		t.Fatalf("source_type should include DVD, got=%v", srcTypes)
	}
}

func TestParserBatchTemplateRangePack_TVQuanJiUsesPackKindNotContents(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws][2.5次元的诱惑/2.5 Jigen no Ririsa][01-24TV全集][1080P][BDRip][HEVC-10bit][简繁外挂][FLAC][MKV](2.5次元の誘惑)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := canonicalEpisodeRange(res); got != "1-24" {
		t.Fatalf("episode_range=%q want=%q", got, "1-24")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "TV_PACK" {
		t.Fatalf("pack_kind=%q want=%q", got, "TV_PACK")
	}
	if vals := attrValues(res, model.AttrContents); len(vals) > 0 {
		t.Fatalf("contents should be empty for TV全集 batch flag, got=%v", vals)
	}
	for _, a := range canonicalAliases(res) {
		if strings.Contains(a, "01 24TV") || strings.Contains(a, "24TV") {
			t.Fatalf("episode range marker leaked into alias: %q aliases=%v", a, canonicalAliases(res))
		}
	}
}

func TestParserHeadGroupFallback_UsesSecondBracketWhenFirstIsMeta(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[1080P][DBD-Raws][东映蜘蛛侠/四百大妈/Toei Spider-Man][01-41TV全集+剧场版][HEVC-10bit][AC3][MKV](とうえい スパイダーマン‎)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := canonicalGroup(res); got != "DBD-Raws" {
		t.Fatalf("group=%q want=%q", got, "DBD-Raws")
	}
	title := canonicalTitle(res)
	if strings.Contains(strings.ToLower(title), "dbd") || strings.Contains(strings.ToLower(title), "raws") {
		t.Fatalf("title polluted by group fallback failure: %q", title)
	}
}

func TestParserEpisodeCompactEndFlag_NotBatch(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TestGroup][Sample Title][12END][1080p][WEB-DL][AAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalEpisode(res); got != "12" {
		t.Fatalf("episode=%q want=%q", got, "12")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "" {
		t.Fatalf("pack_kind=%q want empty", got)
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := firstAttrValue(res, model.AttrReleaseStatus); got != "END" {
		t.Fatalf("release_status=%q want=%q", got, "END")
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "END") {
		t.Fatalf("END should not remain in note after release_status promotion, notes=%v", attrValues(res, model.AttrNote))
	}
}

func TestParserEpisodeCompactEndFlag_WithUnderscore_NotBatch(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[雪飄工作室][公主協奏管弦樂/Princession_Orchestra/プリンセッション・オーケストラ][48_END][繁]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalEpisode(res); got != "48" {
		t.Fatalf("episode=%q want=%q", got, "48")
	}
	if got := firstAttrValue(res, model.AttrReleaseStatus); got != "END" {
		t.Fatalf("release_status=%q want=%q", got, "END")
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "END") {
		t.Fatalf("END should not remain in note after release_status promotion, notes=%v", attrValues(res, model.AttrNote))
	}
	if aliases := canonicalAliases(res); hasAttrValue(aliases, "48 END") {
		t.Fatalf("episode marker leaked into aliases: %v", aliases)
	}
}

func TestParserBatchTemplateRangePack_TVQuanJiWithExtrasStillKeepsRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws][AIR/青空][01-12TV全集+SP+剧场版+特典映像][1080P][BDRip][HEVC-10bit][简繁外挂][FLAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=%q", got, "1-12")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "TV_PACK" {
		t.Fatalf("pack_kind=%q want=%q", got, "TV_PACK")
	}
}

func TestParserBatchTemplateRangePack_CuratedCountSuffixAsRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][Sample Title][12合集][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=%q", got, "1-12")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "合集" {
		t.Fatalf("pack_kind=%q want=%q", got, "合集")
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "12合集") && !tok.Removed {
			t.Fatalf("12合集 should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserBatchTemplateRangePack_CuratedRangeSuffixAsRange(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group][Sample Title][01-12修正合集][1080p][WEB-DL]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=%q", got, "1-12")
	}
	if got := firstAttrValue(res, model.AttrPackKind); got != "合集" {
		t.Fatalf("pack_kind=%q want=%q", got, "合集")
	}
	if !hasAttrValue(attrValues(res, model.AttrPackProfile), "curated_pack") {
		t.Fatalf("expected pack_profile=curated_pack, got=%v", attrValues(res, model.AttrPackProfile))
	}
	if hasAttrValue(attrValues(res, model.AttrNote), "CURATED_PACK") {
		t.Fatalf("CURATED_PACK should be promoted from note, notes=%v", attrValues(res, model.AttrNote))
	}
	for _, tok := range res.Tokens {
		if strings.EqualFold(strings.TrimSpace(tok.Text), "12修正合集") && !tok.Removed {
			t.Fatalf("12修正合集 should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestParserEpisodeList_ConanDoubleEpisodeBracket(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws][名侦探柯南/Detective Conan/名探偵コナン][804&805][柯南与海老藏 歌舞伎十八番之谜/コナンと海老蔵 歌舞伎十八番ミステリー][1080P][BDRip][HEVC-10bit][FLAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalEpisode(res); got != "804" {
		t.Fatalf("episode=%q want=%q", got, "804")
	}
	if got := canonicalEpisodeList(res); strings.Join(got, ",") != "804,805" {
		t.Fatalf("episode_list=%v want=%q", got, "804,805")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := canonicalEPKind(res); got != "EP_LIST" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "EP_LIST")
	}
}

func TestParserBatchTemplate_YangirePackSpecialsCleanTitleAndPlatform(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Wata-nare - There's No Freaking Way I'll Be Your Lover! Unless... PACK (S01+SPÉCIAUX) VOSTFR 1080p WEB AV1 AAC - Yangire-Raws (YUZUKITV)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := canonicalSeason(res); got != "1" {
		t.Fatalf("season=%q want=%q", got, "1")
	}
	if got := firstAttrValue(res, model.AttrPlatform); got != "YUZUKITV" {
		t.Fatalf("platform=%q want=%q", got, "YUZUKITV")
	}
	if got := canonicalTitle(res); strings.Contains(strings.ToLower(got), "pack") || strings.Contains(strings.ToLower(got), "spéciaux") {
		t.Fatalf("title=%q should drop trailing pack/special meta", got)
	}
}

func TestParserBatchTemplate_CatTalesLNTVPlatformExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Cat.Tales.S01E20.1080p.LNTV.WEB-DL.JPN.AAC2.0.H.264.MSubs-ToonsHub.mkv"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalSeason(res); got != "1" {
		t.Fatalf("season=%q want=%q", got, "1")
	}
	if got := canonicalEpisode(res); got != "20" {
		t.Fatalf("episode=%q want=%q", got, "20")
	}
	if got := firstAttrValue(res, model.AttrPlatform); got != "LNTV" {
		t.Fatalf("platform=%q want=%q", got, "LNTV")
	}
}

func TestParserBatchTemplate_BGlobalSplitPlatformExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[ANi] Sample Show - 01 [1080p][B Global][WEB-DL][AAC AVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := firstAttrValue(res, model.AttrPlatform); got != "BILIBILI" {
		t.Fatalf("platform=%q want=%q", got, "BILIBILI")
	}
}

func TestParserBatchTemplate_FODPlatformExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Show - 01 [WEB-DL][1080p][FOD][AAC AVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := firstAttrValue(res, model.AttrPlatform); got != "FOD" {
		t.Fatalf("platform=%q want=%q", got, "FOD")
	}
}

func TestParserBatchTemplate_BStationPlatformExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Show - 01 [WEB-DL][1080p][BSTATION][AAC AVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := firstAttrValue(res, model.AttrPlatform); got != "BILIBILI" {
		t.Fatalf("platform=%q want=%q", got, "BILIBILI")
	}
}

func TestParserBatchTemplate_ZhJpBilingualSubFlagExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Show - 01 [中日雙語][1080p][WEB-DL][AAC AVC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if flags := attrValues(res, model.AttrSubFlag); len(flags) != 0 {
		t.Fatalf("expected no sub_flag for 双语 marker without mode, got=%v", flags)
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "SC") || !hasAttrValue(attrValues(res, model.AttrSubLang), "JPN") {
		t.Fatalf("expected sub_lang SC+JPN, got=%v", attrValues(res, model.AttrSubLang))
	}
}

func TestParserBatchTemplate_ATXMpeg2AndTSContainerExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] SI-VIS ~The Sound of Heroes~ - 08 (AT-X 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := firstAttrValue(res, model.AttrPlatform); got != "AT-X" {
		t.Fatalf("platform=%q want=%q", got, "AT-X")
	}
	if got := firstAttrValue(res, model.AttrVideoCodec); got != "MPEG2" {
		t.Fatalf("video_codec=%q want=%q", got, "MPEG2")
	}
	if got := firstAttrValue(res, model.AttrContainer); got != "TS" {
		t.Fatalf("container=%q want=%q", got, "TS")
	}
}

func TestParserBatchTemplate_SxxPartIndexWithoutStrongEP_PrefersSeasonPack(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Breeze] Dr. STONE - Science Future - S04 Part 1 [1080p AV1][Dual Audio]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := canonicalSeason(res); got != "4" {
		t.Fatalf("season=%q want=%q", got, "4")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
}

func TestParserBatchTemplate_SxxPartIndexWithStrongEP_StillKeepsEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Breeze] Dr. STONE - Science Future - S04 Part 1 - 03 [1080p AV1][Dual Audio]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content_kind=%q want=%q", got, "TV_EP")
	}
	if got := canonicalSeason(res); got != "4" {
		t.Fatalf("season=%q want=%q", got, "4")
	}
	if got := canonicalEpisode(res); got != "3" {
		t.Fatalf("episode=%q want=%q", got, "3")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := canonicalEPKind(res); got != "EP" {
		t.Fatalf("ep_semantics.kind=%q want=%q", got, "EP")
	}
}
