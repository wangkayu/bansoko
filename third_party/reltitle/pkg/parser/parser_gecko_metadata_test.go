package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"reflect"
	"testing"
)

func TestReleasePlatformAbbreviations(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ raw, want string }{
		{"IQ", "IQIYI"}, {"NHK1", "NHK_ONE"}, {"TWTR", "TWITTER"},
		{"VIU", "VIU"}, {"WETV", "WETV"}, {"VIKI", "VIKI"},
		{"OV", "OCEANVEIL"}, {"OVEIL", "OCEANVEIL"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			res, err := p.Parse("[Gecko] Show - S01E01 [" + tc.raw + ".WEB-DL 1080P AVC, AAC]")
			if err != nil {
				t.Fatal(err)
			}
			if got := attrValues(res, model.AttrPlatform); !reflect.DeepEqual(got, []string{tc.want}) {
				t.Errorf("platform=%v want %s", got, tc.want)
			}
		})
	}
	res, err := p.Parse("[Doomdos] - Galilei Donna - 04 [1080p OV WEB-DL]")
	if err != nil {
		t.Fatal(err)
	}
	if got := attrValues(res, model.AttrPlatform); !reflect.DeepEqual(got, []string{"OCEANVEIL"}) {
		t.Errorf("OV platform=%v", got)
	}
}

func TestGeckoNumericCRC(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"64104795", "81495462", "52370168"} {
		res, err := p.Parse("[Gecko] Show - S01E11 [1080p AVC AAC][" + code + "].mkv")
		if err != nil {
			t.Fatal(err)
		}
		if got := attrValues(res, model.AttrCRC); !reflect.DeepEqual(got, []string{code}) {
			t.Errorf("crc=%v want %s", got, code)
		}
	}
	res, err := p.Parse("[G] Show - 01 [1080p][20260103]")
	if err != nil {
		t.Fatal(err)
	}
	if got := attrValues(res, model.AttrCRC); len(got) > 0 {
		t.Errorf("date became crc: %v", got)
	}
}

func TestGeckoAliasAndSeasonBoundaries(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Parse("[TokekHutan] I'll Live a Long Life to Dote on My Favorite Stepbrother! - S01E11 (最推しの義兄を愛でるため、長生きします！; Saioshi no Gikei wo Mederu Tame, Nagaiki shimasu!) [AMZN.WEB-DL 1080P AVC, EAC3, MULTi][7139EB96]")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"最推しの義兄を愛でるため、長生きします！", "Saioshi no Gikei wo Mederu Tame, Nagaiki shimasu!"}
	if got := attrValues(res, model.AttrTitleAlias); !reflect.DeepEqual(got, want) {
		t.Errorf("aliases=%v want %v", got, want)
	}
	res, err = p.Parse("[Gecko] 9-nine- Ruler's Crown - S01 (9: Ruler's Crown) [IQ.WEB-DL 1080P AVC, AAC, M-SUB][BATCH]")
	if err != nil {
		t.Fatal(err)
	}
	if got := canonicalEpisode(res); got != "" {
		t.Errorf("alias digit became episode: %s", got)
	}
	if res.ContentKind != model.ContentKindBatch {
		t.Errorf("kind=%s want BATCH", res.ContentKind)
	}
	res, err = p.Parse("[Gecko] iii icecrin - S01-S02 (iiiあいすくりん) [CTHP.WEB-DL 1080P AVC, AAC, M-SUB][BATCH]")
	if err != nil {
		t.Fatal(err)
	}
	if got := canonicalSeasonRange(res); got != "S1-S2" {
		t.Errorf("season range=%s want 1-2", got)
	}
	res, err = p.Parse("[Gecko] Fate Grand Order You've Lost Ritsuka Fujimaru - S04E04 (Fate/Grand Order 藤丸立香はわからない; Fate/Grand Order: Fujimaru Ritsuka wa Wakaranai Season 4) [YTB.WEB-DL 1080P AVC, Opus][1AE8E673]")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"Fate/Grand Order 藤丸立香はわからない", "Fate/Grand Order: Fujimaru Ritsuka wa Wakaranai"}
	if got := attrValues(res, model.AttrTitleAlias); !reflect.DeepEqual(got, want) {
		t.Errorf("slash aliases=%v want %v", got, want)
	}
	res, err = p.Parse("[geckyzz] Fudanshi Shoukan - S11 (Fudanshi Shoukan: Isekai de Shinjuu ni Hameraremashita 11th Season) [YTB.WEB-DL 1080P AVC, Opus][BATCH]")
	if err != nil {
		t.Fatal(err)
	}
	if got := canonicalTitle(res); got != "Fudanshi Shoukan" {
		t.Errorf("title=%q", got)
	}
	if got := canonicalSeason(res); got != "11" {
		t.Errorf("season=%q", got)
	}
}

func TestGeckoExplicitMetadata(t *testing.T) {
	p, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw  string
		kind model.AttrType
		want string
	}{
		{"[geckyzz] Chainsmoker Cat - S01E10 [UNCENSORED, OVEIL.WEB-DL 1080P AVC, AAC, HARD SUB (EN)][3773E7E9]", model.AttrVideoCodec, "AVC"},
		{"[geckyzz] Chainsmoker Cat - S01E10 [UNCENSORED, OVEIL.WEB-DL 1080P AVC, AAC, HARD SUB (EN)][3773E7E9]", model.AttrAudioCodec, "AAC"},
		{"[geckyzz] Chainsmoker Cat - S01E10 [UNCENSORED, OVEIL.WEB-DL 1080P AVC, AAC, HARD SUB (EN)][3773E7E9]", model.AttrSubLang, "ENG"},
		{"[Gecko] Show - S01E01 [1080P WEB-DL, AVC, AAC, HARD SUB (EN)]", model.AttrSourceType, "WEB-DL"},
		{"[G] Film (2024) [AMZN.WEB-DL 1080P AVC, EAC3, SUBBED (EN)]", model.AttrAudioCodec, "EAC3"},
		{"[geckyzz] I Shall Never Return (1994) [OVEIL.WEB-DL 1080P AVC, AAC, HARD SUB (EN)][2F00EA91]", model.AttrAudioCodec, "AAC"},
		{"[geckyzz] Marika's Love Meter Malfunction - S01 [UNCENSORED, OVEIL.WEB-DL 1080P AVC, AAC, HARD SUB (EN)][BATCH]", model.AttrAudioCodec, "AAC"},
		{"[geckyzz] Perfect Addiction - S01E10 [OVEIL.WEB-DL 1080P AVC, AAC, HARD SUB (EN)][F92E6DD6]", model.AttrPlatform, "OCEANVEIL"},
		{"[CicakRumah] Everyday HOST CLUB - S01E01v3 [AMZN.WEB-DL 1080P AVC EAC3]", model.AttrVersion, "v3"},
		{"[Gecko] Show - S01E02v2 [1080p]", model.AttrVersion, "v2"},
		{"[CicakRumah] False Memory (2020) [v2, BSITE.WEB-DL 1080P HEVC, AAC D-AUD, D-SUB]", model.AttrVersion, "v2"},
		{"[CicakRumah] False Memory (2020) [BSITE.WEB-DL 1080P AAC D-AUD, D-SUB]", model.AttrAudioFlag, "DUAL_AUDIO"},
		{"[Gecko] Show - S01E01 [1080P AAC M-AUD, M-SUB]", model.AttrAudioFlag, "MULTI_AUDIO"},
		{"[Gecko] Show - S01E01 [CTHP.WEB-DL 1080p AVC AAC M-SUB]", model.AttrSubFlag, "soft"},
		{"[Gecko] Show - S01E01 [CTHP.WEB-DL 1080p AVC AAC D-SUB]", model.AttrSubFlag, "soft"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			got := attrValues(res, tc.kind)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("%s=%v want %s", tc.kind, got, tc.want)
			}
			if tc.want == "v3" && (canonicalEpisode(res) != "1") {
				t.Errorf("episode=%s want 1", canonicalEpisode(res))
			}
		})
	}
}
