package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

var bracketStandaloneMIndexPattern = regexp.MustCompile(`(?i)^m0*(\d{1,3})$`)
var bracketStandaloneMovieIndexPattern = regexp.MustCompile(`(?i)^movie\s*0*(\d{1,3})$`)
var bracketStandaloneNumericIndexPattern = regexp.MustCompile(`^0*(\d{1,3})$`)
var bracketInlineMovieRangePattern = regexp.MustCompile(`(?i)\b(?:movie|film)\s*0*\d{1,3}\s*[-~+]\s*0*\d{1,3}\b`)
var bracketInlineMovieIndexPattern = regexp.MustCompile(`(?i)\b(?:movie|film)\s*0*(\d{1,3})\b`)
var bracketInlineMovieRangeCapturePattern = regexp.MustCompile(`(?i)\b(?:movie|film)\s*0*(\d{1,3})\s*[-~]\s*0*(\d{1,3})\b`)

// extractBracketTitleAliasesAndRemoveFromTokens scans the leading bracket blocks
// before the tech section: the blocks become the main title and alias candidates,
// and the consumed tokens are marked Removed. Returns the main title, aliases,
// and the token span of the main title block.
func extractBracketTitleAliasesAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute, aliasValid func(string) bool) (string, []string, int, int) {
	if len(tokens) == 0 {
		return "", nil, -1, -1
	}
	if attrs == nil {
		return "", nil, -1, -1
	}

	groupText := ""
	bestGroupConf := -1.0
	for _, a := range *attrs {
		if a.Type != model.AttrGroup {
			continue
		}
		g := strings.TrimSpace(a.Normalized)
		if g == "" {
			continue
		}
		if a.Confidence > bestGroupConf {
			bestGroupConf = a.Confidence
			groupText = g
		}
	}

	techStart, hasTechStart := firstTechTokenIndexFromAttrs(*attrs)
	if !hasTechStart {
		techStart = len(tokens)
	}
	if techStart < 0 {
		techStart = len(tokens)
	}
	if techStart > len(tokens) {
		techStart = len(tokens)
	}

	start := titleStartAfterLeadingEmptyBracketBlocks(tokens)
	if start < 0 {
		start = 0
	}
	if start-1 >= 0 {
		prev := strings.TrimSpace(tokens[start-1].Text)
		if prev == "[" || prev == "【" || prev == "(" || prev == "（" {
			start = start - 1
		}
	}
	if start >= techStart {
		start = 0
	}

	searchHintAliases := findBracketSearchHintAliases(tokens, attrs)

	blocks := scanBracketTitleBlocks(tokens, attrs, start, techStart, groupText)
	if len(blocks) < 2 {
		return "", searchHintAliases, -1, -1
	}
	return selectBracketMainAndAliases(tokens, attrs, aliasValid, blocks, searchHintAliases)
}

// bracketAliasCompactKey folds a string for comparison: lowercase, no spaces,
// no "&", "-" or "_". Used to tell the group block apart from title blocks.
func bracketAliasCompactKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "&", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	return s
}

func normalizeBracketDigitRange(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = reExtractBracketTitleAliasesAndRemoveFromTokens1.ReplaceAllString(s, "$1-$2")
	s = reExtractBracketTitleAliasesAndRemoveFromTokens2.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func parseBracketStandaloneMovieIndex(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if strings.ContainsAny(s, "+-/~") {
		return "", false
	}
	if m := bracketStandaloneMIndexPattern.FindStringSubmatch(s); len(m) == 2 {
		n := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
		if n == "" {
			n = "0"
		}
		return n, true
	}
	if m := bracketStandaloneMovieIndexPattern.FindStringSubmatch(s); len(m) == 2 {
		n := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
		if n == "" {
			n = "0"
		}
		return n, true
	}
	return "", false
}

func parseBracketInlineMovieIndex(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if bracketInlineMovieRangePattern.MatchString(s) {
		return "", false
	}
	if m := bracketInlineMovieIndexPattern.FindStringSubmatch(s); len(m) == 2 {
		n := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
		if n == "" {
			n = "0"
		}
		if n != "0" {
			return n, true
		}
	}
	return "", false
}

func parseBracketInlineMovieIndexRange(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	m := bracketInlineMovieRangeCapturePattern.FindStringSubmatch(s)
	if len(m) != 3 {
		return "", false
	}
	left, ok := parseMovieRangeDigits(m[1], m[2])
	if !ok {
		return "", false
	}
	return left, true
}

func parseBracketContextualNumericMovieIndex(s string, hasMovieContext bool) (string, bool) {
	if !hasMovieContext {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if strings.ContainsAny(s, "+-/~") {
		return "", false
	}
	if m := bracketStandaloneNumericIndexPattern.FindStringSubmatch(s); len(m) == 2 {
		n := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
		if n == "" {
			n = "0"
		}
		if n != "0" {
			return n, true
		}
	}
	return "", false
}

func hasBracketMovieKeyword(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if bracketInlineMovieIndexPattern.FindStringIndex(s) != nil {
		return true
	}
	if strings.Contains(s, "劇場版") || strings.Contains(s, "剧场版") || strings.Contains(s, "映画") || strings.Contains(s, "大电影") || strings.Contains(s, "大電影") {
		return true
	}
	return false
}

func bracketContainsEpisodeMarker(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens3.MatchString(s) {
		return true
	}
	// Batch range forms should stay in token stream for episode_range detection.
	// Examples: "01-24TV全集", "01-12 全集", "01-12FIN", "01-12TV全集+SP+特典映像".
	if reExtractBracketTitleAliasesAndRemoveFromTokens4.MatchString(s) {
		return true
	}
	// Multi-episode list forms: "804&805", "07+08".
	if reExtractBracketTitleAliasesAndRemoveFromTokens5.MatchString(s) {
		return true
	}
	// Any embedded episode marker like "第687集" (even with extra words) should be kept
	// for episode detection and should not be used as main title/alias.
	if reExtractBracketTitleAliasesAndRemoveFromTokens6.FindStringIndex(s) != nil {
		return true
	}
	// Summary/special blocks like "总集篇01_12.5" should be treated as episode markers.
	// Tokenizer may split it into "总集篇01_12 . 5".
	if reExtractBracketTitleAliasesAndRemoveFromTokens7.FindStringIndex(s) != nil {
		return true
	}
	// Dual episode markers like "01(73)" / "S01E01(13)" / "01-10(17-26)" should stay
	// in the token stream for later EP adjudication.
	if reExtractBracketTitleAliasesAndRemoveFromTokens8.MatchString(s) {
		return true
	}
	// Tagged TV ranges like "TV01-25Fin" should be treated as episode/range markers.
	if reExtractBracketTitleAliasesAndRemoveFromTokens9.FindStringIndex(s) != nil {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens10.FindStringIndex(s) != nil {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens11.FindStringIndex(s) != nil {
		return true
	}
	return false
}

func isLikelyBracketTechLike(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if low == "" {
		return false
	}
	if hasCJK(s) || hasLatin(s) {
	} else {
		if isLikelyMetaBracketText(s) {
			return true
		}
	}
	if strings.Contains(low, "webrip") || strings.Contains(low, "web-dl") || strings.Contains(low, "webdl") {
		return true
	}
	if strings.Contains(low, "hevc") || strings.Contains(low, "x265") || strings.Contains(low, "h265") || strings.Contains(low, "x264") || strings.Contains(low, "h264") || strings.Contains(low, "avc") {
		return true
	}
	if strings.Contains(low, "8bit") || strings.Contains(low, "10bit") || strings.Contains(low, "bit") {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens12.MatchString(s) {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens13.MatchString(s) {
		return true
	}
	return false
}

func looksLikeBracketEpOnly(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens14.MatchString(s) {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens15.MatchString(s) {
		return true
	}
	// Composite EP + special index like "07+ES07" / "07 + ES07".
	if reExtractBracketTitleAliasesAndRemoveFromTokens16.MatchString(s) {
		return true
	}
	// Standalone special index like "ES07".
	if reExtractBracketTitleAliasesAndRemoveFromTokens17.MatchString(s) {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens18.MatchString(s) {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens19.MatchString(s) {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens20.MatchString(s) {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens21.MatchString(s) {
		return true
	}
	if episodeOfTotalPattern.MatchString(s) {
		return true
	}
	if episodeRangePattern.MatchString(normalizeBracketDigitRange(s)) {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens22.MatchString(s) {
		return true
	}
	if reExtractBracketTitleAliasesAndRemoveFromTokens23.MatchString(s) {
		return true
	}
	// Patterns like "01_标题" are often episode markers. Keep this strict (underscore only)
	// to avoid misclassifying titles like "86 - 不存在的战区".
	if m := reExtractBracketTitleAliasesAndRemoveFromTokens24.FindStringSubmatch(s); len(m) == 3 {
		n := strings.TrimLeft(m[1], "0")
		if n == "" {
			n = "0"
		}
		if n != "0" {
			if v := reExtractBracketTitleAliasesAndRemoveFromTokens25.FindString(n); v != "" {
				if hasCJK(m[2]) {
					return true
				}
			}
		}
	}
	return false
}

// findBracketSearchHintAliases extracts the aliases listed after a search-hint
// keyword (检索用: / search: / ...), records the hint keyword as a note
// attribute, and marks the hint span consumed.
func findBracketSearchHintAliases(tokens []model.Token, attrs *[]model.Attribute) []string {
	out := make([]string, 0, 6)
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if !strings.Contains(t, "检索") && !strings.Contains(t, "檢索") && !strings.Contains(t, "索引") && !strings.Contains(t, "搜索") && !strings.Contains(t, "搜尋") && !strings.Contains(strings.ToLower(t), "search") {
			continue
		}

		if i+2 < len(tokens) {
			sep := strings.TrimSpace(tokens[i+1].Text)
			if sep == ":" || sep == "：" {
				end := len(tokens)
				inParen := isInParen(tokens, i)
				if inParen {
					depth := 0
					for j := i; j < len(tokens); j++ {
						if tokens[j].Text == "(" || tokens[j].Text == "（" {
							depth++
							continue
						}
						if tokens[j].Text == ")" || tokens[j].Text == "）" {
							if depth == 0 {
								end = j
								break
							}
							depth--
						}
					}
				} else {
					depth := 0
					for j := i; j < len(tokens); j++ {
						if tokens[j].Text == "[" || tokens[j].Text == "【" {
							depth++
							continue
						}
						if tokens[j].Text == "]" || tokens[j].Text == "】" {
							if depth == 0 {
								end = j
								break
							}
							depth--
						}
					}
				}

				parts := make([]string, 0, 16)
				for j := i + 2; j < end && j < len(tokens); j++ {
					if tokens[j].Removed {
						continue
					}
					p := strings.TrimSpace(tokens[j].Text)
					if p == "" {
						continue
					}
					if p == "(" || p == ")" || p == "（" || p == "）" || p == "[" || p == "]" || p == "【" || p == "】" {
						continue
					}
					parts = append(parts, p)
				}
				cand := strings.TrimSpace(strings.Join(parts, " "))
				cand = strings.Trim(cand, "()（）")
				cand = strings.TrimSpace(cand)
				if cand != "" {
					cands, isHint, hintKeyword := splitAliasCandidatesFromParenContent(strings.TrimSpace(t + ":" + cand))
					if isHint {
						hk := strings.TrimSpace(hintKeyword)
						if hk != "" {
							*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: hk, Confidence: 0.6, Source: model.AttrSourceHintKeyword})
						}
						for _, a := range cands {
							a = cleanAliasToken(a)
							if a == "" {
								continue
							}
							compact := strings.ReplaceAll(a, " ", "")
							if len([]rune(compact)) < 4 {
								continue
							}
							if isLikelyMetaBracketText(a) {
								continue
							}
							if !hasCJK(a) && len([]rune(a)) < 3 {
								continue
							}
							if len([]rune(a)) > 400 {
								continue
							}
							out = append(out, a)
						}
						openIdx := i
						if inParen {
							depth := 0
							for j := i - 1; j >= 0; j-- {
								tj := tokens[j].Text
								if tj == ")" || tj == "）" {
									depth++
									continue
								}
								if tj == "(" || tj == "（" {
									if depth == 0 {
										openIdx = j
										break
									}
									depth--
								}
							}
						} else {
							depth := 0
							for j := i - 1; j >= 0; j-- {
								tj := tokens[j].Text
								if tj == "]" || tj == "】" {
									depth++
									continue
								}
								if tj == "[" || tj == "【" {
									if depth == 0 {
										openIdx = j
										break
									}
									depth--
								}
							}
						}
						for j := openIdx; j <= end && j >= 0 && j < len(tokens); j++ {
							tokens[j].Removed = true
						}
						continue
					}
				}
			}
		}

		if idx := strings.IndexAny(t, ":："); idx >= 0 {
			inner := strings.TrimSpace(t[idx+1:])
			inner = strings.Trim(inner, "()（）")
			inner = strings.TrimSpace(inner)
			if inner == "" {
				continue
			}
			cands, isHint, hintKeyword := splitAliasCandidatesFromParenContent(strings.TrimSpace(t[:idx] + ":" + inner))
			if isHint {
				hk := strings.TrimSpace(hintKeyword)
				if hk != "" {
					*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: hk, Confidence: 0.6, Source: model.AttrSourceHintKeyword})
				}
				for _, a := range cands {
					a = cleanAliasToken(a)
					if a == "" {
						continue
					}
					if isLikelyMetaBracketText(a) {
						continue
					}
					if !hasCJK(a) && len([]rune(a)) < 3 {
						continue
					}
					if len([]rune(a)) > 400 {
						continue
					}
					out = append(out, a)
				}
				tokens[i].Removed = true
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// bracketTitleBlock is one leading bracket block: its token span and inner text.
type bracketTitleBlock struct {
	open  int
	close int
	inner string
}

// scanBracketTitleBlocks collects the leading bracket blocks that are title
// candidates, skipping blocks that are the group, meta text, episode markers,
// SP kinds, or tech-like. Movie index blocks emit attributes and are consumed
// instead of becoming title candidates.
func scanBracketTitleBlocks(tokens []model.Token, attrs *[]model.Attribute, start, techStart int, groupText string) []bracketTitleBlock {
	blocks := make([]bracketTitleBlock, 0, 4)
	for i := start; i < techStart && i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		open, close, ok := headAliasBracketPair(tokens[i].Text)
		if !ok {
			break
		}
		end := -1
		depth := 0
		for j := i; j < techStart && j < len(tokens); j++ {
			if tokens[j].Text == open {
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
			break
		}
		parts := make([]string, 0, end-i)
		partsRaw := make([]string, 0, end-i)
		for k := i + 1; k < end; k++ {
			p := strings.TrimSpace(tokens[k].Text)
			if p == "" {
				continue
			}
			if p == "[" || p == "]" || p == "【" || p == "】" {
				continue
			}
			partsRaw = append(partsRaw, p)
			if tokens[k].Removed {
				continue
			}
			parts = append(parts, p)
		}
		inner := strings.TrimSpace(strings.Join(parts, " "))
		inner = normalizeBracketDigitRange(inner)
		innerRaw := strings.TrimSpace(strings.Join(partsRaw, " "))
		innerRaw = normalizeBracketDigitRange(innerRaw)
		if inner == "" {
			i = end
			continue
		}
		if bracketAliasCompactKey(inner) != "" && bracketAliasCompactKey(groupText) != "" {
			if bracketAliasCompactKey(inner) == bracketAliasCompactKey(groupText) {
				i = end
				continue
			}
		}
		if mi, ok := parseBracketInlineMovieIndex(innerRaw); ok {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrMovieIndex,
				Normalized: mi,
				TokenStart: i + 1,
				TokenEnd:   end,
				Confidence: 0.72,
				Source:     model.AttrSourceBracketInlineMovieIndex,
			})
		}
		if mr, ok := parseBracketInlineMovieIndexRange(innerRaw); ok {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrMovieIndexRange,
				Normalized: mr,
				TokenStart: i + 1,
				TokenEnd:   end,
				Confidence: 0.72,
				Source:     model.AttrSourceBracketInlineMovieIndexRange,
			})
		}
		if looksLikeLeadingTitleMeta(inner) {
			i = end
			continue
		}
		if isLikelyMetaBracketText(inner) {
			i = end
			continue
		}
		if bracketContainsEpisodeMarker(inner) || bracketContainsEpisodeMarker(innerRaw) || isUnderscoreDualEpisodeBracket(tokens, i, end) {
			// Keep this block in tokens for episode detection. Do not consume it
			// into title/alias candidates.
			i = end
			continue
		}
		compactSP := reExtractBracketTitleAliasesAndRemoveFromTokens26.ReplaceAllString(inner, "")
		if compactSP != "" {
			if m := spKindPattern.FindStringSubmatch(compactSP); len(m) >= 2 {
				i = end
				continue
			}
		}
		if mi, ok := parseBracketStandaloneMovieIndex(innerRaw); ok {
			// [M21] / [Movie 21] after title blocks should be treated as movie_index.
			// Keep title/alias extraction deterministic and avoid leaking Mxx as alias.
			if len(blocks) > 0 {
				*attrs = append(*attrs, model.Attribute{
					Type:       model.AttrMovieIndex,
					Normalized: mi,
					TokenStart: i + 1,
					TokenEnd:   end,
					Confidence: 0.8,
					Source:     model.AttrSourceBracketMIndex,
				})
				for k := i; k <= end && k >= 0 && k < len(tokens); k++ {
					tokens[k].Removed = true
				}
				i = end
				continue
			}
		}
		hasMovieContext := false
		for bi := 0; bi < len(blocks); bi++ {
			if hasBracketMovieKeyword(blocks[bi].inner) {
				hasMovieContext = true
				break
			}
		}
		if !hasMovieContext {
			for _, a := range *attrs {
				if a.Type != model.AttrNote && a.Type != model.AttrTitle {
					continue
				}
				if hasBracketMovieKeyword(a.Normalized) {
					hasMovieContext = true
					break
				}
				if strings.EqualFold(strings.TrimSpace(a.Normalized), "MOVIE") || strings.EqualFold(strings.TrimSpace(a.Normalized), "THEMOVIE") {
					hasMovieContext = true
					break
				}
			}
		}
		if mi, ok := parseBracketContextualNumericMovieIndex(innerRaw, hasMovieContext); ok {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrMovieIndex,
				Normalized: mi,
				TokenStart: i + 1,
				TokenEnd:   end,
				Confidence: 0.72,
				Source:     model.AttrSourceBracketMovieContextIndex,
			})
			for k := i; k <= end && k >= 0 && k < len(tokens); k++ {
				tokens[k].Removed = true
			}
			i = end
			continue
		}
		if isLikelyBracketTechLike(inner) {
			break
		}
		if looksLikeBracketEpOnly(inner) || looksLikeBracketEpOnly(innerRaw) {
			if len(blocks) == 0 {
				i = end
				continue
			}
			break
		}
		blocks = append(blocks, bracketTitleBlock{open: i, close: end, inner: inner})
		i = end
	}
	return blocks
}

// selectBracketMainAndAliases scores the collected title blocks (CJK-heavy and
// longer wins), records explicit season numbers as note attributes, cleans and
// dedupes the remaining blocks into aliases, and marks all blocks consumed.
func selectBracketMainAndAliases(tokens []model.Token, attrs *[]model.Attribute, aliasValid func(string) bool, blocks []bracketTitleBlock, searchHintAliases []string) (string, []string, int, int) {
	mainIdx := 0
	bestScore := -1
	for i := range blocks {
		cand := blocks[i].inner
		score := 0
		if hasCJK(cand) {
			score += 50
		}
		if hasLatin(cand) {
			score += 10
		}
		l := len([]rune(cand))
		score += l
		if score > bestScore {
			bestScore = score
			mainIdx = i
		}
	}

	mainTitle := normalizeTitleMain(blocks[mainIdx].inner)
	if mainTitle == "" {
		return "", searchHintAliases, -1, -1
	}
	if s := extractExplicitSeasonNumber(blocks[mainIdx].inner); s != "" {
		*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: "S" + s, TokenStart: -1, TokenEnd: -1, Confidence: 0.65, Source: model.AttrSourceMainTitleSeason})
	}
	aliases := make([]string, 0, len(blocks))
	seen := map[string]bool{}
	seen[strings.ToLower(strings.TrimSpace(mainTitle))] = true
	for i := range blocks {
		if i == mainIdx {
			continue
		}
		// Extract season from the raw alias block before cleanAliasToken/stripExplicitSeasonSuffix drops it.
		if s := extractExplicitSeasonNumber(blocks[i].inner); s != "" {
			*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: "S" + s, TokenStart: -1, TokenEnd: -1, Confidence: 0.65, Source: model.AttrSourceAliasSeason})
		}
		a := cleanAliasToken(blocks[i].inner)
		if stripped := stripExplicitSeasonSuffix(a); stripped != "" {
			a = stripped
		}
		if !aliasValid(a) {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(a))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		aliases = append(aliases, a)
	}
	aliases = mergeAliases(aliases, searchHintAliases)

	mainStart := blocks[mainIdx].open + 1
	mainEnd := blocks[mainIdx].close
	for _, b := range blocks {
		for k := b.open; k <= b.close && k >= 0 && k < len(tokens); k++ {
			tokens[k].Removed = true
		}
	}

	if len(aliases) == 0 {
		return mainTitle, nil, mainStart, mainEnd
	}
	return mainTitle, aliases, mainStart, mainEnd
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reExtractBracketTitleAliasesAndRemoveFromTokens1  = regexp.MustCompile(`\b(\d{1,4})\s*-\s*(\d{1,4})\b`)
	reExtractBracketTitleAliasesAndRemoveFromTokens2  = regexp.MustCompile(`\s+`)
	reExtractBracketTitleAliasesAndRemoveFromTokens3  = regexp.MustCompile(`(?i)^0?\d{1,4}(?:\s|[_-])*(?:end|fin|final)$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens4  = regexp.MustCompile(`(?i)^0?\d{1,4}\s*[-~]\s*0?\d{1,4}(?:\.\d{1,2})?.*$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens5  = regexp.MustCompile(`(?i)^\d{1,4}(?:\s*(?:&|＆|\+|＋|/|／|,|，|、)\s*\d{1,4})+$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens6  = regexp.MustCompile(`第\d{1,4}(?:集|话|話)`)
	reExtractBracketTitleAliasesAndRemoveFromTokens7  = regexp.MustCompile(`总集篇\s*0*\d{1,3}\s*[_\s-]+\s*\d{1,4}(?:\s*\.\s*\d{1,2})?`)
	reExtractBracketTitleAliasesAndRemoveFromTokens8  = regexp.MustCompile(`(?i)^(?:S\d{1,2}E)?\d{1,4}(?:\s*[-~]\s*\d{1,4})?\s*[\(（]\s*(?:S\d{1,2}E)?\d{1,4}(?:\s*[-~]\s*\d{1,4})?\s*[\)）]$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens9  = regexp.MustCompile(`(?i)\bTV\s*0*\d{1,4}\s*[-~]\s*\d{1,4}\s*(?:FIN|FINAL)?\b`)
	reExtractBracketTitleAliasesAndRemoveFromTokens10 = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,4}\b`)
	reExtractBracketTitleAliasesAndRemoveFromTokens11 = regexp.MustCompile(`(?i)\b(?:ep|e)\s*\d{1,4}(?:\.\d{1,2})?\b`)
	reExtractBracketTitleAliasesAndRemoveFromTokens12 = regexp.MustCompile(`(?i)\b(?:v\d{1,2}|ver\.?\d{1,2}|rev\d{1,2}|repack|proper|rerip|remaster)\b`)
	reExtractBracketTitleAliasesAndRemoveFromTokens13 = regexp.MustCompile(`(?i)\b\d{3,4}p\b`)
	reExtractBracketTitleAliasesAndRemoveFromTokens14 = regexp.MustCompile(`(?i)^0?\d{1,4}(?:\s|[_-])*(?:end|fin|final)$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens15 = regexp.MustCompile(`(?i)^\d{1,4}(?:\s*(?:&|＆|\+|＋|/|／|,|，|、)\s*\d{1,4})+$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens16 = regexp.MustCompile(`(?i)^\d{1,4}\s*\+\s*(?:es|sp|ova|oad|ona|ex)0*\d{1,3}$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens17 = regexp.MustCompile(`(?i)^(?:es|sp|ova|oad|ona|ex)0*\d{1,3}$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens18 = regexp.MustCompile(`(?i)^0?\d{1,4}[vV]\d{1,2}$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens19 = regexp.MustCompile(`(?i)^(?:S\d{1,2}E)?\d{1,4}(?:\s*[-~]\s*\d{1,4})?\s*[\(（]\s*(?:S\d{1,2}E)?\d{1,4}(?:\s*[-~]\s*\d{1,4})?\s*[\)）]$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens20 = regexp.MustCompile(`(?i)^第?\d{1,4}(?:\s*-\s*\d{1,4})?(?:集|话|話|回|章)(?:\s|$)`)
	reExtractBracketTitleAliasesAndRemoveFromTokens21 = regexp.MustCompile(`^\d{1,4}$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens22 = regexp.MustCompile(`(?i)^ep\s*\d{1,4}$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens23 = regexp.MustCompile(`(?i)^0?\d{1,4}\s*(?:&|＆)\s*(?:sp|ova|oad|oav|pv|op|ed)(?:\s|$)`)
	reExtractBracketTitleAliasesAndRemoveFromTokens24 = regexp.MustCompile(`^0?(\d{1,4})_+(.+)$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens25 = regexp.MustCompile(`^\d+$`)
	reExtractBracketTitleAliasesAndRemoveFromTokens26 = regexp.MustCompile(`[^0-9A-Za-z]+`)
)
