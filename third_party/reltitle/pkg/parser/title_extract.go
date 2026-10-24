package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// removeBracketsAroundFullyConsumedBlocks removes bracket tokens ( [ ] 【 】 ) whose
// entire inner block has already been consumed by attribute/episode extraction.
// Example: "[08]" and "[WebRip]" leave the inner tokens removed but the bracket
// separators behind; without this cleanup the final token stream keeps stray "[]".
func removeBracketsAroundFullyConsumedBlocks(tokens []model.Token) {
	for i := 0; i < len(tokens); i++ {
		text := tokens[i].Text
		if text != "[" && text != "【" {
			continue
		}
		open := text
		closeTok := "]"
		if open == "【" {
			closeTok = "】"
		}
		depth := 0
		closeIdx := -1
		for j := i; j < len(tokens); j++ {
			t := tokens[j].Text
			if t == open {
				depth++
				continue
			}
			if t == closeTok {
				depth--
				if depth == 0 {
					closeIdx = j
					break
				}
			}
		}
		if closeIdx < 0 {
			// Unclosed bracket: nothing to clean up. Moving i back to closeIdx (-1)
			// would restart the scan from the first token and never terminate.
			continue
		}
		if closeIdx <= i+1 {
			i = closeIdx
			continue
		}
		anyRemoved := false
		allRemoved := true
		for j := i + 1; j < closeIdx; j++ {
			if tokens[j].Removed {
				anyRemoved = true
			} else {
				allRemoved = false
				break
			}
		}
		if anyRemoved && allRemoved {
			tokens[i].Removed = true
			tokens[closeIdx].Removed = true
		}
		i = closeIdx
	}
}

func (p *Parser) extractTitle(tokens []model.Token, episodeIdx int, episodeRangeIdx int, seasonCutIdx int) string {
	start := titleStartAfterLeadingEmptyBracketBlocks(tokens)

	end := len(tokens)
	if start < 0 {
		start = 0
	}
	if start > end {
		start = end
	}
	if episodeRangeIdx >= 0 {
		end = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < end {
		// If episode is embedded at the end of a title token like "...第08集",
		// keep the token in title extraction and strip the suffix later.
		if !embeddedChineseEpisodeSuffixPattern.MatchString(strings.TrimSpace(tokens[episodeIdx].Text)) &&
			shouldUseEpisodeAsTitleCut(tokens, start, episodeIdx) {
			end = episodeIdx
		}
	}
	if seasonCutIdx >= start && seasonCutIdx >= 0 && seasonCutIdx < end {
		end = seasonCutIdx
	}

	dotCount := 0
	for i := start; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		if strings.TrimSpace(tokens[i].Text) == "." {
			dotCount++
		}
	}
	dotMode := dotCount >= 4

	seenTitle := false
	for i := start; i < end; i++ {
		if !seenTitle {
			if tokens[i].Removed {
				continue
			}
			t := strings.TrimSpace(tokens[i].Text)
			if t == "" {
				continue
			}
			if t == "[" || t == "]" || t == "(" || t == ")" || t == "（" || t == "）" {
				continue
			}
			if t == "-" || t == "|" || t == "｜" || t == "/" || t == "／" {
				continue
			}
			if isTitleNoiseToken(t) {
				continue
			}
			seenTitle = true
		} else {
			// After we've started the title, aggressively stop when meta/pack sections begin,
			// so canonical title and derived aliases do not include them.
			if tokens[i].Removed {
				continue
			}
			t := strings.TrimSpace(tokens[i].Text)
			if dotMode {
				if p.looksLikeDotSeparatedMetaCutToken(t) {
					end = i
					for end > start {
						j := end - 1
						for j >= start {
							if tokens[j].Removed {
								j--
								continue
							}
							break
						}
						if j < start {
							break
						}
						pj := strings.TrimSpace(tokens[j].Text)
						if pj == "." || pj == "_" || pj == "-" {
							end = j
							continue
						}
						break
					}
					break
				}
			}
			if looksLikeTitleCutMetaToken(t) {
				end = i
				for j := i - 1; j >= start && j >= 0; j-- {
					if tokens[j].Removed {
						continue
					}
					pj := strings.TrimSpace(tokens[j].Text)
					if pj == "" {
						continue
					}
					if pj == ":" || pj == "：" {
						end = j
					}
					break
				}
				break
			}
		}
		if tokens[i].Text == "[" && !tokens[i].Protected {
			end = i
			break
		}
		if tokens[i].Text == "(" || tokens[i].Text == "（" {
			if looksLikeParenMetaOrTech(tokens, i, end) {
				if ni, ok := removeParenBlockTokens(tokens, i, end); ok {
					i = ni - 1
					continue
				}
			} else if seenTitle {
				openIdx, closeIdx, inner, ok := parseParenBlock(tokens, i, end)
				if ok {
					inner = strings.TrimSpace(inner)
					if inner != "" && !strings.Contains(inner, "※") {
						_, isSearchHint, _ := splitAliasCandidatesFromParenContent(inner)
						next := nextVisibleTokenText(tokens, closeIdx+1, end)
						if isSearchHint || next == "" || next == "[" || next == "|" || next == "｜" {
							end = openIdx
							break
						}
					}
				}
			}
		}
		if tokens[i].Text == "|" || tokens[i].Text == "｜" {
			end = i
			break
		}
	}
	if end < 0 {
		end = 0
	}
	if end > len(tokens) {
		end = len(tokens)
	}
	if start >= end {
		return ""
	}

	parts := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if t == "[" || t == "]" || t == "【" || t == "】" {
			if tokens[i].Protected {
				parts = append(parts, t)
			}
			continue
		}
		if t == "(" || t == ")" || t == "（" || t == "）" {
			continue
		}
		if i == episodeIdx {
			// Strip embedded Chinese episode suffix like "第08集" from title tokens.
			if embeddedChineseEpisodeSuffixPattern.MatchString(t) {
				stripped := embeddedChineseEpisodeSuffixPattern.ReplaceAllString(t, "")
				stripped = strings.TrimSpace(stripped)
				if stripped == "" {
					continue
				}
				t = stripped
			}
		}
		if isTitleNoiseToken(t) {
			continue
		}
		if t == "|" || t == "｜" {
			continue
		}
		if t == "." {
			if dotMode {
				continue
			}
			prev := lastVisibleTokenText(tokens, start, i)
			next := nextVisibleTokenText(tokens, i+1, end)
			if isMostlyLatinToken(prev) && isMostlyLatinToken(next) {
				parts = append(parts, ".")
				continue
			}
			continue
		}
		if len([]rune(t)) == 1 {
			r := []rune(t)[0]
			if ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z') {
				// Merge patterns like "离开 A 级队伍..." -> "离开A级队伍...".
				if i+1 < end && i+1 < len(tokens) && !tokens[i+1].Removed {
					nextRaw := strings.TrimSpace(tokens[i+1].Text)
					if strings.HasPrefix(nextRaw, "级") {
						prevPart := ""
						if len(parts) > 0 {
							prevPart = parts[len(parts)-1]
						}
						if prevPart != "" && hasCJK(prevPart) {
							parts[len(parts)-1] = prevPart + t + nextRaw
							i++
							continue
						}
						parts = append(parts, t+nextRaw)
						i++
						continue
					}
				}
			}
		}
		if t == "-" {
			prev := lastVisibleTokenText(tokens, start, i)
			next := nextVisibleTokenText(tokens, i+1, end)
			if isMostlyLatinToken(prev) && isMostlyLatinToken(next) {
				parts = append(parts, "-")
				continue
			}
			if episodeOfTotalPattern.MatchString(strings.TrimSpace(prev)) && episodeOfTotalPattern.MatchString(strings.TrimSpace(next)) {
				parts = append(parts, "-")
				continue
			}
			continue
		}
		if t == "/" || t == "／" {
			// Drop a trailing lone '/' when the following token is removed/meta.
			next := nextVisibleTokenText(tokens, i+1, end)
			if strings.TrimSpace(next) == "" {
				continue
			}
			parts = append(parts, "/")
			continue
		}
		parts = append(parts, t)
	}

	// Protected square brackets are title punctuation, not release blocks.
	for i := range parts {
		if parts[i] == "[" || parts[i] == "【" {
			if i+1 < len(parts) {
				parts[i+1] = parts[i] + parts[i+1]
				parts[i] = ""
			}
		} else if parts[i] == "]" || parts[i] == "】" {
			if i > 0 {
				parts[i-1] += parts[i]
				parts[i] = ""
			}
		}
	}
	joined := strings.Join(parts, " ")
	joined = strings.Join(strings.Fields(joined), " ")
	out := compactWideBracketSpaces(joined)
	out = normalizeParenSpacing(out)
	out = normalizeTitleMain(out)
	return out
}

func lastVisibleTokenText(tokens []model.Token, start int, idx int) string {
	for j := idx - 1; j >= start && j >= 0; j-- {
		if tokens[j].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[j].Text)
		if t == "" {
			continue
		}
		if t == "[" || t == "]" || t == "(" || t == ")" || t == "（" || t == "）" {
			continue
		}
		if t == "|" || t == "｜" || t == "/" || t == "／" {
			continue
		}
		return t
	}
	return ""
}

func nextVisibleTokenText(tokens []model.Token, idx int, end int) string {
	for i := idx; i < end && i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if t == "[" || t == "]" || t == "(" || t == ")" || t == "（" || t == "）" {
			continue
		}
		if t == "|" || t == "｜" || t == "/" || t == "／" {
			continue
		}
		return t
	}
	return ""
}

func isTitleNoiseToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Only treat as title noise when it's a standalone restriction note, not when glued to a valid title.
	// Examples to drop: "仅限港澳台地区" "（仅限港澳台地区）".
	s2 := strings.Trim(s, "()（）[]【】")
	s2 = strings.TrimSpace(s2)
	if strings.HasPrefix(s2, "仅限") {
		if strings.Contains(s2, "港澳") || strings.Contains(s2, "港澳台") || strings.Contains(s2, "港澳臺") || strings.Contains(s2, "台灣") || strings.Contains(s2, "臺灣") {
			return true
		}
	}
	return false
}

var reSpaceBeforeFullwidthParenOpen = regexp.MustCompile(`([。！？…、，：；])\s+（`)

func normalizeParenSpacing(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "( ", "(")
	s = strings.ReplaceAll(s, "（ ", "（")
	s = strings.ReplaceAll(s, " )", ")")
	s = strings.ReplaceAll(s, " ）", "）")
	s = reSpaceBeforeFullwidthParenOpen.ReplaceAllString(s, "$1（")
	return strings.TrimSpace(s)
}

func removeParenBlockTokens(tokens []model.Token, openIdx int, limit int) (int, bool) {
	if openIdx < 0 || openIdx >= len(tokens) {
		return openIdx, false
	}
	op := tokens[openIdx].Text
	if op != "(" && op != "（" {
		return openIdx, false
	}
	close := ")"
	if op == "（" {
		close = "）"
	}
	end := -1
	depth := 0
	maxJ := limit
	if maxJ > len(tokens) {
		maxJ = len(tokens)
	}
	for j := openIdx; j < maxJ; j++ {
		if tokens[j].Text == op {
			depth++
			continue
		}
		if tokens[j].Text == close {
			if depth > 0 {
				depth--
				if depth == 0 {
					end = j
					break
				}
			}
		}
	}
	if end < 0 {
		return openIdx, false
	}
	for k := openIdx; k <= end; k++ {
		tokens[k].Removed = true
	}
	return end + 1, true
}

func looksLikeParenMetaOrTech(tokens []model.Token, openIdx int, limit int) bool {
	if openIdx < 0 || openIdx >= len(tokens) {
		return false
	}
	op := tokens[openIdx].Text
	if op != "(" && op != "（" {
		return false
	}
	close := ")"
	if op == "（" {
		close = "）"
	}
	end := -1
	depth := 0
	maxJ := limit
	if maxJ > len(tokens) {
		maxJ = len(tokens)
	}
	for j := openIdx; j < maxJ; j++ {
		if tokens[j].Text == op {
			depth++
			continue
		}
		if tokens[j].Text == close {
			if depth > 0 {
				depth--
				if depth == 0 {
					end = j
					break
				}
			}
		}
	}
	if end < 0 {
		return false
	}
	parts := make([]string, 0, end-openIdx)
	for k := openIdx + 1; k < end; k++ {
		if tokens[k].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[k].Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	inner := strings.TrimSpace(strings.Join(parts, " "))
	if inner == "" {
		return false
	}
	if isTitleNoiseToken(inner) {
		return true
	}
	low := strings.ToLower(inner)
	if reMonthNewShow.MatchString(strings.TrimSpace(strings.Trim(inner, "★☆"))) {
		return true
	}
	if strings.Contains(low, "1080p") || strings.Contains(low, "2160p") || strings.Contains(low, "720p") {
		return true
	}
	if strings.Contains(low, "webrip") || strings.Contains(low, "web-dl") || strings.Contains(low, "webdl") || strings.Contains(low, "bluray") {
		return true
	}
	if strings.Contains(low, "hevc") || strings.Contains(low, "x265") || strings.Contains(low, "h265") || strings.Contains(low, "x264") || strings.Contains(low, "h264") {
		return true
	}
	if strings.Contains(low, "mkv") || strings.Contains(low, "mp4") {
		return true
	}
	if reLooksLikeParenMetaOrTech1.FindStringIndex(inner) != nil {
		return true
	}
	if reLooksLikeParenMetaOrTech2.FindStringIndex(inner) != nil {
		return true
	}
	return false
}

func normalizeTitleMain(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Normalize common fullwidth punctuation for stable matching/aliases.
	s = strings.ReplaceAll(s, "：", ":")
	s = strings.ReplaceAll(s, "＆", "&")
	// Remove region restriction notes when they are embedded in title tokens.
	// Keep other parenthetical notes like "（※似乎可行？）".
	reRegion := reNormalizeTitleMain1
	s = reRegion.ReplaceAllString(s, "")
	// Remove embedded audio-version notes that should be structured as audio metadata
	// rather than retained in the canonical title surface.
	reAudioMeta := reNormalizeTitleMain2
	s = reAudioMeta.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	// Compact spaces around hyphen only for very short left parts (e.g. "X - Clamp" -> "X-Clamp").
	reSingleLetterHyphen := reNormalizeTitleMain3
	s = reSingleLetterHyphen.ReplaceAllString(s, "$1-$2")
	// Compact spaces around hyphen for common latin honorific suffixes (e.g. "Kyuuketsuki - chan" -> "Kyuuketsuki-chan").
	// Keep this conservative to avoid merging multi-part titles like "... - The Outcast - ...".
	reLatinSuffixHyphen := reNormalizeTitleMain4
	s = reLatinSuffixHyphen.ReplaceAllString(s, "$1-$2")
	reLatinDot := reNormalizeTitleMain5
	s = reLatinDot.ReplaceAllString(s, "$1.$2")
	reDigitHyphen := reNormalizeTitleMain6
	s = reDigitHyphen.ReplaceAllString(s, "$1-$2")
	s = strings.TrimSpace(s)
	s = normalizeSpacedUnderscore(s)
	if hasCJK(s) && !hasLatin(s) {
		s = reNormalizeTitleMain7.ReplaceAllString(s, "$1")
		s = reNormalizeTitleMain8.ReplaceAllString(s, "$1")
		s = reNormalizeTitleMain9.ReplaceAllString(s, "$1$2")
		s = strings.TrimSpace(s)
		s = strings.TrimRight(s, "。")
		s = strings.TrimSpace(s)
	}
	s = stripFileExtSuffix(s)
	return s
}

func compactWideBracketSpaces(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	repl := strings.NewReplacer(
		" 【", "【",
		"【 ", "【",
		" 】", "】",
		"】 ", "】",
	)
	return strings.TrimSpace(repl.Replace(s))
}

var reSpacedUnderscore = regexp.MustCompile(`\s+_\s+`)
var reTrailingUnderscore = regexp.MustCompile(`\s+_\s*$`)
var reUnderscoreRun = regexp.MustCompile(`_+`)
var reMultiSpace = regexp.MustCompile(`\s+`)

func normalizeSpacedUnderscore(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.Contains(s, "_") {
		return s
	}
	s = reSpacedUnderscore.ReplaceAllString(s, "_")
	s = reTrailingUnderscore.ReplaceAllString(s, "")
	s = reUnderscoreRun.ReplaceAllString(s, " ")
	s = reMultiSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reLooksLikeParenMetaOrTech1 = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)
	reLooksLikeParenMetaOrTech2 = regexp.MustCompile(`(?i)\b(?:s\d{1,2}|season\s*\d{1,2}|cour\s*\d{1,2}|part\s*\d{1,2})\b`)
	reNormalizeTitleMain1       = regexp.MustCompile(`（\s*仅限港澳台地区\s*）|（\s*僅限港澳台地區\s*）|\(\s*仅限港澳台地区\s*\)|\(\s*僅限港澳台地區\s*\)`)
	reNormalizeTitleMain2       = regexp.MustCompile(`（\s*(?:日配版|日语配音版|日語配音版|中配版?|中文配音版?|中文配音|国语配音|國語配音)\s*）|\(\s*(?:(?:Japanese|JPN|JP)\s+(?:Dub|Audio)|(?:Mandarin(?:\s+Chinese)?|Chinese)\s+Dub)\s*\)`)
	reNormalizeTitleMain3       = regexp.MustCompile(`(?i)\b([a-z0-9])\s*-\s*([a-z0-9]{2,})\b`)
	reNormalizeTitleMain4       = regexp.MustCompile(`\b([A-Za-z0-9]{2,})\s*-\s*([a-z]{2,4})\b`)
	reNormalizeTitleMain5       = regexp.MustCompile(`(?i)\b([a-z0-9]{2,})\s*\.\s*([a-z0-9]{2,})\b`)
	reNormalizeTitleMain6       = regexp.MustCompile(`\b(\d{1,4})\s*-\s*(\d{1,4})\b`)
	reNormalizeTitleMain7       = regexp.MustCompile(`([【（「『《〈])\s+`)
	reNormalizeTitleMain8       = regexp.MustCompile(`\s+([】）」』》〉])`)
	reNormalizeTitleMain9       = regexp.MustCompile(`([】）」』》〉])\s+([\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}])`)
)

func extractSearchHintIndexAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		text := strings.TrimSpace(tokens[i].Text)
		if text == "" {
			continue
		}
		if !strings.Contains(text, "检索用") {
			continue
		}
		if !strings.Contains(text, "(") && !strings.Contains(text, "（") {
			continue
		}

		end := i
		for j := i; j < len(tokens); j++ {
			if tokens[j].Removed {
				continue
			}
			end = j
			tj := strings.TrimSpace(tokens[j].Text)
			if strings.Contains(tj, ")") || strings.Contains(tj, "）") {
				break
			}
		}

		for k := i; k <= end; k++ {
			tokens[k].Removed = true
		}
		*out = append(*out, model.Attribute{Type: model.AttrNote, Normalized: "检索用", TokenStart: i, TokenEnd: end + 1, Confidence: 0.7, Source: model.AttrSourceHeuristic})
		i = end
	}
}

func extractSeasonShowTagAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if !isInBracket(tokens, i) {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if !yearMonthShowTagPattern.MatchString(t) {
			continue
		}
		tokens[i].Removed = true
		*out = append(*out, model.Attribute{Type: model.AttrNote, Normalized: t, TokenStart: i, TokenEnd: i + 1, Confidence: 0.7, Source: model.AttrSourceHeuristic})
	}
}

var yearMonthShowTagPattern = regexp.MustCompile(`^(?:\d{4}年)?\d{1,2}月番$`)
