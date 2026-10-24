package parser

import "testing"

// 22/7 (Nanabun no Nijuuni): the title itself contains digits and a slash,
// which naive heuristics mistake for season/episode markers ("7" as season or
// episode, "22" as a single episode, or the whole "22/7" dropped from the title).
// All release formats must yield title "22/7" and the romaji reading as alias.

func TestNanabunNoNijuuniHalfWidthSlash(t *testing.T) {
	cases := []struct {
		raw         string
		wantKind    string
		wantEPKind  string
		wantEpisode string
		wantRange   string
	}{
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
		res, err := loadFujimotoTatsukiParser(t).Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := canonicalTitle(res); got != "22/7" {
			t.Fatalf("title=%q want %q for %q", got, "22/7", c.raw)
		}
		if !containsAlias(canonicalAliases(res), "Nanabun no Nijuuni") {
			t.Fatalf("missing romaji alias: %v for %q", canonicalAliases(res), c.raw)
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
	}
}

func TestNanabunNoNijuuniFullWidthSlash(t *testing.T) {
	// Full-width slash: "22／7" must match the canonical title "22/7".
	raw := "[Lilith-Raws] 22／7 - 04 [Baha][WEB-DL][1080p][AVC AAC][CHT][MKV]"
	res, err := loadFujimotoTatsukiParser(t).Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalTitle(res); got != "22/7" {
		t.Fatalf("title=%q want %q", got, "22/7")
	}
	if got := canonicalEPKind(res); got != "EP" {
		t.Fatalf("ep kind=%q want EP", got)
	}
	if got := canonicalEpisode(res); got != "4" {
		t.Fatalf("episode=%q want 4", got)
	}
	if got := canonicalEPAxis(res); got != "ABS" {
		t.Fatalf("ep axis=%q want ABS", got)
	}
	if got := canonicalSeason(res); got != "" {
		t.Fatalf("season=%q want empty", got)
	}
}

func TestNanabunNoNijuuniTitleOnlyNoEpisode(t *testing.T) {
	// Pure title release without any episode marker: no episode semantics at all.
	raw := "[Snow-Raws] 22／7/Nanabun no Nijyuuni/22／7 (BD 1920x1080 HEVC-YUV420P10 FLACx2)"
	res, err := loadFujimotoTatsukiParser(t).Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := canonicalTitle(res); got != "22/7" {
		t.Fatalf("title=%q want %q", got, "22/7")
	}
	if !containsAlias(canonicalAliases(res), "Nanabun no Nijyuuni") {
		t.Fatalf("missing romaji alias: %v", canonicalAliases(res))
	}
	if res.CanonicalEPSemantics() != nil {
		t.Fatalf("expected no episode semantics, got %+v", res.CanonicalEPSemantics())
	}
	if got := canonicalEpisode(res); got != "" {
		t.Fatalf("episode=%q want empty", got)
	}
}
