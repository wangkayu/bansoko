package parser

import (
	"strings"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

// 藤本树 17-26 / Fujimoto Tatsuki 17-26 is the actual title of an ONA anthology
// (8 episodes). The "17-26" suffix is part of the title, NOT an episode range.
// These tests cover both release naming conventions:
//   - bracket style:  [SweetSub][藤本樹 17-26][Fujimoto Tatsuki 17-26][08][...]
//   - slash style:    [SweetSub&LoliHouse] 藤本树 17-26 / Fujimoto Tatsuki 17-26 [01-08 精校合集][...]

func loadFujimotoTatsukiParser(t *testing.T) *Parser {
	t.Helper()
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return New(cfg)
}

func TestFujimotoTatsukiBracketStyleSingleEpisode(t *testing.T) {
	raw := "[SweetSub][藤本樹 17-26][Fujimoto Tatsuki 17-26][08][WebRip][1080P][AVC 8bit][繁日雙語]"
	res, err := loadFujimotoTatsukiParser(t).Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	aliases := canonicalAliases(res)
	if got := canonicalTitle(res); got != "藤本树 17-26" {
		t.Fatalf("title=%q want %q", got, "藤本树 17-26")
	}
	if !containsAlias(aliases, "Fujimoto Tatsuki 17-26") {
		t.Fatalf("missing romaji alias: %v", aliases)
	}
	if !containsAlias(aliases, "藤本樹 17-26") {
		t.Fatalf("missing traditional-form alias: %v", aliases)
	}
	// The title suffix must not be reinterpreted as an episode range.
	if got := canonicalEpisodeRange(res); got != "" {
		t.Fatalf("episode range must be empty, got %q", got)
	}
	if got := canonicalEPKind(res); got != "EP" {
		t.Fatalf("ep kind=%q want EP", got)
	}
	if got := canonicalEpisode(res); got != "8" {
		t.Fatalf("episode=%q want 8", got)
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep axis=%q want ABS", got)
	}
	if got := res.ContentKind; got != "TV_EP" {
		t.Fatalf("content kind=%q want TV_EP", got)
	}
	// Brackets around fully-consumed blocks ([08], [WebRip], [1080P], [AVC 8bit])
	// must be removed together with their content.
	for _, tok := range res.Tokens {
		if tok.Removed {
			continue
		}
		t.Fatalf("token %q should have been removed", tok.Text)
	}
}

func TestFujimotoTatsukiSlashStyleBatchCollection(t *testing.T) {
	raw := "[SweetSub&LoliHouse] 藤本树 17-26 / Fujimoto Tatsuki 17-26 [01-08 精校合集][WebRip 1080p HEVC-10bit AAC EAC3][简繁日内封字幕][Fin]"
	res, err := loadFujimotoTatsukiParser(t).Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalTitle(res); got != "藤本树 17-26" {
		t.Fatalf("title=%q want %q", got, "藤本树 17-26")
	}
	aliases := canonicalAliases(res)
	if !containsAlias(aliases, "Fujimoto Tatsuki 17-26") {
		t.Fatalf("missing romaji alias: %v", aliases)
	}
	// Script variants not present in the raw title must not be injected.
	for _, a := range aliases {
		if strings.Contains(a, "藤本樹") || strings.Contains(a, "藤本タツキ") {
			t.Fatalf("alias %q not present in raw title", a)
		}
	}
	// The collection range in brackets wins over the title suffix.
	if got := canonicalEPKind(res); got != "RANGE" {
		t.Fatalf("ep kind=%q want RANGE", got)
	}
	if got := canonicalEpisodeRange(res); got != "1-8" {
		t.Fatalf("episode range=%q want 1-8", got)
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep axis=%q want ABS", got)
	}
	if got := canonicalSeason(res); got != "" {
		t.Fatalf("season=%q want empty", got)
	}
	if got := res.ContentKind; got != "BATCH" {
		t.Fatalf("content kind=%q want BATCH", got)
	}
	if got := canonicalPackKind(res); got != "合集" {
		t.Fatalf("pack kind=%q want 合集", got)
	}
}

func TestFujimotoTatsukiJapaneseTitleForm(t *testing.T) {
	raw := "[SweetSub] [藤本タツキ17-26] [Fujimoto Tatsuki 17-26] [01] [1080p] [JPSC]"
	res, err := loadFujimotoTatsukiParser(t).Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalTitle(res); got != "藤本树 17-26" {
		t.Fatalf("title=%q want %q", got, "藤本树 17-26")
	}
	if !containsAlias(canonicalAliases(res), "藤本タツキ17-26") {
		t.Fatalf("missing Japanese alias: %v", canonicalAliases(res))
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Fatalf("episode=%q want 1", got)
	}
}

// The title-knowledge entry must not leak: an unrelated title containing "17-26"
// keeps its regular episode interpretation.
func TestUnrelatedTitle17_26NotAffectedByKnowledge(t *testing.T) {
	cases := []struct {
		raw         string
		wantKind    string
		wantRange   string
		wantSeason  string
		wantEpisode string
	}{
		// Batch: "- 17-26" is a plain-text episode range.
		{"[LoliHouse] Some Show - 17-26 [1080p]", "BATCH", "17-26", "", ""},
		// Single release: "17-26" after title keeps the season/episode reading.
		{"[Group] Random Anime 17-26 [1080p]", "TV_EP", "", "17", "26"},
		// Bracket range unaffected.
		{"[LoliHouse] Some Show [01-08][1080p]", "BATCH", "1-8", "", ""},
		// Split-form pack suffix "[01-08 精校合集]" recognized without title knowledge.
		{"[LoliHouse] Some Show [01-08 精校合集][1080p]", "BATCH", "1-8", "", ""},
	}
	for _, c := range cases {
		res, err := loadFujimotoTatsukiParser(t).Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := canonicalTitle(res); got != "Some Show" && got != "Random Anime" {
			t.Fatalf("title=%q for %q", got, c.raw)
		}
		if got := string(res.ContentKind); got != c.wantKind {
			t.Fatalf("content kind=%q want %q for %q", got, c.wantKind, c.raw)
		}
		if got := canonicalEpisodeRange(res); got != c.wantRange {
			t.Fatalf("episode range=%q want %q for %q", got, c.wantRange, c.raw)
		}
		if got := canonicalSeason(res); got != c.wantSeason {
			t.Fatalf("season=%q want %q for %q", got, c.wantSeason, c.raw)
		}
		if got := canonicalEpisode(res); got != c.wantEpisode {
			t.Fatalf("episode=%q want %q for %q", got, c.wantEpisode, c.raw)
		}
	}
}
