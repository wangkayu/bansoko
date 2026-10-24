package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/model"
)

// Underscore-joined language combos such as CHS_JP are a compact notation for
// two subtitle languages. Like the lexicon's code-combo entries (GB&JP),
// they split into independent sub_langs with no group and no sub_flag;
// an explicit enclosure marker (e.g. 简日内嵌) wins when both appear.
func TestUnderscoreJoinedLangComboDoesNotInferSoft(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw      string
		wantLang []string
		wantFlag []string
	}{
		{
			raw:      "[G][Show][01][1080p HEVC][CHS_JP]",
			wantLang: []string{"SC", "JPN"},
			wantFlag: nil,
		},
		{
			raw:      "[G][Show][01][1080p HEVC][CHT_JP]",
			wantLang: []string{"TC", "JPN"},
			wantFlag: nil,
		},
		{
			raw:      "[G][Show][01][1080p HEVC][GB_JPN]",
			wantLang: []string{"SC", "JPN"},
			wantFlag: nil,
		},
		{
			raw:      "[G][Show][01][1080p HEVC][CHS_JP][简日内嵌]",
			wantLang: []string{"JPSC"},
			wantFlag: []string{"hard"},
		},
		{
			raw:      "[悠哈璃羽字幕社][FX战士久留美 / FX Senshi Kurumi-chan][01][1080p HEVC][CHS_JP][简日内嵌]",
			wantLang: []string{"JPSC"},
			wantFlag: []string{"hard"},
		},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := attrValues(res, model.AttrSubLang); !equalAttrValues(got, tc.wantLang) {
				t.Errorf("sub_lang=%v want %v", got, tc.wantLang)
			}
			if got := attrValues(res, model.AttrSubFlag); !equalAttrValues(got, tc.wantFlag) {
				t.Errorf("sub_flag=%v want %v", got, tc.wantFlag)
			}
		})
	}
}

// Subtitle-claimed language codes in codec-less brackets must not become
// audio_lang; a codec/dub context (e.g. [JPN AAC]) keeps it.
func TestSubClaimedLangCodeNotAudioLang(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"[G][Show][01][1080p HEVC][CHS_JP]",
		"[G][Show][01][1080p HEVC][GB_JP]",
		"[悠哈璃羽字幕社][FX战士久留美 / FX Senshi Kurumi-chan][01][1080p HEVC][CHS_JP][简日内嵌]",
	} {
		t.Run(raw, func(t *testing.T) {
			res, err := p.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := attrValues(res, model.AttrAudioLang); len(got) > 0 {
				t.Errorf("audio_lang should be empty, got %v", got)
			}
		})
	}
	res, err := p.Parse("[G][Show][01][1080p][JPN AAC]")
	if err != nil {
		t.Fatal(err)
	}
	if got := attrValues(res, model.AttrAudioLang); !equalAttrValues(got, []string{"JPN"}) {
		t.Errorf("[JPN AAC] audio_lang=%v want [JPN]", got)
	}
}

// Ampersand-joined language lists are language enumerations too (dual/multi
// language): split into independent sub_langs, no group, no sub_flag.
func TestAmpersandJoinedLangListDualLanguage(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw      string
		wantLang []string
	}{
		{"[G][Show][01][1080p][CHS&JP]", []string{"SC", "JPN"}},
		{"[G][Show][01][1080p][CHT&ENG]", []string{"TC", "ENG"}},
		{"[G][Show][01][1080p][CHS&CHT&ENG]", []string{"SC", "TC", "ENG"}},
		{"[G][Show][01][1080p][GB&JP]", []string{"SC", "JPN"}},
		{"[G][Show][01][1080p][GB_ENG]", []string{"SC", "ENG"}},
		{"[G][Show][01][1080p][BIG5&JP]", []string{"TC", "JPN"}},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := attrValues(res, model.AttrSubLang); !equalAttrValues(got, tc.wantLang) {
				t.Errorf("sub_lang=%v want %v", got, tc.wantLang)
			}
			if got := attrValues(res, model.AttrSubLangGroup); len(got) > 0 {
				t.Errorf("sub_lang_group should be empty, got %v", got)
			}
			if got := attrValues(res, model.AttrSubFlag); len(got) > 0 {
				t.Errorf("sub_flag should be empty, got %v", got)
			}
		})
	}
}

func equalAttrValues(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
