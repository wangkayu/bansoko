package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

var taggedRangePattern = regexp.MustCompile(`(?i)^(abs|se|tv|ova|sp)[:_ -]?(\d{1,4}(?:\.\d{1,2})?(?:[-~]\d{1,4}(?:\.\d{1,2})?)?)$`)

func buildEPSlotCandidates(tokens []model.Token, season string, seasonIdx int, episodeFromSxxExx string, sxxExxIdx int, episode string, episodeIdx int, episodeRange string, episodeRangeIdx int, episodeList string, episodeListIdx int, spKind string) *model.EPSlot {
	cands := make([]model.EPEntity, 0, 8)
	seen := map[string]bool{}
	add := func(e model.EPEntity) {
		k := strings.TrimSpace(string(e.Kind)) + "|" + strings.TrimSpace(string(e.Axis)) + "|" + strings.TrimSpace(e.Season) + "|" + strings.TrimSpace(e.Episode) + "|" + strings.TrimSpace(e.EpisodeRange) + "|" + strings.TrimSpace(e.EpisodeList) + "|" + strings.TrimSpace(e.SPKind) + "|" + strconv.Itoa(e.TokenStart) + ":" + strconv.Itoa(e.TokenEnd)
		if seen[k] {
			return
		}
		seen[k] = true
		cands = append(cands, e)
	}

	axisSE := model.EPAxisABS
	if strings.TrimSpace(season) != "" {
		axisSE = model.EPAxisSE
	}
	if strings.TrimSpace(episodeFromSxxExx) != "" && sxxExxIdx >= 0 {
		add(model.EPEntity{Kind: model.EPKindEP, Axis: model.EPAxisSE, Season: season, Episode: episodeFromSxxExx, TokenStart: sxxExxIdx, TokenEnd: sxxExxIdx + 1, Source: model.EPSourceSxxExx})
	}
	if strings.TrimSpace(episode) != "" && episodeIdx >= 0 {
		add(model.EPEntity{Kind: model.EPKindEP, Axis: axisSE, Season: season, Episode: episode, TokenStart: episodeIdx, TokenEnd: episodeIdx + 1, Source: model.EPSourceEpisode})
	}
	if strings.TrimSpace(episodeRange) != "" && episodeRangeIdx >= 0 {
		add(model.EPEntity{Kind: model.EPKindRange, Axis: axisSE, Season: season, EpisodeRange: episodeRange, TokenStart: episodeRangeIdx, TokenEnd: episodeRangeIdx + 1, Source: model.EPSourceEpisodeRange})
	}
	if strings.TrimSpace(episodeList) != "" && episodeListIdx >= 0 {
		first := strings.TrimSpace(episode)
		if first == "" {
			if parts := strings.Split(strings.TrimSpace(episodeList), ","); len(parts) > 0 {
				first = strings.TrimSpace(parts[0])
			}
		}
		add(model.EPEntity{Kind: model.EPKindEPList, Axis: axisSE, Season: season, Episode: first, EpisodeList: strings.TrimSpace(episodeList), TokenStart: episodeListIdx, TokenEnd: episodeListIdx + 1, Source: model.EPSourceEpisodeList})
	}
	if strings.TrimSpace(spKind) != "" {
		ax := strings.ToUpper(strings.TrimSpace(spKind))
		if ax == "" {
			ax = "SP"
		}
		add(model.EPEntity{Kind: model.EPKindSP, Axis: model.EPAxis(ax), Season: season, SPKind: strings.ToUpper(strings.TrimSpace(spKind)), Source: model.EPSourceSPKind})
	}

	// Scan tokens for additional episode-like candidates.
	for i := 0; i < len(tokens); i++ {
		if epScanSkipped(tokens, i) {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if isTitlePrefixedDigitRangeInBracket(tokens, i) {
			continue
		}
		if isRightSideOfSharedPrefixSeasonEpisodeRange(tokens, i) {
			continue
		}
		// Tagged ranges like ABS:01-12 / TV01-12 / OVA:01
		if m := taggedRangePattern.FindStringSubmatch(txt); len(m) == 3 {
			axis := model.EPAxis(strings.ToUpper(strings.TrimSpace(m[1])))
			val := strings.TrimSpace(m[2])
			if strings.ContainsAny(val, "-~") {
				add(model.EPEntity{Kind: model.EPKindRange, Axis: axis, Season: season, EpisodeRange: val, TokenStart: i, TokenEnd: i + 1, Source: model.EPSourceTaggedRange})
			} else {
				add(model.EPEntity{Kind: model.EPKindEP, Axis: axis, Season: season, Episode: strings.TrimLeft(val, "0"), TokenStart: i, TokenEnd: i + 1, Source: model.EPSourceTaggedEP})
			}
			continue
		}
		// SP tokens with optional index like OVA1 / SP01.
		if m := spKindPattern.FindStringSubmatch(txt); len(m) >= 2 {
			k := normalizeSPKindToken(m[1])
			suffix := ""
			if len(m) >= 3 {
				suffix = strings.TrimSpace(m[2])
			}
			idx := ""
			if suffix != "" {
				idx = strings.TrimLeft(suffix, "0")
			}
			if !shouldAcceptSPKindToken(tokens, i, k, suffix) {
				continue
			}
			e := model.EPEntity{Kind: model.EPKindSP, Axis: model.EPAxis(k), Season: season, SPKind: k, TokenStart: i, TokenEnd: i + 1, Source: model.EPSourceSPToken}
			if idx != "" {
				e.Episode = idx
			}
			add(e)
			continue
		}
		// Range candidates in bracket or plain like 01-12 / 25-48.
		if m := episodeRangePattern.FindStringSubmatch(txt); len(m) == 3 {
			add(model.EPEntity{Kind: model.EPKindRange, Axis: axisSE, Season: season, EpisodeRange: strings.TrimSpace(txt), TokenStart: i, TokenEnd: i + 1, Source: model.EPSourceRangeToken})
			continue
		}
		// Single episode digits (often in brackets). Keep it conservative.
		if episodeOfTotalPattern.MatchString(txt) {
			if hasPartIndexLeadBeforeToken(tokens, i) || shouldSkipPartIndexEpisodeCandidate(tokens, i, isInBracket(tokens, i)) {
				continue
			}
			if looksLikeVolumeContextNumberToken(tokens, i) {
				continue
			}
			if looksLikeDottedNumericSequence(tokens, i) {
				continue
			}
			if yearPattern.MatchString(txt) {
				continue
			}
			if hasMatchingSeasonNumberAroundToken(tokens, i, 12) && !hasDashSeparatorAroundToken(tokens, i) {
				continue
			}
			// "Season 3 - 01": the 3 is the season value, not an episode candidate.
			if i > 0 && seasonWordLoosePattern.MatchString(strings.TrimSpace(tokens[i-1].Text)) {
				continue
			}
			if !isInBracket(tokens, i) && !isInParen(tokens, i) {
				if len(txt) >= 3 && !strings.HasPrefix(txt, "0") {
					continue
				}
			}
			// Guard: avoid extracting episode candidates from romaji title fragments like "29-sai".
			if i+2 < len(tokens) {
				sep := strings.TrimSpace(tokens[i+1].Text)
				if sep == "-" {
					next := strings.TrimSpace(tokens[i+2].Text)
					if looksLikeLowercaseLatinWordToken(next) {
						continue
					}
				}
			}
			// Avoid obvious meta bracket content.
			if isInBracket(tokens, i) {
				// Guard: skip catalog/product IDs like "[KAXA-9023]".
				if i-2 >= 0 {
					prevSep := strings.TrimSpace(tokens[i-1].Text)
					prevLead := strings.TrimSpace(tokens[i-2].Text)
					if prevSep == "-" && bracketCatalogIDPrefixPattern.MatchString(prevLead) {
						continue
					}
				}
				if isTitlePrefixedDigitRangeInBracket(tokens, i) {
					continue
				}
				if inner := bracketBlockInnerText(tokens, i); inner != "" {
					norm := reBuildEPSlotCandidates1.ReplaceAllString(inner, "$1-$2")
					if episodeRangePattern.MatchString(strings.TrimSpace(norm)) {
						prev := ""
						next := ""
						if i-1 >= 0 {
							prev = strings.TrimSpace(tokens[i-1].Text)
						}
						if i+1 < len(tokens) {
							next = strings.TrimSpace(tokens[i+1].Text)
						}
						if prev == "-" || prev == "~" || next == "-" || next == "~" {
							continue
						}
					}
				}
				if inner := bracketBlockInnerText(tokens, i); inner != "" {
					if isLikelyMetaBracketText(inner) {
						continue
					}
				}
			}
			if isInParen(tokens, i) {
				if inner := parenBlockInnerText(tokens, i); inner != "" {
					if looksLikeDateOrTimeText(inner) {
						continue
					}
				}
			}
			nn := strings.TrimLeft(txt, "0")
			if nn == "" {
				nn = "0"
			}
			add(model.EPEntity{Kind: model.EPKindEP, Axis: axisSE, Season: season, Episode: nn, TokenStart: i, TokenEnd: i + 1, Source: model.EPSourceEPDigitToken})
			continue
		}
	}

	if len(cands) == 0 {
		return nil
	}
	_ = seasonIdx
	return &model.EPSlot{Candidates: cands}
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reBuildEPSlotCandidates1 = regexp.MustCompile(`\b(\d{1,4})\s*[-~]\s*(\d{1,4})\b`)
)
