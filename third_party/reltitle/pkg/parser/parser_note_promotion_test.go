package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
)

func TestPromoteNoteAttributes(t *testing.T) {
	makeRule := func(vals []string, typ model.AttrType, norm string, normFromToken bool) notePromotionRule {
		m := make(map[string]struct{}, len(vals))
		for _, v := range vals {
			m[v] = struct{}{}
		}
		return notePromotionRule{
			values:              m,
			targetType:          typ,
			normalized:          norm,
			normalizedFromToken: normFromToken,
		}
	}
	p := &Parser{
		platformCanonical: map[string]string{
			"nf": "NETFLIX",
		},
		notePromotionRules: []notePromotionRule{
			makeRule([]string{"web"}, model.AttrSourceType, "WEBRip", false),
			makeRule([]string{"bdrip"}, model.AttrSourceType, "BDRip", false),
			makeRule([]string{"nf"}, model.AttrPlatform, "", true),
			makeRule([]string{"multi"}, model.AttrSubFlag, "MULTI", false),
			makeRule([]string{"subs", "sub"}, model.AttrSubFlag, "soft", false),
			makeRule([]string{"vostfr"}, model.AttrSubFlag, "soft", false),
		},
	}
	in := []model.Attribute{
		{Type: model.AttrNote, Normalized: "WEB"},
		{Type: model.AttrNote, Normalized: "BDRIP"},
		{Type: model.AttrNote, Normalized: "NF"},
		{Type: model.AttrNote, Normalized: "MULTI"},
		{Type: model.AttrNote, Normalized: "Subs"},
		{Type: model.AttrNote, Normalized: "VOSTFR"},
		{Type: model.AttrNote, Normalized: "ESP"},
		{Type: model.AttrSubFlag, Normalized: "soft"},
		{Type: model.AttrNote, Normalized: "WEEKLY"},
	}

	out := p.promoteNoteAttributes(in)

	assertAttr := func(idx int, typ model.AttrType, norm string) {
		if out[idx].Type != typ || out[idx].Normalized != norm {
			t.Fatalf("idx=%d got=(%s,%q) want=(%s,%q)", idx, out[idx].Type, out[idx].Normalized, typ, norm)
		}
	}

	assertAttr(0, model.AttrSourceType, "WEBRip")
	assertAttr(1, model.AttrSourceType, "BDRip")
	assertAttr(2, model.AttrPlatform, "NETFLIX")
	assertAttr(3, model.AttrSubFlag, "MULTI")
	assertAttr(4, model.AttrSubFlag, "soft")
	assertAttr(5, model.AttrSubFlag, "soft")
	assertAttr(6, model.AttrSubLang, "SPA")
	// non-promoted value should remain note
	assertAttr(8, model.AttrNote, "WEEKLY")
}

func TestPromoteNoteAttributes_CompositeSubAudioNotes(t *testing.T) {
	p := &Parser{}
	in := []model.Attribute{
		{Type: model.AttrNote, Normalized: "CHS_SUBS"},
		{Type: model.AttrNote, Normalized: "JPN_AUDIO_CHS_SUBS"},
		{Type: model.AttrNote, Normalized: "ENG_AUDIO_CHS_SUBS"},
		{Type: model.AttrNote, Normalized: "CHN_AUDIO_CHS_SUBS"},
		{Type: model.AttrNote, Normalized: "CHN_YUE_AUDIO"},
		{Type: model.AttrNote, Normalized: "CHN_YUE_AUDIO_SUBS"},
		{Type: model.AttrNote, Normalized: "CHT_HARDSUB"},
	}
	out := p.promoteNoteAttributes(in)

	has := func(typ model.AttrType, norm string) bool {
		for _, a := range out {
			if a.Type == typ && a.Normalized == norm {
				return true
			}
		}
		return false
	}
	if !has(model.AttrSubLang, "SC") {
		t.Fatalf("expected sub_lang=SC, got=%v", out)
	}
	for _, lang := range []string{"JPN", "ENG", "CHN"} {
		if !has(model.AttrAudioLang, lang) {
			t.Fatalf("expected audio_lang=%s, got=%v", lang, out)
		}
	}
	if !has(model.AttrAudioLang, "YUE") {
		t.Fatalf("expected audio_lang=YUE, got=%v", out)
	}
	if !has(model.AttrAudioFlag, "DUAL_AUDIO") {
		t.Fatalf("expected audio_flag=DUAL_AUDIO, got=%v", out)
	}
	if !has(model.AttrSubLang, "TC") {
		t.Fatalf("expected sub_lang=TC, got=%v", out)
	}
	if !has(model.AttrSubFlag, "hard") {
		t.Fatalf("expected sub_flag=hard, got=%v", out)
	}
	if !has(model.AttrSubFlag, "soft") {
		t.Fatalf("expected sub_flag=soft, got=%v", out)
	}
	for _, a := range out {
		if a.Type == model.AttrNote &&
			(a.Normalized == "CHS_SUBS" ||
				a.Normalized == "JPN_AUDIO_CHS_SUBS" ||
				a.Normalized == "ENG_AUDIO_CHS_SUBS" ||
				a.Normalized == "CHN_AUDIO_CHS_SUBS" ||
				a.Normalized == "CHN_YUE_AUDIO" ||
				a.Normalized == "CHN_YUE_AUDIO_SUBS" ||
				a.Normalized == "CHT_HARDSUB") {
			t.Fatalf("composite notes should be promoted, got=%v", out)
		}
	}
}
