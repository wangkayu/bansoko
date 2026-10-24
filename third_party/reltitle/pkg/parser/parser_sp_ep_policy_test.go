package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

func TestParserSPEPPolicy_Matrix(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []struct {
		name          string
		raw           string
		wantKind      string
		wantEpisode   string
		wantEpRange   string
		wantSPKind    string
		titleContains string
	}{
		{
			name:        "bracket_ova_number",
			raw:         "【幻櫻字幕組】【黃金神威 Golden Kamuy】【OVA】【05】【BDrip】【BIG5_MP4】【1280X720】",
			wantKind:    "TV_EP",
			wantEpisode: "5",
			wantSPKind:  "OVA",
		},
		{
			name:          "plain_ova_dash_number_title_context",
			raw:           "[shincaps] Futsuu no Joshikousei ga [Locodol] Yattemita. OVA - 01 (AT-X 1440x1080 MPEG2 AAC).ts",
			wantKind:      "TV_EP",
			wantEpisode:   "1",
			wantSPKind:    "",
			titleContains: "OVA",
		},
		{
			name:        "plain_ova_hyphen_number_title_context",
			raw:         "[Test] Sample OVA-01 [1080p][WEB-DL]",
			wantKind:    "TV_EP",
			wantEpisode: "1",
			wantSPKind:  "",
		},
		{
			name:        "standalone_bracket_ova_hyphen_number_is_special",
			raw:         "[Test] Sample [OVA-01] [1080p][WEB-DL]",
			wantKind:    "SPECIAL",
			wantSPKind:  "OVA",
			wantEpisode: "",
		},
		{
			name:        "joined_ova_number",
			raw:         "[AHU-SUB][生而为狗，我很幸福/无意间变成狗，被喜欢的女生捡回家。_My Life as Inukai-san's Dog][OVA1][x264 1080p][CHS][Uncensored]",
			wantKind:    "TV_EP",
			wantEpisode: "1",
			wantSPKind:  "OVA",
		},
		{
			name:        "tail_ova_before_meta_block",
			raw:         "[Blasphemboys] Sanzoku Diary / Bandit's Diary / 山賊ダイアリー OVA (2015) [DVD AVC AC3]",
			wantKind:    "SPECIAL",
			wantEpisode: "",
			wantSPKind:  "OVA",
		},
		{
			name:          "title_word_ova_project",
			raw:           "[Test] Futsuu no OVA Project - 01 [1080p][WEB-DL]",
			wantKind:      "TV_EP",
			wantEpisode:   "1",
			wantSPKind:    "",
			titleContains: "OVA",
		},
		{
			name:        "raw_bracket_ova_number",
			raw:         "[RAW][Inu ni Nattara Suki na Hito ni Hirowareta.][犬になったら好きな人に拾われた.][OVA][01][BDRip][AV1][1080P]",
			wantKind:    "TV_EP",
			wantEpisode: "1",
			wantSPKind:  "OVA",
		},
		{
			name:        "mixed_batch_tv_plus_ova_range",
			raw:         "[Fatum Fatalis] 英雄教室 / 英雄教室 / Eiyuu Kyoushitsu (BD REMUX EP01-12+OVA01-03+SP Fin 1920x1080 AVC PCM PGS 英文字幕)",
			wantKind:    "BATCH",
			wantEpRange: "1-12",
			wantSPKind:  "OVA",
		},
		{
			name:        "sp_with_episode",
			raw:         "[ANi] 关于我转生变成史莱姆这档事 第三季 [特别篇] - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]",
			wantKind:    "TV_EP",
			wantEpisode: "1",
			wantSPKind:  "SP",
		},
		{
			name:       "sp_without_episode",
			raw:        "[ANi] DDDD 恶魔的破坏 [特别篇] [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]",
			wantKind:   "SPECIAL",
			wantSPKind: "SP",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if tc.wantKind != "" && string(res.ContentKind) != tc.wantKind {
				t.Fatalf("content_kind=%q want=%q", res.ContentKind, tc.wantKind)
			}
			if got := canonicalEpisode(res); got != tc.wantEpisode {
				t.Fatalf("episode=%q want=%q", got, tc.wantEpisode)
			}
			if got := canonicalEpisodeRange(res); got != tc.wantEpRange {
				t.Fatalf("episode_range=%q want=%q", got, tc.wantEpRange)
			}
			if got := canonicalSPKind(res); got != tc.wantSPKind {
				t.Fatalf("sp_kind=%q want=%q", got, tc.wantSPKind)
			}
			if tc.titleContains != "" {
				title := canonicalTitle(res)
				if !strings.Contains(strings.ToUpper(title), strings.ToUpper(tc.titleContains)) {
					t.Fatalf("title=%q should contain %q", title, tc.titleContains)
				}
			}
		})
	}
}
