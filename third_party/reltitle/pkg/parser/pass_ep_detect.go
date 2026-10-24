package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

var specialIndexTokenPattern = regexp.MustCompile(`(?i)^(es|sp|ova|oad|ona|ex|ncop|nced|pv)0*(\d{1,3})$`)
var episodePlusSpecialPattern = regexp.MustCompile(`(?i)^(\d{1,4})\s*\+\s*(es|sp|ova|oad|ona|ex|ncop|nced|pv)0*(\d{1,3})$`)
var summarySpecialLeadPattern = regexp.MustCompile(`^(总集篇)0*(\d{1,3})$`)
var episodeListPattern = regexp.MustCompile(`(?i)^\s*\d{1,4}(?:\s*(?:&|＆|\+|＋|/|／|,|，|、)\s*\d{1,4})+\s*$`)
var episodeListDigitsPattern = regexp.MustCompile(`\d{1,4}`)
var episodeRangePackRightPattern = regexp.MustCompile(`(?i)^0*(\d{1,4})\s*(tv(?:全集|全话|全話)?|修正合集|合集)$`)
var episodeRangePackEmbeddedPattern = regexp.MustCompile(`(?i)^0*(\d{1,4})\s*[-~]\s*0*(\d{1,4})\s*(tv(?:全集|全话|全話)?|修正合集|合集)$`)
var episodeCountPackSuffixPattern = regexp.MustCompile(`(?i)^0*(\d{1,4})\s*(修正合集|合集)$`)

func detectSeasonFromAttrs(attrs []model.Attribute) (season string, tokenIdx int) {
	bestIdx := -1
	bestSeason := ""
	seenAny := false
	for _, a := range attrs {
		s := strings.TrimSpace(a.Normalized)
		if a.Type != model.AttrNote {
			continue
		}
		// Some early passes emit season markers like "S2" as note and remove the token.
		// Treat these as season candidates to avoid losing season info.
		if m := reDetectSeasonFromAttrs1.FindStringSubmatch(s); len(m) == 2 {
			s = strings.TrimLeft(m[1], "0")
			if s == "" {
				s = m[1]
			}
		} else {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		seenAny = true
		idx := a.TokenStart
		// Prefer smallest non-negative token index when available; otherwise fall back to any season.
		if idx >= 0 {
			if bestIdx < 0 || idx < bestIdx {
				bestIdx = idx
				bestSeason = s
			}
			continue
		}
		if bestSeason == "" {
			bestSeason = s
		}
	}
	if !seenAny {
		return "", -1
	}
	return bestSeason, bestIdx
}

func passDetectSeasonSPMovie(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	if ctx.EPDecision == nil {
		ctx.EPDecision = newEPDecision()
	}
	addSeasonHint := func(season string, idx int, source string) {
		ctx.EPDecision.addSeasonHint(season, idx, source)
	}
	if ctx.SeasonRange == "" {
		if sr, s, idx := detectSeasonRangeByKeyword(tokens); sr != "" {
			ctx.SeasonRange = sr
			if s != "" {
				addSeasonHint(s, idx, "season_range_keyword")
			}
		}
	}
	if attrs != nil {
		if s, idx := detectSeasonFromAttrs(*attrs); s != "" {
			addSeasonHint(s, idx, "attr")
		}
	}
	if s, idx := detectTVVariantSeason(tokens); s != "" {
		addSeasonHint(s, idx, "tv_variant")
		removeTVVariantSeasonTokens(tokens)
	}
	if s, idx := detectSeasonBeforeDashEpisode(tokens); s != "" {
		addSeasonHint(s, idx, "season_before_dash")
	}
	seasonSxx, epFromSxxExx, sxxIdx := detectSxxExx(tokens)
	ctx.EpisodeFromSxxExx = epFromSxxExx
	ctx.SxxExxIdx = sxxIdx
	if seasonSxx != "" {
		addSeasonHint(seasonSxx, sxxIdx, "sxxexx")
	}
	if s, idx := detectSeasonOnly(tokens); s != "" {
		addSeasonHint(s, idx, "season_only")
	}
	if s, idx := detectSeasonKeyword(tokens); s != "" {
		addSeasonHint(s, idx, "season_keyword")
	}
	if s, idx := detectSeasonInParen(tokens); s != "" {
		addSeasonHint(s, idx, "season_in_paren")
	}
	if ctx.PackKind != "" {
		if s, idx := detectSeasonOnlyLoose(tokens); s != "" {
			addSeasonHint(s, idx, "season_only_loose")
		}
	}
	ctx.Season = ""
	ctx.SeasonIdx = -1
	if hint := selectBestSeasonHint(ctx.EPDecision.SeasonHints); hint != nil {
		ctx.Season = strings.TrimSpace(hint.Season)
		ctx.SeasonIdx = hint.TokenIdx
	}
	if ctx.Season != "" && ctx.SeasonIdx >= 0 {
		removeSeasonTokens(tokens, ctx.SeasonIdx)
	}
	ctx.SPKind = detectSPKind(tokens)
	ctx.MovieIndexRange, ctx.MovieIndexRangeIdx, ctx.HasMovieKeyword = detectMovieIndexRangeAndKeyword(tokens)
	ctx.MovieIndex, ctx.MovieIndexIdx, ctx.HasMovieKeyword = detectMovieIndexAndKeyword(tokens)
	if attrs != nil {
		ctx.HasMovieKeyword = ctx.HasMovieKeyword || detectMovieKeywordFromAttrs(*attrs)
		if strings.TrimSpace(ctx.MovieIndexRange) == "" {
			if mr, idx := detectMovieIndexRangeFromAttrs(*attrs); strings.TrimSpace(mr) != "" {
				ctx.MovieIndexRange = strings.TrimSpace(mr)
				ctx.MovieIndexRangeIdx = idx
				ctx.HasMovieKeyword = true
			}
		}
		if strings.TrimSpace(ctx.MovieIndex) == "" {
			if mi, idx := detectMovieIndexFromAttrs(*attrs); strings.TrimSpace(mi) != "" {
				ctx.MovieIndex = strings.TrimSpace(mi)
				ctx.MovieIndexIdx = idx
				ctx.HasMovieKeyword = true
			}
		}
	}
	if strings.TrimSpace(ctx.MovieIndexRange) != "" {
		ctx.MovieIndex = ""
		ctx.MovieIndexIdx = -1
	}
	if strings.TrimSpace(ctx.MovieIndexRange) == "" && strings.TrimSpace(ctx.MovieIndex) == "" && ctx.HasMovieKeyword {
		if mi, idx := detectMovieShortIndexWithContext(tokens); strings.TrimSpace(mi) != "" {
			ctx.MovieIndex = strings.TrimSpace(mi)
			ctx.MovieIndexIdx = idx
		}
	}
	if ctx.MovieIndex != "" && ctx.MovieIndexIdx >= 0 {
		removeMovieIndexTokens(tokens, ctx.MovieIndexIdx)
	}
}

func detectMovieKeywordFromAttrs(attrs []model.Attribute) bool {
	for _, a := range attrs {
		// Lexicon may remove tokens like [Movie] early and emit them as notes.
		// Also, title extraction may absorb movie keywords into the inferred title attribute.
		if a.Type != model.AttrNote && a.Type != model.AttrTitle && a.Type != model.AttrContents {
			continue
		}
		n := strings.TrimSpace(a.Normalized)
		if n == "" {
			continue
		}
		up := strings.ToUpper(n)
		if up == "MOVIE" || up == "THEMOVIE" {
			return true
		}
		// Some notes are emitted for JP/CN movie keywords via other rules/passes.
		// Keep strict matching for 映画 to avoid over-triggering on title phrases.
		if strings.Contains(n, "劇場版") || strings.Contains(n, "剧场版") || strings.EqualFold(strings.TrimSpace(n), "映画") || strings.Contains(n, "大电影") || strings.Contains(n, "大電影") {
			return true
		}
	}
	return false
}

func detectMovieIndexFromAttrs(attrs []model.Attribute) (string, int) {
	best := ""
	bestIdx := -1
	bestConf := -1.0
	for _, a := range attrs {
		if a.Type != model.AttrMovieIndex {
			continue
		}
		v := strings.TrimSpace(a.Normalized)
		if v == "" {
			continue
		}
		if a.Confidence > bestConf {
			bestConf = a.Confidence
			best = v
			bestIdx = a.TokenStart
		}
	}
	return best, bestIdx
}

func detectMovieIndexRangeFromAttrs(attrs []model.Attribute) (string, int) {
	best := ""
	bestIdx := -1
	bestConf := -1.0
	for _, a := range attrs {
		if a.Type != model.AttrMovieIndexRange {
			continue
		}
		v := strings.TrimSpace(a.Normalized)
		if v == "" {
			continue
		}
		if a.Confidence > bestConf {
			bestConf = a.Confidence
			best = v
			bestIdx = a.TokenStart
		}
	}
	return best, bestIdx
}

func detectMovieShortIndexWithContext(tokens []model.Token) (string, int) {
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		text := strings.TrimSpace(tokens[i].Text)
		m := movieShortIndexPattern.FindStringSubmatch(text)
		if len(m) != 2 {
			continue
		}
		// Exclude range/list forms like M1-M4 / M1+M2.
		if i+2 < len(tokens) {
			sep := strings.TrimSpace(tokens[i+1].Text)
			right := strings.TrimSpace(tokens[i+2].Text)
			if (sep == "-" || sep == "~" || sep == "+" || sep == "＋") && movieShortIndexPattern.MatchString(right) {
				continue
			}
		}
		if i-2 >= 0 {
			left := strings.TrimSpace(tokens[i-2].Text)
			sep := strings.TrimSpace(tokens[i-1].Text)
			if (sep == "-" || sep == "~" || sep == "+" || sep == "＋") && movieShortIndexPattern.MatchString(left) {
				continue
			}
		}
		v := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
		if v == "" {
			v = "0"
		}
		return v, i
	}
	return "", -1
}

func passDetectEpisodeRangeAndEpisode(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	taggedEpisode := ""
	taggedEpisodeIdx := -1
	taggedRange := ""
	taggedRangeIdx := -1
	// Extract special/summary patterns early so they can drive EPSlot primary selection.
	// Example: "总集篇01_12.5" -> special_flag=总集篇, special_index=01, tagged_ep=ABS:12.5.
	if attrs != nil {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Removed {
				continue
			}
			if !isInBracket(tokens, i) {
				continue
			}
			text := strings.TrimSpace(tokens[i].Text)
			if text == "" {
				continue
			}
			// TV tagged range case: tokens like [ "TV01", "-", "25Fin" ].
			if m := rePassDetectEpisodeRangeAndEpisode1.FindStringSubmatch(text); len(m) == 3 {
				if i+2 < len(tokens) {
					sep := strings.TrimSpace(tokens[i+1].Text)
					if sep == "-" || sep == "~" {
						rawR := strings.TrimSpace(tokens[i+2].Text)
						mm := rePassDetectEpisodeRangeAndEpisode2.FindStringSubmatch(rawR)
						if len(mm) >= 2 {
							l := strings.TrimLeft(strings.TrimSpace(m[2]), "0")
							if l == "" {
								l = strings.TrimSpace(m[2])
							}
							r := strings.TrimLeft(strings.TrimSpace(mm[1]), "0")
							if r == "" {
								r = strings.TrimSpace(mm[1])
							}
							if l != "" && r != "" {
								taggedRange = l + "-" + r
								taggedRangeIdx = i
								tokens[i].Text = "TV:" + taggedRange
								tokens[i+1].Removed = true
								tokens[i+2].Removed = true
								if len(mm) >= 3 && strings.TrimSpace(mm[2]) != "" {
									*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: "FIN", TokenStart: i, TokenEnd: i + 1, Confidence: 0.7, Source: model.AttrSourceTVRangeFin})
								}
								continue
							}
						}
					}
				}
			}
			// Split TV tagged range case: tokens like [ "TV", "01", "-", "101", ... ].
			if strings.EqualFold(text, "tv") {
				if i+3 < len(tokens) {
					lraw := strings.TrimSpace(tokens[i+1].Text)
					sep := strings.TrimSpace(tokens[i+2].Text)
					rawR := strings.TrimSpace(tokens[i+3].Text)
					if (sep == "-" || sep == "~") && episodeOfTotalPattern.MatchString(lraw) {
						mm := rePassDetectEpisodeRangeAndEpisode3.FindStringSubmatch(rawR)
						if len(mm) >= 2 {
							l := strings.TrimLeft(lraw, "0")
							if l == "" {
								l = lraw
							}
							r := strings.TrimLeft(strings.TrimSpace(mm[1]), "0")
							if r == "" {
								r = strings.TrimSpace(mm[1])
							}
							if l != "" && r != "" {
								taggedRange = l + "-" + r
								taggedRangeIdx = i
								tokens[i].Text = "TV:" + taggedRange
								tokens[i+1].Removed = true
								tokens[i+2].Removed = true
								tokens[i+3].Removed = true
								if len(mm) >= 3 && strings.TrimSpace(mm[2]) != "" {
									*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: "FIN", TokenStart: i, TokenEnd: i + 1, Confidence: 0.7, Source: model.AttrSourceTVRangeFin})
								}
								continue
							}
						}
					}
				}
			}
			// Tokenizer-split case: ["总集篇01", "_", "12", ".", "5"].
			if m := summarySpecialLeadPattern.FindStringSubmatch(text); len(m) == 3 {
				// Require a separator.
				if i+2 >= len(tokens) {
					continue
				}
				sep := strings.TrimSpace(tokens[i+1].Text)
				if sep != "_" && sep != "-" {
					continue
				}
				n1 := strings.TrimSpace(tokens[i+2].Text)
				if !rePassDetectEpisodeRangeAndEpisode4.MatchString(n1) {
					continue
				}
				ep := n1
				end := i + 2
				if i+4 < len(tokens) {
					dot := strings.TrimSpace(tokens[i+3].Text)
					n2 := strings.TrimSpace(tokens[i+4].Text)
					if dot == "." && rePassDetectEpisodeRangeAndEpisode5.MatchString(n2) {
						ep = n1 + "." + n2
						end = i + 4
					}
				}
				flag := strings.TrimSpace(m[1])
				idx := strings.TrimSpace(m[2])
				if strings.TrimSpace(idx) != "" {
					*attrs = append(*attrs, model.Attribute{Type: model.AttrSpecialFlag, Normalized: flag, TokenStart: i, TokenEnd: i + 1, Confidence: 0.85, Source: model.AttrSourceSpecialSummary})
					*attrs = append(*attrs, model.Attribute{Type: model.AttrSpecialIndex, Normalized: idx, TokenStart: i, TokenEnd: i + 1, Confidence: 0.85, Source: model.AttrSourceSpecialSummary})
				}
				tokens[i].Text = "ABS:" + ep
				taggedEpisode = ep
				taggedEpisodeIdx = i
				// Remove the separator and numeric tail tokens to keep token stream clean.
				for k := i + 1; k <= end && k < len(tokens); k++ {
					tokens[k].Removed = true
				}
				// Remove surrounding bracket tokens if they are immediate neighbors.
				if i-1 >= 0 {
					p := strings.TrimSpace(tokens[i-1].Text)
					if p == "[" || p == "【" {
						tokens[i-1].Removed = true
					}
				}
				if end+1 < len(tokens) {
					n := strings.TrimSpace(tokens[end+1].Text)
					if n == "]" || n == "】" {
						tokens[end+1].Removed = true
					}
				}
				continue
			}
		}
	}

	ctx.EpisodeRange, ctx.EpisodeRangeIdx = detectEpisodeRange(tokens)
	if ctx.EpisodeRange == "" {
		if r, idx := detectEpisodeOfTotalInBracket(tokens); r != "" {
			ctx.EpisodeRange = r
			ctx.EpisodeRangeIdx = idx
		}
	}
	if strings.TrimSpace(ctx.EpisodeRange) == "" && strings.TrimSpace(taggedRange) != "" && taggedRangeIdx >= 0 {
		ctx.EpisodeRange = taggedRange
		ctx.EpisodeRangeIdx = taggedRangeIdx
	}
	if strings.TrimSpace(ctx.EpisodeRange) != "" && isSharedPrefixSeasonEpisodeRangeAt(tokens, ctx.EpisodeRangeIdx) {
		ctx.EpisodeFromSxxExx = ""
		ctx.SxxExxIdx = -1
	}
	if strings.TrimSpace(ctx.EpisodeRange) != "" && ctx.EpisodeRangeIdx >= 0 {
		if pk := normalizeEpisodeRangeTVPackSuffix(tokens, attrs, ctx.EpisodeRangeIdx); pk != "" {
			if strings.TrimSpace(ctx.PackKind) == "" || strings.EqualFold(pk, "TV_PACK") {
				ctx.PackKind = pk
			}
		}
	}
	ctx.EpisodeList = ""
	ctx.EpisodeListIdx = -1
	ctx.Episode = ""
	ctx.EpisodeIdx = -1
	ctx.EpisodeVersion = ""
	ctx.EpisodeVersionIdx = -1
	if strings.TrimSpace(ctx.EpisodeRange) == "" {
		if epList, firstEp, idx := detectEpisodeList(tokens); epList != "" && idx >= 0 {
			ctx.EpisodeList = epList
			ctx.EpisodeListIdx = idx
			ctx.Episode = firstEp
			ctx.EpisodeIdx = idx
		}
	}
	allowEpisodeDetectionWithRange := strings.TrimSpace(ctx.EpisodeRange) != "" &&
		ctx.EpisodeRangeIdx >= 0 &&
		shouldAllowEpisodeDetectionWithRange(tokens, ctx.EpisodeRangeIdx, ctx.Season, ctx.SeasonIdx)
	if (strings.TrimSpace(ctx.EpisodeRange) == "" || allowEpisodeDetectionWithRange) && (strings.TrimSpace(ctx.Episode) == "" || ctx.EpisodeIdx < 0) {
		ctx.Episode, ctx.EpisodeIdx, ctx.EpisodeVersion, ctx.EpisodeVersionIdx = detectEpisodeWithVersion(tokens)
	}
	if attrs != nil && ctx.EpisodeIdx >= 0 && ctx.EpisodeIdx < len(tokens) {
		if m := episodeEndSuffixPattern.FindStringSubmatch(strings.TrimSpace(tokens[ctx.EpisodeIdx].Text)); len(m) == 3 {
			flag := strings.ToUpper(strings.TrimSpace(m[2]))
			if flag != "" {
				*attrs = append(*attrs, model.Attribute{
					Type:       model.AttrNote,
					Normalized: flag,
					TokenStart: ctx.EpisodeIdx,
					TokenEnd:   ctx.EpisodeIdx + 1,
					Confidence: 0.75,
					Source:     model.AttrSourceEpisodeEndflag,
				})
			}
		}
	}
	if strings.TrimSpace(ctx.EpisodeRange) == "" && (strings.TrimSpace(ctx.Episode) == "" || ctx.EpisodeIdx < 0) {
		if strings.TrimSpace(taggedEpisode) != "" && taggedEpisodeIdx >= 0 {
			ctx.Episode = taggedEpisode
			ctx.EpisodeIdx = taggedEpisodeIdx
			ctx.EpisodeVersion = ""
			ctx.EpisodeVersionIdx = -1
		}
	}
	if strings.TrimSpace(ctx.EpisodeRange) == "" && (strings.TrimSpace(ctx.Episode) == "" || ctx.EpisodeIdx < 0) {
		if ep, idx, ok := detectEpisodeFromSPBracketSegment(tokens); ok {
			ctx.Episode = ep
			ctx.EpisodeIdx = idx
			ctx.EpisodeVersion = ""
			ctx.EpisodeVersionIdx = -1
		}
	}
	if shouldTreatParenSxxExxAsAlternate(tokens, ctx.Season, ctx.SeasonIdx, ctx.Episode, ctx.EpisodeIdx, ctx.EpisodeFromSxxExx, ctx.SxxExxIdx) {
		if ctx.SeasonIdx == ctx.SxxExxIdx && strings.TrimSpace(ctx.Season) == "0" {
			ctx.Season = ""
			ctx.SeasonIdx = -1
		}
		ctx.EpisodeFromSxxExx = ""
		ctx.SxxExxIdx = -1
	}
	// Detect additional special index like ES07 in patterns such as "07+ES07".
	// This should not override the main episode; it is extra content metadata.
	if attrs != nil {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Removed {
				continue
			}
			text := strings.TrimSpace(tokens[i].Text)
			if text == "" {
				continue
			}
			// Single token like ES07 / SP01.
			if m := specialIndexTokenPattern.FindStringSubmatch(text); len(m) == 3 {
				kind := strings.ToUpper(strings.TrimSpace(m[1]))
				idx := strings.TrimLeft(m[2], "0")
				if idx == "" {
					idx = m[2]
				}
				if idx != "" && idx != "0" {
					*attrs = append(*attrs, model.Attribute{Type: model.AttrSpecialFlag, Normalized: kind, TokenStart: i, TokenEnd: i + 1, Confidence: 0.8, Source: model.AttrSourceSpecialIndex})
					*attrs = append(*attrs, model.Attribute{Type: model.AttrSpecialIndex, Normalized: idx, TokenStart: i, TokenEnd: i + 1, Confidence: 0.8, Source: model.AttrSourceSpecialIndex})
					tokens[i].Removed = true
					// Remove adjacent '+' if present.
					if i-1 >= 0 {
						p := strings.TrimSpace(tokens[i-1].Text)
						if p == "+" || p == "＋" {
							tokens[i-1].Removed = true
						}
					}
				}
				continue
			}
			// Combined token like "07+ES07" (in case tokenizer keeps '+').
			if m := episodePlusSpecialPattern.FindStringSubmatch(text); len(m) == 4 {
				kind := strings.ToUpper(strings.TrimSpace(m[2]))
				idx := strings.TrimLeft(m[3], "0")
				if idx == "" {
					idx = m[3]
				}
				if idx != "" && idx != "0" {
					*attrs = append(*attrs, model.Attribute{Type: model.AttrSpecialFlag, Normalized: kind, TokenStart: i, TokenEnd: i + 1, Confidence: 0.8, Source: model.AttrSourceSpecialIndex})
					*attrs = append(*attrs, model.Attribute{Type: model.AttrSpecialIndex, Normalized: idx, TokenStart: i, TokenEnd: i + 1, Confidence: 0.8, Source: model.AttrSourceSpecialIndex})
					tokens[i].Removed = true
				}
				continue
			}
		}
	}
	if ctx.EPDecision == nil {
		ctx.EPDecision = newEPDecision()
	}
	if epslot := buildEPSlotCandidates(tokens, ctx.Season, ctx.SeasonIdx, ctx.EpisodeFromSxxExx, ctx.SxxExxIdx, ctx.Episode, ctx.EpisodeIdx, ctx.EpisodeRange, ctx.EpisodeRangeIdx, ctx.EpisodeList, ctx.EpisodeListIdx, ctx.SPKind); epslot != nil {
		ctx.EPDecision.replaceCandidates(epslot.Candidates)
	} else {
		ctx.EPDecision.replaceCandidates(nil)
	}
	adjudicateEPDecisionIntoContext(tokens, ctx)
}

func normalizeEpisodeRangeTVPackSuffix(tokens []model.Token, attrs *[]model.Attribute, rangeIdx int) string {
	if rangeIdx < 0 || rangeIdx >= len(tokens) {
		return ""
	}
	appendPackAttrs := func(packKind, note string, tokenStart, tokenEnd int, source model.AttrSource) {
		if attrs == nil {
			return
		}
		if note != "" {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrNote,
				Normalized: note,
				TokenStart: tokenStart,
				TokenEnd:   tokenEnd,
				Confidence: 0.7,
				Source:     source,
			})
		}
		if packKind != "" {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrPackKind,
				Normalized: packKind,
				TokenStart: tokenStart,
				TokenEnd:   tokenEnd,
				Confidence: 0.75,
				Source:     source,
			})
		}
	}
	packKindFromSuffix := func(suffix string) (string, string) {
		lower := strings.ToLower(strings.TrimSpace(suffix))
		if strings.HasPrefix(lower, "tv") {
			return "TV_PACK", "TV_PACK"
		}
		if strings.Contains(lower, "合集") {
			return "合集", "CURATED_PACK"
		}
		return "", ""
	}

	text := strings.TrimSpace(tokens[rangeIdx].Text)
	if m := episodeRangePackEmbeddedPattern.FindStringSubmatch(text); len(m) == 4 {
		if pk, note := packKindFromSuffix(m[3]); pk != "" {
			tokens[rangeIdx].Removed = true
			appendPackAttrs(pk, note, rangeIdx, rangeIdx+1, model.AttrSourceEpisodeRangePackSuffixEmbedded)
			return pk
		}
	}
	if m := episodeCountPackSuffixPattern.FindStringSubmatch(text); len(m) == 3 {
		if pk, note := packKindFromSuffix(m[2]); pk != "" {
			tokens[rangeIdx].Removed = true
			appendPackAttrs(pk, note, rangeIdx, rangeIdx+1, model.AttrSourceEpisodeRangePackSuffixCount)
			return pk
		}
	}

	if rangeIdx+2 >= len(tokens) {
		return ""
	}
	left := strings.TrimSpace(tokens[rangeIdx].Text)
	sep := strings.TrimSpace(tokens[rangeIdx+1].Text)
	right := strings.TrimSpace(tokens[rangeIdx+2].Text)
	if left == "" || right == "" {
		return ""
	}
	if sep != "-" && sep != "~" {
		return ""
	}
	if !episodeOfTotalPattern.MatchString(left) {
		return ""
	}
	m := episodeRangePackRightPattern.FindStringSubmatch(right)
	if len(m) != 3 {
		// Split-form suffix: "01 - 08 精校合集" / "01 - 08 修正合集" / "01 - 08 合集".
		// The suffix keyword is a separate token right after the range right bound.
		if episodeOfTotalPattern.MatchString(right) && rangeIdx+3 < len(tokens) {
			if pk, note := packKindFromSuffix(strings.TrimSpace(tokens[rangeIdx+3].Text)); pk != "" {
				tokens[rangeIdx+3].Removed = true
				appendPackAttrs(pk, note, rangeIdx+3, rangeIdx+4, model.AttrSourceEpisodeRangePackSuffixSplit)
				return pk
			}
		}
		return ""
	}
	pk, note := packKindFromSuffix(m[2])
	if pk == "" {
		return ""
	}
	n := strings.TrimLeft(strings.TrimSpace(m[1]), "0")
	if n == "" {
		n = "0"
	}
	tokens[rangeIdx+2].Text = n
	tokens[rangeIdx+2].Removed = true
	appendPackAttrs(pk, note, rangeIdx+2, rangeIdx+3, model.AttrSourceEpisodeRangePackSuffix)
	return pk
}

func detectEpisodeList(tokens []model.Token) (episodeList string, firstEpisode string, tokenIdx int) {
	parseEpisodeListText := func(s string) (string, string, bool) {
		txt := strings.TrimSpace(s)
		if txt == "" || !episodeListPattern.MatchString(txt) {
			return "", "", false
		}
		rawNums := episodeListDigitsPattern.FindAllString(txt, -1)
		if len(rawNums) < 2 {
			return "", "", false
		}
		nums := make([]string, 0, len(rawNums))
		allYearLike := true
		for _, rn := range rawNums {
			nn := strings.TrimLeft(strings.TrimSpace(rn), "0")
			if nn == "" {
				nn = "0"
			}
			nums = append(nums, nn)
			nv, err := strconv.Atoi(nn)
			if err != nil || nv < 1900 || nv > 2099 {
				allYearLike = false
			}
		}
		if allYearLike {
			return "", "", false
		}
		return strings.Join(nums, ","), nums[0], true
	}

	for i := 0; i < len(tokens); i++ {
		if epScanSkipped(tokens, i) {
			continue
		}
		if isInParen(tokens, i) {
			continue
		}
		if list, first, ok := parseEpisodeListText(tokens[i].Text); ok {
			return list, first, i
		}
		if !isInBracket(tokens, i) {
			continue
		}
		openIdx, closeIdx, ok := bracketBoundsAt(tokens, i)
		if !ok {
			continue
		}
		firstIdx, _ := firstMeaningfulTokenInRange(tokens, openIdx+1, closeIdx)
		if firstIdx != i {
			continue
		}
		if inner := bracketBlockInnerText(tokens, i); inner != "" {
			if list, first, ok := parseEpisodeListText(inner); ok {
				return list, first, i
			}
		}
	}
	return "", "", -1
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reDetectSeasonFromAttrs1            = regexp.MustCompile(`(?i)^s(\d{1,2})$`)
	rePassDetectEpisodeRangeAndEpisode1 = regexp.MustCompile(`(?i)^(tv)0*(\d{1,4})$`)
	rePassDetectEpisodeRangeAndEpisode2 = regexp.MustCompile(`(?i)^(\d{1,4})(fin|final)?$`)
	rePassDetectEpisodeRangeAndEpisode3 = regexp.MustCompile(`(?i)^(\d{1,4})(fin|final)?$`)
	rePassDetectEpisodeRangeAndEpisode4 = regexp.MustCompile(`^\d{1,4}$`)
	rePassDetectEpisodeRangeAndEpisode5 = regexp.MustCompile(`^\d{1,2}$`)
)
