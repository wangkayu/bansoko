package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func (p *Parser) extractParenAliasesAndRemoveFromTitle(tokens []model.Token, titleEnd int, episodeIdx int, episodeRangeIdx int, attrs *[]model.Attribute) []string {
	if titleEnd < 0 {
		return nil
	}
	start := titleEnd
	end := len(tokens)
	if episodeRangeIdx >= 0 {
		end = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < end {
		end = episodeIdx
	}
	if end <= start {
		return nil
	}

	aliases := make([]string, 0, 8)
	for i := start; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		if tokens[i].Text != "(" && tokens[i].Text != "（" {
			continue
		}
		openIdx, closeIdx, inner, ok := parseParenBlock(tokens, i, end)
		if !ok {
			continue
		}
		inner = strings.TrimSpace(inner)
		if inner == "" {
			for k := openIdx; k <= closeIdx; k++ {
				tokens[k].Removed = true
			}
			i = closeIdx
			continue
		}
		if strings.Contains(inner, "※") {
			i = closeIdx
			continue
		}
		cands, isSearchHint, hintKeyword := splitAliasCandidatesFromParenContent(inner)
		if !isSearchHint {
			if markers := extractParenContentMarkers(inner); len(markers) > 0 {
				emitContentsAttrs(attrs, markers, model.AttrSourceParenContentsMarker)
				for k := openIdx; k <= closeIdx; k++ {
					tokens[k].Removed = true
				}
				i = closeIdx
				continue
			}
		}
		if looksLikeParenMetaOrTech(tokens, openIdx, end) && !isSearchHint {
			if attrs != nil {
				spl := reExtractParenAliasesAndRemoveFromTitle1.Split(inner, -1)
				for _, it := range spl {
					it = strings.TrimSpace(it)
					if it == "" {
						continue
					}
					if isStreamMetaText(it) {
						classifyAndEmitStreamMeta(it, attrs)
						continue
					}
					if reTailMetaToken.MatchString(it) {
						classifyAndEmitTailMetaNote(it, attrs)
						continue
					}
				}
			}
			for k := openIdx; k <= closeIdx; k++ {
				tokens[k].Removed = true
			}
			i = closeIdx
			continue
		}
		if isSearchHint && attrs != nil {
			hk := strings.TrimSpace(hintKeyword)
			if hk != "" {
				*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: hk, Confidence: 0.6, Source: model.AttrSourceHintKeyword})
			}
		}
		for _, c := range cands {
			c = cleanAliasToken(c)
			if c == "" {
				continue
			}
			if isSearchHint || isLikelySearchHintText(c) {
				appendSearchHintAttribute(attrs, c, model.AttrSourceParenSearchHint)
				continue
			}
			if !p.isValidAliasToken(c) {
				continue
			}
			if stripped := stripExplicitSeasonSuffix(c); stripped != "" {
				c = stripped
				if !p.isValidAliasToken(c) {
					continue
				}
			}
			if reTailMetaToken.MatchString(c) || isAliasMetaSemanticOnly(c) {
				classifyAndEmitTailMetaNote(c, attrs)
				if markers := extractParenContentMarkers(c); len(markers) > 0 {
					emitContentsAttrs(attrs, markers, model.AttrSourceAliasMetaDemote)
				}
				continue
			}
			aliases = append(aliases, c)
		}
		for k := openIdx; k <= closeIdx; k++ {
			tokens[k].Removed = true
		}
		i = closeIdx
	}
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

func (p *Parser) inferTailAliasesFromParens(tokens []model.Token, attrs *[]model.Attribute, episodeIdx int, episodeRangeIdx int, seasonIdx int) []string {
	if attrs == nil {
		return nil
	}
	isPlatformOnlyParen := func(inner string) bool {
		inner = strings.TrimSpace(inner)
		if inner == "" {
			return false
		}
		for _, a := range *attrs {
			if a.Type != model.AttrPlatform {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(a.Normalized), inner) {
				return true
			}
		}
		return false
	}
	techStart, ok := firstTechTokenIndexFromAttrs(*attrs)
	if !ok {
		return nil
	}
	scanStart := techStart
	epAnchor := episodeIdx
	if episodeRangeIdx > epAnchor {
		epAnchor = episodeRangeIdx
	}
	if seasonIdx > epAnchor {
		epAnchor = seasonIdx
	}
	if epAnchor >= 0 && epAnchor+1 < scanStart {
		scanStart = epAnchor + 1
	}
	if scanStart < 0 {
		scanStart = 0
	}
	if scanStart >= len(tokens) {
		return nil
	}

	aliases := make([]string, 0, 8)
	for i := scanStart; i < len(tokens); i++ {
		if tokens[i].Text != "(" && tokens[i].Text != "（" {
			continue
		}
		openRemoved := tokens[i].Removed
		_, closeIdx, inner, ok := parseParenBlockAll(tokens, i, len(tokens))
		if !ok {
			continue
		}
		inner = strings.TrimSpace(inner)
		if inner == "" {
			i = closeIdx
			continue
		}
		if strings.Contains(inner, "※") {
			i = closeIdx
			continue
		}
		if isPlatformOnlyParen(inner) {
			i = closeIdx
			continue
		}
		cands, isSearchHint, hintKeyword := splitAliasCandidatesFromParenContent(inner)
		if !isSearchHint {
			if markers := extractParenContentMarkers(inner); len(markers) > 0 {
				emitContentsAttrs(attrs, markers, model.AttrSourceParenContentsMarker)
				i = closeIdx
				continue
			}
		}
		if isLikelyMetaBracketText(inner) && !isSearchHint {
			i = closeIdx
			continue
		}
		if isSearchHint {
			hk := strings.TrimSpace(hintKeyword)
			if hk != "" {
				*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: hk, Confidence: 0.6, Source: model.AttrSourceHintKeyword})
			}
		}
		for _, c := range cands {
			c = cleanAliasToken(c)
			if c == "" {
				continue
			}
			if isSearchHint || isLikelySearchHintText(c) {
				appendSearchHintAttribute(attrs, c, model.AttrSourceTailParenSearchHint)
				continue
			}
			if !p.isValidAliasToken(c) {
				continue
			}
			if stripped := stripExplicitSeasonSuffix(c); stripped != "" {
				c = stripped
				if !p.isValidAliasToken(c) {
					continue
				}
			}
			if isStreamMetaText(c) {
				classifyAndEmitStreamMeta(c, attrs)
				continue
			}
			if reTailMetaToken.MatchString(c) || isAliasMetaSemanticOnly(c) {
				classifyAndEmitTailMetaNote(c, attrs)
				if markers := extractParenContentMarkers(c); len(markers) > 0 {
					emitContentsAttrs(attrs, markers, model.AttrSourceAliasMetaDemote)
				}
				continue
			}
			if len([]rune(c)) > 80 {
				continue
			}
			if openRemoved && !isSearchHint {
				continue
			}
			aliases = append(aliases, c)
		}
		for k := i; k <= closeIdx && k < len(tokens); k++ {
			tokens[k].Removed = true
		}
		i = closeIdx
	}
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

func splitAliasCandidatesFromParenContent(content string) ([]string, bool, string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false, ""
	}
	searchHint := false
	hintKeyword := ""
	if m := reHintPrefix.FindStringSubmatch(content); len(m) == 3 {
		hintKeyword = strings.TrimSpace(m[1])
		content = strings.TrimSpace(m[2])
		searchHint = true
	}
	if content == "" {
		return nil, searchHint, hintKeyword
	}
	allowComma := searchHint || reStreamMetaFlag.MatchString(content)
	preserveTitleCommas := !allowComma && strings.ContainsAny(content, ";；")
	norm := strings.NewReplacer(
		"｜", "|",
		"／", "/",
		"；", ";",
		"，", ",",
		"、", ",",
	).Replace(content)
	if preserveTitleCommas {
		norm = strings.NewReplacer("｜", "|", "／", "/", "；", ";").Replace(content)
	}
	if !allowComma && strings.Contains(norm, ",") {
		commaParts := strings.Split(norm, ",")
		validAliasParts := 0
		for _, p := range commaParts {
			pp := strings.TrimSpace(p)
			if pp == "" {
				continue
			}
			if isLikelyMetaBracketText(pp) || isStreamMetaText(pp) {
				validAliasParts = 0
				break
			}
			if len([]rune(pp)) >= 4 {
				validAliasParts++
			}
		}
		if validAliasParts >= 2 && !strings.Contains(norm, ";") {
			allowComma = true
		}
	}
	parts := strings.FieldsFunc(norm, func(r rune) bool {
		if r == ',' {
			return allowComma
		}
		return r == '/' || r == '|' || r == ';' || r == '\\'
	})
	// Search-hint text: extract candidates for AttrSearchHint; do not keep as title_alias.
	if searchHint {
		if len(parts) > 0 {
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				out = append(out, p)
			}
			if len(out) > 0 {
				return out, searchHint, hintKeyword
			}
		}
		cand := strings.TrimSpace(norm)
		if cand == "" {
			return nil, searchHint, hintKeyword
		}
		return []string{cand}, searchHint, hintKeyword
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return []string{content}, searchHint, hintKeyword
	}
	return out, searchHint, hintKeyword
}

func appendSearchHintAttribute(attrs *[]model.Attribute, value string, source model.AttrSource) {
	if attrs == nil {
		return
	}
	v := strings.TrimSpace(value)
	if v == "" {
		return
	}
	key := aliasSemanticKey(v)
	for _, a := range *attrs {
		if a.Type != model.AttrSearchHint {
			continue
		}
		if aliasSemanticKey(a.Normalized) == key {
			return
		}
	}
	*attrs = append(*attrs, model.Attribute{
		Type:       model.AttrSearchHint,
		Normalized: v,
		TokenStart: -1,
		TokenEnd:   -1,
		Confidence: 0.65,
		Source:     source,
	})
}

func isLikelySearchHintText(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return false
	}
	if reSearchHintKeyword.MatchString(ss) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(ss), "search:")
}

func isAliasMetaSemanticOnly(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return false
	}
	if reAliasMetaSemanticOnly.MatchString(ss) {
		return true
	}
	// Content markers-only tokens should not remain as aliases.
	if markers := extractParenContentMarkers(ss); len(markers) > 0 {
		return true
	}
	return false
}

var reParenContentSeasonRange = regexp.MustCompile(`(?i)^season\s*\d{1,2}(?:\s*[-~]\s*\d{1,2})?$`)
var reParenContentNumericRange = regexp.MustCompile(`^\d{1,3}(?:\s*[-~]\s*\d{1,3})?$`)
var reParenContentMarkerSplit = regexp.MustCompile(`[+＋/&|,;，；、]+`)

func extractParenContentMarkers(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	norm := strings.NewReplacer(
		"＋", "+",
		"＆", "&",
		"｜", "|",
		"／", "/",
		"　", " ",
	).Replace(content)
	parts := reParenContentMarkerSplit.Split(norm, -1)
	if len(parts) == 0 {
		return nil
	}
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	hasSeasonCtx := false
	for _, p := range parts {
		s := strings.TrimSpace(strings.Trim(p, "[](){}【】（）"))
		if s == "" {
			continue
		}
		ls := strings.ToLower(s)
		if strings.HasPrefix(ls, "season ") || strings.HasPrefix(ls, "saison ") {
			hasSeasonCtx = true
		}
		if reParenContentSeasonRange.MatchString(s) {
			hasSeasonCtx = true
			continue
		}
		if reParenContentNumericRange.MatchString(strings.ReplaceAll(s, " ", "")) {
			continue
		}
		marker, ok := normalizeParenContentMarkerToken(s)
		if !ok {
			return nil
		}
		if _, exists := seen[marker]; exists {
			continue
		}
		seen[marker] = struct{}{}
		out = append(out, marker)
	}
	if hasSeasonCtx && len(out) > 0 {
		if _, ok := seen["TV"]; !ok {
			out = append([]string{"TV"}, out...)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractContentsFromAliases(aliases []string) ([]string, []string) {
	if len(aliases) == 0 {
		return nil, nil
	}
	kept := make([]string, 0, len(aliases))
	collected := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, a := range aliases {
		s := strings.TrimSpace(a)
		if s == "" {
			continue
		}
		markers := extractParenContentMarkers(s)
		if len(markers) == 0 {
			kept = append(kept, a)
			continue
		}
		for _, m := range markers {
			m = strings.ToUpper(strings.TrimSpace(m))
			if m == "" {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			collected = append(collected, m)
		}
	}
	if len(kept) == 0 {
		kept = nil
	}
	if len(collected) == 0 {
		collected = nil
	}
	return kept, collected
}

var reRawParenContent = regexp.MustCompile(`[（(]([^（）()]{1,80})[）)]`)

func extractContentsFromRawParens(rawTitle string) []string {
	rawTitle = strings.TrimSpace(rawTitle)
	if rawTitle == "" {
		return nil
	}
	ms := reRawParenContent.FindAllStringSubmatch(rawTitle, -1)
	if len(ms) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 4)
	for _, m := range ms {
		if len(m) < 2 {
			continue
		}
		for _, marker := range extractParenContentMarkers(m[1]) {
			marker = strings.ToUpper(strings.TrimSpace(marker))
			if marker == "" {
				continue
			}
			if _, ok := seen[marker]; ok {
				continue
			}
			seen[marker] = struct{}{}
			out = append(out, marker)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeParenContentMarkerToken(s string) (string, bool) {
	low := strings.ToLower(strings.TrimSpace(s))
	switch low {
	case "movie", "movies", "film", "films", "eiga", "gekijouban", "劇場版", "剧场版", "電影", "电影":
		return "MOVIE", true
	case "special", "specials", "sp", "spéciaux", "speciaux", "特典", "特典映像", "bonus", "bonuses", "bouns", "tokuten", "特别篇", "特別篇":
		return "SPECIAL", true
	case "ova":
		return "OVA", true
	case "oad":
		return "OAD", true
	case "ona":
		return "ONA", true
	case "ex":
		return "EX", true
	case "tv":
		return "TV", true
	default:
		return "", false
	}
}

func emitContentsAttrs(attrs *[]model.Attribute, values []string, source model.AttrSource) {
	if attrs == nil || len(values) == 0 {
		return
	}
	for _, v := range values {
		v = strings.TrimSpace(strings.ToUpper(v))
		if v == "" {
			continue
		}
		*attrs = append(*attrs, model.Attribute{
			Type:       model.AttrContents,
			Normalized: v,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.8,
			Source:     source,
		})
	}
}

func headAliasBracketPair(s string) (open string, close string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "(" {
		return "(", ")", true
	}
	if s == "（" {
		return "（", "）", true
	}
	if s == "[" {
		return "[", "]", true
	}
	if s == "【" {
		return "【", "】", true
	}
	return "", "", false
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reExtractParenAliasesAndRemoveFromTitle1 = regexp.MustCompile(`[,，;；\|/]+`)
)
