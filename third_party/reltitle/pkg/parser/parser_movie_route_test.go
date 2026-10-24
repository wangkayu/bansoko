package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserMovieRoute_MixedTVMovie_NoPackKindStillBatch(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AI-Raws] Sample Franchise TV+Movie 1080p MKV"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	contents := attrValues(res, model.AttrContents)
	if len(contents) == 0 {
		t.Fatalf("expected contents attrs for TV+Movie mixed title")
	}
}

func TestParserMovieRoute_ShortMovieIndexWithContext(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Atlas] Fate strange Fake 劇場版 M1 [1080p][WEB-DL][AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "1" {
		t.Fatalf("movie_index=%q want=%q", got, "1")
	}
}

func TestParserMovieRoute_ChineseMovieKeywordInTitleStaysMovie(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【MCE汉化组】[剧场版 少女与战车 最终章][Girls und Panzer Saishuushou][04][冒头映像][Trial Version][简体][1080P][x264 AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
}

func TestParserMovieRoute_MoviesRangePlusOVA_StaysBatch(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[SoM] Dragon Ball / DBZ Movies 1-17 + OVAs - Color Corrected [1080p][x264]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
}

func TestParserMovieRoute_BatchContentPrefersBatchTemplateOverTVSP(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AI-Raws] Baribari Legend バリバリ伝説 OVA+Movie BDRip 720p MKV"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := res.TemplateID; !strings.HasPrefix(got, "TF_BATCH_") {
		t.Fatalf("template_id=%q want TF_BATCH_*", got)
	}
}

func TestParserMovieRoute_BatchRangePackTemplatePreferred(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws][Blood-C/血战-C/ブラッドシー][01-12TV全集+剧场版+特典映像][1080P][BDRip][HEVC-10bit][简繁外挂][FLAC][MKV]"
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
	if got := res.TemplateID; got != "TF_BATCH_RANGE_PACK_01" {
		t.Fatalf("template_id=%q want=%q", got, "TF_BATCH_RANGE_PACK_01")
	}
}

func TestParserMovieRoute_CommaGroupRangePackTemplatePreferred(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[雪飘工作室,WBX-SUB][ワッチャプリマジ！/Waccha_Primagi!/哇恰美妙魔法！][BDrip][01-12](检索:/美妙旋律/美妙天堂/美妙频道)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := firstAttrValue(res, model.AttrGroup); got == "" {
		t.Fatalf("group should not be empty")
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=%q", got, "1-12")
	}
	if got := res.TemplateID; got != "TF_BATCH_RANGE_PACK_01" {
		t.Fatalf("template_id=%q want=%q", got, "TF_BATCH_RANGE_PACK_01")
	}
}

func TestParserMovieRoute_AtGroupRangePackTemplatePreferred(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Jsum@U2][2.43 清阴高中男子排球社][2.43 Seiin Koukou Danshi Volley Bu][2.43 清陰高校男子バレー部][BDRip][1920x1080][TV 01-12 Fin+SP][H264 FLAC MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := firstAttrValue(res, model.AttrGroup); got != "Jsum@U2" {
		t.Fatalf("group=%q want=%q", got, "Jsum@U2")
	}
	if got := canonicalEpisodeRange(res); got != "1-12" {
		t.Fatalf("episode_range=%q want=%q", got, "1-12")
	}
	if got := res.TemplateID; got != "TF_BATCH_RANGE_PACK_01" {
		t.Fatalf("template_id=%q want=%q", got, "TF_BATCH_RANGE_PACK_01")
	}
}

func TestParserMovieRoute_DashCodecNumberNotEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[OneStar][福音战士新剧场版：终 / 福音戰士新劇場版：終(2021)][1080P][WEB][Amazon][HEVC-265 10bit][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
}

func TestParserMovieRoute_DayIndexDemotedToMovieIndex(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[GJ.Y] IDOLiSH7 Movie: LIVE 4bit - BEYOND THE PERiOD - Day 1 (CR 1920x1080 AVC AAC MKV)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "1" {
		t.Fatalf("movie_index=%q want=%q", got, "1")
	}
}

func TestParserMovieRoute_SeasonPartTvEpisodeNotDemoted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[LoliHouse] 关于我转生变成史莱姆这档事 第二季 第2部分 / Tensei Shitara Slime Datta Ken 2nd Season Part 2 - 41 [WebRip 1080p HEVC-10bit AAC][简繁英内封]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got == "MOVIE" {
		t.Fatalf("content_kind=%q should not be MOVIE", got)
	}
	if got := canonicalEpisode(res); got != "41" {
		t.Fatalf("episode=%q want=%q", got, "41")
	}
}

func TestParserMovieRoute_OVAWithYearTech_PrefersSpecial(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Blasphemboys] Sanzoku Diary / Bandit's Diary / 山賊ダイアリー OVA (2015) [DVD AVC AC3]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "SPECIAL" {
		t.Fatalf("content_kind=%q want=%q", got, "SPECIAL")
	}
	if got := canonicalSPKind(res); got != "OVA" {
		t.Fatalf("sp_kind=%q want=%q", got, "OVA")
	}
}

func TestParserMovieRoute_MovieYearTech_NoSpKindStillMovie(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Colorful (2010) MULTi 1080p WEB x265 Opus -Tsundere-Raws (VF, FRENCH, VOSTFR, Colorful (Movie))"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
}

func TestParserMovieRoute_GenericMovieWordInEpisodeTitleNotMovie(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[丸子家族][海螺小姐(Sazae-san)][2712]鲑仔和电线杆叔叔&不要责怪我把自己当女儿&喜欢恐怖电影吗[2023.08.06][GB][1080P][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got == "MOVIE" {
		t.Fatalf("content_kind=%q should not be MOVIE", got)
	}
}

func TestParserMovieRoute_TrailingMovieNumberDemotedToMovieIndex(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws][惊声尖笑2/Scary Movie 2][1080P][BDRip][HEVC-10bit][中英外挂][FLAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "2" {
		t.Fatalf("movie_index=%q want=%q", got, "2")
	}
}

func TestParserMovieRoute_MovieDashEpisodeTailNotInTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws] Scary Movie 31 - 01 [1080P][BDRip][HEVC-10bit][AAC][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "31" {
		t.Fatalf("movie_index=%q want=%q", got, "31")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := canonicalTitle(res); got == "" || strings.HasSuffix(got, " 01") {
		t.Fatalf("title=%q should not carry demoted dash-episode tail", got)
	}
	if got := canonicalTitle(res); !strings.EqualFold(strings.TrimSpace(got), "Scary Movie") {
		t.Fatalf("title=%q want=%q", got, "Scary Movie")
	}
}

func TestParserMovieRoute_PartIndexCanStayInTitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Movie - Part 2 [1080p][WEB][AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "2" {
		t.Fatalf("movie_index=%q want=%q", got, "2")
	}
	if got := canonicalTitle(res); !strings.Contains(strings.ToLower(strings.TrimSpace(got)), "part 2") {
		t.Fatalf("title=%q should keep part segment", got)
	}
}

func TestParserMovieRoute_MShortIndexWithMovieContext(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Atlas] Fate strange Fake 劇場版 M02 [1080p][WEB-DL][AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "MOVIE" {
		t.Fatalf("content_kind=%q want=%q", got, "MOVIE")
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "2" {
		t.Fatalf("movie_index=%q want=%q", got, "2")
	}
}

func TestParserMovieRoute_MShortIndexWithoutMovieContextIgnored(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Show M2 - 03 [1080p][WEB][AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "" {
		t.Fatalf("movie_index=%q want empty", got)
	}
	if got := canonicalEpisode(res); got != "3" {
		t.Fatalf("episode=%q want=%q", got, "3")
	}
}

func TestParserMovieRoute_BracketMIndexExtractedFromRealSample(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[LDP-Embed][名侦探柯南•唐红的恋歌][M21][BDRIP][1080P][CHS][台日双轨].mkv"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "21" {
		t.Fatalf("movie_index=%q want=%q", got, "21")
	}
	for _, a := range canonicalAliases(res) {
		if strings.EqualFold(strings.TrimSpace(a), "M21") {
			t.Fatalf("alias should not keep standalone movie index block, aliases=%v", canonicalAliases(res))
		}
	}
}

func TestParserMovieRoute_InlineMIndexWithMovieContextExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[银色子弹字幕组&VCB-Studio] 名侦探柯南 M24 绯色的子弹 / Detective Conan The Scarlet Bullet 10-bit 1080p HEVC BDRip [MOVIE]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "24" {
		t.Fatalf("movie_index=%q want=%q", got, "24")
	}
}

func TestParserMovieRoute_BracketInlineMovieNumberExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[APTX4869][CONAN][名侦探柯南 MOVIE 27 百万美元的五棱星][BDRIP][1080P][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "27" {
		t.Fatalf("movie_index=%q want=%q", got, "27")
	}
}

func TestParserMovieRoute_ParenInlineMovieNumberExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[丸子家族][电影][海螺小姐(Sazae-san Movie 1)][1956][GB][1080P][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "1" {
		t.Fatalf("movie_index=%q want=%q", got, "1")
	}
}

func TestParserMovieRoute_BracketEpisodeAsMovieIndexWithMovieContext(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【喵萌Production】[LoveLive! 虹咲学园学园偶像同好会 完结篇 / Love Live! Nijigasaki Movie][01][BDRip][1080p][简日双语]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "1" {
		t.Fatalf("movie_index=%q want=%q", got, "1")
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
}

func TestParserMovieRoute_MovieRangeBracketExtracted(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[SweetSub][剧场版回转企鹅罐 RE:cycle of the PENGUINDRUM][Movie 01-02 合集][BDRip][1080P][AVC 8bit][简日双语]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndexRange); got != "1-2" {
		t.Fatalf("movie_index_range=%q want=%q", got, "1-2")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "" {
		t.Fatalf("movie_index=%q want empty", got)
	}
}

func TestParserMovieRoute_PlainMovieRangeIsBatchWithoutEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Puella Magi Madoka Magica the Movie 01-03"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content_kind=%q want=%q", got, "BATCH")
	}
	if got := firstAttrValue(res, model.AttrMovieIndexRange); got != "1-3" {
		t.Fatalf("movie_index_range=%q want=%q", got, "1-3")
	}
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode_range=%q want empty", got)
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
	if got := canonicalTitle(res); got == "" || strings.HasSuffix(got, " 01") {
		t.Fatalf("title=%q should not carry movie range start tail", got)
	}
}

func TestParserMovieRoute_TVMovieMixedRangeNoSingleIndex(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[LoliHouse] 灌篮高手 / SLAM DUNK [TV 01-101 + Movie 01-04 Fin][BDRip 720p HEVC-10bit AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndexRange); got != "1-4" {
		t.Fatalf("movie_index_range=%q want=%q", got, "1-4")
	}
	if got := canonicalEpisodeRange(res); got != "1-101" {
		t.Fatalf("episode_range=%q want=%q", got, "1-101")
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "" {
		t.Fatalf("movie_index=%q want empty", got)
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
}

func TestParserMovieRoute_TitleMNumberNotMovieIndex(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[VCB-Studio] M3 ~黑钢~ / M3 Sono Kuroki Hagane / M3〜ソノ黒キ鋼〜 10-bit 1080p HEVC BDRip [Fin]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := firstAttrValue(res, model.AttrMovieIndex); got != "" {
		t.Fatalf("movie_index=%q want empty", got)
	}
}
