package naming

import (
	"strings"

	"bansoko/internal/templatefacts"
)

// This file derives the presentation-shaped subtitle-language facts the
// naming language carries: the short code (sub.lang_code) and the display
// word (sub.lang_name). Both are composed from the same two-stage chain the
// other sub.* facts read (the subtitle file's own tag first, else the release
// title's rt.sub_lang), so they mirror sub.lang's values and add no data
// source of their own; the type tail additionally reads the release's
// subtitle-mode flags (rt.sub_flag) and the AniBT subtitle tag.
//
// The word tables are owner rulings (10-22 evening): the code is the priority
// pick jpsc > jptc > sc > tc > eng > jpn > other; the display word is a
// priority pick over the Chinese composite family carrying the release's
// subtitle-mode flag set (rt.sub_flag) as its type tail — dual-language
// tracks keep their composite word, non-Chinese lists keep one top word, and
// only Chinese displays carry a tail at all.

// subtitleLangScope is what one language list says, normalized to the shape
// the derivation reads: which reltitle sub_lang vocabulary values are on
// board. The composite values a reltitle parse folds into sub_lang count as
// both their halves (a JPSC row means SC and JPN are present) — the same
// shapes the jpsc/jptc codes come from.
type subtitleLangScope struct {
	set map[string]bool
	sc  bool
	tc  bool
	eng bool
	jpn bool
}

func newSubtitleLangScope(codes []string) subtitleLangScope {
	scope := subtitleLangScope{set: map[string]bool{}}
	for _, code := range codes {
		code = strings.ToLower(strings.TrimSpace(code))
		switch code {
		case "jpsc":
			scope.set["jpsc"], scope.sc, scope.jpn = true, true, true
		case "jptc":
			scope.set["jptc"], scope.tc, scope.jpn = true, true, true
		case "sc":
			scope.set["sc"], scope.sc = true, true
		case "tc":
			scope.set["tc"], scope.tc = true, true
		case "eng":
			scope.set["eng"], scope.eng = true, true
		case "jpn":
			scope.set["jpn"], scope.jpn = true, true
		default:
			scope.set[code] = true
		}
	}
	return scope
}

// subtitleLangCode is the presentation short code (sub.lang_code) of one
// subtitle language list: the approved composites first (sc+jpn → jpsc wins
// over tc+jpn → jptc), then a single-code list keeps its own lowercased code,
// then the priority pick over sc/tc/eng/jpn, and "other" when none match.
func subtitleLangCode(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	scope := newSubtitleLangScope(codes)
	switch {
	case scope.sc && scope.jpn:
		return "jpsc"
	case scope.tc && scope.jpn:
		return "jptc"
	}
	if len(scope.set) == 1 {
		for code := range scope.set {
			return code
		}
	}
	for _, code := range []string{"sc", "tc", "eng", "jpn"} {
		if scope.set[code] {
			return code
		}
	}
	return "other"
}

// subtitleLangWords are the single-language display words. The word order
// below is the priority pick of a non-Chinese list (英文 before 日文; an
// out-of-set language keeps its own word).
var subtitleLangWords = map[string]string{
	"eng": "英文", "jpn": "日文", "kor": "韩文", "spa": "西班牙文",
	"fra": "法文", "deu": "德文", "ita": "意大利文", "por": "葡萄牙文",
	"rus": "俄文", "ara": "阿拉伯文", "cat": "加泰罗尼亚文",
	"ind": "印尼文", "tha": "泰文", "pol": "波兰文", "msa": "马来文",
	"vie": "越南文", "zh": "中文", "raw": "生肉", "multi": "多语",
}

// subtitleLangWordOrder is the priority order of the non-composite word pick.
var subtitleLangWordOrder = []string{
	"eng", "jpn", "kor", "spa", "fra", "deu", "ita", "por", "rus", "ara",
	"cat", "ind", "tha", "pol", "msa", "vie", "zh", "multi", "raw",
}

// subtitleLangFlagWord is the type tail word of one flag shape of the
// rt.sub_flag set: the embedded flags (soft, hard) without ex name container
// tracks, ex alone the external files, and an ex beside either the mixed
// display. soft and hard together stay on the soft word (the embedded shape).
func subtitleLangFlagWord(flags map[string]bool) string {
	switch {
	case flags["ex"] && (flags["soft"] || flags["hard"]):
		return "混合"
	case flags["ex"]:
		return "外挂"
	case flags["hard"]:
		return "内嵌"
	case flags["soft"]:
		return "内封"
	}
	return ""
}

// subtitleLangTail is one release's type tail: the word a Chinese display
// appends, or "" for none.
//
// The data chain is RT-FIRST: rt.sub_flag wins outright. The AniBT subtitle
// tag is only a fallback, consulted when the release title's parse shows a
// dual-language subtitle list (rt.sub_lang with MULTI, the folded bilingual
// composites, or SC/TC beside ENG — the group shapes) and naming INTERNAL; a
// tag naming anything else (EXTERNAL included) leaves the tail empty, and so
// does no tag at all on a single-language display. Without any resolved type
// a composite display falls back to the 双语 tail; a single 简体/繁体 shows
// bare (双语 has no established use for one track).
func subtitleLangTail(rtSubFlag, rtSubLang, anibtSubtitle string, composite bool) string {
	flags := map[string]bool{}
	for _, value := range templatefacts.SplitValues(rtSubFlag) {
		flags[strings.ToLower(strings.TrimSpace(value))] = true
	}
	if tail := subtitleLangFlagWord(flags); tail != "" {
		return tail
	}
	dualOrMulti := func() bool {
		scope := newSubtitleLangScope(templatefacts.SplitValues(rtSubLang))
		return scope.set["multi"] || scope.set["jpsc"] || scope.set["jptc"] ||
			(scope.eng && (scope.sc || scope.tc))
	}()
	if !dualOrMulti {
		if composite {
			return "双语"
		}
		return ""
	}
	tagged := false
	for _, value := range templatefacts.SplitValues(anibtSubtitle) {
		tagged = true
		if strings.EqualFold(value, "INTERNAL") {
			return "内封"
		}
	}
	if tagged {
		// The AniBT tag names a type the display does not carry (EXTERNAL or
		// an unknown value): no tail, no invented one.
		return ""
	}
	if composite {
		return "双语"
	}
	return ""
}

// subtitleLangName is the display word (sub.lang_name) of one subtitle
// language list. The Chinese composite family is a priority pick from the
// SC (简体), TC (繁体), JPN and ENG languages and absorbs whatever else rides
// along (SC+JPN+ENG → 简日, SC+TC+ENG → 简繁, SC+TC+JPN+ENG → 简繁日); a list
// without Chinese keeps one top word by priority. The type tail is a Chinese
// display only (只有中文才需要 type).
func subtitleLangName(codes []string, rtSubFlag, rtSubLang, anibtSubtitle string) string {
	scope := newSubtitleLangScope(codes)
	var word string
	composite := true
	switch {
	case scope.sc && scope.tc && scope.jpn:
		word = "简繁日"
	case scope.sc && scope.jpn:
		word = "简日"
	case scope.tc && scope.jpn:
		word = "繁日"
	case scope.sc && scope.tc:
		word = "简繁"
	case scope.sc && scope.eng:
		word = "简英"
	case scope.tc && scope.eng:
		word = "繁英"
	case scope.sc:
		word, composite = "简体", false
	case scope.tc:
		word, composite = "繁体", false
	default:
		for _, code := range subtitleLangWordOrder {
			if scope.set[code] {
				return subtitleLangWords[code]
			}
		}
		return ""
	}
	tail := subtitleLangTail(rtSubFlag, rtSubLang, anibtSubtitle, composite)
	if tail != "" {
		return word + tail
	}
	return word
}
