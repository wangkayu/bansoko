package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func shouldDropEpisodeRangeForMovieRange(rawTitle string, movieIndexRange string, episode string, episodeRange string) bool {
	mr := strings.TrimSpace(movieIndexRange)
	ep := strings.TrimSpace(episode)
	er := strings.TrimSpace(episodeRange)
	if mr == "" {
		return false
	}
	if ep == "" && er == "" {
		return false
	}
	// Keep TV range semantics for explicit mixed patterns like:
	// "TV 01-101 + Movie 01-04".
	if explicitTVEpisodeRangeRawPattern.MatchString(rawTitle) {
		return false
	}
	if ep != "" {
		return true
	}
	if er == mr {
		return true
	}
	return false
}

func shouldDemoteEpisodeToMovieIndex(rawTitle string, episode string, episodeRange string, season string, seasonRange string, packKind string, hasMovieKeyword bool, movieIndex string, movieIndexRange string) bool {
	if strings.TrimSpace(episode) == "" {
		return false
	}
	if strings.TrimSpace(movieIndexRange) != "" {
		return false
	}
	if strings.TrimSpace(episodeRange) != "" || strings.TrimSpace(season) != "" || strings.TrimSpace(seasonRange) != "" {
		return false
	}
	if isMovieExcludedByPackKind(packKind) {
		return false
	}
	hasMovieCtx := hasMovieKeyword || strings.TrimSpace(movieIndex) != "" || hasMovieKeywordInRawTitle(rawTitle)
	if !hasMovieCtx {
		return false
	}
	if strings.TrimSpace(movieIndex) != "" {
		return true
	}
	if moviePartSignalPattern.MatchString(rawTitle) {
		return true
	}
	if hasMovieKeywordInRawTitle(rawTitle) && movieBracketEpisodePattern.MatchString(rawTitle) {
		return true
	}
	return false
}

func normalizeMovieEpisodeSignals(rawTitle string, contentKind model.ContentKind, episode string, episodeRange string, season string, seasonRange string, packKind string, hasMovieKeyword bool, movieIndex string, movieIndexRange string, episodeIdx int, movieIndexIdx int) (string, string, int, int, bool) {
	if contentKind.Canonical() != model.ContentKindMovie {
		return episode, movieIndex, episodeIdx, movieIndexIdx, false
	}
	if strings.TrimSpace(movieIndexRange) != "" {
		return episode, movieIndex, episodeIdx, movieIndexIdx, false
	}
	ep := strings.TrimSpace(episode)
	if ep == "" || strings.TrimSpace(episodeRange) != "" || strings.TrimSpace(season) != "" || strings.TrimSpace(seasonRange) != "" {
		return episode, movieIndex, episodeIdx, movieIndexIdx, false
	}
	if isMovieExcludedByPackKind(packKind) {
		return episode, movieIndex, episodeIdx, movieIndexIdx, false
	}
	hasMovieCtx := hasMovieKeyword || strings.TrimSpace(movieIndex) != "" || hasMovieKeywordInRawTitle(rawTitle)
	if !hasMovieCtx {
		return episode, movieIndex, episodeIdx, movieIndexIdx, false
	}
	newMovieIndex := strings.TrimSpace(movieIndex)
	if newMovieIndex == "" {
		base := ep
		if dot := strings.Index(base, "."); dot > 0 {
			base = base[:dot]
		}
		if n, err := strconv.Atoi(base); err == nil && n > 0 && n <= 120 {
			newMovieIndex = ep
			if movieIndexIdx < 0 {
				movieIndexIdx = episodeIdx
			}
		}
	}
	return "", newMovieIndex, -1, movieIndexIdx, true
}

func trimMovieDashEpisodeTailFromTitle(rawTitle string, title string, episode string, movieIndex string) string {
	if strings.TrimSpace(title) == "" {
		return title
	}
	if strings.TrimSpace(episode) != "" || strings.TrimSpace(movieIndex) == "" {
		return title
	}
	if !movieDashEpisodePattern.MatchString(rawTitle) {
		return title
	}
	out := strings.TrimSpace(title)
	out = reTrimMovieDashEpisodeTailFromTitle1.ReplaceAllString(out, "")
	out = strings.TrimSpace(strings.Trim(out, "-:： "))
	if out != "" && !isMovieKeywordText(out) {
		base := strings.TrimSpace(movieIndex)
		if base != "" {
			movieWordPattern := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(out) + `\s+movie\s*0*` + regexp.QuoteMeta(base) + `\s*[-:：]\s*0*\d{1,3}\b`)
			if movieWordPattern.MatchString(rawTitle) {
				out = strings.TrimSpace(out + " Movie")
			}
		}
	}
	if out == "" {
		return title
	}
	return out
}

func trimMovieRangeStartTailFromTitle(rawTitle string, title string, movieIndexRange string, episode string, episodeRange string) string {
	if strings.TrimSpace(title) == "" {
		return title
	}
	if strings.TrimSpace(movieIndexRange) == "" {
		return title
	}
	if strings.TrimSpace(episode) != "" || strings.TrimSpace(episodeRange) != "" {
		return title
	}
	rng := strings.SplitN(strings.TrimSpace(movieIndexRange), "-", 2)
	if len(rng) != 2 {
		return title
	}
	start := strings.TrimLeft(strings.TrimSpace(rng[0]), "0")
	end := strings.TrimLeft(strings.TrimSpace(rng[1]), "0")
	if start == "" || end == "" {
		return title
	}
	if start == "0" || end == "0" {
		return title
	}
	ctxPattern := regexp.MustCompile(`(?i)(?:movie|film|劇場版|剧场版)\s*0*` + regexp.QuoteMeta(start) + `\s*[-~]\s*0*` + regexp.QuoteMeta(end) + `\b`)
	if !ctxPattern.MatchString(rawTitle) {
		return title
	}
	out := regexp.MustCompile(`(?i)((?:movie|film|劇場版|剧场版)\s*)0*`+regexp.QuoteMeta(start)+`\s*$`).ReplaceAllString(strings.TrimSpace(title), "$1")
	out = strings.TrimSpace(strings.Trim(out, "-:：_ "))
	if out == "" {
		return title
	}
	return out
}

func hasMovieRouteHardExclusion(rawTitle string, attrs []model.Attribute, episodeRange string, season string, seasonRange string, packKind string) bool {
	if strings.TrimSpace(episodeRange) != "" {
		return true
	}
	if strings.TrimSpace(seasonRange) != "" {
		return true
	}
	if strings.TrimSpace(season) != "" {
		return true
	}
	if isMovieExcludedByPackKind(packKind) {
		return true
	}
	if rawMovieMixPattern.MatchString(rawTitle) {
		return true
	}
	hasMovie := false
	hasTVLike := false
	for _, a := range attrs {
		if a.Type != model.AttrContents {
			continue
		}
		v := strings.ToUpper(strings.TrimSpace(a.Normalized))
		if v == "" {
			continue
		}
		switch v {
		case "MOVIE":
			hasMovie = true
		case "TV", "OVA", "OAD", "ONA", "SP", "EX":
			hasTVLike = true
		}
		if hasMovie && hasTVLike {
			return true
		}
	}
	return false
}

func isMovieExcludedByPackKind(packKind string) bool {
	pk := strings.ToLower(strings.TrimSpace(packKind))
	if pk == "" {
		return false
	}
	switch pk {
	case "batch", "complete", "collection", "boxset", "box set", "全集", "合集", "全话", "全話", "完结", "完結", "mixed_pack", "tv_pack":
		return true
	default:
		return false
	}
}

func hasMovieContentSignal(attrs []model.Attribute, hasMovieKeyword bool, movieIndex string, spKind string) bool {
	if hasMovieKeyword || strings.TrimSpace(movieIndex) != "" || strings.EqualFold(strings.TrimSpace(spKind), "MOVIE") {
		return true
	}
	for _, a := range attrs {
		if a.Type == model.AttrMovieFlag {
			if strings.TrimSpace(a.Normalized) != "" {
				return true
			}
			continue
		}
		if a.Type != model.AttrContents {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(a.Normalized), "MOVIE") {
			return true
		}
	}
	return false
}

func inferPackKindFromContentsAttrs(currentPackKind string, attrs []model.Attribute) string {
	if strings.TrimSpace(currentPackKind) != "" {
		return strings.TrimSpace(currentPackKind)
	}
	seen := map[string]struct{}{}
	hasTV := false
	hasOther := false
	for _, a := range attrs {
		if a.Type != model.AttrContents {
			continue
		}
		n := strings.ToUpper(strings.TrimSpace(a.Normalized))
		if n == "" {
			continue
		}
		seen[n] = struct{}{}
		if n == "TV" {
			hasTV = true
		} else {
			hasOther = true
		}
	}
	if hasTV && hasOther {
		return "mixed_contents"
	}
	// Require at least two distinct non-TV content markers to derive "contents",
	// so a single "(Movie)" marker does not force content_kind to BATCH.
	if len(seen) >= 2 {
		return "contents"
	}
	return ""
}

func inferSPKindFromContentsAttrs(attrs []model.Attribute) string {
	for _, a := range attrs {
		if a.Type != model.AttrContents {
			continue
		}
		if n, ok := normalizeSPKindFromContents(a.Normalized); ok {
			return n
		}
	}
	return ""
}

func normalizeSPKindFromContents(v string) (string, bool) {
	n := strings.ToUpper(strings.TrimSpace(v))
	switch n {
	case "SPECIAL", "SP":
		return "SP", true
	case "OVA", "OAD", "ONA", "NCOP", "NCED", "PV":
		return n, true
	default:
		return "", false
	}
}

func dropContentsAttrsInsideTitleSpan(attrs []model.Attribute, titleStart int, titleEnd int) []model.Attribute {
	if len(attrs) == 0 {
		return attrs
	}
	if titleStart < 0 || titleEnd <= titleStart {
		return attrs
	}
	out := make([]model.Attribute, 0, len(attrs))
	for _, a := range attrs {
		if a.Type == model.AttrContents && a.TokenStart >= titleStart && a.TokenStart < titleEnd {
			// Do not treat in-title markers (e.g. "剧场版" in canonical title span)
			// as contents/meta markers.
			continue
		}
		out = append(out, a)
	}
	return out
}

func (p *Parser) inferPackProfile(contentKind model.ContentKind, packKind string, season string, seasonRange string, attrs []model.Attribute) string {
	contentKindUp := string(contentKind.Canonical())
	packKindLow := strings.ToLower(strings.TrimSpace(packKind))
	hasSeason := strings.TrimSpace(season) != ""
	hasSeasonRange := strings.TrimSpace(seasonRange) != ""
	contentSet := collectContentsSet(attrs)

	for _, r := range p.packProfileRules {
		if !packProfileRuleMatches(r, contentKindUp, packKindLow, contentSet, hasSeason, hasSeasonRange) {
			continue
		}
		return r.emit
	}
	return ""
}

func packProfileRuleMatches(r packProfileRule, contentKind string, packKind string, contentSet map[string]struct{}, hasSeason bool, hasSeasonRange bool) bool {
	if len(r.contentKindIn) > 0 {
		if _, ok := r.contentKindIn[contentKind]; !ok {
			return false
		}
	}
	if len(r.packKindIn) > 0 {
		if _, ok := r.packKindIn[packKind]; !ok {
			return false
		}
	}
	if len(r.packKindNotIn) > 0 {
		if _, blocked := r.packKindNotIn[packKind]; blocked {
			return false
		}
	}
	if r.hasSeason != nil && *r.hasSeason != hasSeason {
		return false
	}
	if r.hasSeasonRange != nil && *r.hasSeasonRange != hasSeasonRange {
		return false
	}
	if r.minContentsDistinct > 0 && len(contentSet) < r.minContentsDistinct {
		return false
	}
	if len(r.requiresContentsAll) > 0 {
		for k := range r.requiresContentsAll {
			if _, ok := contentSet[k]; !ok {
				return false
			}
		}
	}
	if len(r.requiresContentsAny) > 0 {
		hit := false
		for k := range r.requiresContentsAny {
			if _, ok := contentSet[k]; ok {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(r.forbidContentsAny) > 0 {
		for k := range r.forbidContentsAny {
			if _, blocked := contentSet[k]; blocked {
				return false
			}
		}
	}
	return true
}

func collectContentsSet(attrs []model.Attribute) map[string]struct{} {
	out := map[string]struct{}{}
	for _, a := range attrs {
		if a.Type != model.AttrContents {
			continue
		}
		n := strings.ToUpper(strings.TrimSpace(a.Normalized))
		if n == "" {
			continue
		}
		out[n] = struct{}{}
	}
	return out
}

func computeEpRoute(tokens []model.Token, episode string, episodeList string, episodeRange string, season string, seasonRange string, spKind string, movieIndex string, hasMovieKeyword bool, movieRouteHardExcluded bool) string {
	// Priority (high -> low):
	// RANGE (TV signal) > EP_LIST > SXXEXX > ABS_EP > PLAIN_EP > MOVIE_ONLY > SP_ONLY > NONE
	if strings.TrimSpace(episodeRange) != "" {
		return "RANGE"
	}
	// Tagged ranges like ABS:01-12 / TV01-12 / OVA:01-05.
	for i := 0; i < len(tokens); i++ {
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if m := taggedRangePattern.FindStringSubmatch(txt); len(m) == 3 {
			val := strings.TrimSpace(m[2])
			if strings.ContainsAny(val, "-~") {
				return "RANGE"
			}
		}
	}
	if hasSxxExxInTokens(tokens) && strings.TrimSpace(season) != "" && strings.TrimSpace(episode) != "" {
		return "SXXEXX"
	}
	if strings.TrimSpace(episodeList) != "" {
		return "EP_LIST"
	}
	// Tagged ABS episode like ABS:12.
	for i := 0; i < len(tokens); i++ {
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if m := taggedRangePattern.FindStringSubmatch(txt); len(m) == 3 {
			axis := strings.ToUpper(strings.TrimSpace(m[1]))
			val := strings.TrimSpace(m[2])
			if axis == "ABS" && !strings.ContainsAny(val, "-~") {
				return "ABS_EP"
			}
		}
	}
	if strings.TrimSpace(episode) != "" {
		return "PLAIN_EP"
	}
	if strings.TrimSpace(spKind) != "" {
		return "SP_ONLY"
	}
	// For season packs, do not route as SP_ONLY even if sp_kind exists.
	// These are still packs (BATCH) and should match BATCH templates.
	if strings.TrimSpace(seasonRange) != "" {
		return "NONE"
	}
	if strings.TrimSpace(season) != "" {
		return "NONE"
	}
	if !movieRouteHardExcluded && (strings.TrimSpace(movieIndex) != "" || hasMovieKeyword) {
		return "MOVIE_ONLY"
	}
	return "NONE"
}

var packExtraMarkerPattern = regexp.MustCompile(`(?i)^(?:\+\s*)?(movie|special|sp|ova|oad|ona|ex)$`)
var packSeasonTokenPattern = regexp.MustCompile(`(?i)^s0*\d{1,2}$`)

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reTrimMovieDashEpisodeTailFromTitle1 = regexp.MustCompile(`(?:\s*[-:：]\s*)?0*\d{1,3}\s*$`)
)
