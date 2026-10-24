package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func (p *Parser) applyGroupRouteOverride(
	rawTitle string,
	group string,
	episodeRange string,
	title string,
	aliases []string,
	templateID string,
	contentKind model.ContentKind,
	routeID string,
) (string, string, []string, string, model.ContentKind, string, bool) {
	if p == nil || len(p.groupRouteRules) == 0 {
		return group, title, aliases, templateID, contentKind, routeID, false
	}
	gk := strings.ToLower(strings.TrimSpace(group))
	rawLow := strings.ToLower(strings.TrimSpace(rawTitle))
	for _, r := range p.groupRouteRules {
		if len(r.groups) > 0 {
			if _, ok := r.groups[gk]; !ok {
				continue
			}
		}
		if r.requireEpisodeRange && strings.TrimSpace(episodeRange) == "" {
			continue
		}
		if r.requireEpisodeLike {
			if _, _, ok := extractEpisodeLikeFromThirdBracket(rawTitle); !ok {
				continue
			}
		}
		if r.requireEpisodeWithTitle {
			_, _, epTitle, epSep, ok := extractEpisodeLikeFromThirdBracketDetailedWithSep(rawTitle)
			if !ok || strings.TrimSpace(epTitle) == "" {
				continue
			}
			if len(r.episodeTitleSepIn) > 0 {
				if _, ok := r.episodeTitleSepIn[epSep]; !ok {
					continue
				}
			}
		}
		if len(r.rawMustContainAny) > 0 {
			hit := false
			for _, kw := range r.rawMustContainAny {
				if kw != "" && strings.Contains(rawLow, kw) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if r.forceRouteID == "group_wgjd_overall_range_v1" {
			start, end := splitNormalizedRangeBounds(episodeRange)
			primary := &model.EPReference{Kind: model.EPKindRange, Axis: model.EPAxisSE, RangeStart: start, RangeEnd: end}
			if _, ok := detectOverallBracketAlternate(rawTitle, primary); !ok {
				continue
			}
		}
		if r.forceRouteID == "group_baidong_underscore_alias_v1" {
			if _, _, ok := applyBracketUnderscoreAliasBridge(rawTitle, title, nil); !ok {
				continue
			}
		}
		outGroup := group
		outTitle := title
		outAliases := aliases
		outTemplateID := templateID
		outContentKind := contentKind
		outRouteID := routeID

		if strings.TrimSpace(r.forceGroup) != "" {
			outGroup = r.forceGroup
		}
		if strings.TrimSpace(r.forceTitle) != "" {
			outTitle = r.forceTitle
		}
		if r.clearAliases {
			outAliases = nil
		}
		if strings.TrimSpace(r.forceTemplateID) != "" {
			outTemplateID = r.forceTemplateID
		}
		if strings.TrimSpace(r.forceContentKind) != "" {
			outContentKind = model.ContentKind(r.forceContentKind)
		}
		if strings.TrimSpace(r.forceRouteID) != "" {
			outRouteID = r.forceRouteID
		}
		if len(r.titleStripPrefixes) > 0 {
			outTitle = stripConfiguredPrefixes(outTitle, r.titleStripPrefixes)
		}
		if len(r.aliasStripPrefixes) > 0 && len(outAliases) > 0 {
			cleaned := make([]string, 0, len(outAliases))
			for _, a := range outAliases {
				c := stripConfiguredPrefixes(a, r.aliasStripPrefixes)
				if strings.TrimSpace(c) == "" {
					continue
				}
				cleaned = append(cleaned, c)
			}
			outAliases = cleaned
		}

		// Safe fallback: if override cannot produce required core fields, skip it.
		if strings.TrimSpace(outGroup) == "" || strings.TrimSpace(outTitle) == "" {
			continue
		}
		// If this override depends on range releases, keep general route when range is absent.
		if r.requireEpisodeRange && strings.TrimSpace(episodeRange) == "" {
			continue
		}
		return outGroup, outTitle, outAliases, outTemplateID, outContentKind, outRouteID, true
	}
	return group, title, aliases, templateID, contentKind, routeID, false
}

func stripConfiguredPrefixes(s string, prefixes []string) string {
	out := strings.TrimSpace(s)
	if out == "" || len(prefixes) == 0 {
		return out
	}
	for {
		changed := false
		for _, p := range prefixes {
			pp := strings.TrimSpace(p)
			if pp == "" {
				continue
			}
			rest, ok := trimPrefixFold(out, pp)
			if !ok {
				continue
			}
			out = strings.TrimSpace(strings.TrimLeft(rest, "-_—–:：|/·+~. "))
			changed = true
			break
		}
		if !changed {
			break
		}
	}
	return strings.TrimSpace(out)
}

func applyGroupTitleAliasSpecialization(rawTitle string, routeID string, title string, aliases []string) (string, []string) {
	rid := strings.ToLower(strings.TrimSpace(routeID))
	switch rid {
	case "group_epseg_fate_sf_wgjd_v1", "group_epseg_fate_sf_atlas_v1":
		return applyFateStrangeFakeTitleAliasSpecialization(rawTitle, title, aliases)
	case "group_underscore_delim_v1":
		return applyUnderscoreDelimiterTitleAliasSpecialization(rawTitle, title, aliases)
	case "group_baidong_underscore_alias_v1":
		if main, alts, ok := applyBracketUnderscoreAliasBridge(rawTitle, title, nil); ok {
			return main, alts
		}
		return title, aliases
	default:
		return title, aliases
	}
}

func applyBracketUnderscoreAliasBridge(rawTitle string, title string, aliases []string) (string, []string, bool) {
	rawTitle = strings.NewReplacer("【", "[", "】", "]").Replace(rawTitle)
	if strings.TrimSpace(rawTitle) == "" {
		return title, aliases, false
	}
	if len(aliases) > 0 {
		return title, aliases, false
	}
	matches := bracketSegmentPattern.FindAllStringSubmatch(rawTitle, -1)
	if len(matches) < 2 {
		return title, aliases, false
	}
	seg2 := strings.TrimSpace(matches[1][1])
	if seg2 == "" || strings.ContainsAny(seg2, "/／") {
		return title, aliases, false
	}
	parts := strings.SplitN(seg2, "_", 2)
	if len(parts) != 2 {
		return title, aliases, false
	}
	left := cleanTitleToken(parts[0])
	right := cleanTitleToken(parts[1])
	if left == "" || right == "" {
		return title, aliases, false
	}
	if !hasCJK(left) || !hasLatin(right) {
		return title, aliases, false
	}
	if underscoreAliasTechTailPattern.MatchString(strings.ToLower(right)) {
		return title, aliases, false
	}
	right = strings.TrimSpace(strings.ReplaceAll(right, "／", "/"))
	right = strings.Join(strings.Fields(right), " ")
	if right == "" {
		return title, aliases, false
	}
	merged := mergeAliases([]string{right}, aliases)
	merged = dropAliasFragmentsOfPrimary(left, merged)
	return left, merged, true
}

func applyFateStrangeFakeTitleAliasSpecialization(rawTitle string, title string, aliases []string) (string, []string) {
	matches := bracketSegmentPattern.FindAllStringSubmatch(rawTitle, -1)
	if len(matches) < 2 {
		return title, aliases
	}
	seg2 := strings.TrimSpace(matches[1][1])
	if seg2 == "" {
		return title, aliases
	}
	parts := strings.SplitN(seg2, "_", 2)
	if len(parts) != 2 {
		return title, aliases
	}
	mainTitle := cleanTitleToken(parts[0])
	alias := cleanTitleToken(parts[1])
	if mainTitle == "" || alias == "" {
		return title, aliases
	}
	alias = strings.TrimSpace(strings.ReplaceAll(alias, "／", "/"))
	alias = strings.Join(strings.Fields(alias), " ")
	merged := mergeAliases([]string{alias}, aliases)
	merged = dropAliasFragmentsOfPrimary(alias, merged)
	return mainTitle, merged
}

var titleUnderscoreEdgePattern = regexp.MustCompile(`^[\s_-]+|[\s_-]+$`)
var titleUnderscoreRunPattern = regexp.MustCompile(`_+`)
var titleSpaceRunPattern = regexp.MustCompile(`\s+`)

func applyUnderscoreDelimiterTitleAliasSpecialization(rawTitle string, title string, aliases []string) (string, []string) {
	orig := strings.TrimSpace(title)
	if orig == "" {
		return title, aliases
	}

	// CJK + Latin split bridge: "中文标题_English Title"
	if strings.Count(orig, "_") == 1 {
		parts := strings.SplitN(orig, "_", 2)
		left := normalizeUnderscoreDelimiterTitleToken(parts[0])
		right := normalizeUnderscoreDelimiterTitleToken(parts[1])
		if left != "" && right != "" && hasCJK(left) && hasLatin(right) {
			merged := mergeAliases([]string{right}, aliases)
			merged = dropAliasFragmentsOfPrimary(left, merged)
			return left, merged
		}
	}

	cleanTitle := normalizeUnderscoreDelimiterTitleToken(orig)
	if cleanTitle == "" {
		cleanTitle = cleanTitleToken(orig)
	}
	if cleanTitle == "" {
		cleanTitle = strings.TrimSpace(orig)
	}
	if cleanTitle == "" {
		return title, aliases
	}

	if len(aliases) == 0 {
		return cleanTitle, aliases
	}
	outAliases := make([]string, 0, len(aliases))
	for _, a := range aliases {
		aa := normalizeUnderscoreDelimiterTitleToken(a)
		if aa == "" {
			continue
		}
		outAliases = append(outAliases, aa)
	}
	outAliases = mergeAliases(nil, outAliases)
	outAliases = dropAliasFragmentsOfPrimary(cleanTitle, outAliases)
	return cleanTitle, outAliases
}

func normalizeUnderscoreDelimiterTitleToken(s string) string {
	s = cleanTitleToken(s)
	if s == "" {
		return ""
	}
	s = strings.TrimSpace(titleUnderscoreEdgePattern.ReplaceAllString(s, ""))
	s = titleUnderscoreRunPattern.ReplaceAllString(s, " ")
	s = titleSpaceRunPattern.ReplaceAllString(s, " ")
	s = strings.TrimSpace(strings.Trim(s, "-_"))
	return s
}

func dropAliasFragmentsOfPrimary(primary string, aliases []string) []string {
	pk := aliasSemanticKey(primary)
	if pk == "" || len(aliases) == 0 {
		return aliases
	}
	out := make([]string, 0, len(aliases))
	for _, a := range aliases {
		aa := strings.TrimSpace(a)
		if aa == "" {
			continue
		}
		ak := aliasSemanticKey(aa)
		if ak != "" && ak != pk && len(ak) >= 4 && strings.Contains(pk, ak) {
			continue
		}
		out = append(out, aa)
	}
	return out
}

var embeddedJapaneseDubRawPattern = regexp.MustCompile(`(?i)(?:日配版|日语配音版|日語配音版|\b(?:Japanese|JPN|JP)\s+Dub\b)`)
var embeddedChineseDubRawPattern = regexp.MustCompile(`(?i)(?:中配版?|中文配音版?|国语配音|國語配音|\b(?:Mandarin(?:\s+Chinese)?|Chinese)\s+Dub\b)`)
var embeddedEnglishSubRawPattern = regexp.MustCompile(`(?i)\b(?:English|ENG)[-_ ]Sub\b`)

func appendEmbeddedJapaneseDubAttrs(attrs []model.Attribute, rawTitle string) []model.Attribute {
	if strings.TrimSpace(rawTitle) == "" {
		return attrs
	}
	if !embeddedJapaneseDubRawPattern.MatchString(rawTitle) {
		return attrs
	}
	hasAudioLang := false
	hasDub := false
	for _, a := range attrs {
		switch a.Type {
		case model.AttrAudioLang:
			if strings.EqualFold(strings.TrimSpace(normalizeAudioLangToken(a.Normalized)), "JPN") {
				hasAudioLang = true
			}
		case model.AttrAudioFlag:
			if strings.EqualFold(strings.TrimSpace(a.Normalized), "JAPANESE_DUB") || strings.EqualFold(strings.TrimSpace(a.Normalized), "DUB") {
				hasDub = true
			}
		}
	}
	if !hasAudioLang {
		attrs = append(attrs, model.Attribute{
			Type:       model.AttrAudioLang,
			Normalized: "JPN",
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.65,
			Source:     model.AttrSourceRawAudioMeta,
		})
	}
	if !hasDub {
		attrs = append(attrs, model.Attribute{
			Type:       model.AttrAudioFlag,
			Normalized: "JAPANESE_DUB",
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.65,
			Source:     model.AttrSourceRawAudioMeta,
		})
	}
	return attrs
}

func appendEmbeddedChineseDubAttrs(attrs []model.Attribute, rawTitle string) []model.Attribute {
	if strings.TrimSpace(rawTitle) == "" {
		return attrs
	}
	if !embeddedChineseDubRawPattern.MatchString(rawTitle) {
		return attrs
	}
	hasAudioLang := false
	hasDub := false
	for _, a := range attrs {
		switch a.Type {
		case model.AttrAudioLang:
			if strings.EqualFold(strings.TrimSpace(normalizeAudioLangToken(a.Normalized)), "CMN") {
				hasAudioLang = true
			}
		case model.AttrAudioFlag:
			n := strings.TrimSpace(a.Normalized)
			if strings.EqualFold(n, "CHINESE_DUB") || strings.EqualFold(n, "DUB") {
				hasDub = true
			}
		}
	}
	if !hasAudioLang {
		attrs = append(attrs, model.Attribute{
			Type:       model.AttrAudioLang,
			Normalized: "CHN",
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.65,
			Source:     model.AttrSourceRawAudioMeta,
		})
	}
	if !hasDub {
		attrs = append(attrs, model.Attribute{
			Type:       model.AttrAudioFlag,
			Normalized: "CHINESE_DUB",
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.65,
			Source:     model.AttrSourceRawAudioMeta,
		})
	}
	return attrs
}

func appendEmbeddedEnglishSubAttrs(attrs []model.Attribute, rawTitle string) []model.Attribute {
	if strings.TrimSpace(rawTitle) == "" {
		return attrs
	}
	if !embeddedEnglishSubRawPattern.MatchString(rawTitle) {
		return attrs
	}
	hasSubLang := false
	hasSubFlag := false
	for _, a := range attrs {
		switch a.Type {
		case model.AttrSubLang:
			if strings.EqualFold(strings.TrimSpace(normalizeSubLangToken(a.Normalized)), "ENG") {
				hasSubLang = true
			}
		case model.AttrSubFlag:
			n, _, _ := normalizeSubFlagToken(a.Normalized)
			if strings.EqualFold(strings.TrimSpace(n), "soft") {
				hasSubFlag = true
			}
		}
	}
	if !hasSubLang {
		attrs = append(attrs, model.Attribute{
			Type:       model.AttrSubLang,
			Normalized: "ENG",
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.65,
			Source:     model.AttrSourceRawSubMeta,
		})
	}
	if !hasSubFlag {
		attrs = append(attrs, model.Attribute{
			Type:       model.AttrSubFlag,
			Normalized: "ENGSUB",
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.65,
			Source:     model.AttrSourceRawSubMeta,
		})
	}
	return attrs
}

func normalizeEpisodeTitleSepConstraint(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "_", "underscore":
		return "_"
	case "-", "dash":
		return "-"
	case ".", "dot":
		return "."
	case "&", "amp", "and":
		return "&"
	case "space", "whitespace":
		return " "
	}
	if raw == " " {
		return " "
	}
	return ""
}

func trimPrefixFold(s, prefix string) (string, bool) {
	if prefix == "" {
		return s, false
	}
	if len(s) < len(prefix) {
		return s, false
	}
	head := s[:len(prefix)]
	if !strings.EqualFold(head, prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

var bracketSegmentPattern = regexp.MustCompile(`\[(.*?)\]`)
var episodeLikeThirdBracketPattern = regexp.MustCompile(`(?i)^\s*(\d{1,4})(?:\s*([_ .-]|&)\s*(.*))?\s*$`)
var spKindLeadPattern = regexp.MustCompile(`(?i)^(SP|OVA|OAD|ONA|NCOP|NCED|PV|CM)\b`)
var episodeLikeAliasPattern = regexp.MustCompile(`(?i)^\s*\d{1,4}(?:\s*([_ .-]|&)\s*.*)?$`)
var bracketDatePattern = regexp.MustCompile(`\[(\d{4}[./-]\d{2}[./-]\d{2})\]`)
var aptx4869ConanEpisodeTitlePattern = regexp.MustCompile(`^\s*(名[侦偵]探柯南)\s+0*(\d{1,4})\s+(.+?)\s*$`)

func extractEpisodeLikeFromThirdBracket(rawTitle string) (episode string, spKind string, ok bool) {
	ep, sp, _, _, ok := extractEpisodeLikeFromThirdBracketDetailedWithSep(rawTitle)
	return ep, sp, ok
}

func extractAPTX4869ConanEpisodeTitleRoute(rawTitle string) (title string, episode string, epTitle string, ok bool) {
	if strings.TrimSpace(rawTitle) == "" {
		return "", "", "", false
	}
	matches := bracketSegmentPattern.FindAllStringSubmatch(rawTitle, -1)
	if len(matches) < 3 {
		return "", "", "", false
	}
	if !strings.EqualFold(strings.TrimSpace(matches[1][1]), "CONAN") {
		return "", "", "", false
	}
	seg := strings.TrimSpace(matches[2][1])
	if seg == "" {
		return "", "", "", false
	}
	m := aptx4869ConanEpisodeTitlePattern.FindStringSubmatch(seg)
	if len(m) != 4 {
		return "", "", "", false
	}
	title = normalizeTitleMain(cleanTitleToken(m[1]))
	if title == "" {
		return "", "", "", false
	}
	epNum, err := strconv.Atoi(strings.TrimSpace(m[2]))
	if err != nil || epNum <= 0 {
		return "", "", "", false
	}
	episode = strconv.Itoa(epNum)
	epTitle = normalizeTitleMain(cleanTitleToken(m[3]))
	if epTitle == "" {
		return "", "", "", false
	}
	return title, episode, epTitle, true
}

func extractEpisodeLikeFromThirdBracketDetailed(rawTitle string) (episode string, spKind string, epTitle string, ok bool) {
	ep, sp, title, _, ok := extractEpisodeLikeFromThirdBracketDetailedWithSep(rawTitle)
	return ep, sp, title, ok
}

func extractEpisodeLikeFromThirdBracketDetailedWithSep(rawTitle string) (episode string, spKind string, epTitle string, epSep string, ok bool) {
	if strings.TrimSpace(rawTitle) == "" {
		return "", "", "", "", false
	}
	matches := bracketSegmentPattern.FindAllStringSubmatch(rawTitle, -1)
	if len(matches) < 3 {
		return "", "", "", "", false
	}
	seg := strings.TrimSpace(matches[2][1])
	if seg == "" {
		return "", "", "", "", false
	}
	if dateYMDPattern.MatchString(seg) {
		return "", "", "", "", false
	}
	m := episodeLikeThirdBracketPattern.FindStringSubmatch(seg)
	if len(m) == 0 {
		return "", "", "", "", false
	}
	epRaw := strings.TrimSpace(m[1])
	epNum, err := strconv.Atoi(epRaw)
	if err != nil || epNum < 0 {
		return "", "", "", "", false
	}
	episode = strconv.Itoa(epNum)
	rest := ""
	sep := strings.TrimSpace(m[2])
	if m[2] == " " {
		sep = " "
	}
	if len(m) >= 4 {
		rest = strings.TrimSpace(m[3])
	}
	if strings.EqualFold(strings.TrimSpace(m[2]), "&") {
		if sm := spKindLeadPattern.FindStringSubmatch(rest); len(sm) > 1 {
			spKind = strings.ToUpper(strings.TrimSpace(sm[1]))
			epTitle = strings.TrimSpace(rest[len(strings.TrimSpace(sm[0])):])
			epTitle = strings.TrimSpace(strings.TrimLeft(epTitle, "_ .-"))
			return episode, spKind, epTitle, "&", true
		}
	}
	if sm := spKindLeadPattern.FindStringSubmatch(rest); len(sm) > 1 {
		spKind = strings.ToUpper(strings.TrimSpace(sm[1]))
		epTitle = strings.TrimSpace(rest[len(strings.TrimSpace(sm[0])):])
		epTitle = strings.TrimSpace(strings.TrimLeft(epTitle, "_ .-"))
		return episode, spKind, epTitle, sep, true
	}
	return episode, "", rest, sep, true
}

func extractTailBetweenThirdAndFourthBracket(rawTitle string) string {
	if strings.TrimSpace(rawTitle) == "" {
		return ""
	}
	closeCount := 0
	start := -1
	for i, r := range rawTitle {
		if r == ']' || r == '】' {
			closeCount++
			if closeCount == 3 {
				start = i + 1
				break
			}
		}
	}
	if start < 0 || start >= len(rawTitle) {
		return ""
	}
	end := len(rawTitle)
	for i, r := range rawTitle[start:] {
		if r == '[' || r == '【' {
			end = start + i
			break
		}
	}
	if end <= start {
		return ""
	}
	s := strings.TrimSpace(rawTitle[start:end])
	s = strings.TrimSpace(strings.Trim(s, "-_/ "))
	return s
}

func pruneEpisodeLikeAliases(aliases []string) []string {
	if len(aliases) == 0 {
		return aliases
	}
	out := make([]string, 0, len(aliases))
	for _, a := range aliases {
		aa := strings.TrimSpace(a)
		if aa == "" {
			continue
		}
		if episodeLikeAliasPattern.MatchString(aa) {
			continue
		}
		out = append(out, a)
	}
	return out
}

var rawMovieMixPattern = regexp.MustCompile(`(?i)\b(?:tv|ova|oad|ona|sp|special)\s*\+\s*(?:movie|movies|film|films)\b|\b(?:movie|movies|film|films)\s*\+\s*(?:tv|ova|oad|ona|sp|special)\b|\btv\b.{0,32}\+\s*(?:movie|movies|film|films)\b|\b(?:movie|movies|film|films)\b.{0,32}\+\s*tv\b|\b(?:movies|films)\s*\d{1,3}\s*[-~]\s*\d{1,3}\b|\bmovies\b|\bfilms\b`)
var explicitTVEpisodeRangeRawPattern = regexp.MustCompile(`(?i)\btv\s*0*\d{1,4}\s*[-~]\s*0*\d{1,4}\b`)

func hasMovieKeywordInRawTitle(raw string) bool {
	low := strings.ToLower(strings.TrimSpace(raw))
	if low == "" {
		return false
	}
	if strings.Contains(low, "movie") || strings.Contains(low, "movies") || strings.Contains(low, "film") || strings.Contains(low, "films") {
		return true
	}
	if strings.Contains(raw, "劇場版") || strings.Contains(raw, "剧场版") || strings.Contains(raw, "大电影") || strings.Contains(raw, "大電影") {
		return true
	}
	if rawStandaloneMovieJPTagPattern.MatchString(raw) {
		return true
	}
	return rawMovieTagPattern.MatchString(raw)
}
