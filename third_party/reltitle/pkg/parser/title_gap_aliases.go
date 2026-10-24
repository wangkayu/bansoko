package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func hasEmbeddedSeasonMarkerToken(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return false
	}
	if isSingleSlashSeasonSignalToken(ss) {
		return true
	}
	if reExplicitSeasonMarkerCJK.MatchString(ss) {
		return true
	}
	if titleGapEmbeddedSeasonTokenPattern.MatchString(ss) {
		return true
	}
	return false
}

func hasSemanticTokenAroundDelimiter(tokens []model.Token, start int, end int, step int) bool {
	if step == 0 {
		return false
	}
	for i := start; ; i += step {
		if step > 0 {
			if i >= end {
				break
			}
		} else {
			if i < end {
				break
			}
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" || isAliasChainDelimiterToken(txt) {
			continue
		}
		switch txt {
		case "-", "_", "~", ":", "：", "·", "•", "☆", "★", ".", "。":
			continue
		}
		return aliasChainSemanticKey(txt) != ""
	}
	return false
}

func (p *Parser) consumeGroupSpecificTitleChainDelimiters(tokens []model.Token, groupSpan [2]int, group string, episodeIdx int, episodeRangeIdx int, attrs []model.Attribute) {
	if len(tokens) == 0 {
		return
	}
	groupKey := strings.ToLower(strings.TrimSpace(group))
	if !strings.Contains(groupKey, "jibaketa") {
		return
	}
	scanStart := titleStartAfterLeadingEmptyBracketBlocks(tokens)
	if len(groupSpan) == 2 && groupSpan[1] > scanStart {
		scanStart = groupSpan[1]
	}
	if scanStart < 0 {
		scanStart = 0
	}
	scanEnd := len(tokens)
	if episodeRangeIdx >= scanStart && episodeRangeIdx < scanEnd {
		scanEnd = episodeRangeIdx
	}
	if episodeIdx >= scanStart && episodeIdx < scanEnd {
		scanEnd = episodeIdx
	}
	if techStart, ok := firstTechTokenIndexFromAttrs(attrs); ok && techStart >= scanStart && techStart < scanEnd {
		scanEnd = techStart
	}
	if scanStart >= scanEnd {
		return
	}
	for i := scanStart; i < scanEnd; i++ {
		if tokens[i].Removed || isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		if strings.TrimSpace(tokens[i].Text) != "/" && strings.TrimSpace(tokens[i].Text) != "／" {
			continue
		}
		leftOK := hasSemanticTokenAroundDelimiter(tokens, i-1, scanStart, -1)
		rightOK := hasSemanticTokenAroundDelimiter(tokens, i+1, scanEnd, 1)
		if leftOK && rightOK {
			tokens[i].Removed = true
		}
	}
}

func (p *Parser) extractPreEpisodePipeAliasesAndConsume(tokens []model.Token, start int, end int) (string, []string, bool) {
	if p == nil || len(tokens) == 0 {
		return "", nil, false
	}
	if start < 0 {
		start = 0
	}
	if end > len(tokens) {
		end = len(tokens)
	}
	if start >= end {
		return "", nil, false
	}

	firstDelim := -1
	pipeCount := 0
	slashCount := 0
	for i := start; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		// A protected token is part of a known title ("乱马1/2", "22/7"), not a delimiter.
		if isAliasChainDelimiterToken(t) && !tokens[i].Protected {
			if firstDelim < 0 {
				firstDelim = i
			}
			if t == "|" || t == "｜" {
				pipeCount++
			} else {
				slashCount++
			}
			continue
		}
	}
	if firstDelim < 0 {
		return "", nil, false
	}
	// Safe trigger:
	// - pipe chain: single pipe is enough
	// - slash chain: require at least two separators to avoid treating regular "Fate/Zero" as alias split
	if pipeCount == 0 && slashCount < 2 {
		return "", nil, false
	}

	scanEnd := end
	for i := firstDelim + 1; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "[" || t == "【" {
			scanEnd = i
			break
		}
	}
	if scanEnd <= start {
		return "", nil, false
	}

	type seg struct {
		text      string
		tokenFrom int
		tokenTo   int
	}
	segments := make([]seg, 0, 4)
	delimCount := 0
	segStart := start
	for i := start; i <= scanEnd; i++ {
		isSplit := i == scanEnd
		if !isSplit {
			t := strings.TrimSpace(tokens[i].Text)
			if isAliasChainDelimiterToken(t) && !tokens[i].Protected {
				isSplit = true
				delimCount++
			}
		}
		if !isSplit {
			continue
		}
		if segStart < i {
			txt := extractTitleFromSpanAllowRemoved(tokens, segStart, i, false)
			txt = strings.TrimSpace(txt)
			if txt != "" {
				segments = append(segments, seg{text: txt, tokenFrom: segStart, tokenTo: i})
			}
		}
		segStart = i + 1
	}
	if delimCount == 0 || len(segments) < 2 {
		return "", nil, false
	}

	rawParts := make([]string, 0, len(segments))
	for _, s := range segments {
		rawParts = append(rawParts, s.text)
	}
	main, aliases := inferTitleAndAliasesFromTitleCanonical(strings.Join(rawParts, " / "), p.isValidAliasToken)
	main = normalizeTitleMain(cleanTitleToken(main))
	if strings.TrimSpace(main) == "" || len(aliases) == 0 {
		return "", nil, false
	}
	mainKey := strings.ToLower(strings.TrimSpace(main))
	mainSegIdx := -1
	mainSegScore := -1
	for i, s := range segments {
		key := strings.ToLower(strings.TrimSpace(normalizeTitleMain(cleanTitleToken(s.text))))
		if key == "" {
			continue
		}
		score := -1
		if key == mainKey {
			score = len(key) + 100
		} else if strings.Contains(key, mainKey) {
			score = len(mainKey)
		} else if strings.Contains(mainKey, key) {
			score = len(key)
		}
		if score > mainSegScore {
			mainSegScore = score
			mainSegIdx = i
		}
	}

	for i := firstDelim; i < scanEnd; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if isAliasChainDelimiterToken(t) && !tokens[i].Protected {
			tokens[i].Removed = true
		}
	}
	if mainSegIdx < 0 {
		return main, aliases, true
	}
	for i := 0; i < len(segments); i++ {
		if i == mainSegIdx {
			continue
		}
		from := segments[i].tokenFrom
		to := segments[i].tokenTo
		if from < 0 {
			from = 0
		}
		if to > len(tokens) {
			to = len(tokens)
		}
		for k := from; k < to; k++ {
			tokens[k].Removed = true
		}
	}

	return main, aliases, true
}

var singleSlashAliasSeasonSignalPattern = regexp.MustCompile(`(?i)^(?:s\d{1,2}|p\d{1,2}|season|saison|part|cour|\d{1,2}(?:st|nd|rd|th))$`)
var singleSlashAliasSeasonSignalCJKPattern = regexp.MustCompile(`^第?[一二三四五六七八九十0-9]{1,3}(?:季|期|部)$`)
var singleSlashAliasSeasonPhrasePattern = regexp.MustCompile(`(?i)^season\s*\d{1,2}$`)
var singleSlashAliasEmbeddedSeasonCJKPattern = regexp.MustCompile(`^第?[一二三四五六七八九十0-9]{1,3}(?:季|期|部).*$`)
var singleSlashAliasBridgeTokenPattern = regexp.MustCompile(`(?i)^(?:special|ova|oad|ona|sp|dub|audio|dual|sub|subs|subtitle|uncut|version|ver|vol|chapter|box|anime|mini|gekijou|theater)$`)
var titleGapLatinAliasTokenPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9'.’\-]*$`)
var titleGapEmbeddedSeasonTokenPattern = regexp.MustCompile(`(?i)\b(?:season|saison|part|cour)\s*\d{1,2}\b|\bs\d{1,2}\b`)

func (p *Parser) inferTitleGapSingleSlashAlias(tokens []model.Token, title string, titleEnd int, episodeIdx int, episodeRangeIdx int) []string {
	if len(tokens) == 0 || titleEnd < 0 || titleEnd >= len(tokens) {
		return nil
	}
	end := len(tokens)
	if episodeRangeIdx >= 0 && episodeRangeIdx < end {
		end = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < end {
		end = episodeIdx
	}
	if titleEnd >= end {
		return nil
	}
	slashIdx := -1
	for i := titleEnd; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "/" || t == "／" {
			if slashIdx >= 0 {
				// multi-slash chains are handled by existing alias chain path.
				return nil
			}
			slashIdx = i
		}
	}
	if slashIdx < 0 || slashIdx+1 >= end {
		return nil
	}
	if !hasSingleSlashBridgeSignal(tokens, titleEnd, end, slashIdx) {
		return nil
	}

	rightRaw := extractTitleFromSpanAllowRemoved(tokens, slashIdx+1, end, false)
	right := cleanAliasToken(rightRaw)
	if stripped := stripExplicitSeasonSuffix(right); stripped != "" {
		right = stripped
	}
	right = p.normalizeAliasCandidateForOutput(right)
	right = strings.TrimSpace(right)
	if right == "" || !p.isValidAliasToken(right) {
		return nil
	}
	titleKey := aliasSemanticKey(title)
	rightKey := aliasSemanticKey(right)
	if rightKey == "" || (titleKey != "" && rightKey == titleKey) {
		return nil
	}
	return []string{right}
}

func (p *Parser) inferTitleGapDashAlias(tokens []model.Token, title string, titleEnd int, episodeIdx int, episodeRangeIdx int) []string {
	if len(tokens) == 0 || titleEnd < 0 || titleEnd >= len(tokens) {
		return nil
	}
	end := len(tokens)
	if episodeRangeIdx >= 0 && episodeRangeIdx < end {
		end = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < end {
		end = episodeIdx
	}
	if titleEnd >= end {
		return nil
	}
	if !hasDashAliasBridgeSignal(tokens, titleEnd, end) {
		return nil
	}

	dashIdx := make([]int, 0, 3)
	for i := titleEnd; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		if isDashDelimiterToken(tokens[i].Text) {
			if isInlineAliasWordDash(tokens, i, titleEnd, end) {
				continue
			}
			dashIdx = append(dashIdx, i)
		}
	}
	if len(dashIdx) == 0 {
		return nil
	}

	segments := make([][2]int, 0, len(dashIdx))
	if len(dashIdx) == 1 {
		segments = append(segments, [2]int{dashIdx[0] + 1, end})
	} else {
		for i := 0; i+1 < len(dashIdx); i++ {
			segments = append(segments, [2]int{dashIdx[i] + 1, dashIdx[i+1]})
		}
	}

	titleKey := aliasSemanticKey(title)
	seen := map[string]bool{}
	out := make([]string, 0, 2)
	for _, seg := range segments {
		start, stop := seg[0], seg[1]
		if start < 0 {
			start = 0
		}
		if stop > end {
			stop = end
		}
		if start >= stop {
			continue
		}
		candidate := cleanAliasToken(extractTitleFromSpanAllowRemoved(tokens, start, stop, false))
		if stripped := stripExplicitSeasonSuffix(candidate); stripped != "" {
			candidate = stripped
		}
		candidate = p.normalizeAliasCandidateForOutput(candidate)
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || !p.isValidAliasToken(candidate) {
			continue
		}
		if !isBilingualDashAliasCandidate(title, candidate) {
			continue
		}
		key := aliasSemanticKey(candidate)
		if key == "" || key == titleKey || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p *Parser) inferTitleGapBilingualRunAlias(tokens []model.Token, title string, titleEnd int, episodeIdx int, episodeRangeIdx int) []string {
	if len(tokens) == 0 || titleEnd < 0 || titleEnd >= len(tokens) {
		return nil
	}
	// Conservative path: CJK primary title with latin run alias in title-gap.
	if !hasCJK(title) || hasLatin(title) {
		return nil
	}
	end := len(tokens)
	if episodeRangeIdx >= 0 && episodeRangeIdx < end {
		end = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < end {
		end = episodeIdx
	}
	if titleEnd >= end || !hasDashAliasBridgeSignal(tokens, titleEnd, end) {
		return nil
	}
	for i := titleEnd; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "/" || txt == "／" {
			// slash chains are handled by inferTitleGapSingleSlashAlias.
			return nil
		}
	}

	bestStart := -1
	bestEnd := -1
	for i := titleEnd; i < end; i++ {
		if tokens[i].Removed || !isTitleGapLatinAliasToken(tokens[i].Text) {
			continue
		}
		start := i
		for i < end && !tokens[i].Removed && isTitleGapLatinAliasToken(tokens[i].Text) {
			i++
		}
		stop := i
		if stop-start >= 2 && (bestStart < 0 || stop-start > bestEnd-bestStart) {
			bestStart = start
			bestEnd = stop
		}
	}
	if bestStart < 0 || bestEnd <= bestStart {
		return nil
	}

	cand := cleanAliasToken(extractTitleFromSpanAllowRemoved(tokens, bestStart, bestEnd, false))
	if stripped := stripExplicitSeasonSuffix(cand); stripped != "" {
		cand = stripped
	}
	cand = p.normalizeAliasCandidateForOutput(cand)
	cand = strings.TrimSpace(cand)
	if cand == "" || !p.isValidAliasToken(cand) {
		return nil
	}
	if !hasLatin(cand) || hasCJK(cand) {
		return nil
	}
	titleKey := aliasSemanticKey(title)
	candKey := aliasSemanticKey(cand)
	if candKey == "" || (titleKey != "" && candKey == titleKey) {
		return nil
	}
	return []string{cand}
}

func (p *Parser) promoteInlineBracketContinuationIntoTitle(tokens []model.Token, title string, aliases []string, titleEnd int, episodeIdx int, episodeRangeIdx int, attrs []model.Attribute) (string, []string) {
	title = strings.TrimSpace(title)
	if len(tokens) == 0 || title == "" || titleEnd < 0 || titleEnd >= len(tokens) {
		return title, aliases
	}
	// Conservative scope: latin primary title with inline bracket phrase + tail word run.
	if !hasLatin(title) || hasCJK(title) {
		return title, aliases
	}

	end := len(tokens)
	if episodeRangeIdx >= 0 && episodeRangeIdx < end {
		end = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < end {
		end = episodeIdx
	}
	if techStart, ok := firstTechTokenIndexFromAttrs(attrs); ok && techStart > titleEnd && techStart < end {
		end = techStart
	}
	if titleEnd >= end {
		return title, aliases
	}

	openIdx := -1
	for i := titleEnd; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "[" || t == "【" || t == "(" || t == "（" {
			openIdx = i
			break
		}
		if isDashDelimiterToken(t) || t == "/" || t == "／" {
			return title, aliases
		}
	}
	if openIdx < 0 {
		return title, aliases
	}

	open, close, ok := headAliasBracketPair(strings.TrimSpace(tokens[openIdx].Text))
	if !ok {
		return title, aliases
	}
	closeIdx := -1
	depth := 0
	for i := openIdx; i < end; i++ {
		if strings.TrimSpace(tokens[i].Text) == open {
			depth++
			continue
		}
		if strings.TrimSpace(tokens[i].Text) == close {
			if depth > 0 {
				depth--
				if depth == 0 {
					closeIdx = i
					break
				}
			}
		}
	}
	if closeIdx <= openIdx+1 {
		return title, aliases
	}

	inner := cleanAliasToken(extractTitleFromSpanAllowRemoved(tokens, openIdx+1, closeIdx, false))
	inner = strings.Trim(inner, "\"'“”‘’")
	inner = strings.TrimSpace(inner)
	if inner == "" || isLikelyMetaBracketText(inner) || !p.isValidAliasToken(inner) {
		return title, aliases
	}

	tailStart := closeIdx + 1
	for tailStart < end {
		txt := strings.TrimSpace(tokens[tailStart].Text)
		if txt == "" || txt == "." || txt == "。" || txt == "!" || txt == "！" || txt == "?" || txt == "？" {
			tailStart++
			continue
		}
		break
	}
	if tailStart >= end {
		return title, aliases
	}
	tailRaw := cleanAliasToken(extractTitleFromSpanAllowRemoved(tokens, tailStart, end, false))
	tailRaw = strings.TrimSpace(strings.Trim(tailRaw, "-_/;:,+.。!！?？ "))
	if tailRaw == "" || isLikelyMetaBracketText(tailRaw) {
		return title, aliases
	}
	if isSingleSlashSeasonSignalToken(tailRaw) || isSingleSlashBridgeToken(tailRaw) {
		return title, aliases
	}

	merged := sanitizePrimaryTitleForOutput(strings.TrimSpace(title + " " + inner + " " + tailRaw))
	if merged == "" || aliasSemanticKey(merged) == aliasSemanticKey(title) {
		return title, aliases
	}

	aliases = removeAliasBySemanticKey(aliases, aliasSemanticKey(inner))
	return merged, aliases
}

func isDashDelimiterToken(t string) bool {
	switch strings.TrimSpace(t) {
	case "-", "–", "—", "－":
		return true
	default:
		return false
	}
}

func isInlineAliasWordDash(tokens []model.Token, idx int, start int, end int) bool {
	prev := lastVisibleTokenText(tokens, start, idx)
	next := nextVisibleTokenText(tokens, idx+1, end)
	if prev == "" || next == "" {
		return false
	}
	return isTitleGapLatinAliasToken(prev) && isTitleGapLatinAliasToken(next)
}

func hasDashAliasBridgeSignal(tokens []model.Token, start int, end int) bool {
	for i := start; i < end; i++ {
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if isSingleSlashSeasonSignalToken(txt) || isSingleSlashBridgeToken(txt) {
			return true
		}
	}
	return false
}

func isTitleGapLatinAliasToken(t string) bool {
	s := strings.TrimSpace(t)
	if s == "" {
		return false
	}
	if isSingleSlashSeasonSignalToken(s) || isDashDelimiterToken(s) {
		return false
	}
	switch s {
	case "[", "]", "(", ")", "（", "）", "【", "】", "{", "}":
		return false
	}
	s = strings.Trim(s, "[](){}【】（）.,;:!?\"'")
	if s == "" {
		return false
	}
	return titleGapLatinAliasTokenPattern.MatchString(s)
}

func isBilingualDashAliasCandidate(title string, candidate string) bool {
	titleHasCJK := hasCJK(title)
	titleHasLatin := hasLatin(title)
	candHasCJK := hasCJK(candidate)
	candHasLatin := hasLatin(candidate)
	if !candHasCJK && !candHasLatin {
		return false
	}
	if candHasCJK && !titleHasCJK {
		return true
	}
	if candHasLatin && !titleHasLatin {
		return true
	}
	if candHasCJK && candHasLatin {
		return true
	}
	return false
}

func hasSingleSlashBridgeSignal(tokens []model.Token, start int, end int, slashIdx int) bool {
	left := lastVisibleTokenText(tokens, start, slashIdx)
	right := nextVisibleTokenText(tokens, slashIdx+1, end)
	if isSingleSlashSeasonSignalToken(left) || isSingleSlashSeasonSignalToken(right) {
		return true
	}
	if isSingleSlashBridgeToken(left) || isSingleSlashBridgeToken(right) {
		return true
	}

	windowStart := slashIdx - 3
	if windowStart < start {
		windowStart = start
	}
	windowEnd := slashIdx + 4
	if windowEnd > end {
		windowEnd = end
	}
	for i := windowStart; i < windowEnd; i++ {
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if isSingleSlashSeasonSignalToken(txt) {
			return true
		}
		if isSingleSlashBridgeToken(txt) {
			return true
		}
	}
	return false
}

func isSingleSlashSeasonSignalToken(t string) bool {
	s := strings.TrimSpace(t)
	if s == "" {
		return false
	}
	if singleSlashAliasSeasonSignalPattern.MatchString(s) {
		return true
	}
	if singleSlashAliasSeasonSignalCJKPattern.MatchString(s) {
		return true
	}
	if singleSlashAliasSeasonPhrasePattern.MatchString(s) {
		return true
	}
	if singleSlashAliasEmbeddedSeasonCJKPattern.MatchString(s) {
		return true
	}
	return false
}

func isSingleSlashBridgeToken(t string) bool {
	s := strings.TrimSpace(t)
	if s == "" {
		return false
	}
	if singleSlashAliasBridgeTokenPattern.MatchString(s) {
		return true
	}
	ls := strings.ToLower(s)
	if strings.Contains(ls, "special") || strings.Contains(ls, "dub") || strings.Contains(ls, "audio") {
		return true
	}
	if strings.Contains(ls, "anime") || strings.Contains(ls, "gekijou") || strings.Contains(ls, "theater") {
		return true
	}
	if strings.Contains(s, "特別篇") || strings.Contains(s, "特别篇") || strings.Contains(s, "年齡限制版") || strings.Contains(s, "年龄限制版") || strings.Contains(s, "日配版") || strings.Contains(s, "中文配音") {
		return true
	}
	if strings.Contains(s, "小劇場") || strings.Contains(s, "小剧场") || strings.Contains(s, "劇場") || strings.Contains(s, "剧场") {
		return true
	}
	return false
}

func isAliasChainDelimiterToken(s string) bool {
	switch strings.TrimSpace(s) {
	case "|", "｜", "/", "／":
		return true
	default:
		return false
	}
}
