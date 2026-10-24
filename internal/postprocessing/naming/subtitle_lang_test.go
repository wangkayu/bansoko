package naming

import (
	"testing"

	"bansoko/internal/templatefacts"
)

func TestSubtitleLangCodePriority(t *testing.T) {
	cases := []struct {
		name  string
		codes []string
		want  string
	}{
		{"jpsc beats jptc", []string{"SC", "TC", "JPN"}, "jpsc"},
		{"jptc", []string{"TC", "JPN"}, "jptc"},
		{"folded jpsc row", []string{"JPSC"}, "jpsc"},
		{"jpsc wins with tc also aboard", []string{"JPN", "ENG", "TC", "SC"}, "jpsc"},
		{"sc beats tc beats eng", []string{"ENG", "TC", "SC"}, "sc"},
		{"tc beats eng", []string{"ENG", "TC"}, "tc"},
		{"eng before jpn", []string{"JPN", "ENG"}, "eng"},
		{"single keeps out-of-set code", []string{"KOR"}, "kor"},
		{"single multi", []string{"MULTI"}, "multi"},
		{"several out-of-set code", []string{"KOR", "SPA"}, "other"},
		{"zh rounds to jpn", []string{"ZH", "JPN"}, "jpn"},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		if got := subtitleLangCode(tc.codes); got != tc.want {
			t.Errorf("%s: code = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSubtitleLangNameDisplayWords(t *testing.T) {
	// no flags: no resolved type, no dual/multi parse, AniBT quiet.
	cases := []struct {
		name   string
		codes  []string
		flag   string
		subLang string
		anibt  string
		want   string
	}{
		// The pinned composite family; a third language absorbs into the word.
		// A composite with no resolved type carries the 双语 fallback tail.
		{"简体", []string{"SC"}, "", "", "", "简体"},
		{"繁体", []string{"TC"}, "", "", "", "繁体"},
		{"简日", []string{"SC", "JPN"}, "", "", "", "简日双语"},
		{"繁日", []string{"TC", "JPN"}, "", "", "", "繁日双语"},
		{"简繁", []string{"SC", "TC"}, "", "", "", "简繁双语"},
		{"简繁日", []string{"SC", "TC", "JPN"}, "", "", "", "简繁日双语"},
		{"简英", []string{"SC", "ENG"}, "", "", "", "简英双语"},
		{"繁英", []string{"TC", "ENG"}, "", "", "", "繁英双语"},
		{"SC+JPN+ENG → 简日", []string{"SC", "JPN", "ENG"}, "", "", "", "简日双语"},
		{"SC+TC+ENG → 简繁", []string{"SC", "TC", "ENG"}, "", "", "", "简繁双语"},
		{"SC+TC+JPN+ENG → 简繁日", []string{"SC", "TC", "JPN", "ENG"}, "", "", "", "简繁日双语"},
		{"SC+KOR absorbs", []string{"SC", "KOR"}, "", "", "", "简体"},
		{"non-Chinese multi top word", []string{"ENG", "JPN"}, "", "", "", "英文"},
		{"non-Chinese multi even with a flag", []string{"ENG", "JPN"}, "soft", "", "", "英文"},
		{"lone out-of-set word", []string{"KOR"}, "", "", "", "韩文"},
		{"zh is not in the family", []string{"ZH"}, "", "", "", "中文"},
		// The type tail rides a Chinese display only, from rt.sub_flag first.
		{"简体内封", []string{"SC"}, "soft", "", "", "简体内封"},
		{"简体内嵌", []string{"SC"}, "hard", "", "", "简体内嵌"},
		{"简繁外挂", []string{"SC", "TC"}, "ex", "", "", "简繁外挂"},
		{"简繁日内封", []string{"JPSC", "TC"}, "soft", "", "", "简繁日内封"},
		{"简英内封", []string{"SC", "ENG"}, "soft", "", "", "简英内封"},
		{"混合", []string{"SC"}, "soft+ex", "", "", "简体混合"},
		{"no tail for non-Chinese", []string{"ENG"}, "soft", "", "", "英文"},
		{"composite 双语 fallback", []string{"SC", "JPN"}, "", "", "", "简日双语"},
		{"繁体 bare without a type", []string{"TC"}, "", "", "", "繁体"},
		{"AniBT INTERNAL fallback inside the dual gate", []string{"SC", "JPN"}, "", "MULTI", "INTERNAL", "简日内封"},
		{"AniBT INTERNAL needs the dual parse", []string{"SC", "ENG"}, "", "SC", "INTERNAL", "简英双语"},
		{"AniBT EXTERNAL leaves empty", []string{"SC", "TC"}, "", "SC+ENG", "EXTERNAL", "简繁"},
		{"AniBT unknown value leaves empty", []string{"SC", "JPN"}, "", "MULTI", "SUB", "简日"},
		{"AniBT unknown value leaves empty", []string{"SC", "JPN"}, "", "MULTI", "SUB", "简日"},
		{"simple 英文 with flag stays bare", []string{"ENG"}, "hard", "", "", "英文"},
	}
	for _, tc := range cases {
		if got := subtitleLangName(tc.codes, tc.flag, tc.subLang, tc.anibt); got != tc.want {
			t.Errorf("%s: name = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSubtitleLanguageFactsIntegration pins the new facts on the UnifiedFacts
// projection: the code and the display word join the two-stage chain, a video
// file keeps them empty, and the type tail reads the release faces RT-first.
func TestSubtitleLanguageFactsIntegration(t *testing.T) {
	ctx := NamingContext{
		Release:   ReleaseNamingFacts{OriginalTitle: "[SampleGroup] Show - 07"},
		Rt:        templatefacts.Values{"SubFlag": "soft+ex", "SubLang": "SC"},
		AniBTTags: templatefacts.Values{"Subtitle": "INTERNAL"},
		File: FileNamingFacts{
			OriginalName: "show.chs.ass", Basename: "show.chs", Extension: "ass", Kind: "subtitle",
		},
	}
	ctx.Parsed.Subtitle.TagLanguages = []string{"SC", "JPN"}
	facts := UnifiedFacts(ctx).Sub
	if facts.Lang != "JPN+SC" || facts.LangCode != "jpsc" || facts.LangName != "简日混合" {
		t.Fatalf("subtitle facts = %q/%q/%q, want JPN+SC/jpsc/简日混合", facts.Lang, facts.LangCode, facts.LangName)
	}
	if facts.LangTag == "" || facts.LangSource != "file" {
		t.Fatalf("unrelated sub facts moved: %+v", facts)
	}
	// No file tag: the release faces name the chain (rt.sub_lang).
	rtOnly := NamingContext{
		Release: ReleaseNamingFacts{OriginalTitle: "[SampleGroup] Show - 07"},
		Rt:      templatefacts.Values{"SubFlag": "ex", "SubLang": "SC+TC"},
		File: FileNamingFacts{
			OriginalName: "show.ass", Basename: "show", Extension: "ass", Kind: "subtitle",
		},
	}
	facts = UnifiedFacts(rtOnly).Sub
	if facts.Lang != "SC+TC" || facts.LangCode != "sc" || facts.LangName != "简繁外挂" {
		t.Fatalf("rt chain = %q/%q/%q, want SC+TC/sc/简繁外挂", facts.Lang, facts.LangCode, facts.LangName)
	}
	// A video file has none of them.
	video := NamingContext{File: FileNamingFacts{Kind: "video", Extension: "mkv"}}
	facts = UnifiedFacts(video).Sub
	if facts.Lang != "" || facts.LangCode != "" || facts.LangName != "" {
		t.Fatalf("video file = %+v, want empty", facts)
	}
}

// TestLegacySubtitleModeReplay pins the renderer-version split of the
// subtitle-mode swap: the current renderer reads the rt.sub_flag projection,
// and every naming-render-5-and-earlier version replays the retired token
// scan, whatever the frozen facts say.
func TestLegacySubtitleModeReplay(t *testing.T) {
	// The title names the external flag but reltitle held no sub_flag value;
	// the frozen release fact carries the projection's unknown mode, which the
	// legacy versions re-derive from the title scan.
	ctx := NamingContext{
		Release: ReleaseNamingFacts{OriginalTitle: "[SampleGroup] Show - 01 [外挂字幕]", SubtitleMode: "unknown"},
		Rt:      templatefacts.Values{"SubLang": "SC"},
		File:    FileNamingFacts{OriginalName: "x.mkv", Basename: "x", Extension: "mkv", Kind: "video"},
	}
	renderMode := func(t *testing.T, engine *Engine, inner string) string {
		t.Helper()
		compiled, err := engine.Compile(KindVideoFilename, "${release.subtitle_mode}"+inner)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		got, err := engine.Render(compiled, ctx)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return got
	}
	if got := renderMode(t, NewEngine(), ".mkv"); got != "unknown.mkv" {
		t.Fatalf("current mode = %q, want unknown.mkv", got)
	}
	legacy, err := NewEngine().ForRenderer("naming-render-5")
	if err != nil {
		t.Fatal(err)
	}
	if got := renderMode(t, legacy, ".mkv"); got != "external.mkv" {
		t.Fatalf("naming-render-5 replay = %q, want external.mkv", got)
	}
	naming4, err := NewEngine().ForRenderer("naming-render-4")
	if err != nil {
		t.Fatal(err)
	}
	if got := renderMode(t, naming4, ".mkv"); got != "external.mkv" {
		t.Fatalf("naming-render-4 replay = %q, want external.mkv", got)
	}
	if _, err := NewEngine().ForRenderer("naming-render-99"); err == nil {
		t.Fatal("unknown renderer version must be refused")
	}
}
