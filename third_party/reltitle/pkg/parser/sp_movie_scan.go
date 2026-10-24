package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func (p *Parser) inferTailAliasesFromPipes(tokens []model.Token, attrs []model.Attribute, group string) []string {
	techStart, ok := firstTechTokenIndexFromAttrs(attrs)
	if !ok {
		return nil
	}
	if techStart < 0 || techStart >= len(tokens) {
		return nil
	}
	aliases := make([]string, 0, 4)
	seen := map[string]bool{}
	groupKey := strings.ToLower(strings.TrimSpace(group))
	if groupKey != "" {
		seen[groupKey] = true
	}
	for i := techStart; i < len(tokens); i++ {
		if tokens[i].Text != "|" && tokens[i].Text != "｜" {
			continue
		}
		segStart := i + 1
		segEnd := len(tokens)
		for j := segStart; j < len(tokens); j++ {
			if tokens[j].Text == "|" || tokens[j].Text == "｜" {
				segEnd = j
				break
			}
		}
		if segStart >= segEnd {
			i = segEnd
			continue
		}
		parts := make([]string, 0, segEnd-segStart)
		for k := segStart; k < segEnd; k++ {
			if tokens[k].Removed {
				continue
			}
			t := strings.TrimSpace(tokens[k].Text)
			if t == "" {
				continue
			}
			if t == "[" || t == "]" || t == "(" || t == ")" || t == "（" || t == "）" || t == "{" || t == "}" || t == "【" || t == "】" {
				continue
			}
			parts = append(parts, t)
		}
		inner := strings.TrimSpace(strings.Join(parts, " "))
		if inner == "" {
			i = segEnd
			continue
		}
		inner = cleanAliasToken(inner)
		if stripped := stripExplicitSeasonSuffix(inner); stripped != "" {
			inner = stripped
		}
		if !p.isValidAliasToken(inner) {
			i = segEnd
			continue
		}
		if isTailGroupLike(inner) {
			i = segEnd
			continue
		}
		k := strings.ToLower(strings.TrimSpace(inner))
		if k != "" && !seen[k] {
			seen[k] = true
			aliases = append(aliases, inner)
		}
		i = segEnd
	}
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

func isTailGroupLike(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return false
	}
	if strings.HasPrefix(ss, "-") || strings.HasPrefix(ss, "@") {
		return true
	}
	if reIsTailGroupLike1.MatchString(ss) {
		return true
	}
	return false
}

var tvPackKeywordPattern = regexp.MustCompile(`(?i)tv\s*(全集|全话|全話)`)

func detectPackKind(cfg *config.Config, rawTitle string) string {
	lower := strings.ToLower(rawTitle)
	if tvPackKeywordPattern.MatchString(rawTitle) {
		return "TV_PACK"
	}

	keywords := make([]string, 0, 16)
	for _, r := range cfg.Inference.ContentTypeInference {
		if len(r.PackKeywords) > 0 {
			keywords = append(keywords, r.PackKeywords...)
		}
	}

	if strings.Contains(rawTitle, "TV动画") || strings.Contains(rawTitle, "tv动画") {
		hasEnum := strings.Contains(rawTitle, "+") || strings.Contains(rawTitle, "；") || strings.Contains(rawTitle, ";")
		if hasEnum {
			hits := 0
			if strings.Contains(rawTitle, "OVA") || strings.Contains(rawTitle, "ova") || strings.Contains(rawTitle, "OVA篇") {
				hits++
			}
			if strings.Contains(rawTitle, "小说") {
				hits++
			}
			if strings.Contains(rawTitle, "漫画") {
				hits++
			}
			if strings.Contains(rawTitle, "CD") || strings.Contains(rawTitle, "cd") {
				hits++
			}
			if strings.Contains(rawTitle, "其他") {
				hits++
			}
			if hits >= 2 {
				return "mixed_pack"
			}
		}
	}

	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(kw)) {
			return kw
		}
	}

	return ""
}

var spKindPattern = regexp.MustCompile(`(?i)^(sp|special|ova|oad|ona|ex|ncop|nced|pv)(\d{1,3})?$`)

func normalizeSPKindToken(kind string) string {
	k := strings.ToUpper(strings.TrimSpace(kind))
	switch k {
	case "SPECIAL":
		return "SP"
	default:
		return k
	}
}

var shortEpisodeNumberPattern = regexp.MustCompile(`^\d{1,3}$`)

func isShortEpisodeNumberToken(raw string) bool {
	txt := strings.TrimSpace(raw)
	if txt == "" {
		return false
	}
	if !shortEpisodeNumberPattern.MatchString(txt) {
		return false
	}
	n, err := strconv.Atoi(txt)
	if err != nil {
		return false
	}
	return n >= 0 && n <= 999
}

func isSPLeadToken(raw string) bool {
	up := strings.ToUpper(strings.TrimSpace(raw))
	switch up {
	case "SP", "SPECIAL", "特别篇", "特別篇", "OVA", "OAD", "ONA":
		return true
	default:
		return false
	}
}

func isSimpleBoundarySep(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "-", ":", "：", "&", "＆", "+", "＋", "/", "／":
		return true
	default:
		return false
	}
}

func isBracketOpen(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "[", "【", "(", "（":
		return true
	default:
		return false
	}
}

func isBracketClose(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "]", "】", ")", "）":
		return true
	default:
		return false
	}
}

func isLikelyDelimiterToken(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "", "-", "_", ".", ":", "：", "&", "＆", "+", "＋", "/", "／", "|", "｜":
		return true
	default:
		return false
	}
}

func isIsolatedInBracketOrParen(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	inBracket := isInBracket(tokens, idx)
	inParen := isInParen(tokens, idx)
	if !inBracket && !inParen {
		return false
	}
	if idx-1 >= 0 {
		prev := strings.TrimSpace(tokens[idx-1].Text)
		if !(isBracketOpen(prev) || isLikelyDelimiterToken(prev)) {
			return false
		}
	}
	if idx+1 < len(tokens) {
		next := strings.TrimSpace(tokens[idx+1].Text)
		if !(isBracketClose(next) || isLikelyDelimiterToken(next)) {
			return false
		}
	}
	return true
}

func shouldAcceptSPKindToken(tokens []model.Token, idx int, kind string, suffix string) bool {
	k := normalizeSPKindToken(kind)
	if k != "EX" && k != "OVA" && k != "OAD" && k != "ONA" && k != "SP" {
		return true
	}
	if strings.TrimSpace(suffix) != "" {
		return true
	}
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	if isInBracket(tokens, idx) || isInParen(tokens, idx) {
		if !isIsolatedInBracketOrParen(tokens, idx) {
			return false
		}
		return true
	}
	if k != "EX" {
		// OVA/OAD/ONA/SP without numeric suffix is ambiguous in plain title text.
		// Keep it as title word by default (e.g. "XXX OVA - 01"), and only accept
		// explicit standalone marker forms (bracket/paren handled above) or tail meta form.
		if hasSPTailMetaBoundary(tokens, idx) {
			return true
		}
		return false
	}
	prev := prevAliveToken(tokens, idx-1)
	if prev >= 0 && prev < idx {
		between := ""
		if idx-1 >= 0 {
			between = strings.TrimSpace(tokens[idx-1].Text)
		}
		if isShortEpisodeNumberToken(tokens[prev].Text) && isSimpleBoundarySep(between) {
			return true
		}
		if isSPLeadToken(tokens[prev].Text) && isSimpleBoundarySep(between) {
			return true
		}
	}
	next := nextAliveToken(tokens, idx+1)
	if next >= 0 && next > idx {
		between := ""
		if idx+1 < len(tokens) {
			between = strings.TrimSpace(tokens[idx+1].Text)
		}
		if isShortEpisodeNumberToken(tokens[next].Text) && isSimpleBoundarySep(between) {
			return true
		}
	}
	return false
}

func hasSPTailMetaBoundary(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	// Immediate raw token opens a meta block, even if that token was already
	// removed by earlier tech/source extraction: "Title - SP (BS11 ...)".
	if idx+1 < len(tokens) {
		if isBracketOpen(strings.TrimSpace(tokens[idx+1].Text)) {
			return true
		}
	}
	// Allow one separator before the raw meta opener.
	if idx+2 < len(tokens) {
		sep := strings.TrimSpace(tokens[idx+1].Text)
		open := strings.TrimSpace(tokens[idx+2].Text)
		if isSimpleBoundarySep(sep) && isBracketOpen(open) {
			return true
		}
	}
	// Immediate next alive token starts a meta block: "OVA (2015) [BD ...]"
	next := nextAliveToken(tokens, idx+1)
	if next >= 0 && next < len(tokens) {
		nt := strings.TrimSpace(tokens[next].Text)
		if isBracketOpen(nt) {
			return true
		}
	}
	// Allow one separator before the alive-token meta block.
	if idx+2 < len(tokens) {
		sep := strings.TrimSpace(tokens[idx+1].Text)
		open := strings.TrimSpace(tokens[idx+2].Text)
		if isSimpleBoundarySep(sep) && isBracketOpen(open) {
			return true
		}
	}
	return false
}

func detectSPKind(tokens []model.Token) string {
	for i := 0; i < len(tokens); i++ {
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if tokens[i].Removed {
			if m := spKindPattern.FindStringSubmatch(txt); len(m) >= 2 {
				kind := normalizeSPKindToken(m[1])
				// Removed EX token is often part of channel/source markers (BS-EX/CS-EX1),
				// so we do not infer sp_kind=EX from removed tokens.
				if kind != "EX" {
					return kind
				}
			}
			continue
		}
		if m := reDetectSPKind1.FindStringSubmatch(txt); len(m) == 2 {
			return normalizeSPKindToken(m[1])
		}
		m := spKindPattern.FindStringSubmatch(txt)
		if len(m) >= 2 {
			kind := normalizeSPKindToken(m[1])
			suffix := ""
			if len(m) >= 3 {
				suffix = strings.TrimSpace(m[2])
			}
			if shouldAcceptSPKindToken(tokens, i, kind, suffix) {
				return kind
			}
		}
	}
	return ""
}

func extractSeasonRangeAndRemoveFromTokens(tokens []model.Token) string {
	best := ""
	for i := 0; i+2 < len(tokens); i++ {
		if tokens[i].Removed || tokens[i+1].Removed || tokens[i+2].Removed {
			continue
		}
		if tokens[i].Protected || tokens[i+1].Protected || tokens[i+2].Protected {
			continue
		}
		l := strings.TrimSpace(tokens[i].Text)
		mid := strings.TrimSpace(tokens[i+1].Text)
		r := strings.TrimSpace(tokens[i+2].Text)
		if mid != "-" {
			continue
		}
		ml := reExtractSeasonRangeAndRemoveFromTokens1.FindStringSubmatch(l)
		mr := reExtractSeasonRangeAndRemoveFromTokens2.FindStringSubmatch(r)
		if len(ml) != 2 || len(mr) != 2 {
			continue
		}
		ls := strings.TrimLeft(ml[1], "0")
		rs := strings.TrimLeft(mr[1], "0")
		if ls == "" {
			ls = "0"
		}
		if rs == "" {
			rs = "0"
		}
		if ls == "0" || rs == "0" {
			continue
		}
		cand := "S" + ls + "-S" + rs
		// Remove tokens so season detection does not pick only one side (e.g., S2).
		tokens[i].Removed = true
		tokens[i+1].Removed = true
		tokens[i+2].Removed = true
		if best == "" {
			best = cand
		}
	}
	return best
}

var movieIndexPattern = regexp.MustCompile(`(?i)^(?:movie)?\s*(\d{1,3})$`)
var jpMovieIndexPattern = regexp.MustCompile(`(?i)(?:劇場版|剧场版)(?:第)?(\d{1,3})(?:部)?`)
var filmIndexPattern = regexp.MustCompile(`(?i)^film[\s._-]?(\d{1,3})$`)
var movieShortIndexPattern = regexp.MustCompile(`(?i)^m0*(\d{1,3})$`)
var movieShortRangePattern = regexp.MustCompile(`(?i)^m0*(\d{1,3})\s*[-~]\s*m?0*(\d{1,3})$`)
var movieRangeTokenPattern = regexp.MustCompile(`(?i)^(?:movie|film)\s*0*(\d{1,3})\s*[-~]\s*0*(\d{1,3})$`)
var movieRangeDigitPattern = regexp.MustCompile(`^0*(\d{1,3})$`)
var moviePartSignalPattern = regexp.MustCompile(`(?i)\b(?:part|day|chapter)\s*[-:：]?\s*0*(\d{1,3})\b|(?:第\s*0*(\d{1,3})\s*部)|(?:前篇|後篇|后篇|完结篇|完結篇)`)
var movieBracketEpisodePattern = regexp.MustCompile(`[\[【(（]\s*0*(\d{1,3})\s*[\]】)）]`)
var rawMovieTagPattern = regexp.MustCompile(`(?i)[\[【(（]\s*(?:movie|film|電影|电影)\s*[\]】)）]`)
var rawStandaloneMovieJPTagPattern = regexp.MustCompile(`(?i)[\[【(（]\s*(?:映画|eiga|gekijouban)\s*[\]】)）]`)
var movieDashEpisodePattern = regexp.MustCompile(`(?i)\b(?:movie|film)\s*0*\d{1,3}\s*[-:：]\s*0*\d{1,3}\b`)

func isMovieKeywordText(text string) bool {
	lt := strings.ToLower(strings.TrimSpace(text))
	if lt == "movie" || lt == "themovie" || lt == "film" {
		return true
	}
	trimmed := strings.TrimSpace(text)
	return trimmed == "劇場版" || trimmed == "剧场版" || trimmed == "大电影" || trimmed == "大電影"
}

func hasMovieContextAround(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	start := idx - 3
	if start < 0 {
		start = 0
	}
	end := idx + 3
	if end >= len(tokens) {
		end = len(tokens) - 1
	}
	for i := start; i <= end; i++ {
		if i == idx || tokens[i].Removed {
			continue
		}
		if isMovieKeywordText(strings.TrimSpace(tokens[i].Text)) {
			return true
		}
	}
	return false
}

func isMovieRangeSepToken(text string) bool {
	text = strings.TrimSpace(text)
	return text == "-" || text == "~"
}

func parseMovieRangeDigits(left string, right string) (string, bool) {
	lm := movieRangeDigitPattern.FindStringSubmatch(strings.TrimSpace(left))
	rm := movieRangeDigitPattern.FindStringSubmatch(strings.TrimSpace(right))
	if len(lm) != 2 || len(rm) != 2 {
		return "", false
	}
	ls := strings.TrimLeft(strings.TrimSpace(lm[1]), "0")
	rs := strings.TrimLeft(strings.TrimSpace(rm[1]), "0")
	if ls == "" {
		ls = "0"
	}
	if rs == "" {
		rs = "0"
	}
	if ls == "0" || rs == "0" {
		return "", false
	}
	li, lerr := strconv.Atoi(ls)
	ri, rerr := strconv.Atoi(rs)
	if lerr != nil || rerr != nil {
		return "", false
	}
	if li > ri {
		return "", false
	}
	return ls + "-" + rs, true
}

func detectMovieIndexRangeAndKeyword(tokens []model.Token) (movieIndexRange string, tokenIdx int, hasMovieKeyword bool) {
	for i := 0; i < len(tokens); i++ {
		text := strings.TrimSpace(tokens[i].Text)
		lt := strings.ToLower(text)
		if text == "" {
			continue
		}
		removed := tokens[i].Removed
		if removed && !isMovieKeywordText(text) && lt != "movie" && lt != "film" {
			continue
		}
		if m := movieRangeTokenPattern.FindStringSubmatch(text); len(m) == 3 {
			rng, ok := parseMovieRangeDigits(m[1], m[2])
			if ok {
				return rng, i, true
			}
		}
		if m := movieShortRangePattern.FindStringSubmatch(text); len(m) == 3 {
			if !removed && (hasMovieKeyword || hasMovieContextAround(tokens, i)) {
				rng, ok := parseMovieRangeDigits(m[1], m[2])
				if ok {
					return rng, i, true
				}
			}
		}
		if isMovieKeywordText(text) || lt == "movie" || lt == "film" {
			hasMovieKeyword = true
			j := i + 1
			for j < len(tokens) {
				next := strings.TrimSpace(tokens[j].Text)
				if next == "." || next == "_" || next == "-" {
					j++
					continue
				}
				break
			}
			if j+2 < len(tokens) && isMovieRangeSepToken(tokens[j+1].Text) {
				if rng, ok := parseMovieRangeDigits(tokens[j].Text, tokens[j+2].Text); ok {
					return rng, j, true
				}
			}
		}
	}
	return "", -1, hasMovieKeyword
}

func detectMovieIndexAndKeyword(tokens []model.Token) (movieIndex string, tokenIdx int, hasMovieKeyword bool) {
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		text := strings.TrimSpace(tokens[i].Text)
		lt := strings.ToLower(text)
		if m := filmIndexPattern.FindStringSubmatch(text); len(m) == 2 {
			return strings.TrimLeft(m[1], "0"), i, true
		}
		if m := movieShortIndexPattern.FindStringSubmatch(text); len(m) == 2 {
			if hasMovieKeyword || hasMovieContextAround(tokens, i) {
				return strings.TrimLeft(m[1], "0"), i, true
			}
		}
		if lt == "film" {
			hasMovieKeyword = true
			j := i + 1
			for j < len(tokens) {
				next := strings.TrimSpace(tokens[j].Text)
				if next == "." || next == "_" || next == "-" {
					j++
					continue
				}
				break
			}
			if j < len(tokens) {
				if j+2 < len(tokens) && isMovieRangeSepToken(tokens[j+1].Text) {
					if _, ok := parseMovieRangeDigits(tokens[j].Text, tokens[j+2].Text); ok {
						continue
					}
				}
				next := strings.TrimSpace(tokens[j].Text)
				m := movieIndexPattern.FindStringSubmatch(next)
				if len(m) == 2 {
					return strings.TrimLeft(m[1], "0"), j, true
				}
			}
		}
		if isMovieKeywordText(text) {
			hasMovieKeyword = true
			j := i + 1
			for j < len(tokens) {
				next := strings.TrimSpace(tokens[j].Text)
				if next == "." || next == "_" || next == "-" {
					j++
					continue
				}
				break
			}
			if j < len(tokens) {
				if j+2 < len(tokens) && isMovieRangeSepToken(tokens[j+1].Text) {
					if _, ok := parseMovieRangeDigits(tokens[j].Text, tokens[j+2].Text); ok {
						continue
					}
				}
				m := movieIndexPattern.FindStringSubmatch(tokens[j].Text)
				if len(m) == 2 {
					return strings.TrimLeft(m[1], "0"), j, true
				}
			}
		}
		m := jpMovieIndexPattern.FindStringSubmatch(text)
		if len(m) == 2 {
			return strings.TrimLeft(m[1], "0"), i, true
		}
		if lt == "movie" {
			hasMovieKeyword = true
		}
	}
	return "", -1, hasMovieKeyword
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reIsTailGroupLike1                       = regexp.MustCompile(`^[A-Z0-9]{2,10}$`)
	reDetectSPKind1                          = regexp.MustCompile(`(?i)^0?\d{1,3}\s*(?:&|＆)\s*(sp|ova|oad|ona|ex|ncop|nced|pv)\b`)
	reExtractSeasonRangeAndRemoveFromTokens1 = regexp.MustCompile(`(?i)^s(\d{1,2})$`)
	reExtractSeasonRangeAndRemoveFromTokens2 = regexp.MustCompile(`(?i)^s(\d{1,2})$`)
)
