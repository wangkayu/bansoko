package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

// "乱马1/2" is protected by the title dictionary. The season marker after it is consumed, so
// the episode in "第二季 - 05" became the first visible token after the group and was
// protected as a "leading title digit". The result was BATCH with no episode.
func TestParserKnownTitleFollowedBySeasonKeepsEpisode(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	res, err := New(cfg).Parse("[G] 乱马1/2 第二季 - 05 [1080P]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.ContentKind != model.ContentKind("TV_EP") {
		t.Errorf("content_kind=%q want TV_EP", res.ContentKind)
	}
	if got := canonicalTitle(res); got != "乱马1/2" {
		t.Errorf("title=%q want 乱马1/2", got)
	}
	if got := canonicalSeason(res); got != "2" {
		t.Errorf("season=%q want 2", got)
	}
	if got := canonicalEpisode(res); got != "5" {
		t.Errorf("episode=%q want 5", got)
	}
}

// In "Season 3 - 01" the 3 is the season value. It was offered as an episode candidate and,
// next to the protected alias "Ranma 1-2", won over the real episode 01.
func TestParserSeasonValueIsNotAnEpisodeCandidate(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	res, err := New(cfg).Parse("[ToonsHub] 乱马1/2 第三季 / Ranma 1-2 (2024) Season 3 - 01 [1080P][DUAL][MKV]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalSeason(res); got != "3" {
		t.Errorf("season=%q want 3", got)
	}
	if got := canonicalEpisode(res); got != "1" {
		t.Errorf("episode=%q want 1", got)
	}
}

// Protected title content before a digit means the digit is not the leading title digit.
func TestProtectTitleLeadingDigitsIgnoresDigitAfterProtectedTitle(t *testing.T) {
	tokens := []model.Token{
		{Text: "乱马1", Protected: true}, {Text: "/", Protected: true}, {Text: "2", Protected: true},
		{Text: "第二季", Removed: true}, {Text: "-", Removed: true}, {Text: "05"}, {Text: "[", Removed: true},
	}
	protectTitleLeadingDigits(tokens, 0)
	if tokens[5].Protected {
		t.Fatal("the episode digit after protected title content was protected")
	}

	// The documented "86" case keeps working.
	lead := []model.Token{{Text: "86"}, {Text: "-"}, {Text: "03"}}
	protectTitleLeadingDigits(lead, 0)
	if !lead[0].Protected || lead[2].Protected {
		t.Fatalf("leading title digit: protected=%v/%v, want true/false", lead[0].Protected, lead[2].Protected)
	}
}

// "乱马1/2 第三季 / Ranma 1-2 ..." has two slashes: the one inside the known title and the
// bilingual separator. The slash alias chain needs two delimiters, so the protected "/" of
// "乱马1/2" used to trigger it and produce the noisy alias "Ranma 1-3".
func TestParserProtectedSlashIsNotAnAliasDelimiter(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	res, err := New(cfg).Parse("[ToonsHub] 乱马1/2 第三季 / Ranma 1-2 (2024) Season 3 - 01 [1080P][DUAL][CHS&CHT&JP&EN&KO][MKV]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalTitle(res); got != "乱马1/2" {
		t.Errorf("title=%q want 乱马1/2", got)
	}
	want := []string{"Ranma 1-2"}
	got := attrValues(res, model.AttrTitleAlias)
	if len(got) != len(want) {
		t.Fatalf("aliases=%q want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("aliases=%q want %q", got, want)
		}
	}
	if canonicalSeason(res) != "3" || canonicalEpisode(res) != "1" {
		t.Errorf("season/episode=%q/%q want 3/1", canonicalSeason(res), canonicalEpisode(res))
	}
}

// A plain bilingual title (one separator slash) must still split into title and alias.
func TestParserBilingualSlashStillSplitsAliases(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	res, err := New(cfg).Parse("[ToonsHub] 青之箱 第二季 / Blue Box Season 2 - 01 [1080P][MKV]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalTitle(res); got != "青之箱" {
		t.Errorf("title=%q want 青之箱", got)
	}
	if got := attrValues(res, model.AttrTitleAlias); len(got) != 1 || got[0] != "Blue Box" {
		t.Errorf("aliases=%q want [Blue Box]", got)
	}
}

// Splitting a title at its own slash ("Ranma 1/2" -> "Ranma 1", "Ranma") leaves fragments.
// When a dictionary entry matches, fragments of the known aliases present in the title are
// not aliases. The known alias itself is kept.
func TestParserKnownTitleDropsSlashFragmentAliases(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	tests := []struct {
		raw     string
		title   string
		aliases []string
	}{
		{"[G] 乱马1/2 / Ranma 1/2 - 05 [1080P]", "乱马1/2", []string{"Ranma 1/2"}},
		{"[ToonsHub] 乱马1/2 第三季 / Ranma 1/2 (2024) Season 3 - 01 [1080P][MKV]", "乱马1/2", []string{"Ranma 1/2"}},
		{"[G] Ranma 1/2 - 01 [1080P]", "Ranma 1/2", nil},
		{"[G] 乱马1/2 - 05 [1080P]", "乱马1/2", nil},
	}
	for _, tc := range tests {
		res, err := p.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := canonicalTitle(res); got != tc.title {
			t.Errorf("%q: title=%q want %q", tc.raw, got, tc.title)
		}
		got := attrValues(res, model.AttrTitleAlias)
		if len(got) != len(tc.aliases) {
			t.Errorf("%q: aliases=%q want %q", tc.raw, got, tc.aliases)
			continue
		}
		for i := range got {
			if got[i] != tc.aliases[i] {
				t.Errorf("%q: aliases=%q want %q", tc.raw, got, tc.aliases)
			}
		}
	}
}

func TestParserTitleAdjunct(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)
	tests := []struct {
		raw     string
		episode string
		aliases []string
		title   string
	}{
		{"[ANi] The Anime「幻想水滸傳」 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT].mp4", "1", nil, "The Anime「幻想水滸傳」"},
		{"[ANi] The Anime「幻想水滸傳」 - 01 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]", "1", nil, "The Anime「幻想水滸傳」"},
		{"[黒ネズミたち] The Anime「幻想水滸傳」 / Gensou Suikoden - 01 (ABEMA 1920x1080 AVC AAC MKV)", "1", []string{"Gensou Suikoden"}, "The Anime「幻想水滸傳」"},
		{"[G] The Anime「幻想水滸傳」 - 12v2 [1080P]", "12", nil, "The Anime「幻想水滸傳」"},
		{"[G] The Animation「某作品」 - 03 [1080p]", "3", nil, "The Animation「某作品」"},
		{"[G] The Anime 某作品 - 03 [1080p]", "3", nil, "The Anime 某作品"},
		{"[G] 快刀乱麻 THE ANIMATION - 03 [1080p]", "3", nil, "快刀乱麻 THE ANIMATION"},
		{"[G] 某作品 The Anime - 03 [1080p]", "3", nil, "某作品 The Anime"},
		{"[Erai-raws] Rick and Morty - The Anime - 09 [1080p]", "9", nil, "Rick and Morty - The Anime"},
		{"[ToonsHub] Rick and Morty The Anime S01E09 [1080p]", "9", nil, "Rick and Morty The Anime"},
		{"[G] Rick and Morty: The Anime - 09 [1080p]", "9", nil, "Rick and Morty : The Anime"},
		{"[G] 小城日常 / CITY THE ANIMATION - 13 [1080p]", "13", []string{"CITY THE ANIMATION"}, "小城日常"},
		{"[G] CITY シティ The Animation - 13 [1080p]", "13", nil, "CITY シティ The Animation"},
		{"[G] Persona4 the ANIMATION - 03 [720p]", "3", nil, "Persona4 the ANIMATION"},
		{"[G] Akibas Trip - The Animation - 01 [720p]", "1", nil, "Akibas Trip - The Animation"},
		{"[G] Hamatora_~The_Animation~_-_01 [720p]", "1", nil, "Hamatora ~The Animation~"},
	}
	for _, tc := range tests {
		res, err := p.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := canonicalTitle(res); got != tc.title {
			t.Errorf("%q: title=%q want %q", tc.raw, got, tc.title)
		}
		if got := canonicalEpisode(res); got != tc.episode {
			t.Errorf("%q: episode=%q want %q", tc.raw, got, tc.episode)
		}
		got := attrValues(res, model.AttrTitleAlias)
		if len(got) != len(tc.aliases) || (len(got) == 1 && got[0] != tc.aliases[0]) {
			t.Errorf("%q: aliases=%q want %q", tc.raw, got, tc.aliases)
		}
	}
}

// The Latin + CJK rule itself stays: for a title not in the dictionary the CJK part is the
// title and the Latin part an alias.
func TestParserLatinThenCJKTitleSplitsWithoutDictionary(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	res, err := New(cfg).Parse("[G] Some Show「某作品」 - 03 [1080P]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalTitle(res); got != "某作品" {
		t.Errorf("title=%q want 某作品", got)
	}
	if got := attrValues(res, model.AttrTitleAlias); len(got) != 1 || got[0] != "Some Show" {
		t.Errorf("aliases=%q want [Some Show]", got)
	}
}
