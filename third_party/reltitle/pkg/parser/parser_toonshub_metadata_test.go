package parser

import (
	"reflect"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestToonsHubCNSubtitleSlot(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		marker string
		langs  []string
		soft   bool
	}{
		{"EN", []string{"ENG"}, false},
		{"JP", []string{"JPN"}, false},
		{"CA", []string{"CAT"}, false},
		{"MULTi", []string{"MULTI"}, true},
		{"CHS&EN&AR&FR&DE&ID&IT&MY&PT&RU&ES&TH&VI&PL&CA", []string{"SC", "ENG", "FRA", "SPA", "CAT", "RUS", "DEU", "ITA", "POR", "ARA", "IND", "THA", "POL", "MSA", "VIE"}, true},
	} {
		t.Run(tc.marker, func(t *testing.T) {
			res, err := p.Parse("[ToonsHub] 某作品 / Some Show - 01 [1080P][" + tc.marker + "][MKV]")
			if err != nil {
				t.Fatal(err)
			}
			if got := attrValues(res, model.AttrSubLang); !reflect.DeepEqual(got, tc.langs) {
				t.Errorf("languages=%v want %v", got, tc.langs)
			}
			if got := attrValues(res, model.AttrAudioLang); len(got) > 0 {
				t.Errorf("subtitle slot produced audio language: %v", got)
			}
			if got := attrValues(res, model.AttrAudioFlag); len(got) > 0 {
				t.Errorf("subtitle slot produced audio flag: %v", got)
			}
			if got := attrValues(res, model.AttrSubFlag); tc.soft && !reflect.DeepEqual(got, []string{"soft"}) || !tc.soft && len(got) > 0 {
				t.Errorf("sub_flag=%v soft=%v", got, tc.soft)
			}
		})
	}
	res, err := p.Parse("[ToonsHub] Some Show S01E01 [1080p][EN][MKV]")
	if err != nil {
		t.Fatal(err)
	}
	if got := attrValues(res, model.AttrAudioLang); !reflect.DeepEqual(got, []string{"ENG"}) {
		t.Errorf("English route changed: audio_lang=%v", got)
	}
}

func TestParserToonsHubMetadata(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatal(err)
	}
	p := New(cfg)
	tests := []struct {
		raw                             string
		subs, aliases, flags, platforms []string
	}{
		{"[ToonsHub] 某作品 / Some Show - 01 [1080p][BSITE][CHT&JA][MKV]", []string{"JPTC"}, []string{"Some Show"}, nil, []string{"BILIBILI"}},
		{"[G] Show - 01 [1080p][CHT&JA][MKV]", []string{"TC", "JPN"}, nil, nil, nil},
		{"[G] Show - 01 [1080p][CHS&EN][MKV]", []string{"SC", "ENG"}, nil, nil, nil},
		{"[G] Show - 01 [1080p][BSITE][CHS&JA&EN][MKV]", []string{"SC", "JPN", "ENG"}, nil, nil, []string{"BILIBILI"}},
		{"[G] Show - 01 [1080p][简日双语][MKV]", []string{"JPSC"}, nil, nil, nil},
		{"[G] Show - 01 [1080p][CHT&JA&EN][MKV]", []string{"TC", "JPN", "ENG"}, nil, nil, nil},
		{"[G] Show - 01 [1080p][CHS&JA&EN][MKV]", []string{"SC", "JPN", "ENG"}, nil, nil, nil},
		{"[ToonsHub] 青之箱 第二季 / Blue Box Season 2 - 01 [1080P][DUAL][CHS&CHT&JP&EN&KO][MKV]", []string{"SC", "TC", "JPN", "ENG", "KOR"}, []string{"Blue Box"}, []string{"DUAL_AUDIO"}, nil},
		{"[ToonsHub] 超时空辉夜姬！ / Cosmic Princess Kaguya! (2026) [1080P][BSITE][WEB-DL][AVC][CHS&JA][MKV]", []string{"JPSC"}, []string{"Cosmic Princess Kaguya!"}, nil, []string{"BILIBILI"}},
		{"[ToonsHub] 某作品 / Some Show - 01 [1080P][MULTi][CHS&CHT&EN&KO][MKV]", []string{"SC", "TC", "ENG", "KOR"}, []string{"Some Show"}, nil, nil},
		{"[G] Sample Film (2026) [Alternate Title] [1080p][WEB-DL][CHS&JA][MKV]", []string{"SC", "JPN"}, []string{"Alternate Title"}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []struct {
				kind   model.AttrType
				values []string
			}{
				{model.AttrSubLang, tc.subs}, {model.AttrTitleAlias, tc.aliases}, {model.AttrAudioFlag, tc.flags}, {model.AttrPlatform, tc.platforms},
			} {
				got := attrValues(res, want.kind)
				if len(got) != len(want.values) || len(got) > 0 && !reflect.DeepEqual(got, want.values) {
					t.Errorf("%s=%v want %v", want.kind, got, want.values)
				}
			}
			var subFlags, groups []string
			if len(tc.subs) >= 2 {
				subFlags = []string{"soft"}
			} else {
				groups = tc.subs
			}
			for _, want := range []struct {
				kind   model.AttrType
				values []string
			}{{model.AttrSubFlag, subFlags}, {model.AttrSubLangGroup, groups}} {
				got := attrValues(res, want.kind)
				if len(got) != len(want.values) || len(got) > 0 && !reflect.DeepEqual(got, want.values) {
					t.Errorf("%s=%v want %v", want.kind, got, want.values)
				}
			}
		})
	}
}
