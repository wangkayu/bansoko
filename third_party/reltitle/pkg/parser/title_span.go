package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func (p *Parser) inferTitleTokenSpan(tokens []model.Token, episodeIdx int, episodeRangeIdx int, seasonCutIdx int) (int, int) {
	start := titleStartAfterLeadingEmptyBracketBlocks(tokens)

	end := len(tokens)
	if episodeRangeIdx >= 0 {
		end = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < end {
		if shouldUseEpisodeAsTitleCut(tokens, start, episodeIdx) {
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
			if t == "[" || t == "]" || t == "【" || t == "】" || t == "(" || t == ")" || t == "（" || t == "）" {
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
					if pj == ":" || pj == "：" || pj == ";" || pj == "；" {
						end = j
					}
					break
				}
				break
			}
		}
		if seenTitle {
			if (tokens[i].Text == "[" || tokens[i].Text == "【") && !tokens[i].Protected {
				end = i
				break
			}
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

	first := -1
	last := -1
	for i := start; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if t == "-" || t == "|" || t == "｜" || t == "/" {
			continue
		}
		if dotMode {
			if t == "." {
				continue
			}
		}
		if t == "[" || t == "]" || t == "(" || t == ")" || t == "（" || t == "）" {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 || last < 0 {
		return -1, -1
	}
	return first, last + 1
}

func firstVisibleTitleTokenIndex(tokens []model.Token, start int, end int) int {
	if start < 0 {
		start = 0
	}
	if end > len(tokens) {
		end = len(tokens)
	}
	if start >= end {
		return -1
	}
	for i := start; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if t == "[" || t == "]" || t == "【" || t == "】" || t == "(" || t == ")" || t == "（" || t == "）" {
			continue
		}
		if t == "-" || t == "|" || t == "｜" || t == "/" || t == "／" {
			continue
		}
		if isTitleNoiseToken(t) {
			continue
		}
		return i
	}
	return -1
}

func shouldUseEpisodeAsTitleCut(tokens []model.Token, titleStart int, episodeIdx int) bool {
	if episodeIdx < 0 || episodeIdx >= len(tokens) {
		return false
	}
	first := firstVisibleTitleTokenIndex(tokens, titleStart, len(tokens))
	if first >= 0 && episodeIdx < first {
		return false
	}
	return true
}

var reDotSeparatedRes = regexp.MustCompile(`(?i)^\d{3,4}p$`)

func (p *Parser) looksLikeDotSeparatedMetaCutToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	low = strings.TrimSpace(strings.Trim(low, "._-"))
	if low == "" {
		return false
	}
	if reDotSeparatedRes.MatchString(low) {
		return true
	}
	if low == "dvdiso" {
		return true
	}
	if p != nil {
		if _, ok := p.sourceTypeCanonical[low]; ok {
			return true
		}
		if _, ok := p.platformCanonical[low]; ok {
			return true
		}
		if _, ok := p.normalizeVideoCodecToken(low); ok {
			return true
		}
		if _, ok := p.normalizeAudioCodecToken(low); ok {
			return true
		}
		if _, ok := p.normalizeContainerToken(low); ok {
			return true
		}
		if _, ok := p.normalizeAudioFlagToken(low); ok {
			return true
		}
	}
	if low == "webrip" || low == "web-dl" || low == "webdl" || low == "bluray" || low == "bdrip" || low == "dvdrip" {
		return true
	}
	if low == "hdr" {
		return true
	}
	if low == "mpeg" || low == "xvid" {
		return true
	}
	if low == "vastfr" || low == "vostfr" || low == "multi" || low == "truefrench" {
		return true
	}
	return false
}

func titleStartAfterLeadingEmptyBracketBlocks(tokens []model.Token) int {
	i := 0
	for i < len(tokens) {
		for i < len(tokens) && (tokens[i].Removed || strings.TrimSpace(tokens[i].Text) == "") {
			i++
		}
		if i >= len(tokens) {
			return i
		}
		if looksLikeLeadingTitleMeta(tokens[i].Text) {
			i++
			continue
		}
		if tokens[i].Text == "★" || tokens[i].Text == "☆" {
			if ni, ok := consumeStarWrappedLeadingMeta(tokens, i); ok {
				i = ni
				continue
			}
		}
		open := strings.TrimSpace(tokens[i].Text)
		if open != "[" && open != "【" {
			return i
		}
		close := "]"
		if open == "【" {
			close = "】"
		}
		end := -1
		depth := 0
		for j := i; j < len(tokens); j++ {
			if tokens[j].Text == open {
				depth++
			} else if tokens[j].Text == close {
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
			return i
		}
		innerHasVisible := false
		innerParts := make([]string, 0, 8)
		for k := i + 1; k < end; k++ {
			if tokens[k].Removed {
				continue
			}
			t := strings.TrimSpace(tokens[k].Text)
			if t == "" {
				continue
			}
			if t == "[" || t == "]" || t == "【" || t == "】" {
				continue
			}
			if t == "_" || t == "-" || t == "." || t == "/" || t == "／" || t == "|" || t == "｜" {
				continue
			}
			innerHasVisible = true
			innerParts = append(innerParts, t)
			break
		}
		if innerHasVisible {
			blockText := strings.TrimSpace(strings.Join(innerParts, " "))
			if isLeadingHeadNoiseText(blockText) || isLikelyMetaBracketText(blockText) || looksLikeLeadingTitleMeta(blockText) {
				i = end + 1
				continue
			}
			for k := i + 1; k < end; k++ {
				if tokens[k].Removed {
					continue
				}
				t := strings.TrimSpace(tokens[k].Text)
				if t == "" || t == "[" || t == "]" || t == "【" || t == "】" {
					continue
				}
				if t == "_" || t == "-" || t == "." || t == "/" || t == "／" || t == "|" || t == "｜" {
					continue
				}
				return k
			}
			return i
		}
		i = end + 1
	}
	return i
}

var reMonthNewShow = regexp.MustCompile(`^\d{1,2}月新番$`)
var reMetaHDRWithKToken = regexp.MustCompile(`(?i)(?:4k|8k).*hdr|hdr.*(?:4k|8k)`)
var reEditionMetaText = regexp.MustCompile(`(?i)\b(?:uncut(?:\s*(?:ver|version)\.?)?|uncensored(?:\s*(?:ver|version)\.?)?|director'?s\s*cut|extended\s*cut|remaster(?:ed)?(?:\s*(?:ver|version)\.?)?|audio[_ -]?commentary(?:\s*(?:ver|version)\.?)?|encode\s*ver)\b`)

func isLeadingHeadNoiseText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	s = strings.TrimSpace(strings.Trim(s, "★☆"))
	switch s {
	case "搬运", "搬運", "整理搬运", "整理搬運", "代发", "代發":
		return true
	}
	return false
}

func looksLikeLeadingTitleMeta(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	s = strings.TrimSpace(strings.Trim(s, "★☆"))
	if isLeadingHeadNoiseText(s) {
		return true
	}
	if reMonthNewShow.MatchString(s) {
		return true
	}
	if strings.Contains(s, "新番") {
		return true
	}
	if strings.Contains(s, "代理商") {
		return true
	}
	if strings.Contains(s, "粤语") || strings.Contains(s, "粵語") {
		return true
	}
	if strings.Contains(s, "招募") {
		return true
	}
	if strings.Contains(s, "检索") {
		return true
	}
	return false
}

func looksLikeTitleCutMetaToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	low = strings.TrimSpace(strings.TrimLeft(low, ":："))
	if strings.Contains(low, "tv动画") {
		return true
	}
	// Common Chinese meta sections in pack titles.
	if strings.Contains(s, "OVA") || strings.Contains(s, "ova") {
		if strings.Contains(s, "篇") {
			return true
		}
	}
	if strings.Contains(s, "剧场") || strings.Contains(s, "劇場") {
		return true
	}
	if s == "漫画" || s == "漫畫" || s == "音乐" || s == "音樂" {
		return true
	}
	if strings.Contains(s, "音轨") || strings.Contains(s, "音軌") {
		return true
	}
	if strings.Contains(s, "字幕") {
		return true
	}
	if strings.Contains(s, "合集") || strings.Contains(s, "全集") {
		return true
	}
	if strings.Contains(s, "整理时间") || strings.Contains(s, "整理時間") {
		return true
	}
	return false
}

func isShortLatinTitleCandidate(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if hasCJK(s) {
		return false
	}
	if !hasLatin(s) {
		return false
	}
	if strings.ContainsAny(s, "!?,") {
		return false
	}
	if len([]rune(s)) > 28 {
		return false
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "webrip") || strings.Contains(low, "web-dl") || strings.Contains(low, "bluray") {
		return false
	}
	return true
}
