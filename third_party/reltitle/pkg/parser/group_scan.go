package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func extractHeadBracketGroup(tokens []model.Token) (string, [2]int) {
	if len(tokens) < 3 {
		return "", [2]int{0, 0}
	}
	start := 0
	for start < len(tokens) && tokens[start].Removed {
		start++
	}
	if start >= len(tokens) {
		return "", [2]int{0, 0}
	}
	_, _, ok0 := headGroupBracketPair(tokens[start].Text)
	if !ok0 {
		return "", [2]int{0, 0}
	}

	blocks := make([][3]int, 0, 4)
	i := start
	for i < len(tokens) {
		if tokens[i].Removed {
			i++
			continue
		}
		open, close, ok := headGroupBracketPair(tokens[i].Text)
		if !ok {
			break
		}
		end := -1
		depth := 0
		for j := i; j < len(tokens); j++ {
			if tokens[j].Removed {
				continue
			}
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
			break
		}
		blocks = append(blocks, [3]int{i, end + 1, end})
		i = end + 1
		for i < len(tokens) && tokens[i].Removed {
			i++
		}
	}
	if len(blocks) == 0 {
		return "", [2]int{0, 0}
	}

	// Prefer first bracket block. If first block is clear meta (e.g. [1080P], [BDRip]),
	// allow fallback to the first subsequent strong group candidate block.
	for bi, b := range blocks {
		if !headBracketBlockHasAliveToken(tokens, b) {
			continue
		}
		g := headBracketBlockText(tokens, b)
		if g == "" {
			continue
		}
		if isLeadingHeadNoiseText(g) {
			continue
		}
		if !isGroupCandidateText(g) {
			continue
		}
		if (isLikelyMetaBracketText(g) && !hasStrongGroupKeyword(g)) || isEpisodeLikeBracketText(g) {
			continue
		}
		if bi == 0 {
			return g, [2]int{b[0], b[1]}
		}
		if areLeadingHeadBlocksMeta(tokens, blocks[:bi]) {
			return g, [2]int{b[0], b[1]}
		}
		break
	}
	return "", [2]int{0, 0}
}

func headBracketBlockText(tokens []model.Token, block [3]int) string {
	parts := make([]string, 0, block[1]-block[0])
	for k := block[0] + 1; k < block[1]-1; k++ {
		if strings.TrimSpace(tokens[k].Text) == "" {
			continue
		}
		parts = append(parts, tokens[k].Text)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func headBracketBlockHasAliveToken(tokens []model.Token, block [3]int) bool {
	for k := block[0] + 1; k < block[1]-1; k++ {
		if tokens[k].Removed {
			continue
		}
		if strings.TrimSpace(tokens[k].Text) == "" {
			continue
		}
		return true
	}
	return false
}

func areLeadingHeadBlocksMeta(tokens []model.Token, blocks [][3]int) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if !headBracketBlockHasAliveToken(tokens, b) {
			continue
		}
		text := headBracketBlockText(tokens, b)
		if text == "" {
			continue
		}
		if isLeadingHeadNoiseText(text) || isLikelyMetaBracketText(text) || isEpisodeLikeBracketText(text) {
			continue
		}
		return false
	}
	return true
}

func headGroupBracketPair(s string) (open string, close string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "[" {
		return "[", "]", true
	}
	if s == "【" {
		return "【", "】", true
	}
	return "", "", false
}

func extractHeadParenGroup(tokens []model.Token) (string, [2]int) {
	if len(tokens) < 3 || strings.TrimSpace(tokens[0].Text) != "(" {
		return "", [2]int{0, 0}
	}
	end := -1
	depth := 0
	for i := 0; i < len(tokens); i++ {
		t := strings.TrimSpace(tokens[i].Text)
		if t == "(" {
			depth++
		} else if t == ")" {
			if depth > 0 {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
	}
	if end <= 1 {
		return "", [2]int{0, 0}
	}
	parts := make([]string, 0, end-1)
	for i := 1; i < end; i++ {
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	cand := strings.TrimSpace(strings.Join(parts, " "))
	if !isHeadParenGroupCandidate(cand) {
		return "", [2]int{0, 0}
	}
	return cand, [2]int{0, end + 1}
}

var headParenUpperTokenPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._&+\-]{1,23}$`)
var headParenVersionLikePattern = regexp.MustCompile(`(?i)^v(?:er)?\d+$`)

func isHeadParenGroupCandidate(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if isLikelyMetaBracketText(s) || isEpisodeLikeBracketText(s) {
		return false
	}
	if len([]rune(s)) > 24 {
		return false
	}
	if headParenVersionLikePattern.MatchString(s) {
		return false
	}
	if headParenUpperTokenPattern.MatchString(s) {
		return true
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "raw") || strings.Contains(low, "sub") || strings.Contains(low, "team") || strings.Contains(low, "studio") {
		return isGroupCandidateText(s)
	}
	return false
}

func extractHeadBracketGroupAfterLeadingParenMeta(tokens []model.Token) (string, [2]int) {
	if len(tokens) < 4 || strings.TrimSpace(tokens[0].Text) != "(" {
		return "", [2]int{0, 0}
	}
	offset := 0
	consumedMeta := false
	for offset < len(tokens) && strings.TrimSpace(tokens[offset].Text) == "(" {
		end := -1
		depth := 0
		for i := offset; i < len(tokens); i++ {
			t := strings.TrimSpace(tokens[i].Text)
			if t == "(" {
				depth++
			} else if t == ")" {
				if depth > 0 {
					depth--
					if depth == 0 {
						end = i
						break
					}
				}
			}
		}
		if end <= offset+1 {
			return "", [2]int{0, 0}
		}
		parts := make([]string, 0, end-offset-1)
		for i := offset + 1; i < end; i++ {
			t := strings.TrimSpace(tokens[i].Text)
			if t == "" {
				continue
			}
			parts = append(parts, t)
		}
		cand := strings.TrimSpace(strings.Join(parts, " "))
		if !isLeadingParenMetaText(cand) {
			return "", [2]int{0, 0}
		}
		consumedMeta = true
		offset = end + 1
	}
	if !consumedMeta || offset >= len(tokens) {
		return "", [2]int{0, 0}
	}
	g, span := extractHeadBracketGroup(tokens[offset:])
	if g == "" {
		return "", [2]int{0, 0}
	}
	return g, [2]int{span[0] + offset, span[1] + offset}
}

func isLeadingParenMetaText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if headParenVersionLikePattern.MatchString(s) {
		return true
	}
	low := strings.ToLower(s)
	if low == "partial" || low == "reseed" || low == "fixed" {
		return true
	}
	return isLikelyMetaBracketText(s)
}

func isGroupCandidateText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	runeLen := len([]rune(s))
	if runeLen > 64 {
		return false
	}
	if runeLen > 32 && !hasStrongGroupKeyword(s) {
		return false
	}
	allDigits := true
	for _, r := range s {
		if !(('0' <= r && r <= '9') || r == '-' || r == '~' || r == ' ' || r == '+') {
			allDigits = false
			break
		}
	}
	if allDigits {
		return false
	}
	for _, r := range s {
		if ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') {
			continue
		}
		if r == ' ' || r == '_' || r == '-' || r == '.' || r == '&' || r == '＆' || r == '+' || r == '＋' || r == ',' || r == '，' || r == '@' {
			continue
		}
		if r > 127 {
			continue
		}
		return false
	}
	return true
}

func hasStrongGroupKeyword(s string) bool {
	ss := strings.ToLower(strings.TrimSpace(s))
	if ss == "" {
		return false
	}
	if strings.Contains(s, "字幕") || strings.Contains(s, "漢化") || strings.Contains(s, "汉化") {
		return true
	}
	if strings.Contains(ss, "sub") || strings.Contains(ss, "raw") || strings.Contains(ss, "fansub") || strings.Contains(ss, "team") || strings.Contains(ss, "studio") {
		return true
	}
	return false
}

func isLikelyMetaBracketText(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	up := strings.ToUpper(strings.TrimSpace(s))
	trim := strings.TrimSpace(s)
	if editionVersionNoteForSegment(trim) != "" || len(extractCompositeEditionNotes(trim)) > 0 {
		return true
	}
	if trim == "无字幕" || trim == "無字幕" {
		return true
	}
	if strings.Contains(trim, "超分") {
		return true
	}
	if strings.Contains(trim, "合集") || strings.Contains(trim, "合輯") {
		// E.g. "個人製作合集".
		return true
	}
	if strings.Contains(trim, "新番") {
		return true
	}
	if trim == "先行版" || trim == "先行版本" {
		return true
	}
	if trim == "转" || trim == "轉" || trim == "转载" || trim == "轉載" || trim == "转发" || trim == "轉發" {
		return true
	}
	if low == "weekly" || low == "batch" || low == "reseed" || low == "complete" {
		return true
	}
	if strings.HasPrefix(low, "final season") {
		return true
	}
	if low == "1080p" || low == "2160p" || low == "720p" {
		return true
	}
	if low == "hdr" || low == "dovi" || low == "dolbyvision" {
		return true
	}
	if reMetaHDRWithKToken.MatchString(low) {
		return true
	}
	if strings.Contains(low, "1080p") || strings.Contains(low, "2160p") || strings.Contains(low, "720p") {
		return true
	}
	if up == "MP4" || up == "MKV" || up == "AVI" {
		return true
	}
	if up == "GB" || up == "BIG5" || up == "CHS" || up == "CHT" || up == "JP" {
		return true
	}
	if up == "BDMV" {
		return true
	}
	if up == "BDBOX" || up == "BD-BOX" {
		return true
	}
	// Common combined language tags like "CHS&JPN" / "CHT&JPN" / "CHS&CHT&JPN".
	if reIsLikelyMetaBracketText1.MatchString(trim) {
		return true
	}
	if strings.Contains(s, "简") || strings.Contains(s, "繁") {
		if strings.Contains(s, "简中") || strings.Contains(s, "繁中") || strings.Contains(s, "简体") || strings.Contains(s, "繁体") || strings.Contains(s, "内嵌") || strings.Contains(s, "内封") || strings.Contains(s, "双语") {
			return true
		}
	}
	if strings.Contains(s, "招募") || strings.Contains(s, "时轴") {
		return true
	}
	if strings.Contains(s, "招人") {
		return true
	}
	if strings.Contains(s, "附字幕") {
		return true
	}
	if strings.Contains(s, "分角色") {
		return true
	}
	if reEditionMetaText.MatchString(trim) {
		return true
	}
	return false
}

func isEpisodeLikeBracketText(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return false
	}
	if reIsEpisodeLikeBracketText1.MatchString(ss) {
		return true
	}
	if reIsEpisodeLikeBracketText2.MatchString(ss) {
		return true
	}
	if reIsEpisodeLikeBracketText3.MatchString(ss) {
		return true
	}
	if reIsEpisodeLikeBracketText4.MatchString(ss) {
		return true
	}
	return false
}

var sxxExxPattern = regexp.MustCompile(`(?i)^s(\d{1,2})e(\d{1,4})(?:v\d+)?(?:[^0-9a-z].*)?$`)
var seasonOnlyPattern = regexp.MustCompile(`(?i)^s(\d{1,2})$`)
var seasonOnlyVersionPattern = regexp.MustCompile(`(?i)^s(\d{1,2})v\d+$`)
var seasonEmbeddedPattern = regexp.MustCompile(`(?i)^s(\d{1,2})[^0-9a-z].*$`)
var seasonWordPattern = regexp.MustCompile(`(?i)^season$`)
var seasonWordLoosePattern = regexp.MustCompile(`(?i)^(season|saison)$`)
var seasonOrdinalPattern = regexp.MustCompile(`(?i)^(\d{1,2})(?:st|nd|rd|th)$`)
var seasonCJKPattern = regexp.MustCompile(`^第?([一二三四五六七八九十0-9]{1,3})季$`)

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reIsLikelyMetaBracketText1  = regexp.MustCompile(`(?i)^(?:CHS|CHT|JPN|JP|ENG)(?:\s*(?:&|＆|\+|＋)\s*(?:CHS|CHT|JPN|JP|ENG))+$`)
	reIsEpisodeLikeBracketText1 = regexp.MustCompile(`(?i)^0?\d{1,4}\s*[-~]\s*0?\d{1,4}(?:\.\d{1,2})?.*$`)
	reIsEpisodeLikeBracketText2 = regexp.MustCompile(`(?i)^第?\d{1,4}(?:\s*-\s*\d{1,4})?(?:集|话|話|回|章)(?:\s|$)`)
	reIsEpisodeLikeBracketText3 = regexp.MustCompile(`(?i)^0?\d{1,4}\s*(?:&|＆)\s*(?:sp|ova|oad|ona|ex|pv|op|ed)(?:\s|$)`)
	reIsEpisodeLikeBracketText4 = regexp.MustCompile(`^0?\d{1,4}[_\s-]+.+$`)
)
