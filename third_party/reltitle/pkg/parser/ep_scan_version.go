package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// epVersionScan carries the mutable state of detectEpisodeWithVersion: the
// token stream plus the running best episode candidate. Each scan* method
// handles one episode pattern; a true return mirrors a `continue` of the
// outer scan loop in the original monolithic function (the token is handled,
// move to the next one), while false means the pattern did not match and the
// next pattern should be tried. Methods are invoked in a fixed priority
// order by detectEpisodeWithVersion; this file only holds the pieces.
type epVersionScan struct {
	tokens []model.Token

	best               string
	bestIdx            int
	bestScore          int
	bestVer            string
	bestConsumedEnd    int
	bestKeywordIdx     int
	bestKeywordDashIdx int
}

func newEpVersionScan(tokens []model.Token) *epVersionScan {
	return &epVersionScan{
		tokens:             tokens,
		bestIdx:            -1,
		bestScore:          -1,
		bestConsumedEnd:    -1,
		bestKeywordIdx:     -1,
		bestKeywordDashIdx: -1,
	}
}

// considerCandidate records the candidate when it beats the running best and
// reports whether it won, so callers can update pattern-specific extra state
// (bestVer / bestConsumedEnd) only on a win, exactly like the original
// inline `if score > bestScore` blocks.
func (s *epVersionScan) considerCandidate(score, idx int, n string) bool {
	if score > s.bestScore {
		s.bestScore = score
		s.bestIdx = idx
		s.best = n
		return true
	}
	return false
}

// scanEpisodeKeyword handles the "Episode 52" / "- Episode 52" keyword
// pattern (common in English TV rip naming). It runs as its own pass before
// the main scan loop.
func (s *epVersionScan) scanEpisodeKeyword() {
	tokens := s.tokens
	for i := 0; i+1 < len(tokens); i++ {
		if epScanSkipped(tokens, i) || epScanSkipped(tokens, i+1) {
			continue
		}
		kw := strings.TrimSpace(tokens[i].Text)
		if !(strings.EqualFold(kw, "episode") || strings.EqualFold(kw, "ep")) {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		d := strings.TrimSpace(tokens[i+1].Text)
		if !episodeOfTotalPattern.MatchString(d) {
			continue
		}
		if yearPattern.MatchString(d) {
			continue
		}
		score := 74
		dashIdx := -1
		// Prefer the explicit "- Episode N" style, but do not require it.
		if i-1 >= 0 {
			prev := strings.TrimSpace(tokens[i-1].Text)
			if prev == "-" {
				score += 4
				dashIdx = i - 1
			}
		}
		n := strings.TrimLeft(d, "0")
		if n == "" {
			n = "0"
		}
		if s.considerCandidate(score, i+1, n) {
			s.bestConsumedEnd = i + 1
			s.bestKeywordIdx = i
			s.bestKeywordDashIdx = dashIdx
		}
	}
}

// skip reports whether token i is ineligible for the main scan loop:
// protected/removed tokens, title-span digit ranges, meta brackets, parenthesized
// text, codec numbers, dotted sequences, tech suffixes, and years are never
// episode candidates.
func (s *epVersionScan) skip(i int, inBracket bool) bool {
	tokens := s.tokens
	if epScanSkipped(tokens, i) {
		// Protected = known-title span: numbers inside are title text,
		// not episode numbers. Removed = already consumed elsewhere.
		return true
	}
	if isTitlePrefixedDigitRangeInBracket(tokens, i) {
		return true
	}
	if isRightSideOfPlainDigitRangeInTitle(tokens, i) {
		return true
	}
	if isRightSideOfSharedPrefixSeasonEpisodeRange(tokens, i) {
		return true
	}
	if inBracket {
		if txt := bracketBlockInnerText(tokens, i); txt != "" {
			if isBracketScheduleNoticeText(txt) {
				return true
			}
			if isLikelyMetaBracketText(txt) && !isEpisodeLikeBracketText(txt) {
				return true
			}
		}
		if isBracketLeadingNumericTitlePrefix(tokens, i) {
			return true
		}
	}
	if isInParen(tokens, i) {
		return true
	}
	if looksLikeCodecNumber(tokens, i) {
		return true
	}
	if looksLikeDottedNumericSequence(tokens, i) {
		return true
	}
	if looksLikeDashSeparatedTechSuffix(tokens, i) {
		return true
	}
	if yearPattern.MatchString(tokens[i].Text) {
		return true
	}
	return false
}

// scanSubMarkerEpisode handles "... 06 Sub ..." where the episode number is
// followed by a subtitle marker. Note: earlier passes (tech_subs) may mark the
// following "Sub" token as Removed, but we still want to detect the episode
// boundary. This pattern never consumes the token by itself: scanning falls
// through to the remaining patterns afterwards.
func (s *epVersionScan) scanSubMarkerEpisode(i int) {
	tokens := s.tokens
	if i+1 < len(tokens) {
		next := strings.TrimSpace(tokens[i+1].Text)
		if strings.EqualFold(next, "sub") || strings.EqualFold(next, "subs") {
			cur := strings.TrimSpace(tokens[i].Text)
			if episodeOfTotalPattern.MatchString(cur) && !yearPattern.MatchString(cur) {
				n := strings.TrimLeft(cur, "0")
				if n == "" {
					n = "0"
				}
				if n != "0" {
					score := len(cur) + 92
					if s.considerCandidate(score, i, n) {
						s.bestVer = ""
					}
				}
			}
		}
	}
}

// scanVersionSuffixedEpisode supports version-suffixed episode tokens like
// "02v2" / "12.5v2".
func (s *epVersionScan) scanVersionSuffixedEpisode(i int) bool {
	tokens := s.tokens
	mm := episodeWithVersionPattern.FindStringSubmatch(strings.TrimSpace(tokens[i].Text))
	if len(mm) != 4 {
		return false
	}
	n := strings.TrimLeft(mm[1], "0")
	if n == "" {
		n = "0"
	}
	if strings.TrimSpace(mm[2]) != "" {
		n = n + "." + strings.TrimSpace(mm[2])
	}
	ver := strings.TrimLeft(mm[3], "0")
	if ver == "" {
		ver = mm[3]
	}
	score := len(mm[1]) + 97
	if s.considerCandidate(score, i, n) {
		s.bestVer = "v" + ver
	}
	return true
}

// scanParenSuffixEpisode supports episode tokens with a trailing parenthetical
// marker: e.g. "03（Onair）" / "03(Premium)".
func (s *epVersionScan) scanParenSuffixEpisode(i int) bool {
	tokens := s.tokens
	mm := episodeWithParenSuffixPattern.FindStringSubmatch(strings.TrimSpace(tokens[i].Text))
	if len(mm) < 4 {
		return false
	}
	if yearPattern.MatchString(strings.TrimSpace(mm[2])) {
		return true
	}
	n := strings.TrimLeft(mm[2], "0")
	if n == "" {
		n = "0"
	}
	if strings.TrimSpace(mm[3]) != "" {
		n = n + "." + strings.TrimSpace(mm[3])
	}
	if n != "0" {
		score := len(mm[2]) + 91
		if s.considerCandidate(score, i, n) {
			s.bestVer = ""
		}
	}
	return true
}

// scanCompactEndFlagEpisode handles compact end-flag episode tokens, e.g.
// "12END"/"12FIN".
func (s *epVersionScan) scanCompactEndFlagEpisode(i int) bool {
	tokens := s.tokens
	mm := episodeEndSuffixPattern.FindStringSubmatch(strings.TrimSpace(tokens[i].Text))
	if len(mm) != 3 {
		return false
	}
	n := strings.TrimLeft(mm[1], "0")
	if n == "" {
		n = "0"
	}
	if n != "0" {
		score := len(mm[1]) + 96
		if s.considerCandidate(score, i, n) {
			s.bestVer = ""
		}
	}
	return true
}

// scanSplitEndFlagEpisode handles split end-flag episode tokens inside a
// bracket block, e.g. "48_END" / "48-FIN".
func (s *epVersionScan) scanSplitEndFlagEpisode(i int, inBracket bool) bool {
	tokens := s.tokens
	if !(inBracket && i+2 < len(tokens) && !tokens[i+1].Removed && !tokens[i+2].Removed) {
		return false
	}
	cur := strings.TrimSpace(tokens[i].Text)
	sep := strings.TrimSpace(tokens[i+1].Text)
	next := strings.ToUpper(strings.TrimSpace(tokens[i+2].Text))
	if !(episodeOfTotalPattern.MatchString(cur) && (sep == "_" || sep == "-") && (next == "END" || next == "FIN" || next == "FINAL")) {
		return false
	}
	n := strings.TrimLeft(cur, "0")
	if n == "" {
		n = "0"
	}
	if n != "0" {
		score := len(cur) + 96
		if s.considerCandidate(score, i, n) {
			s.bestVer = ""
		}
	}
	return true
}

// scanBracketNumericEpisode handles a bare numeric token inside a bracket
// block, e.g. "[01]". It never touches the version field.
func (s *epVersionScan) scanBracketNumericEpisode(i int, inBracket bool) bool {
	tokens := s.tokens
	if !inBracket {
		return false
	}
	if looksLikeBracketCodecNumber(tokens, i) {
		return true
	}
	n := strings.TrimSpace(tokens[i].Text)
	if !episodeOfTotalPattern.MatchString(n) {
		return false
	}
	if looksLikeVolumeContextNumberToken(tokens, i) {
		return true
	}
	if hasMatchingSeasonNumberAroundToken(tokens, i, 12) && !hasDashSeparatorAroundToken(tokens, i) && !isStandaloneNumericBracketBlock(tokens, i) {
		return true
	}
	if i-2 >= 0 {
		prevSep := strings.TrimSpace(tokens[i-1].Text)
		prevLead := strings.TrimSpace(tokens[i-2].Text)
		if prevSep == "-" && bracketCatalogIDPrefixPattern.MatchString(prevLead) {
			return true
		}
	}
	nn := strings.TrimLeft(n, "0")
	if nn == "" {
		nn = "0"
	}
	if isBracketLeadingNumericTitlePrefix(tokens, i) {
		return true
	}
	score := len(n) + 40
	s.considerCandidate(score, i, nn)
	return true
}

// scanSPKindEpisode supports explicit SP-kind episode tokens like
// "OVA02" / "SP01" / "OAD3". These are strong episode markers and should not
// be missed. It never touches the version field.
func (s *epVersionScan) scanSPKindEpisode(i int) bool {
	tokens := s.tokens
	mm := spKindEpisodePattern.FindStringSubmatch(strings.TrimSpace(tokens[i].Text))
	if len(mm) != 2 {
		return false
	}
	nn := strings.TrimLeft(mm[1], "0")
	if nn == "" {
		nn = "0"
	}
	if nn == "0" {
		return false
	}
	score := len(mm[1]) + 98
	s.considerCandidate(score, i, nn)
	return true
}

// scanStarEmbeddedEpisode supports patterns like "...★01★..." where the
// tokenizer keeps the whole token.
func (s *epVersionScan) scanStarEmbeddedEpisode(i int) bool {
	tokens := s.tokens
	mm := starEpisodeEmbeddedPattern.FindStringSubmatch(tokens[i].Text)
	if len(mm) != 2 {
		return false
	}
	nn := strings.TrimLeft(mm[1], "0")
	if nn == "" {
		nn = "0"
	}
	score := len(mm[1]) + 60
	s.considerCandidate(score, i, nn)
	return true
}

// scanEmbeddedChineseSuffixEpisode supports cases like "作品名...第08集" where
// the tokenizer keeps the episode suffix attached.
func (s *epVersionScan) scanEmbeddedChineseSuffixEpisode(i int) bool {
	tokens := s.tokens
	mm := embeddedChineseEpisodeSuffixPattern.FindStringSubmatch(tokens[i].Text)
	if len(mm) != 2 {
		return false
	}
	nn := strings.TrimLeft(mm[1], "0")
	if nn == "" {
		nn = "0"
	}
	if nn == "0" {
		return false
	}
	score := len(mm[1]) + 95
	s.considerCandidate(score, i, nn)
	return true
}

// scanSplitChineseEpisode handles the "第 N 集/话/話" pattern split across
// three tokens.
func (s *epVersionScan) scanSplitChineseEpisode(i int) bool {
	tokens := s.tokens
	if strings.TrimSpace(tokens[i].Text) != "第" {
		return false
	}
	if !(i+2 < len(tokens) && !tokens[i+1].Removed && !tokens[i+2].Removed) {
		return false
	}
	n := strings.TrimSpace(tokens[i+1].Text)
	suf := strings.TrimSpace(tokens[i+2].Text)
	if !(reDetectEpisodeWithVersion1.MatchString(n) && (suf == "集" || suf == "话" || suf == "話")) {
		return false
	}
	nn := strings.TrimLeft(n, "0")
	if nn == "" {
		nn = "0"
	}
	if nn == "0" {
		return false
	}
	score := len(n) + 90
	s.considerCandidate(score, i, nn)
	return true
}

// scanChineseSuffixEpisode handles single tokens with a Chinese episode
// suffix, e.g. "08集". A bare "0集" consumes the token without scoring.
func (s *epVersionScan) scanChineseSuffixEpisode(i int) bool {
	tokens := s.tokens
	mm := chineseEpisodeSuffixPattern.FindStringSubmatch(tokens[i].Text)
	if len(mm) != 2 {
		return false
	}
	n := strings.TrimLeft(mm[1], "0")
	if n == "" {
		n = "0"
	}
	if n == "0" {
		return true
	}
	score := len(mm[1]) + 90
	s.considerCandidate(score, i, n)
	return true
}

// scanChineseSuffixMarkerEpisode handles single tokens with a Chinese episode
// suffix marker; unlike scanChineseSuffixEpisode it scores even "0".
func (s *epVersionScan) scanChineseSuffixMarkerEpisode(i int) bool {
	tokens := s.tokens
	mm := chineseEpisodeWithSuffixMarkerPattern.FindStringSubmatch(tokens[i].Text)
	if len(mm) != 2 {
		return false
	}
	n := strings.TrimLeft(mm[1], "0")
	if n == "" {
		n = "0"
	}
	score := len(mm[1]) + 90
	s.considerCandidate(score, i, n)
	return true
}

// scanChineseLeadingEpisode handles single tokens with a leading Chinese
// episode marker, e.g. "第08".
func (s *epVersionScan) scanChineseLeadingEpisode(i int) bool {
	tokens := s.tokens
	mm := chineseEpisodeLeadingPattern.FindStringSubmatch(tokens[i].Text)
	if len(mm) != 2 {
		return false
	}
	n := strings.TrimLeft(mm[1], "0")
	if n == "" {
		n = "0"
	}
	score := len(mm[1]) + 86
	s.considerCandidate(score, i, n)
	return true
}

// scanChineseDecimalSuffixEpisode handles single tokens with a Chinese
// decimal episode suffix, e.g. "第12.5集".
func (s *epVersionScan) scanChineseDecimalSuffixEpisode(i int) bool {
	tokens := s.tokens
	mm := chineseEpisodeDecimalSuffixPattern.FindStringSubmatch(strings.TrimSpace(tokens[i].Text))
	if len(mm) != 3 {
		return false
	}
	maj := strings.TrimLeft(mm[1], "0")
	if maj == "" {
		maj = "0"
	}
	dec := strings.TrimSpace(mm[2])
	if dec == "" {
		return false
	}
	n := maj + "." + dec
	score := len(mm[1]) + 93
	s.considerCandidate(score, i, n)
	return true
}

// scanSplitChineseDecimalEpisode handles the "第X.Y" decimal episode split
// across three tokens.
func (s *epVersionScan) scanSplitChineseDecimalEpisode(i int) bool {
	tokens := s.tokens
	if i+2 >= len(tokens) {
		return false
	}
	left := strings.TrimSpace(tokens[i].Text)
	dot := strings.TrimSpace(tokens[i+1].Text)
	right := strings.TrimSpace(tokens[i+2].Text)
	if !(strings.HasPrefix(left, "第") && dot == ".") {
		return false
	}
	lm := chineseEpisodeDecimalHeadPattern.FindStringSubmatch(left)
	if len(lm) != 2 {
		return false
	}
	rm := chineseEpisodeDecimalTailPattern.FindStringSubmatch(right)
	if len(rm) != 2 {
		return false
	}
	maj := strings.TrimLeft(lm[1], "0")
	if maj == "" {
		maj = "0"
	}
	dec := strings.TrimSpace(rm[1])
	if dec == "" {
		return false
	}
	n := maj + "." + dec
	score := len(lm[1]) + 93
	s.considerCandidate(score, i, n)
	return true
}

// scanSplitDecimalEpisode handles a decimal episode split across tokens,
// e.g. "12 . 5". A false return means the token falls through to the generic
// episode pattern.
func (s *epVersionScan) scanSplitDecimalEpisode(i int, inBracket bool) bool {
	tokens := s.tokens
	if i+2 >= len(tokens) {
		return false
	}
	left := strings.TrimSpace(tokens[i].Text)
	dot := strings.TrimSpace(tokens[i+1].Text)
	right := strings.TrimSpace(tokens[i+2].Text)
	if !(dot == "." && splitDecimalEpisodeRightPattern.MatchString(right)) {
		return false
	}
	mm := episodePattern.FindStringSubmatch(left)
	if len(mm) < 3 {
		return false
	}
	if len(mm) >= 4 && strings.TrimSpace(mm[3]) != "" {
		// Already a single-token decimal episode like 12.5.
		return false
	}
	prefix := strings.TrimSpace(mm[1])
	maj := strings.TrimLeft(mm[2], "0")
	if maj == "" {
		maj = "0"
	}
	n := maj + "." + right
	score := len(mm[2])
	ok := false
	if prefix != "" {
		ok = true
		score += 72
	} else if inBracket {
		if isBracketLeadingNumericTitlePrefix(tokens, i) {
			return true
		}
		ok = true
		score += 96
	} else if i-1 >= 0 {
		sep := strings.TrimSpace(tokens[i-1].Text)
		if sep == "-" || sep == "★" || sep == "☆" {
			rightBoundary := false
			if i+3 < len(tokens) {
				rightBoundary = isEpisodeBoundaryRightToken(tokens[i+3].Text)
			}
			if sep == "-" && !rightBoundary {
				return true
			}
			ok = true
			if sep == "-" {
				score += 94
			} else {
				score += 92
			}
		}
	}
	if ok && s.considerCandidate(score, i, n) {
		s.bestVer = ""
		s.bestConsumedEnd = i + 2
	}
	return ok
}

// scanGenericEpisode is the fallback episode pattern (EP12 / E12 / 第12 /
// bare numbers with a contextual signal). It runs last and never delegates to
// another pattern.
func (s *epVersionScan) scanGenericEpisode(i int, inBracket bool) {
	tokens := s.tokens
	m := episodePattern.FindStringSubmatch(tokens[i].Text)
	if len(m) < 3 {
		return
	}
	prefix := strings.TrimSpace(m[1])
	n := strings.TrimLeft(m[2], "0")
	if n == "" {
		n = "0"
	}
	if len(m) >= 4 {
		dec := strings.TrimSpace(m[3])
		if dec != "" {
			n = n + "." + dec
		}
	}
	// Guard: bracketed catalog IDs like "[KAXA-9023]" are product codes, not episodes.
	if prefix == "" && inBracket && i-2 >= 0 {
		prevSep := strings.TrimSpace(tokens[i-1].Text)
		prevLead := strings.TrimSpace(tokens[i-2].Text)
		if prevSep == "-" && bracketCatalogIDPrefixPattern.MatchString(prevLead) {
			return
		}
	}
	if prefix == "" && looksLikeVolumeContextNumberToken(tokens, i) {
		return
	}
	if prefix == "" && inBracket && hasMatchingSeasonNumberAroundToken(tokens, i, 12) && !hasDashSeparatorAroundToken(tokens, i) && !isStandaloneNumericBracketBlock(tokens, i) {
		return
	}
	signalScore := 0
	if prefix == "" {
		if shouldSkipPartIndexEpisodeCandidate(tokens, i, inBracket) {
			return
		}
		ok := false
		if inBracket {
			if isBracketLeadingNumericTitlePrefix(tokens, i) {
				return
			}
			ok = true
			signalScore = 96
		}
		if i-1 >= 0 {
			sep := strings.TrimSpace(tokens[i-1].Text)
			if sep == "-" {
				ok = true
				if signalScore < 94 {
					signalScore = 94
				}
				rightBoundary := false
				if i+1 < len(tokens) {
					rightBoundary = isEpisodeBoundaryRightToken(tokens[i+1].Text)
				}
				if i-2 >= 0 {
					prev2 := strings.TrimSpace(tokens[i-2].Text)
					if looksLikeCodecPrefixToken(prev2) {
						ok = false
					}
					if inBracket && bracketCatalogIDPrefixPattern.MatchString(prev2) {
						ok = false
					}
				}
				// Guard: reject cases like "Gintama-3 nen" where the number is followed by a word.
				if !inBracket {
					if i+1 < len(tokens) {
						nextWord := strings.TrimSpace(tokens[i+1].Text)
						if !rightBoundary && (looksLikeLowercaseLatinWordToken(nextWord) || looksLikeBitDepthWordToken(nextWord)) {
							ok = false
						}
					}
				}
				// avoid U-20 style (letter + '-' + digits)
				if i-2 >= 0 {
					p := strings.TrimSpace(tokens[i-2].Text)
					if len(p) <= 2 {
						allLetters := true
						for _, r := range p {
							if !(('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z')) {
								allLetters = false
								break
							}
						}
						if allLetters && hasUpperASCII(p) {
							if !rightBoundary {
								ok = false
							}
						}
					}
				}
				// Guard: title season suffix before bracket episode, e.g.
				// "... Monogatari -2 [01] ...", should prefer [01].
				if ok && !inBracket && hasLaterBracketEpisodeCandidate(tokens, i+1) {
					if hasMatchingSeasonNumberAroundToken(tokens, i, 12) {
						ok = false
					}
				}
			} else if sep == "★" || sep == "☆" {
				ok = true
				if signalScore < 92 {
					signalScore = 92
				}
			}
		}
		if !ok {
			if i+1 < len(tokens) {
				next := strings.TrimSpace(tokens[i+1].Text)
				if next == "[" || next == "【" || next == "(" || next == "（" {
					if n != "0" {
						if n != "720" && n != "1080" && n != "2160" && n != "480" && n != "540" {
							if hasLaterBracketEpisodeCandidate(tokens, i+1) {
								if hasMatchingSeasonNumberAroundToken(tokens, i, 12) {
									return
								}
								if i-1 >= 0 {
									prev := strings.TrimSpace(tokens[i-1].Text)
									if isLikelyBracketTitleWordToken(prev) && !strings.EqualFold(prev, "episode") && !strings.EqualFold(prev, "ep") {
										return
									}
								}
							}
							ok = true
							if signalScore < 88 {
								signalScore = 88
							}
						}
					}
				}
			}
			if !ok {
				return
			}
		}
		if n == "0" {
			isStrongZero := inBracket
			if !isStrongZero && i-1 >= 0 && strings.TrimSpace(tokens[i-1].Text) == "-" {
				if i+1 < len(tokens) && isEpisodeBoundaryRightToken(tokens[i+1].Text) {
					isStrongZero = true
				}
			}
			if !isStrongZero {
				return
			}
		}
	}
	score := len(m[2])
	if prefix != "" {
		// Medium EP signal: EP12 / E12 / 第12
		score += 72
	} else if signalScore > 0 {
		// Strong EP signal: bracketed number / dash-number / star-number.
		score += signalScore
	}
	if s.considerCandidate(score, i, n) {
		s.bestVer = ""
		s.bestConsumedEnd = i
	}
}

// finish consumes the tokens spanned by the best candidate and reports the
// detection result.
func (s *epVersionScan) finish() (string, int, string, int) {
	tokens := s.tokens
	if s.bestIdx >= 0 {
		if s.bestConsumedEnd < s.bestIdx {
			s.bestConsumedEnd = s.bestIdx
		}
		if s.bestKeywordIdx >= 0 && s.bestKeywordIdx < len(tokens) {
			tokens[s.bestKeywordIdx].Removed = true
		}
		if s.bestKeywordDashIdx >= 0 && s.bestKeywordDashIdx < len(tokens) {
			tokens[s.bestKeywordDashIdx].Removed = true
		}
		for j := s.bestIdx + 1; j <= s.bestConsumedEnd && j < len(tokens); j++ {
			tokens[j].Removed = true
		}
		text := strings.TrimSpace(tokens[s.bestIdx].Text)
		// If the episode token is itself a special/extra marker token like "OVA02",
		// or a version-suffixed token like "05v2", remove it to avoid polluting title
		// and being misclassified as tail_group later.
		if spKindEpisodePattern.MatchString(text) || episodeVersionSuffixPattern.MatchString(text) {
			tokens[s.bestIdx].Removed = true
		}
	}
	if s.bestIdx >= 0 && s.bestVer != "" {
		return s.best, s.bestIdx, s.bestVer, s.bestIdx
	}
	return s.best, s.bestIdx, "", -1
}
