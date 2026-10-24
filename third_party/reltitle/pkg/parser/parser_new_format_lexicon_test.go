package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestLexiconNewFormat_SeasonVersionCompactS01V2(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "The Fragrant Flower Blooms with Dignity S01v2 MULTi 1080p WEB x264 AAC -Tsundere-Raws (NF)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if got := canonicalSeason(res); got != "1" {
		t.Fatalf("expected season=1 from S01v2, got=%q", got)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrVersion, "V2") {
		t.Fatalf("expected version=V2 from S01v2, attrs=%v", res.Attributes)
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(res.CanonicalTitle())), "s01v2") {
		t.Fatalf("compact season/version token should not stay in title, title=%q", res.CanonicalTitle())
	}
}

func TestLexiconNewFormat_MultiAudioSubDottedBlock(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Shingeki.no.Kyojin.The.Last.Attack.Movie.2024.MULTI.Audio.Sub[FRE][ENG][GER][ITA][SPA][POR][CHI][ARA].1080p.BDRip.CUSTOM.x264.TrueHD.AC3.PCM.EAC3-KuroNeko VOSTFR Attack on Titan: The Last Attack 劇場版 進撃の巨人 完結編 THE LAST ATTACK"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrAudioFlag, "MULTI_AUDIO") {
		t.Fatalf("expected audio_flag=MULTI_AUDIO, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubFlag, "soft") {
		t.Fatalf("expected sub_flag=soft, attrs=%v", res.Attributes)
	}
	for _, lang := range []string{"FRA", "ENG", "DEU", "ITA", "SPA", "POR", "ZH"} {
		if !hasAttrNormalized(res.Attributes, model.AttrSubLang, lang) {
			t.Fatalf("expected sub_lang=%s from bracket language bundle, attrs=%v", lang, res.Attributes)
		}
	}
	for _, tok := range []string{"CHI", "ARA"} {
		seenRemoved := false
		for _, tk := range res.Tokens {
			if strings.EqualFold(strings.TrimSpace(tk.Text), tok) && tk.Removed {
				seenRemoved = true
				break
			}
		}
		if !seenRemoved {
			t.Fatalf("expected %s token to be consumed as language marker", tok)
		}
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(res.CanonicalTitle())), "audio sub") {
		t.Fatalf("dotted MULTI.Audio.Sub marker should not pollute title, title=%q", res.CanonicalTitle())
	}
}

func TestLexiconNewFormat_MSubIsStructuredAndConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	res, err := New(cfg).Parse("[Gecko] Sample Title - 01 [BILI.WEB-DL 1080P AVC, AAC, M-SUB][4130B845]")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrSubFlag, "soft") {
		t.Fatalf("expected M-SUB to emit sub_flag=soft, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubLang, "MULTI") {
		t.Fatalf("expected M-SUB to emit sub_lang=MULTI, attrs=%v", res.Attributes)
	}
	for _, tk := range res.Tokens {
		if (strings.EqualFold(tk.Text, "M") || strings.EqualFold(tk.Text, "SUB")) && !tk.Removed {
			t.Fatalf("expected M-SUB token %q to be consumed, tokens=%v", tk.Text, res.Tokens)
		}
	}
}

func TestLexiconNewFormat_StandaloneVFIsFrenchDubAndConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	res, err := New(cfg).Parse("Cybersix.S01.VF.480p.DVDRIP.X264-NOTAG")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrAudioLang, "FRA") {
		t.Fatalf("expected VF to emit audio_lang=FRA, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrAudioFlag, "DUB") {
		t.Fatalf("expected VF to emit audio_flag=DUB, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrSubFlag, "soft") {
		t.Fatalf("VF must not be classified as a subtitle marker, attrs=%v", res.Attributes)
	}
	for _, tk := range res.Tokens {
		if strings.EqualFold(tk.Text, "VF") && !tk.Removed {
			t.Fatalf("expected standalone VF to be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestLexiconNewFormat_VOSTFRVFComboIsConsumedAsFrenchAudioAndSubs(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	res, err := New(cfg).Parse("[ERR] Mermaid Melody Pichi Pichi Pitch MULTI VOSTFR_VF [BDRip 720p]")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	for typ, normalized := range map[model.AttrType]string{
		model.AttrAudioLang: "FRA",
		model.AttrAudioFlag: "DUB",
		model.AttrSubLang:   "FRA",
		model.AttrSubFlag:   "soft",
	} {
		if !hasAttrNormalized(res.Attributes, typ, normalized) {
			t.Fatalf("expected %s=%s, attrs=%v", typ, normalized, res.Attributes)
		}
	}
	for _, tk := range res.Tokens {
		if (strings.EqualFold(tk.Text, "VOSTFR") || strings.EqualFold(tk.Text, "VF") || tk.Text == "_") && !tk.Removed {
			t.Fatalf("expected VOSTFR_VF token %q to be consumed, tokens=%v", tk.Text, res.Tokens)
		}
	}
	if got := res.CanonicalTitle(); strings.Contains(strings.ToUpper(got), "VF") {
		t.Fatalf("VOSTFR_VF must not pollute title, title=%q", got)
	}
}

func TestLexiconNewFormat_SubDotEspanolBracketDoesNotBecomeTailGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Z-A] Fate strange Fake - S01E09 (WEB 1080p AAC) [Sub. Español]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrSubLang, "SPA") {
		t.Fatalf("expected sub_lang=SPA for bracket sub. español, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubFlag, "soft") {
		t.Fatalf("expected sub_flag=soft for bracket sub. español, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrGroup, "Sub. Español") {
		t.Fatalf("Sub. Español should not be parsed as group, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrTailGroup, "Sub. Español") {
		t.Fatalf("Sub. Español should not be parsed as tail_group, attrs=%v", res.Attributes)
	}
}

func TestLexiconNewFormat_SubDotEspanolParen(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[GSF] Hitoribocchi no Marumaru Seikatsu [BD 810p] (sub. español)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrSubLang, "SPA") {
		t.Fatalf("expected sub_lang=SPA for sub. español, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubFlag, "soft") {
		t.Fatalf("expected sub_flag=soft for sub. español, attrs=%v", res.Attributes)
	}
}

func TestLexiconNewFormat_SubValenciaTail(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[AMF]Shibou Yuugi de Meshi o Kuu - 08v2 ----it all - Sub valencià.mkv"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrSubLang, "CAT") {
		t.Fatalf("expected sub_lang=CAT for sub valencià, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubFlag, "soft") {
		t.Fatalf("expected sub_flag=soft for sub valencià, attrs=%v", res.Attributes)
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(res.CanonicalTitle())), "valenci") {
		t.Fatalf("sub valencià tail should not pollute title, title=%q", res.CanonicalTitle())
	}
}

func TestLexiconNewFormat_SubCnJpTrilingualPlainBundle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[弗里吉亚宫内厅字幕组] 悲剧元凶的最强外道最终BOSS女王为民竭力 / Lastame S2 - 01 HEVC 1080P 简繁日三语 MKV"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	for _, lang := range []string{"JPSC", "JPTC"} {
		if !hasAttrNormalized(res.Attributes, model.AttrSubLang, lang) {
			t.Fatalf("expected sub_lang=%s from 简繁日三语, attrs=%v", lang, res.Attributes)
		}
		if !hasAttrNormalized(res.Attributes, model.AttrSubLangGroup, lang) {
			t.Fatalf("expected sub_lang_group=%s from 简繁日三语, attrs=%v", lang, res.Attributes)
		}
	}
	for _, rawLang := range []string{"SC", "TC", "JPN"} {
		if hasAttrNormalized(res.Attributes, model.AttrSubLang, rawLang) {
			t.Fatalf("did not expect raw sub_lang=%s after normalization, attrs=%v", rawLang, res.Attributes)
		}
	}
	if strings.Contains(strings.TrimSpace(res.CanonicalTitle()), "简繁日三语") {
		t.Fatalf("trilingual subtitle token should not remain in title, title=%q", res.CanonicalTitle())
	}
}

func TestLexiconNewFormat_PlatformUNEXTVariants(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Duel Masters LOST: Boukyaku no Taiyou S01E04 1080p U.NEXT WEB-DL AAC2.0 H.264 (Duel Masters LOST ～忘却の太陽～)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrPlatform, "UNEXT") {
		t.Fatalf("expected platform=UNEXT for U.NEXT variant, attrs=%v", res.Attributes)
	}
	if sem := canonicalEPSemantics(res); sem == nil {
		t.Fatalf("expected ep semantics for S01E04 title")
	}
}

func TestLexiconNewFormat_PlatformUNXTVariant(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Crayon Shin-chan the Movie Super Hot The Spicy Kasukabe Dancers 2025 1080p UNXT WEB-DL AAC2.0 H 264-VARYG (Crayon Shin-chan: Chou Karei! Shakunetsu no Kasukabe Dancers)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrPlatform, "UNEXT") {
		t.Fatalf("expected platform=UNEXT for UNXT variant, attrs=%v", res.Attributes)
	}
	if strings.Contains(strings.ToUpper(strings.TrimSpace(res.CanonicalTitle())), "UNXT") {
		t.Fatalf("UNXT platform marker should not remain in title, title=%q", res.CanonicalTitle())
	}
}

func TestLexiconNewFormat_CJKSubtitleBlockDoesNotBecomeTailGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[豌豆字幕组&LoliHouse] 关于我转生变成史莱姆这档事 第四季 / Tensei Shitara Slime Datta Ken 4th Season - 01(73) [WebRip 1080p HEVC-10bit AAC][简繁外挂字幕]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrSubLang, "SC") || !hasAttrNormalized(res.Attributes, model.AttrSubLang, "TC") {
		t.Fatalf("expected SC/TC subtitle langs from 简繁外挂字幕, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubFlag, "ex") {
		t.Fatalf("expected sub_flag=ex from 简繁外挂字幕, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrGroup, "简繁外挂字幕") {
		t.Fatalf("subtitle meta block should not become group, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrTailGroup, "简繁外挂字幕") {
		t.Fatalf("subtitle meta block should not become tail_group, attrs=%v", res.Attributes)
	}
}

func TestLexiconNewFormat_VOSTFRSeasonBatchKeepsFrenchSubLang(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "Ave Mujica - The Die is Cast S01 VOSTFR 1080p WEB x264 AAC -Tsundere-Raws (CR) (Multi-Subs, BanG Dream! Ave Mujica)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrSubLang, "FRA") {
		t.Fatalf("expected sub_lang=FRA for VOSTFR marker, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubFlag, "soft") {
		t.Fatalf("expected sub_flag=soft for VOSTFR marker, attrs=%v", res.Attributes)
	}
}

func TestLexiconNewFormat_YTBPlatformAndZhHansSubtitle(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Gecko] Dog's Tea - S01 (犬之茗; Cat's Tea; Quan Zhi Ming; The Dog's Tea) [YTB.WEB-DL 1080P AVC, Opus, SUB, HARD SUB (ZH-HANS)][BATCH]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrPlatform, "YTB") {
		t.Fatalf("expected platform=YTB for YTB marker, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubLang, "SC") {
		t.Fatalf("expected sub_lang=SC for ZH-HANS marker, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrSubLang, "ZH") {
		t.Fatalf("ZH-HANS should keep SC precision and not collapse to ZH, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrSubFlag, "hard") {
		t.Fatalf("expected sub_flag=hard for HARD SUB marker, attrs=%v", res.Attributes)
	}
}

func TestLexiconNewFormat_ZhHantSubtitleKeepsTC(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Gecko] Dog's Tea - S01 [WEB-DL 1080P AVC, SUB, HARD SUB (ZH-HANT)][BATCH]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrSubLang, "TC") {
		t.Fatalf("expected sub_lang=TC for ZH-HANT marker, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrSubLang, "ZH") {
		t.Fatalf("ZH-HANT should keep TC precision and not collapse to ZH, attrs=%v", res.Attributes)
	}
}

func TestLexiconNewFormat_HiResConsumedAsNoteNotTailGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "【TSDM字幕组】[超时空辉夜姬][Cosmic Princess Kaguya][HEVC-10bit 1080p AAC][MKV][简日内封字幕]付『超かぐや姫！』劇中歌集10首自购Hi-Res"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if !hasAttrNormalized(res.Attributes, model.AttrGroup, "TSDM 字幕组") {
		t.Fatalf("expected group=TSDM 字幕组, attrs=%v", res.Attributes)
	}
	if !hasAttrNormalized(res.Attributes, model.AttrTitle, "超时空辉夜姬") {
		t.Fatalf("expected title=超时空辉夜姬, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrGroup, "Res") {
		t.Fatalf("Hi-Res tail should not be parsed as group=Res, attrs=%v", res.Attributes)
	}
	for _, a := range res.Attributes {
		if a.Type == model.AttrTitleAlias && strings.EqualFold(strings.TrimSpace(a.Normalized), "Hi Res") {
			t.Fatalf("Hi-Res should not leak into title_alias, attrs=%v", res.Attributes)
		}
	}
	for _, tk := range res.Tokens {
		if (strings.EqualFold(strings.TrimSpace(tk.Text), "Hi") || strings.EqualFold(strings.TrimSpace(tk.Text), "Res")) && !tk.Removed {
			t.Fatalf("Hi-Res tokens should be consumed, tokens=%v", res.Tokens)
		}
	}
}

func TestLexiconNewFormat_TailCustomNotParsedAsTailGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[EssAirTé] JoJo's Bizarre Adventure STEEL BALL RUN - EP01 VOSTFR-custom"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if hasAttrNormalized(res.Attributes, model.AttrGroup, "custom") {
		t.Fatalf("tail custom should not be parsed as group, attrs=%v", res.Attributes)
	}
	if hasAttrNormalized(res.Attributes, model.AttrTailGroup, "custom") {
		t.Fatalf("tail custom should not be parsed as tail_group, attrs=%v", res.Attributes)
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want=%q", got, "1")
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(canonicalTitle(res))), "custom") {
		t.Fatalf("tail custom should not pollute title, title=%q", canonicalTitle(res))
	}
}
