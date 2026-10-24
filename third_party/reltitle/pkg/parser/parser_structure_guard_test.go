package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
	"github.com/wangkayu/reltitle/pkg/tokenize"
)

// Structure guard tests: protectTitleFractionSpans must recognize "N/N"
// fraction-like title segments (e.g. "22/7") by pure structure, WITHOUT the
// title dictionary. Ambiguous shapes (digits after words, selection lists,
// plain ranges) must NOT be protected.

func parserWithoutTitleKnowledge(t *testing.T) *Parser {
	t.Helper()
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.TitleKnowledge.Entries = nil
	return New(cfg)
}

// All 22/7 release formats must still resolve the correct episode when the
// title dictionary is disabled — the structure guard alone is enough.
//
// Note: without the dictionary, title segmentation may be imperfect (e.g.
// "22／7" split into "22" + "7"). That is expected and acceptable: the
// structure guard's contract is episode correctness; title correctness is
// the dictionary's job (see the guarded tests in
// parser_nanabun_no_nijuuni_regression_test.go).
func TestStructureGuardRecognizes22_7WithoutTitleKnowledge(t *testing.T) {
	cases := []struct {
		raw         string
		wantKind    string
		wantEPKind  string
		wantEpisode string
		wantRange   string
	}{
		{
			"[Lilith-Raws] 22／7 - 04 [Baha][WEB-DL][1080p][AVC AAC][CHT][MKV]",
			"TV_EP", "EP", "4", "",
		},
		{
			"[Snow-Raws] 22／7/Nanabun no Nijyuuni/22／7 (BD 1920x1080 HEVC-YUV420P10 FLACx2)",
			"BATCH", "", "", "",
		},
		{
			"[LoliHouse] 22/7 / Nanabun no Nijuuni - 03 [WebRip 1080p HEVC-10bit AAC][英文内封字幕]",
			"TV_EP", "EP", "3", "",
		},
		{
			"【喵萌奶茶屋】★01月新番★[22/7/Nanabun no Nijuuni][04][720p][简日双语][招募翻译]",
			"TV_EP", "EP", "4", "",
		},
		{
			"【喵萌奶茶屋】[22/7 / Nanabun no Nijuuni][01-13END][BDRip][1080p][简日双语][招募翻译]",
			"BATCH", "RANGE", "", "1-13",
		},
	}
	for _, c := range cases {
		res, err := parserWithoutTitleKnowledge(t).Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := string(res.ContentKind); got != c.wantKind {
			t.Fatalf("content kind=%q want %q for %q", got, c.wantKind, c.raw)
		}
		if got := canonicalEPKind(res); got != c.wantEPKind {
			t.Fatalf("ep kind=%q want %q for %q", got, c.wantEPKind, c.raw)
		}
		if got := canonicalEpisode(res); got != c.wantEpisode {
			t.Fatalf("episode=%q want %q for %q", got, c.wantEpisode, c.raw)
		}
		if got := canonicalEpisodeRange(res); got != c.wantRange {
			t.Fatalf("episode range=%q want %q for %q", got, c.wantRange, c.raw)
		}
		if got := canonicalSeason(res); got != "" {
			t.Fatalf("season=%q want empty for %q", got, c.raw)
		}
	}
}

// Bare leading-digit titles in strict template slots: the digit is the title,
// and an episode is only present when a real episode slot exists.
func TestStructureGuardLeadingDigitTitlesWithoutTitleKnowledge(t *testing.T) {
	cases := []struct {
		raw         string
		wantTitle   string
		wantEPKind  string
		wantEpisode string
	}{
		// [group] N - EP [tech]: N is the title, EP is the episode.
		{"[Group] 86 - 03 [1080p]", "86", "EP", "3"},
		// [group] N [tech]: no episode slot, N is still the title.
		{"[Group] 86 [1080p]", "86", "", ""},
		{"[Group] 86 (Eighty Six) [1080p]", "86", "", ""},
		{"[Group] 86", "86", "", ""},
		// N after title words is an episode number, not the title.
		{"[Group] 标题 86 [1080p]", "标题", "EP", "86"},
	}
	for _, c := range cases {
		res, err := parserWithoutTitleKnowledge(t).Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := canonicalTitle(res); got != c.wantTitle {
			t.Fatalf("title=%q want %q for %q", got, c.wantTitle, c.raw)
		}
		if got := canonicalEPKind(res); got != c.wantEPKind {
			t.Fatalf("ep kind=%q want %q for %q", got, c.wantEPKind, c.raw)
		}
		if got := canonicalEpisode(res); got != c.wantEpisode {
			t.Fatalf("episode=%q want %q for %q", got, c.wantEpisode, c.raw)
		}
		// Invariant: an episode without a title is always a parse error.
		if canonicalEpisode(res) != "" && canonicalTitle(res) == "" {
			t.Fatalf("episode without title for %q", c.raw)
		}
	}
}

// Ambiguous shapes must be left untouched by the structure guard (no false
// positives), with or without the title dictionary.
func TestStructureGuardDoesNotOverreach(t *testing.T) {
	cases := []struct {
		raw         string
		wantEPKind  string
		wantRange   string
		wantEpisode string
	}{
		// Digits AFTER words inside a slash pair: indistinguishable from a real
		// batch range — must keep the range.
		{"[Group] Made in Abyss 01-13 / メイドインアビス 01-13 [BDRip][1080p]", "RANGE", "1-13", ""},
		// Episode selection list "01 / 03 / 05".
		{"[Group] Some Show [01/03/05][1080p]", "EP_LIST", "", "1"},
		// Plain batch range, no fraction segment.
		{"[LoliHouse] Some Show - 17-26 [1080p]", "RANGE", "17-26", ""},
		// Single episode.
		{"[LoliHouse] Some Show - 03 [1080p]", "EP", "", "3"},
	}
	for _, c := range cases {
		res, err := parserWithoutTitleKnowledge(t).Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := canonicalEPKind(res); got != c.wantEPKind {
			t.Fatalf("ep kind=%q want %q for %q", got, c.wantEPKind, c.raw)
		}
		if got := canonicalEpisodeRange(res); got != c.wantRange {
			t.Fatalf("episode range=%q want %q for %q", got, c.wantRange, c.raw)
		}
		if got := canonicalEpisode(res); got != c.wantEpisode {
			t.Fatalf("episode=%q want %q for %q", got, c.wantEpisode, c.raw)
		}
	}
}

// Token-level contract of the structure guard:
//   - "22/7"-style fraction segments get Protected
//   - "86 - 03" and bare leading digits in a strict template slot get Protected
//   - "藤本树 17-26 / Fujimoto Tatsuki 17-26" (digits after words) does NOT
//     (that shape needs the title dictionary)
//   - a digit that is NOT the first token after the group (e.g. an episode
//     number like "标题 86") does NOT
func TestStructureGuardProtectedTokenContract(t *testing.T) {
	cases := []struct {
		raw           string
		wantProtected bool
	}{
		{"[LoliHouse] 22/7 / Nanabun no Nijuuni - 03 [WebRip]", true},
		{"[Lilith-Raws] 22／7 - 04 [Baha][WEB-DL]", true},
		{"[Group] 86 - 03 [1080p]", true},
		{"[Group] 86 - 03v2 [1080p]", true},
		{"[Group] 86 [03][1080p]", true},
		// A bare digit after the group with a tech block / paren / EOL is the
		// only content: it is the title (an episode without a title is invalid).
		{"[Group] 86 [1080p]", true},
		{"[Group] 86 (Eighty Six) [1080p]", true},
		{"[Group] 86", true},
		// Deliberately NOT protected:
		{"[Group] 标题 86 [1080p]", false},
		{"[SweetSub&LoliHouse] 藤本树 17-26 / Fujimoto Tatsuki 17-26 [01-08 精校合集]", false},
		{"[Group] Made in Abyss 01-13 / メイドインアビス 01-13 [BDRip]", false},
	}
	for _, c := range cases {
		rawTitle := normalizeRawTitleForParsing(c.raw)
		toks := tokenize.Tokenize(rawTitle)
		tokens := make([]model.Token, len(toks))
		for i, tt := range toks {
			tokens[i] = model.Token{Text: tt}
		}
		// Simulate group removal so the scan starts after the group.
		groupEnd := 3
		for i := 0; i < groupEnd && i < len(tokens); i++ {
			tokens[i].Removed = true
		}
		protectTitleFractionSpans(tokens, groupEnd)
		protectTitleLeadingDigits(tokens, groupEnd)
		anyProtected := false
		for _, tok := range tokens {
			if tok.Protected {
				anyProtected = true
				break
			}
		}
		if anyProtected != c.wantProtected {
			t.Fatalf("protected=%v want %v for %q", anyProtected, c.wantProtected, c.raw)
		}
	}
}
