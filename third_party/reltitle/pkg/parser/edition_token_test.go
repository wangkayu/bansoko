package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestEditionToken_AudioCommentaryVer(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] Saikyou no Ousama, Nidome no Jinsei wa Nani wo Suru ~Audio Commentary ver.~ - 08 (AT-X 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	title := canonicalTitle(res)
	if strings.Contains(strings.ToLower(title), "audio commentary") {
		t.Fatalf("edition token leaked into title: %q", title)
	}
	if canonicalEpisode(res) != "8" {
		t.Fatalf("episode=%q want=8", canonicalEpisode(res))
	}

	notes := attrValues(res, model.AttrNote)
	found := false
	for _, n := range notes {
		if strings.EqualFold(strings.TrimSpace(n), "AUDIO_COMMENTARY") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected note AUDIO_COMMENTARY, got=%v", notes)
	}
}

func TestEditionToken_AudioCommentaryVer_UnderscoreRoutedByGroup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[shincaps] Saikyou no Ousama ~Audio_Commentary ver.~ - 08 (AT-X 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if canonicalEpisode(res) != "8" {
		t.Fatalf("episode=%q want=8", canonicalEpisode(res))
	}
	if !hasParseAttrValue(res, model.AttrNote, "AUDIO_COMMENTARY") {
		t.Fatalf("expected note AUDIO_COMMENTARY")
	}
}

func TestEditionToken_AudioCommentaryVer_UnderscoreNotGeneric(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[other] Saikyou no Ousama ~Audio_Commentary ver.~ - 08 (AT-X 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if hasParseAttrValue(res, model.AttrNote, "AUDIO_COMMENTARY") {
		t.Fatalf("unexpected note AUDIO_COMMENTARY for non-routed group")
	}
}

func TestEditionToken_EncodeVer(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Moozzi2] Clevatess BD-BOX [ x265-10Bit Ver. ] - TV + SP"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if canonicalTitle(res) != "Clevatess" {
		t.Fatalf("title=%q want=Clevatess", canonicalTitle(res))
	}
	if !hasParseAttrValue(res, model.AttrNote, "ENCODE_VER") {
		t.Fatalf("expected note ENCODE_VER")
	}
}

func TestEditionToken_NoUnderscorePollution_ForEpisodeSplit(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[阿特拉斯字幕组·雪原市出差所][命运-奇异赝品_Fate／strange Fake][02_群像 VS 虚像][简繁日内封PGS][Japanese Dub][Web-DL Remux][1080p AVC AAC][附：第2话先行上映后花泽香菜&诸星堇访谈节目熟肉（流媒体）]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if canonicalEpisode(res) != "2" {
		t.Fatalf("episode=%q want=2", canonicalEpisode(res))
	}
	if hasParseAttrValue(res, model.AttrNote, "AUDIO_COMMENTARY") {
		t.Fatalf("unexpected AUDIO_COMMENTARY note")
	}
}

func TestEditionToken_UncensoredVer(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Example Show [Uncensored Ver.] - 01 [1080p AVC AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if canonicalEpisode(res) != "1" {
		t.Fatalf("episode=%q want=1", canonicalEpisode(res))
	}
	if !hasParseAttrValue(res, model.AttrNote, "UNCENSORED_VER") {
		t.Fatalf("expected note UNCENSORED_VER")
	}
}

func TestEditionToken_DirectorsCut(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Example Show [Director's Cut] - 01 [1080p AVC AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if canonicalEpisode(res) != "1" {
		t.Fatalf("episode=%q want=1", canonicalEpisode(res))
	}
	if !hasParseAttrValue(res, model.AttrNote, "DIRECTORS_CUT") {
		t.Fatalf("expected note DIRECTORS_CUT")
	}
}

func TestEditionToken_UncutVer(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Example Show [Uncut Ver.] - 01 [1080p AVC AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalEpisode(res) != "1" {
		t.Fatalf("episode=%q want=1", canonicalEpisode(res))
	}
	if !hasParseAttrValue(res, model.AttrNote, "UNCUT_VER") {
		t.Fatalf("expected note UNCUT_VER")
	}
}

func TestEditionToken_ExtendedCut(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Example Show [Extended Cut] - 01 [1080p AVC AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalEpisode(res) != "1" {
		t.Fatalf("episode=%q want=1", canonicalEpisode(res))
	}
	if !hasParseAttrValue(res, model.AttrNote, "EXTENDED_CUT") {
		t.Fatalf("expected note EXTENDED_CUT")
	}
}

func TestEditionToken_RemasteredVer(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Example Show [Remastered Ver.] - 01 [1080p AVC AAC]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if canonicalEpisode(res) != "1" {
		t.Fatalf("episode=%q want=1", canonicalEpisode(res))
	}
	if !hasParseAttrValue(res, model.AttrNote, "REMASTERED_VER") {
		t.Fatalf("expected note REMASTERED_VER")
	}
}

func TestEditionToken_WebVersionCleanup(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Lilith-Raws] 叫我對大哥 WEB版 / Ore, Tsushima Web ver. - 09 [Baha][WEB-DL][1080p][AVC AAC][CHT][MP4]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	title := canonicalTitle(res)
	if strings.Contains(strings.ToLower(title), "web") || strings.Contains(title, "版") {
		t.Fatalf("web version token leaked into title: %q", title)
	}
	if !hasParseAttrValue(res, model.AttrNote, "WEB_VERSION") {
		t.Fatalf("expected note WEB_VERSION")
	}
}

func TestEditionToken_WebWordInTitleNotAsEdition(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[DBD-Raws][电脑冒险记/Dennou Boukenki Webdiver][01-52TV全集+SP][480P][BDRip][AVC][AC3][MKV](電脳冒険記ウェブダイバー)"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if hasParseAttrValue(res, model.AttrNote, "WEB_VERSION") {
		t.Fatalf("unexpected WEB_VERSION for title word Webdiver")
	}
	aliases := canonicalAliases(res)
	found := false
	for _, a := range aliases {
		if strings.Contains(strings.ToLower(a), "webdiver") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected alias to keep Webdiver, aliases=%v", aliases)
	}
}

func TestEditionToken_CompositeTVAndUncensoredHeadBlock(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TV版&无修版] 茉莉花酱的好感度正在崩坏 - EP01 [简／繁] (1080p H.264 AAC SRTx2) {茉莉花同學的好感度壞得很徹底 | 茉莉花ちゃんの好感度はぶっ壊れている}"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if canonicalEpisode(res) != "1" {
		t.Fatalf("episode=%q want=1", canonicalEpisode(res))
	}
	if canonicalTitle(res) != "茉莉花酱的好感度正在崩坏" {
		t.Fatalf("title=%q want=茉莉花酱的好感度正在崩坏", canonicalTitle(res))
	}
	if len(attrValues(res, model.AttrGroup)) != 0 {
		t.Fatalf("composite version head block should not become group, groups=%v", attrValues(res, model.AttrGroup))
	}
	if !hasParseAttrValue(res, model.AttrNote, "TV_VERSION") {
		t.Fatalf("expected note TV_VERSION")
	}
	if !hasParseAttrValue(res, model.AttrNote, "UNCENSORED_VERSION") {
		t.Fatalf("expected note UNCENSORED_VERSION")
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "SC") || !hasAttrValue(attrValues(res, model.AttrSubLang), "TC") {
		t.Fatalf("expected sub_lang SC/TC, got=%v", attrValues(res, model.AttrSubLang))
	}
}

func TestEditionToken_CompositeTVAndFullUncensoredHeadBlock(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[TV版&完全无修版] 淫狱团地 - EP01 [简／繁] (1080p H.264 AAC SRTx2) {インゴクダンチ | Ingoku Danchi：Deviant's Apartment Complex}"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if canonicalEpisode(res) != "1" {
		t.Fatalf("episode=%q want=1", canonicalEpisode(res))
	}
	if canonicalTitle(res) != "淫狱团地" {
		t.Fatalf("title=%q want=淫狱团地", canonicalTitle(res))
	}
	if len(attrValues(res, model.AttrGroup)) != 0 {
		t.Fatalf("composite version head block should not become group, groups=%v", attrValues(res, model.AttrGroup))
	}
	if !hasParseAttrValue(res, model.AttrNote, "TV_VERSION") {
		t.Fatalf("expected note TV_VERSION")
	}
	if !hasParseAttrValue(res, model.AttrNote, "UNCENSORED_VERSION") {
		t.Fatalf("expected note UNCENSORED_VERSION")
	}
	if !hasAttrValue(attrValues(res, model.AttrSubLang), "SC") || !hasAttrValue(attrValues(res, model.AttrSubLang), "TC") {
		t.Fatalf("expected sub_lang SC/TC, got=%v", attrValues(res, model.AttrSubLang))
	}
}

func TestEditionToken_Restore4K2KHeadBlockConsumed(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[4K数字修复／2K版][SUK字幕组][哥斯拉][1984][中日双语字幕][HDTVrip][AVC][10bit][1080P][MKV]"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !hasParseAttrValue(res, model.AttrEdition, "RESTORE_4K_2K_EDITION") {
		t.Fatalf("expected edition RESTORE_4K_2K_EDITION, editions=%v", attrValues(res, model.AttrEdition))
	}
	if hasParseAttrValue(res, model.AttrNote, "RESTORE_4K_2K_EDITION") {
		t.Fatalf("RESTORE_4K_2K_EDITION should not remain in note, notes=%v", attrValues(res, model.AttrNote))
	}
	if g := canonicalGroup(res); !strings.Contains(strings.ToLower(strings.TrimSpace(g)), "suk") {
		t.Fatalf("group=%q want contains SUK", g)
	}
	if title := canonicalTitle(res); title != "哥斯拉" {
		t.Fatalf("title=%q want=%q", title, "哥斯拉")
	}
}

func hasParseAttrValue(res *model.ParseResult, typ model.AttrType, normalized string) bool {
	for _, a := range res.Attributes {
		if a.Type != typ {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(a.Normalized), normalized) {
			return true
		}
	}
	return false
}
