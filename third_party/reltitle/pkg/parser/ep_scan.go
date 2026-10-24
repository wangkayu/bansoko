package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// epScanSkipped reports whether token i must be skipped by episode/season/range
// scanning. Protected tokens belong to a known title span (title dictionary or
// structural guard) and are never episode signals; Removed tokens were already
// consumed by an earlier pass. Every scanning loop in this domain MUST use this
// guard so title digits are never reinterpreted as episodes.
func epScanSkipped(tokens []model.Token, i int) bool {
	return i < 0 || i >= len(tokens) || tokens[i].Protected || tokens[i].Removed
}

func detectSxxExx(tokens []model.Token) (season string, episode string, tokenIdx int) {
	for i := 0; i < len(tokens); i++ {
		if epScanSkipped(tokens, i) {
			continue
		}
		m := sxxExxPattern.FindStringSubmatch(tokens[i].Text)
		if len(m) == 3 {
			season = strings.TrimLeft(m[1], "0")
			if season == "" {
				season = "0"
			}
			episode = strings.TrimLeft(m[2], "0")
			if episode == "" {
				episode = "0"
			}
			return season, episode, i
		}
	}
	return "", "", -1
}

func detectSeasonKeyword(tokens []model.Token) (season string, tokenIdx int) {
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		text := strings.TrimSpace(tokens[i].Text)
		if text == "" {
			continue
		}
		if v := extractSeasonKeywordValue(text); v != "" {
			return v, i
		}
		if seasonWordLoosePattern.MatchString(text) {
			if i+1 < len(tokens) {
				next := strings.TrimSpace(tokens[i+1].Text)
				if v := parseSmallSeasonNumberToken(next); v != "" {
					return v, i
				}
			}
		}
		if m := seasonOrdinalPattern.FindStringSubmatch(text); len(m) == 2 {
			if i+1 < len(tokens) && seasonWordPattern.MatchString(strings.TrimSpace(tokens[i+1].Text)) {
				return strings.TrimLeft(m[1], "0"), i
			}
		}
	}
	return "", -1
}

func detectSeasonKeywordAny(tokens []model.Token) (season string, tokenIdx int) {
	for i := 0; i < len(tokens); i++ {
		if v := extractSeasonKeywordValue(tokens[i].Text); v != "" {
			return v, i
		}
	}
	return "", -1
}

func extractSeasonKeywordValue(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if m := reExtractSeasonKeywordValue1.FindStringSubmatch(text); len(m) == 2 {
		if v := parseSmallSeasonNumberToken(m[1]); v != "" {
			return v
		}
	}
	if m := reExtractSeasonKeywordValue2.FindStringSubmatch(text); len(m) == 2 {
		return strings.TrimLeft(m[1], "0")
	}
	if m := reExtractSeasonKeywordValue3.FindStringSubmatch(text); len(m) == 2 {
		if v := parseSmallSeasonNumberToken(m[1]); v != "" {
			return v
		}
	}
	if m := reExtractSeasonKeywordValue4.FindStringSubmatch(text); len(m) == 2 {
		if v := parseSmallSeasonNumberToken(m[1]); v != "" {
			return v
		}
	}
	if m := seasonCJKPattern.FindStringSubmatch(text); len(m) == 2 {
		if v := parseSmallSeasonNumberToken(m[1]); v != "" {
			return v
		}
	}
	return ""
}

var episodeRangePattern = regexp.MustCompile(`(?i)^(\d{1,4})[-~](\d{1,4})$`)
var episodeRangeEmbeddedPattern = regexp.MustCompile(`(?i)(\d{1,4})[-~](\d{1,4})`)
var sxxExxEpisodeRangePattern = regexp.MustCompile(`(?i)^s\d{1,2}e(\d{1,4})[-~]e?(\d{1,4})$`)
var sxxExxLeftPattern = regexp.MustCompile(`(?i)^s(\d{1,2})e(\d{1,4})$`)
var episodeRangeLeftCJKPattern = regexp.MustCompile(`^(?:第)?(\d{1,4})$`)
var episodeRangeRightCJKPattern = regexp.MustCompile(`^(\d{1,4})(?:集|话|話|回|章)?$`)
var epPrefixedDigitsPattern = regexp.MustCompile(`(?i)^e(?:p)?(\d{1,4})$`)
var episodeOfTotalPattern = regexp.MustCompile(`^(\d{1,4})$`)
var leadingDigitsPattern = regexp.MustCompile(`^(\d{1,4})`)

func isTitlePrefixedDigitRangeInBracket(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	if !isInBracket(tokens, idx) {
		return false
	}
	// Detect patterns like: [ 藤本树 17 - 26 ] where 17-26 is part of the title.
	if idx+2 >= len(tokens) {
		return false
	}
	if !episodeOfTotalPattern.MatchString(strings.TrimSpace(tokens[idx].Text)) {
		return false
	}
	if strings.TrimSpace(tokens[idx+1].Text) != "-" {
		return false
	}
	if !episodeOfTotalPattern.MatchString(strings.TrimSpace(tokens[idx+2].Text)) {
		return false
	}
	// If there is meaningful title text before the range within the same bracket block,
	// treat this range as title suffix, not episode_range.
	openIdx := -1
	for j := idx; j >= 0; j-- {
		if tokens[j].Text == "[" || tokens[j].Text == "【" {
			openIdx = j
			break
		}
	}
	if openIdx < 0 {
		return false
	}
	for j := idx - 1; j > openIdx; j-- {
		t := strings.TrimSpace(tokens[j].Text)
		if t == "" {
			continue
		}
		if t == "-" || t == "~" {
			continue
		}
		if episodeOfTotalPattern.MatchString(t) {
			continue
		}
		// Anything that is not pure digits/separators is treated as title prefix.
		// Do NOT skip Removed tokens here: a preceding title block (e.g. "[藤本樹 17-26]")
		// may already have been consumed as title/alias by an earlier pass, and its
		// tokens are exactly the title-text evidence this guard needs.
		return true
	}
	return false
}

func detectEpisodeRange(tokens []model.Token) (string, int) {
	// Pattern: S01E08-E09 / S01E08-09 / S01E08~E09
	for i := 0; i < len(tokens); i++ {
		text := strings.TrimSpace(tokens[i].Text)
		if m := sxxExxEpisodeRangePattern.FindStringSubmatch(text); len(m) == 3 {
			l := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
			if l == "" {
				l = "0"
			}
			r := strings.TrimLeft(strings.TrimSpace(m[2]), "0")
			if r == "" {
				r = "0"
			}
			if l != "0" && r != "0" {
				return l + "-" + r, i
			}
		}
	}
	for i := 0; i+2 < len(tokens); i++ {
		// The left SxxExx token may already be removed by season extraction.
		// Keep matching this range pattern as long as separator/right token remain visible.
		if tokens[i+1].Removed || tokens[i+2].Removed {
			continue
		}
		left := strings.TrimSpace(tokens[i].Text)
		sep := strings.TrimSpace(tokens[i+1].Text)
		right := strings.TrimSpace(tokens[i+2].Text)
		if sep != "-" && sep != "~" {
			continue
		}
		lm := sxxExxLeftPattern.FindStringSubmatch(left)
		if len(lm) != 3 {
			continue
		}
		if rm := epPrefixedDigitsPattern.FindStringSubmatch(strings.ToLower(right)); len(rm) == 2 {
			right = strings.TrimSpace(rm[1])
		}
		if !episodeOfTotalPattern.MatchString(right) {
			continue
		}
		l := strings.TrimLeft(strings.TrimSpace(lm[2]), "0")
		if l == "" {
			l = "0"
		}
		r := strings.TrimLeft(strings.TrimSpace(right), "0")
		if r == "" {
			r = "0"
		}
		if l != "0" && r != "0" {
			return l + "-" + r, i
		}
	}

	// Pattern: Episode 27-40 / EP 01-12
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if isInBracket(tokens, i) {
			continue
		}
		kw := strings.TrimSpace(tokens[i].Text)
		if !(strings.EqualFold(kw, "episode") || strings.EqualFold(kw, "ep")) {
			continue
		}
		if i+1 >= len(tokens) {
			continue
		}
		if m := episodeRangePattern.FindStringSubmatch(strings.TrimSpace(tokens[i+1].Text)); len(m) == 3 {
			l := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
			if l == "" {
				l = "0"
			}
			r := strings.TrimLeft(strings.TrimSpace(m[2]), "0")
			if r == "" {
				r = "0"
			}
			return l + "-" + r, i + 1
		}
		if m := episodeRangeEmbeddedPattern.FindStringSubmatch(strings.TrimSpace(tokens[i+1].Text)); len(m) == 3 {
			l := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
			if l == "" {
				l = "0"
			}
			r := strings.TrimLeft(strings.TrimSpace(m[2]), "0")
			if r == "" {
				r = "0"
			}
			return l + "-" + r, i + 1
		}
		if i+3 < len(tokens) {
			lraw := strings.TrimSpace(tokens[i+1].Text)
			sep := strings.TrimSpace(tokens[i+2].Text)
			rraw := strings.TrimSpace(tokens[i+3].Text)
			if episodeOfTotalPattern.MatchString(lraw) && (sep == "-" || sep == "~") {
				if !episodeOfTotalPattern.MatchString(rraw) {
					if mm := leadingDigitsPattern.FindStringSubmatch(rraw); len(mm) == 2 {
						rraw = strings.TrimSpace(mm[1])
					}
				}
			}
			if episodeOfTotalPattern.MatchString(lraw) && (sep == "-" || sep == "~") && episodeOfTotalPattern.MatchString(rraw) {
				l := strings.TrimLeft(lraw, "0")
				if l == "" {
					l = "0"
				}
				r := strings.TrimLeft(rraw, "0")
				if r == "" {
					r = "0"
				}
				if l != "0" && r != "0" {
					return l + "-" + r, i + 1
				}
			}
		}
	}

	// Pattern: "01 a 12" / "01 à 12" in plain text.
	// We normalize it into a single token "01-12" to avoid leaving connector + right bound as tail noise.
	for i := 0; i+2 < len(tokens); i++ {
		if tokens[i].Removed || tokens[i+1].Removed || tokens[i+2].Removed {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		lraw := strings.TrimSpace(tokens[i].Text)
		mid := strings.TrimSpace(tokens[i+1].Text)
		rraw := strings.TrimSpace(tokens[i+2].Text)
		if !episodeOfTotalPattern.MatchString(lraw) || !episodeOfTotalPattern.MatchString(rraw) {
			continue
		}
		if !isEpisodeRangeWordConnector(mid) {
			continue
		}
		l := strings.TrimLeft(lraw, "0")
		if l == "" {
			l = "0"
		}
		r := strings.TrimLeft(rraw, "0")
		if r == "" {
			r = "0"
		}
		if l != "0" && r != "0" {
			tokens[i].Text = l + "-" + r
			tokens[i+1].Removed = true
			tokens[i+2].Removed = true
			return l + "-" + r, i
		}
	}

	for i := 0; i+2 < len(tokens); i++ {
		left := strings.TrimSpace(tokens[i].Text)
		if left == "" {
			continue
		}
		m := epPrefixedDigitsPattern.FindStringSubmatch(left)
		if len(m) != 2 {
			continue
		}
		if strings.TrimSpace(tokens[i+1].Text) != "-" {
			continue
		}
		rawR := strings.TrimSpace(tokens[i+2].Text)
		if rm := epPrefixedDigitsPattern.FindStringSubmatch(strings.ToLower(rawR)); len(rm) == 2 {
			rawR = strings.TrimSpace(rm[1])
		}
		if !episodeOfTotalPattern.MatchString(rawR) {
			continue
		}
		l := strings.TrimLeft(m[1], "0")
		if l == "" {
			l = "0"
		}
		r := strings.TrimLeft(rawR, "0")
		if r == "" {
			r = "0"
		}
		if len(m[1]) == 4 && len(rawR) == 4 {
			if y1, err1 := strconv.Atoi(m[1]); err1 == nil {
				if y2, err2 := strconv.Atoi(rawR); err2 == nil {
					if y1 >= 1900 && y1 <= 2099 && y2 >= 1900 && y2 <= 2099 {
						continue
					}
				}
			}
		}
		return l + "-" + r, i
	}

	for i := 0; i < len(tokens); i++ {
		if tokens[i].Protected {
			// Tokens inside a known-title span (title_knowledge.yaml or structural
			// guard) are part of the title itself; never reinterpret their numbers
			// as an episode range.
			continue
		}
		if tokens[i].Removed {
			if i-1 >= 0 {
				prevRaw := strings.TrimSpace(tokens[i-1].Text)
				if seasonWordLoosePattern.MatchString(prevRaw) {
					continue
				}
			}
			// Single-episode extraction can consume the left bound before range detection
			// runs, e.g. "Prism Rondo - 01 ~ 20 ...". Preserve detection when the removed
			// token is still the left edge of an explicit numeric range.
			if i+2 >= len(tokens) {
				continue
			}
			leftRaw := strings.TrimSpace(tokens[i].Text)
			sep := strings.TrimSpace(tokens[i+1].Text)
			if hasPartIndexLeadBeforeToken(tokens, i) {
				continue
			}
			if !episodeOfTotalPattern.MatchString(leftRaw) {
				continue
			}
			if sep != "-" && sep != "~" && !isEpisodeRangeWordConnector(sep) {
				continue
			}
		}
		inBracket := isInBracket(tokens, i)
		inParen := isInParen(tokens, i)
		if m := episodeCountPackSuffixPattern.FindStringSubmatch(strings.TrimSpace(tokens[i].Text)); len(m) == 3 {
			n := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
			if n == "" {
				n = "0"
			}
			if n != "0" {
				return "1-" + n, i
			}
		}
		if inBracket {
			if txt := bracketBlockInnerText(tokens, i); txt != "" {
				if isLikelyMetaBracketText(txt) && !isEpisodeLikeBracketText(txt) {
					continue
				}
				if m := reDetectEpisodeRange1.FindStringSubmatch(strings.TrimSpace(txt)); len(m) == 3 {
					l := strings.TrimLeft(m[1], "0")
					if l == "" {
						l = "0"
					}
					r := strings.TrimLeft(m[2], "0")
					if r == "" {
						r = "0"
					}
					if l != "0" && r != "0" {
						return l + "-" + r, i
					}
				}
			}
		}
		if inBracket {
			// Guard: avoid extracting episode_range from bracket content when the range is preceded
			// by title text (e.g. "[藤本树 17-26]").
			if isTitlePrefixedDigitRangeInBracket(tokens, i) {
				continue
			}
		}
		if inParen {
			// Guard: avoid extracting episode_range from paren content when the range is preceded
			// by title text (e.g. "(藤本树 17-26)").
			if isTitlePrefixedDigitRangeInParen(tokens, i) {
				continue
			}
		}
		if episodeRangePattern.MatchString(tokens[i].Text) {
			if hasPartIndexLeadBeforeToken(tokens, i) {
				continue
			}
			m := episodeRangePattern.FindStringSubmatch(tokens[i].Text)
			if len(m) == 3 {
				if len(m[1]) == 4 && len(m[2]) == 4 {
					if y1, err1 := strconv.Atoi(m[1]); err1 == nil {
						if y2, err2 := strconv.Atoi(m[2]); err2 == nil {
							if y1 >= 1900 && y1 <= 2099 && y2 >= 1900 && y2 <= 2099 {
								continue
							}
						}
					}
				}
			}
			return normalizeEpisodeRangeText(tokens[i].Text), i
		}
		if m := episodeRangeEmbeddedPattern.FindStringSubmatch(tokens[i].Text); len(m) == 3 {
			if hasPartIndexLeadBeforeToken(tokens, i) {
				continue
			}
			if len(m[1]) == 4 && len(m[2]) == 4 {
				if y1, err1 := strconv.Atoi(m[1]); err1 == nil {
					if y2, err2 := strconv.Atoi(m[2]); err2 == nil {
						if y1 >= 1900 && y1 <= 2099 && y2 >= 1900 && y2 <= 2099 {
							continue
						}
					}
				}
			}
			return normalizeEpisodeRangeText(m[1] + "-" + m[2]), i
		}
		if i+2 < len(tokens) {
			leftRaw := strings.TrimSpace(tokens[i].Text)
			sep := strings.TrimSpace(tokens[i+1].Text)
			rightRaw := strings.TrimSpace(tokens[i+2].Text)
			if sep == "-" || sep == "~" {
				leftHasMarker := strings.HasPrefix(leftRaw, "第")
				rightHasMarker := strings.HasSuffix(rightRaw, "集") || strings.HasSuffix(rightRaw, "话") || strings.HasSuffix(rightRaw, "話") || strings.HasSuffix(rightRaw, "回") || strings.HasSuffix(rightRaw, "章")
				if leftHasMarker || rightHasMarker {
					lraw := leftRaw
					if lm := episodeRangeLeftCJKPattern.FindStringSubmatch(lraw); len(lm) == 2 {
						lraw = strings.TrimSpace(lm[1])
					}
					rraw := rightRaw
					if rm := episodeRangeRightCJKPattern.FindStringSubmatch(rraw); len(rm) == 2 {
						rraw = strings.TrimSpace(rm[1])
					} else if mm := leadingDigitsPattern.FindStringSubmatch(rraw); len(mm) == 2 {
						rraw = strings.TrimSpace(mm[1])
					}
					if episodeOfTotalPattern.MatchString(lraw) && episodeOfTotalPattern.MatchString(rraw) {
						l := strings.TrimLeft(lraw, "0")
						if l == "" {
							l = "0"
						}
						r := strings.TrimLeft(rraw, "0")
						if r == "" {
							r = "0"
						}
						if l != "0" && r != "0" {
							if len(lraw) == 4 && len(rraw) == 4 {
								if y1, err1 := strconv.Atoi(lraw); err1 == nil {
									if y2, err2 := strconv.Atoi(rraw); err2 == nil {
										if y1 >= 1900 && y1 <= 2099 && y2 >= 1900 && y2 <= 2099 {
											continue
										}
									}
								}
							}
							return l + "-" + r, i
						}
					}
				}
			}
			sep = strings.TrimSpace(tokens[i+1].Text)
			if episodeOfTotalPattern.MatchString(strings.TrimSpace(tokens[i].Text)) && (sep == "-" || sep == "~" || isEpisodeRangeWordConnector(sep)) {
				if hasPartIndexLeadBeforeToken(tokens, i) {
					continue
				}
				rawR := strings.TrimSpace(tokens[i+2].Text)
				if episodeOfTotalPattern.MatchString(rawR) {
					// Plain text range like "... - 241-248 ..."
					// Keep a year guard to avoid interpreting year spans as episodes.
					if !inBracket && !inParen {
						leftRaw := strings.TrimSpace(tokens[i].Text)
						if len(leftRaw) == 4 && len(rawR) == 4 {
							if y1, err1 := strconv.Atoi(leftRaw); err1 == nil {
								if y2, err2 := strconv.Atoi(rawR); err2 == nil {
									if y1 >= 1900 && y1 <= 2099 && y2 >= 1900 && y2 <= 2099 {
										continue
									}
								}
							}
						}
						l := strings.TrimLeft(leftRaw, "0")
						if l == "" {
							l = "0"
						}
						r := strings.TrimLeft(rawR, "0")
						if r == "" {
							r = "0"
						}
						return l + "-" + r, i
					}
					if inBracket {
						if isTitlePrefixedDigitRangeInBracket(tokens, i) {
							continue
						}
						l := strings.TrimLeft(strings.TrimSpace(tokens[i].Text), "0")
						if l == "" {
							l = "0"
						}
						r := strings.TrimLeft(rawR, "0")
						if r == "" {
							r = "0"
						}
						if len(strings.TrimSpace(tokens[i].Text)) == 4 && len(rawR) == 4 {
							if y1, err1 := strconv.Atoi(strings.TrimSpace(tokens[i].Text)); err1 == nil {
								if y2, err2 := strconv.Atoi(rawR); err2 == nil {
									if y1 >= 1900 && y1 <= 2099 && y2 >= 1900 && y2 <= 2099 {
										continue
									}
								}
							}
						}
						return l + "-" + r, i
					}
					if inParen {
						if isTitlePrefixedDigitRangeInParen(tokens, i) {
							continue
						}
						l := strings.TrimLeft(strings.TrimSpace(tokens[i].Text), "0")
						if l == "" {
							l = "0"
						}
						r := strings.TrimLeft(rawR, "0")
						if r == "" {
							r = "0"
						}
						if len(strings.TrimSpace(tokens[i].Text)) == 4 && len(rawR) == 4 {
							if y1, err1 := strconv.Atoi(strings.TrimSpace(tokens[i].Text)); err1 == nil {
								if y2, err2 := strconv.Atoi(rawR); err2 == nil {
									if y1 >= 1900 && y1 <= 2099 && y2 >= 1900 && y2 <= 2099 {
										continue
									}
								}
							}
						}
						return l + "-" + r, i
					}
					continue
				}
				if m := leadingDigitsPattern.FindStringSubmatch(rawR); len(m) == 2 {
					l := strings.TrimLeft(strings.TrimSpace(tokens[i].Text), "0")
					if l == "" {
						l = "0"
					}
					r := strings.TrimLeft(m[1], "0")
					if r == "" {
						r = "0"
					}
					return l + "-" + r, i
				}
			}
		}
	}
	return "", -1
}

func isEpisodeRangeWordConnector(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "a", "à":
		return true
	default:
		return false
	}
}

func normalizeEpisodeRangeText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	m := episodeRangeEmbeddedPattern.FindStringSubmatch(s)
	if len(m) != 3 {
		return s
	}
	a := strings.TrimLeft(m[1], "0")
	if a == "" {
		a = "0"
	}
	b := strings.TrimLeft(m[2], "0")
	if b == "" {
		b = "0"
	}
	return a + "-" + b
}

func extractTitleFromSpanAllowRemoved(tokens []model.Token, start int, end int, allowRemoved bool) string {
	if start < 0 {
		start = 0
	}
	if end > len(tokens) {
		end = len(tokens)
	}
	if start >= end {
		return ""
	}
	parts := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		if tokens[i].Removed && !allowRemoved {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if t == "[" || t == "]" || t == "【" || t == "】" {
			continue
		}
		if t == "(" || t == ")" || t == "（" || t == "）" {
			continue
		}
		if t == "|" || t == "｜" {
			continue
		}
		parts = append(parts, t)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

var episodePattern = regexp.MustCompile(`(?i)^(?:(e|ep|第))?(\d{1,4})(?:\.(\d{1,2}))?(?:[vV]\d{1,2})?$`)
var episodeWithParenSuffixPattern = regexp.MustCompile(`(?i)^(?:(e|ep|第))?(\d{1,4})(?:\.(\d{1,2}))?[（(][^）)]{1,32}[）)]$`)
var episodeVersionSuffixPattern = regexp.MustCompile(`(?i)^(?:\d{1,4}(?:\.\d{1,2})?)[vV]\d{1,2}$`)
var chineseEpisodeSuffixPattern = regexp.MustCompile(`^(?:第)?(\d{1,4})(?:话|集)$`)
var chineseEpisodeWithSuffixMarkerPattern = regexp.MustCompile(`^(?:第)?(\d{1,4})(?:话|集|話)[①②③④⑤⑥⑦⑧⑨⑩⑪⑫⑬⑭⑮⑯⑰⑱⑲⑳]$`)
var chineseEpisodeLeadingPattern = regexp.MustCompile(`^(?:第)?(\d{1,4})(?:话|集|話)(?:$|(?:\s|[&＆(（\[【]).*)`)
var embeddedChineseEpisodeSuffixPattern = regexp.MustCompile(`第(\d{1,4})(?:集|话|話)$`)
var yearPattern = regexp.MustCompile(`^(?:19|20)\d{2}$`)
var starEpisodeEmbeddedPattern = regexp.MustCompile(`(?:★|☆)(\d{1,4})(?:★|☆)`)
var splitDecimalEpisodeRightPattern = regexp.MustCompile(`^\d{1,2}$`)
var dateYMDPattern = regexp.MustCompile(`(?i)(?:\b(?:19|20)\d{2}[./-](?:0?[1-9]|1[0-2])[./-](?:0?[1-9]|[12]\d|3[01])\b)`)
var spKindEpisodePattern = regexp.MustCompile(`(?i)^(?:sp|ova|oad|ona|ex|ncop|nced|pv)0*(\d{1,3})$`)
var episodeWithVersionPattern = regexp.MustCompile(`(?i)^(?:(?:e|ep|第))?(\d{1,4})(?:\.(\d{1,2}))?[vV](\d{1,2})$`)
var episodeEndSuffixPattern = regexp.MustCompile(`(?i)^(\d{1,4})(end|fin|final)$`)
var standaloneVersionPattern = regexp.MustCompile(`(?i)^v(\d{1,2})$`)
var regionHKMOTWEmbeddedPattern = regexp.MustCompile(`(?i)(?:仅限港澳台地区|僅限港澳台地區)`)
var regionHKMOTWRemovalPattern = regexp.MustCompile(`(?i)[(（]?\s*(?:仅限港澳台地区|僅限港澳台地區)\s*[)）]?`)
var bracketScheduleNoticeDatePattern = regexp.MustCompile(`\d{1,2}[./]\d{1,2}`)

func isBracketScheduleNoticeText(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return false
	}
	if !strings.Contains(ss, "停播") && !strings.Contains(ss, "停更") {
		return false
	}
	if bracketScheduleNoticeDatePattern.MatchString(ss) || dateYMDPattern.MatchString(ss) {
		return true
	}
	if strings.Contains(ss, "，") || strings.Contains(ss, ",") || strings.Contains(ss, "。") || strings.Contains(ss, ";") || strings.Contains(ss, "；") {
		return true
	}
	return false
}

func isEpisodeBoundaryRightToken(t string) bool {
	tt := strings.TrimSpace(t)
	if tt == "" {
		return false
	}
	switch tt {
	case "-", "[", "【", "(", "（":
		return true
	}
	if standaloneVersionPattern.MatchString(tt) {
		return true
	}
	up := strings.ToUpper(tt)
	return up == "END" || up == "FIN" || up == "FINAL"
}

func firstMeaningfulTokenInRange(tokens []model.Token, start int, end int) (int, string) {
	if start < 0 {
		start = 0
	}
	if end > len(tokens) {
		end = len(tokens)
	}
	for i := start; i < end; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		return i, t
	}
	return -1, ""
}

func isLikelyBracketTitleWordToken(t string) bool {
	t = strings.TrimSpace(t)
	if t == "" {
		return false
	}
	if t == "-" || t == "~" || t == ":" || t == "：" || t == "|" || t == "｜" || t == "/" || t == "／" {
		return false
	}
	if episodeOfTotalPattern.MatchString(t) {
		return false
	}
	if standaloneVersionPattern.MatchString(t) {
		return false
	}
	up := strings.ToUpper(t)
	if up == "END" || up == "FIN" || up == "FINAL" || up == "V2" || up == "V3" {
		return false
	}
	if looksLikeTitleCutMetaToken(t) || isLikelyMetaBracketText(t) {
		return false
	}
	return hasCJK(t) || hasLatin(t)
}

// Guard title-leading numeric tokens inside bracket title spans:
// [16 位元的輝煌 ...], [86-不存在的戰區] should stay in title, not become episode primary.
func isBracketLeadingNumericTitlePrefix(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	cur := strings.TrimSpace(tokens[idx].Text)
	if !episodeOfTotalPattern.MatchString(cur) {
		return false
	}
	openIdx, closeIdx, ok := bracketBoundsAt(tokens, idx)
	if !ok {
		return false
	}
	for j := openIdx + 1; j < idx; j++ {
		if tokens[j].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[j].Text)
		if t == "" || t == "-" || t == "~" || t == ":" || t == "：" {
			continue
		}
		return false
	}
	n1Idx, n1 := firstMeaningfulTokenInRange(tokens, idx+1, closeIdx)
	if n1Idx < 0 {
		return false
	}
	if n1 == "-" || n1 == "~" {
		_, n2 := firstMeaningfulTokenInRange(tokens, n1Idx+1, closeIdx)
		if n2 == "" || episodeOfTotalPattern.MatchString(strings.TrimSpace(n2)) {
			return false
		}
		return isLikelyBracketTitleWordToken(n2)
	}
	return isLikelyBracketTitleWordToken(n1)
}

func hasUpperASCII(s string) bool {
	for _, r := range s {
		if 'A' <= r && r <= 'Z' {
			return true
		}
	}
	return false
}

func looksLikeDateOrTimeText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "整理时间") || strings.Contains(low, "整理時間") {
		return true
	}
	compact := reLooksLikeDateOrTimeText1.ReplaceAllString(low, "$1")
	if dateYMDPattern.MatchString(low) || dateYMDPattern.MatchString(compact) {
		return true
	}
	return false
}

func looksLikeCodecNumber(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	cur := strings.TrimSpace(tokens[idx].Text)
	if cur != "264" && cur != "265" {
		return false
	}
	if idx-1 >= 0 {
		p := strings.ToLower(strings.TrimSpace(tokens[idx-1].Text))
		if p == "h" || p == "x" {
			return true
		}
		if p == "." && idx-2 >= 0 {
			pp := strings.ToLower(strings.TrimSpace(tokens[idx-2].Text))
			if pp == "h" {
				return true
			}
		}
	}
	return false
}

func looksLikeCodecPrefixToken(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if low == "" {
		return false
	}
	switch low {
	case "hevc", "avc", "h264", "h265", "h266", "x264", "x265", "av1", "mpeg2", "mpeg4", "xvid", "divx":
		return true
	default:
		return false
	}
}

func looksLikeBracketCodecNumber(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	if !isInBracket(tokens, idx) {
		return false
	}
	cur := strings.TrimSpace(tokens[idx].Text)
	if !episodeOfTotalPattern.MatchString(cur) {
		return false
	}
	if idx-1 >= 0 {
		prev := strings.TrimSpace(tokens[idx-1].Text)
		if prev == "-" && idx-2 >= 0 {
			if looksLikeCodecPrefixToken(strings.TrimSpace(tokens[idx-2].Text)) {
				return true
			}
		}
	}
	return false
}

var episodeSPBracketCombinedPattern = regexp.MustCompile(`(?i)^0*(\d{1,3})\s*[_ .-]\s*(sp|ova|oad|ona|ex|pv|op|ed)\s*$`)
var partIndexLeadPattern = regexp.MustCompile(`(?i)^(?:part|pt|cour|cours)$`)
var seasonSignalTokenPattern = regexp.MustCompile(`(?i)^(?:s\d{1,2}|season|saison|cour|cours)$`)
var seasonSignalCJKTokenPattern = regexp.MustCompile(`^第?[一二三四五六七八九十0-9]{1,3}(?:季|期|部)$`)
var episodePrefixedTokenPattern = regexp.MustCompile(`(?i)^(?:e|ep)\d{1,4}(?:\.\d{1,2})?(?:[vV]\d{1,2})?$`)
var chineseEpisodeDecimalSuffixPattern = regexp.MustCompile(`^第?(\d{1,4})\.(\d{1,2})(?:集|话|話|回)$`)
var chineseEpisodeDecimalTailPattern = regexp.MustCompile(`^(\d{1,2})(?:集|话|話|回)$`)
var chineseEpisodeDecimalHeadPattern = regexp.MustCompile(`^第(\d{1,4})$`)
var bracketCatalogIDPrefixPattern = regexp.MustCompile(`^[A-Z]{3,}[A-Z0-9]*$`)

func hasDashSeparatorAroundToken(tokens []model.Token, idx int) bool {
	if idx-1 >= 0 {
		sep := strings.TrimSpace(tokens[idx-1].Text)
		if sep == "-" || sep == "~" {
			return true
		}
	}
	if idx+1 < len(tokens) {
		sep := strings.TrimSpace(tokens[idx+1].Text)
		if sep == "-" || sep == "~" {
			return true
		}
	}
	return false
}

func isStandaloneNumericBracketBlock(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	if !isInBracket(tokens, idx) {
		return false
	}
	openIdx, closeIdx, ok := bracketBoundsAt(tokens, idx)
	if !ok {
		return false
	}
	found := false
	for j := openIdx + 1; j < closeIdx; j++ {
		if tokens[j].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[j].Text)
		if t == "" {
			continue
		}
		if j == idx && episodeOfTotalPattern.MatchString(t) {
			found = true
			continue
		}
		return isDualEpisodeBracketBlockLead(tokens, idx, openIdx, closeIdx)
	}
	return found
}

// isUnderscoreDualEpisodeBracket reports whether the bracket block between
// openIdx and closeIdx is exactly "N_M" read as a season episode and its
// overall episode (see isUnderscoreDualEpisodePair).
func isUnderscoreDualEpisodeBracket(tokens []model.Token, openIdx, closeIdx int) bool {
	inner := make([]string, 0, 3)
	for j := openIdx + 1; j < closeIdx && j < len(tokens); j++ {
		if t := strings.TrimSpace(tokens[j].Text); t != "" {
			inner = append(inner, t)
		}
	}
	return len(inner) == 3 && inner[1] == "_" && isUnderscoreDualEpisodePair(tokens, inner[0], inner[2])
}

// isUnderscoreDualEpisodePair accepts "N_M" as season episode N and overall
// episode M only with structural evidence: an explicit season above 1 and an
// overall episode past the season episode ("S3 ... [03_51]"). Without it the
// pair could be a range such as "[01_12]", so it is not read as one.
func isUnderscoreDualEpisodePair(tokens []model.Token, lead, overall string) bool {
	if !episodeOfTotalPattern.MatchString(lead) || !episodeOfTotalPattern.MatchString(overall) {
		return false
	}
	season, err := strconv.Atoi(explicitSeasonNumberInTokens(tokens))
	if err != nil || season <= 1 {
		return false
	}
	l, _ := strconv.Atoi(lead)
	o, _ := strconv.Atoi(overall)
	return o > l
}

// explicitSeasonNumberInTokens returns the first explicit season marker value
// ("S3", "S3E..", "第三季") in the token stream, or "".
func explicitSeasonNumberInTokens(tokens []model.Token) string {
	for _, tk := range tokens {
		txt := strings.TrimSpace(tk.Text)
		if m := seasonOnlyPattern.FindStringSubmatch(txt); len(m) == 2 {
			return strings.TrimLeft(m[1], "0")
		}
		if m := seasonEmbeddedPattern.FindStringSubmatch(txt); len(m) == 2 {
			return strings.TrimLeft(m[1], "0")
		}
		if m := seasonCJKPattern.FindStringSubmatch(txt); len(m) == 2 {
			if v := parseSmallSeasonNumberToken(m[1]); v != "" {
				return v
			}
		}
	}
	return ""
}

// isDualEpisodeBracketBlockLead reports whether idx is the leading number of a
// bracket block that holds nothing but a season episode and its overall
// episode, e.g. "[03(51)]" or "[03_51]". The lead is an episode even when it
// echoes the season number.
func isDualEpisodeBracketBlockLead(tokens []model.Token, idx, openIdx, closeIdx int) bool {
	inner := make([]string, 0, 4)
	leadPos := -1
	for j := openIdx + 1; j < closeIdx; j++ {
		t := strings.TrimSpace(tokens[j].Text)
		if t == "" {
			continue
		}
		if j == idx {
			leadPos = len(inner)
		}
		inner = append(inner, t)
	}
	if leadPos != 0 || !episodeOfTotalPattern.MatchString(inner[0]) {
		return false
	}
	switch len(inner) {
	case 3:
		return inner[1] == "_" && isUnderscoreDualEpisodePair(tokens, inner[0], inner[2])
	case 4:
		return (inner[1] == "(" && inner[3] == ")" || inner[1] == "（" && inner[3] == "）") && episodeOfTotalPattern.MatchString(inner[2])
	}
	return false
}

func looksLikeVolumeContextNumberToken(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	cur := strings.TrimSpace(tokens[idx].Text)
	if !episodeOfTotalPattern.MatchString(cur) {
		return false
	}
	if idx-1 >= 0 {
		prev := strings.TrimSpace(tokens[idx-1].Text)
		if volumeWordPattern.MatchString(prev) {
			return true
		}
	}
	if idx+1 < len(tokens) {
		next := strings.TrimSpace(tokens[idx+1].Text)
		if volumeWordPattern.MatchString(next) {
			return true
		}
	}
	if idx-2 >= 0 {
		prevSep := strings.TrimSpace(tokens[idx-1].Text)
		prevLead := strings.TrimSpace(tokens[idx-2].Text)
		if prevSep == "." && volumeWordPattern.MatchString(prevLead) {
			return true
		}
	}
	if idx+2 < len(tokens) {
		nextSep := strings.TrimSpace(tokens[idx+1].Text)
		nextLead := strings.TrimSpace(tokens[idx+2].Text)
		if nextSep == "." && volumeWordPattern.MatchString(nextLead) {
			return true
		}
	}
	return false
}

func detectEpisodeFromSPBracketSegment(tokens []model.Token) (string, int, bool) {
	for i := 0; i < len(tokens); i++ {
		if !isInBracket(tokens, i) {
			continue
		}
		cur := strings.TrimSpace(tokens[i].Text)
		if cur == "" {
			continue
		}
		if mm := episodeSPBracketCombinedPattern.FindStringSubmatch(cur); len(mm) == 3 {
			n := strings.TrimLeft(strings.TrimSpace(mm[1]), "0")
			if n == "" {
				n = "0"
			}
			return n, i, true
		}
		if !episodeOfTotalPattern.MatchString(cur) || len(cur) > 3 {
			continue
		}
		if i+2 >= len(tokens) {
			continue
		}
		if !isInBracket(tokens, i+1) || !isInBracket(tokens, i+2) {
			continue
		}
		sep := strings.TrimSpace(tokens[i+1].Text)
		if sep != "_" && sep != "-" && sep != "." && sep != "&" && sep != "＆" {
			continue
		}
		next := strings.TrimSpace(tokens[i+2].Text)
		if m := spKindPattern.FindStringSubmatch(next); len(m) < 2 {
			continue
		}
		n := strings.TrimLeft(cur, "0")
		if n == "" {
			n = "0"
		}
		return n, i, true
	}
	return "", -1, false
}

// detectEpisodeWithVersion orchestrates the episode-with-version scan. The
// scanning state and the per-pattern logic live in ep_scan_version.go; each
// scan* method returning true mirrors a `continue` of this loop.
func detectEpisodeWithVersion(tokens []model.Token) (string, int, string, int) {
	s := newEpVersionScan(tokens)
	s.scanEpisodeKeyword()
	for i := 0; i < len(tokens); i++ {
		inBracket := isInBracket(tokens, i)
		if s.skip(i, inBracket) {
			continue
		}
		s.scanSubMarkerEpisode(i)
		if s.scanVersionSuffixedEpisode(i) {
			continue
		}
		if s.scanParenSuffixEpisode(i) {
			continue
		}
		if s.scanCompactEndFlagEpisode(i) {
			continue
		}
		if s.scanSplitEndFlagEpisode(i, inBracket) {
			continue
		}
		if s.scanBracketNumericEpisode(i, inBracket) {
			continue
		}
		if s.scanSPKindEpisode(i) {
			continue
		}
		if s.scanStarEmbeddedEpisode(i) {
			continue
		}
		if s.scanEmbeddedChineseSuffixEpisode(i) {
			continue
		}
		if s.scanSplitChineseEpisode(i) {
			continue
		}
		if s.scanChineseSuffixEpisode(i) {
			continue
		}
		if s.scanChineseSuffixMarkerEpisode(i) {
			continue
		}
		if s.scanChineseLeadingEpisode(i) {
			continue
		}
		if s.scanChineseDecimalSuffixEpisode(i) {
			continue
		}
		if s.scanSplitChineseDecimalEpisode(i) {
			continue
		}
		if s.scanSplitDecimalEpisode(i, inBracket) {
			continue
		}
		s.scanGenericEpisode(i, inBracket)
	}
	return s.finish()
}
func shouldSkipPartIndexEpisodeCandidate(tokens []model.Token, idx int, inBracket bool) bool {
	if inBracket || idx <= 0 || idx >= len(tokens) {
		return false
	}
	cur := strings.TrimSpace(tokens[idx].Text)
	if !episodeOfTotalPattern.MatchString(cur) {
		return false
	}
	prev := strings.TrimSpace(tokens[idx-1].Text)
	if prev == "" {
		return false
	}
	if !partIndexLeadPattern.MatchString(prev) {
		return false
	}
	return hasSeasonSignalAroundToken(tokens, idx, 6)
}

func hasSeasonSignalAroundToken(tokens []model.Token, idx int, window int) bool {
	if len(tokens) == 0 || idx < 0 || idx >= len(tokens) {
		return false
	}
	start := idx - window
	if start < 0 {
		start = 0
	}
	end := idx + window
	if end >= len(tokens) {
		end = len(tokens) - 1
	}
	for i := start; i <= end; i++ {
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if seasonSignalTokenPattern.MatchString(txt) || seasonSignalCJKTokenPattern.MatchString(txt) {
			return true
		}
	}
	return false
}

func hasPartIndexLeadBeforeToken(tokens []model.Token, idx int) bool {
	if idx <= 0 || idx >= len(tokens) {
		return false
	}
	prev := strings.TrimSpace(tokens[idx-1].Text)
	if prev == "" {
		return false
	}
	return partIndexLeadPattern.MatchString(prev)
}

func hasMatchingSeasonNumberAroundToken(tokens []model.Token, idx int, window int) bool {
	if len(tokens) == 0 || idx < 0 || idx >= len(tokens) {
		return false
	}
	cur := strings.TrimLeft(strings.TrimSpace(tokens[idx].Text), "0")
	if cur == "" {
		cur = "0"
	}
	if !episodeOfTotalPattern.MatchString(cur) && cur != "0" {
		return false
	}
	start := idx - window
	if start < 0 {
		start = 0
	}
	end := idx + window
	if end >= len(tokens) {
		end = len(tokens) - 1
	}
	matchSeasonVal := func(v string) bool {
		v = strings.TrimLeft(strings.TrimSpace(v), "0")
		if v == "" {
			v = "0"
		}
		return v == cur
	}
	for i := start; i <= end; i++ {
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if m := seasonOnlyPattern.FindStringSubmatch(txt); len(m) == 2 && matchSeasonVal(m[1]) {
			return true
		}
		if m := seasonEmbeddedPattern.FindStringSubmatch(txt); len(m) == 2 && matchSeasonVal(m[1]) {
			return true
		}
		if m := seasonCJKPattern.FindStringSubmatch(txt); len(m) == 2 {
			if v := parseSmallSeasonNumberToken(m[1]); v != "" && matchSeasonVal(v) {
				return true
			}
		}
		if m := seasonOrdinalPattern.FindStringSubmatch(txt); len(m) == 2 {
			if i+1 < len(tokens) && seasonWordLoosePattern.MatchString(strings.TrimSpace(tokens[i+1].Text)) && matchSeasonVal(m[1]) {
				return true
			}
		}
		if seasonWordLoosePattern.MatchString(txt) {
			if i+1 < len(tokens) {
				if v := parseSmallSeasonNumberToken(strings.TrimSpace(tokens[i+1].Text)); v != "" && matchSeasonVal(v) {
					return true
				}
			}
		}
	}
	return false
}

func hasLaterBracketEpisodeCandidate(tokens []model.Token, from int) bool {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if !isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" || !episodeOfTotalPattern.MatchString(txt) {
			continue
		}
		if looksLikeVolumeContextNumberToken(tokens, i) || yearPattern.MatchString(txt) {
			continue
		}
		if isTitlePrefixedDigitRangeInBracket(tokens, i) {
			continue
		}
		if hasMatchingSeasonNumberAroundToken(tokens, i, 12) && !hasDashSeparatorAroundToken(tokens, i) && !isStandaloneNumericBracketBlock(tokens, i) {
			continue
		}
		return true
	}
	return false
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reExtractSeasonKeywordValue1 = regexp.MustCompile(`^第?([一二三四五六七八九十0-9]{1,3})期$`)
	reExtractSeasonKeywordValue2 = regexp.MustCompile(`^(\d{1,2})期$`)
	reExtractSeasonKeywordValue3 = regexp.MustCompile(`^第?([一二三四五六七八九十0-9]{1,3})季.+$`)
	reExtractSeasonKeywordValue4 = regexp.MustCompile(`^第?([一二三四五六七八九十0-9]{1,3})期.+$`)
	reDetectEpisodeRange1        = regexp.MustCompile(`(?i)^第?(\d{1,4})\s*[-~]\s*(\d{1,4})(?:集|话|話|回|章)(?:\s|$)`)
	reLooksLikeDateOrTimeText1   = regexp.MustCompile(`\s*([./-])\s*`)
	reDetectEpisodeWithVersion1  = regexp.MustCompile(`^\d{1,4}$`)
)
