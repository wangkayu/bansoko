package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

func TestAliasMetaCleanupRegression(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	tests := []struct {
		name      string
		raw       string
		mustTitle []string
		noAlias   []string
		noTitle   []string
	}{
		{
			name:      "streaming_meta_in_paren_removed_from_alias",
			raw:       "BLOOM S01E05 MULTi 1080p WEB AV1 AAC -Tsundere-Raws (NF) (VF, FRENCH, VOSTFR, Kaoru Hana wa Rin to Saku, The Fragrant Flower Blooms with Dignity)",
			mustTitle: []string{"BLOOM"},
			noAlias:   []string{"NF", "FRENCH", "VOSTFR"},
		},
		{
			name:      "codec_list_removed_from_alias",
			raw:       "[Anime Time] Dragon Ball Z Complete Series (Colour Corrected) [DVD] [Dual Audio] [480p][HEVC 10bit x265][AAC,AC3][Eng Sub] [Batch]",
			mustTitle: []string{"Dragon Ball"},
			noAlias:   []string{"AAC,AC3"},
		},
		{
			name:      "tv_reproduction_removed_from_alias",
			raw:       "[DBD-Raws][Fate / stay night TV Reproduction/命运之夜 总集篇][01-02合集][1080P][BDRip][HEVC-10bit][简繁外挂][FLAC][MKV]",
			mustTitle: []string{"命运之夜"},
			noAlias:   []string{"TV Reproduction"},
		},
		{
			name:      "tv_episode_list_removed_from_title_tail",
			raw:       "[DOMO] Kagaku Boukentai Tansar 5 TV 14,16-18,21-23",
			mustTitle: []string{"Kagaku Boukentai Tansar 5"},
			noTitle:   []string{"TV 14", "16-18", "21-23"},
		},
		{
			name:      "dvd_bundle_tail_removed_from_title",
			raw:       "[DVD Remux] Tetsuwan Atom Astro Boy (1963) Volume 1 R3 DVD Remux [English Sub] Episodes 1~5",
			mustTitle: []string{"Tetsuwan Atom Astro Boy"},
			noTitle:   []string{"DVD", "Episodes"},
		},
		{
			name:    "uploader_at_platform_removed_from_alias",
			raw:     "[我的幸福婚約 My Happy Marriage][全12集][日語中字][MKV][1080P][VARYG@NETFLIX]",
			noTitle: []string{"VARYG@NETFLIX"},
			noAlias: []string{"VARYG@NETFLIX"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			title := canonicalTitle(res)
			aliases := canonicalAliases(res)
			for _, must := range tc.mustTitle {
				if !strings.Contains(strings.ToLower(title), strings.ToLower(must)) {
					t.Fatalf("title=%q must contain %q", title, must)
				}
			}
			for _, ban := range tc.noTitle {
				if strings.Contains(strings.ToLower(title), strings.ToLower(ban)) {
					t.Fatalf("title=%q should not contain %q", title, ban)
				}
			}
			for _, ban := range tc.noAlias {
				for _, a := range aliases {
					if strings.Contains(strings.ToLower(a), strings.ToLower(ban)) {
						t.Fatalf("alias=%q should not contain %q", a, ban)
					}
				}
			}
		})
	}
}
