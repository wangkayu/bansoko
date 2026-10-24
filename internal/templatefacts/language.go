package templatefacts

import "strings"

// languageBCP47 is the one language code -> BCP 47 table. Every fact that
// offers a BCP 47 form of a language code (release.lang_tag,
// sub.lang_tag, file.sub.lang/langs) reads it, so one code always has one
// tag. The keys are the reltitle sub_lang vocabulary (the codes reltitle
// emits; the release title language resolver's lower-case codes sc, tc, jpsc,
// jptc, jpn and eng fold onto them). Chinese uses script subtags (zh-Hans /
// zh-Hant), never region tags. A Japanese+Chinese bilingual code maps to the
// Chinese tag. RAW and MULTI name no language and have no tag; a code missing
// here has no tag, never a guessed one.
var languageBCP47 = map[string]string{
	"ZH":   "zh",
	"SC":   "zh-Hans",
	"TC":   "zh-Hant",
	"JPSC": "zh-Hans",
	"JPTC": "zh-Hant",
	"JPN":  "ja",
	"ENG":  "en",
	"KOR":  "ko",
	"FRA":  "fr",
	"SPA":  "es",
	"CAT":  "ca",
	"RUS":  "ru",
	"DEU":  "de",
	"ITA":  "it",
	"POR":  "pt",
	"ARA":  "ar",
	"IND":  "id",
	"THA":  "th",
	"POL":  "pl",
	"MSA":  "ms",
	"VIE":  "vi",
}

// LanguageTag returns the BCP 47 tag of one language code (case-insensitive)
// and whether the table has one.
func LanguageTag(code string) (string, bool) {
	tag, ok := languageBCP47[strings.ToUpper(strings.TrimSpace(code))]
	return tag, ok
}

// LanguageTags maps language codes to their BCP 47 tags in code order,
// de-duplicated; a code with no tag is left out. Nil when no code has a tag.
func LanguageTags(codes []string) []string {
	var tags []string
	seen := map[string]bool{}
	for _, code := range codes {
		tag, ok := LanguageTag(code)
		if !ok || seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	return tags
}

// SubtitleLanguageTag is the one BCP 47 tag of a subtitle's language codes
// (sub.lang_tag): a media server reads one language per subtitle file.
// With Chinese among the codes: simplified only is zh-Hans, traditional only
// zh-Hant, both (e.g. JPSC+JPTC) or only generic Chinese is zh — one is never
// picked arbitrarily. Without Chinese: the tag of the first code that has
// one. No tag: "".
func SubtitleLanguageTag(codes []string) string {
	tags := LanguageTags(codes)
	hans, hant, zh := false, false, false
	for _, tag := range tags {
		switch tag {
		case "zh-Hans":
			hans = true
		case "zh-Hant":
			hant = true
		case "zh":
			zh = true
		}
	}
	switch {
	case hans && hant:
		return "zh"
	case hans:
		return "zh-Hans"
	case hant:
		return "zh-Hant"
	case zh:
		return "zh"
	case len(tags) > 0:
		return tags[0]
	}
	return ""
}

// SplitValues splits a multi-valued field (JoinValues) back into its values.
func SplitValues(joined string) []string {
	if strings.TrimSpace(joined) == "" {
		return nil
	}
	return strings.Split(joined, MultiValueSeparator)
}
