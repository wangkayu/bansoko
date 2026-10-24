package parser

import (
	"strconv"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

type epSeasonHint struct {
	Season   string
	TokenIdx int
	Source   string
}

type epSuppressedCandidate struct {
	Candidate model.EPEntity
	Reason    string
}

type epDecision struct {
	Candidates  []model.EPEntity
	SeasonHints []epSeasonHint
	Primary     *model.EPEntity
	Suppressed  []epSuppressedCandidate
}

func newEPDecision() *epDecision {
	return &epDecision{}
}

func (d *epDecision) addSeasonHint(season string, tokenIdx int, source string) {
	if d == nil {
		return
	}
	season = strings.TrimSpace(season)
	source = strings.TrimSpace(source)
	if season == "" {
		return
	}
	for _, h := range d.SeasonHints {
		if h.Season == season && h.TokenIdx == tokenIdx && strings.EqualFold(h.Source, source) {
			return
		}
	}
	d.SeasonHints = append(d.SeasonHints, epSeasonHint{
		Season:   season,
		TokenIdx: tokenIdx,
		Source:   source,
	})
}

func (d *epDecision) replaceCandidates(cands []model.EPEntity) {
	if d == nil {
		return
	}
	if len(cands) == 0 {
		d.Candidates = nil
		return
	}
	out := make([]model.EPEntity, 0, len(cands))
	seen := make(map[string]struct{}, len(cands))
	for _, c := range cands {
		key := epslotCandidateKey(c)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	d.Candidates = out
}

func epslotCandidateKey(c model.EPEntity) string {
	return strings.TrimSpace(string(c.Kind)) + "|" +
		strings.TrimSpace(string(c.Axis)) + "|" +
		strings.TrimSpace(c.Season) + "|" +
		strings.TrimSpace(c.Episode) + "|" +
		strings.TrimSpace(c.EpisodeRange) + "|" +
		strings.TrimSpace(c.EpisodeList) + "|" +
		strings.TrimSpace(c.SPKind) + "|" +
		strconv.Itoa(c.TokenStart) + ":" + strconv.Itoa(c.TokenEnd) + "|" +
		strings.TrimSpace(string(c.Source))
}

func scoreSeasonHint(h epSeasonHint) int {
	switch strings.ToLower(strings.TrimSpace(h.Source)) {
	case "sxxexx":
		return 150
	case "attr":
		return 140
	case "season_keyword":
		return 135
	case "season_before_dash":
		return 130
	case "season_in_paren":
		return 125
	case "season_only":
		return 115
	case "season_only_loose":
		return 105
	case "season_range_keyword":
		return 100
	default:
		return 90
	}
}

func selectBestSeasonHint(hints []epSeasonHint) *epSeasonHint {
	if len(hints) == 0 {
		return nil
	}
	bestIdx := -1
	bestScore := -1
	bestTokenIdx := 1 << 30
	for i, h := range hints {
		score := scoreSeasonHint(h)
		tokenIdx := h.TokenIdx
		if tokenIdx < 0 {
			tokenIdx = 1 << 29
		}
		if score > bestScore || (score == bestScore && tokenIdx < bestTokenIdx) {
			bestIdx = i
			bestScore = score
			bestTokenIdx = tokenIdx
		}
	}
	if bestIdx < 0 {
		return nil
	}
	h := hints[bestIdx]
	return &h
}

func looksLikeExplicitRangeToken(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	text := strings.TrimSpace(tokens[idx].Text)
	if text == "" {
		return false
	}
	if taggedRangePattern.FindStringSubmatch(text) != nil {
		return true
	}
	if episodeRangePattern.MatchString(text) {
		return true
	}
	if m := episodeRangeEmbeddedPattern.FindStringSubmatch(text); len(m) == 3 {
		return true
	}
	return false
}

func hasSeasonLeadForRange(tokens []model.Token, idx int, seasonHint *epSeasonHint) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	if idx-1 >= 0 {
		prev := strings.TrimSpace(tokens[idx-1].Text)
		if seasonWordLoosePattern.MatchString(prev) {
			return true
		}
	}
	if seasonHint == nil {
		return false
	}
	if seasonHint.TokenIdx == idx {
		return true
	}
	if seasonHint.TokenIdx == idx-1 && idx-1 >= 0 {
		prev := strings.TrimSpace(tokens[idx-1].Text)
		if seasonWordLoosePattern.MatchString(prev) {
			return true
		}
	}
	return false
}

func hasEpisodeCandidateNear(cands []model.EPEntity, idx int) bool {
	for _, c := range cands {
		kind := c.Kind.Canonical()
		if kind != model.EPKindEP && kind != model.EPKindEPList {
			continue
		}
		if c.TokenStart < 0 {
			continue
		}
		if c.TokenStart >= idx && c.TokenStart <= idx+2 {
			return true
		}
	}
	return false
}

func hasSPCandidate(cands []model.EPEntity) bool {
	for _, c := range cands {
		if c.Kind.Canonical() == model.EPKindSP {
			return true
		}
	}
	return false
}

func hasSPCandidateNear(cands []model.EPEntity, idx int) bool {
	for _, c := range cands {
		if c.Kind.Canonical() != model.EPKindSP {
			continue
		}
		if c.TokenStart < 0 {
			continue
		}
		if c.TokenStart >= idx+1 && c.TokenStart <= idx+2 {
			return true
		}
	}
	return false
}

func isAliasSeasonEchoRange(tokens []model.Token, idx int, seasonHint *epSeasonHint, cands []model.EPEntity) bool {
	if seasonHint == nil || idx < 0 || idx+2 >= len(tokens) {
		return false
	}
	if !looksLikeAliasSeasonEchoRange(tokens, idx, seasonHint.Season) {
		return false
	}
	return hasEpisodeCandidateNear(cands, idx+1)
}

func shouldSuppressRangeCandidate(tokens []model.Token, c model.EPEntity, seasonHint *epSeasonHint, cands []model.EPEntity) (string, bool) {
	if c.Kind.Canonical() != model.EPKindRange {
		return "", false
	}
	if c.TokenStart < 0 || c.TokenStart >= len(tokens) {
		return "", false
	}
	if isSharedPrefixSeasonEpisodeRangeAt(tokens, c.TokenStart) {
		return "", false
	}
	if hasPartIndexLeadBeforeToken(tokens, c.TokenStart) && hasEpisodeCandidateNear(cands, c.TokenStart+1) {
		return "part_index_range_conflict", true
	}
	if isAliasSeasonEchoRange(tokens, c.TokenStart, seasonHint, cands) {
		return "alias_season_echo_range_conflict", true
	}
	if hasSeasonLeadForRange(tokens, c.TokenStart, seasonHint) && hasEpisodeCandidateNear(cands, c.TokenStart+1) {
		return "season_episode_range_conflict", true
	}
	return "", false
}

func shouldSuppressEpisodeCandidate(tokens []model.Token, c model.EPEntity, seasonHint *epSeasonHint, cands []model.EPEntity) (string, bool) {
	if c.Kind.Canonical() != model.EPKindEP {
		return "", false
	}
	if c.Source.Canonical() != model.EPSourceEPDigitToken {
		return "", false
	}
	if !hasSPCandidate(cands) {
		return "", false
	}
	if seasonHint == nil {
		return "", false
	}
	if strings.TrimSpace(c.Episode) == "" || strings.TrimSpace(c.Episode) != strings.TrimSpace(seasonHint.Season) {
		return "", false
	}
	if c.TokenStart < 0 || c.TokenStart >= len(tokens) {
		return "", false
	}
	if hasMatchingSeasonNumberAroundToken(tokens, c.TokenStart, 12) && hasSPCandidateNear(cands, c.TokenStart) {
		return "season_special_episode_conflict", true
	}
	if hasMatchingSeasonNumberAroundToken(tokens, c.TokenStart, 12) && !hasDashSeparatorAroundToken(tokens, c.TokenStart) {
		return "season_special_episode_conflict", true
	}
	return "", false
}

func scoreEPCandidate(tokens []model.Token, c model.EPEntity) int {
	kind := c.Kind.Canonical()
	source := string(c.Source.Canonical())
	switch kind {
	case model.EPKindEP:
		switch {
		case strings.Contains(source, "sxxexx"):
			return 150
		case strings.Contains(source, "tagged_ep"):
			return 142
		case source == "episode":
			return 120
		case source == "ep_digit_token":
			return 92
		default:
			return 108
		}
	case model.EPKindEPList:
		return 132
	case model.EPKindRange:
		switch {
		case strings.Contains(source, "tagged_range"):
			return 140
		case source == "range_token":
			return 136
		case looksLikeExplicitRangeToken(tokens, c.TokenStart):
			return 134
		case source == "episode_range":
			return 128
		default:
			return 124
		}
	case model.EPKindSP:
		if strings.TrimSpace(c.Episode) != "" {
			return 104
		}
		return 70
	default:
		return 0
	}
}

func choosePrimaryCandidate(tokens []model.Token, cands []model.EPEntity, seasonHint *epSeasonHint) (*model.EPEntity, []model.EPEntity, []epSuppressedCandidate) {
	if len(cands) == 0 {
		return nil, nil, nil
	}
	kept := make([]model.EPEntity, 0, len(cands))
	suppressed := make([]epSuppressedCandidate, 0, 4)
	bestIdx := -1
	bestScore := -1
	for _, cand := range cands {
		c := cand
		if strings.TrimSpace(c.Season) == "" && seasonHint != nil {
			c.Season = strings.TrimSpace(seasonHint.Season)
		}
		if reason, drop := shouldSuppressRangeCandidate(tokens, c, seasonHint, cands); drop {
			suppressed = append(suppressed, epSuppressedCandidate{
				Candidate: c,
				Reason:    reason,
			})
			continue
		}
		if reason, drop := shouldSuppressEpisodeCandidate(tokens, c, seasonHint, cands); drop {
			suppressed = append(suppressed, epSuppressedCandidate{
				Candidate: c,
				Reason:    reason,
			})
			continue
		}
		score := scoreEPCandidate(tokens, c)
		if score <= 0 {
			continue
		}
		kept = append(kept, c)
		curIdx := len(kept) - 1
		if score > bestScore {
			bestIdx = curIdx
			bestScore = score
			continue
		}
		if score == bestScore {
			best := kept[bestIdx]
			bestKind := best.Kind.Canonical()
			curKind := c.Kind.Canonical()
			if bestKind == model.EPKindSP && curKind != model.EPKindSP {
				bestIdx = curIdx
				continue
			}
			if bestKind == model.EPKindEP && curKind == model.EPKindRange {
				bestIdx = curIdx
				continue
			}
		}
	}
	if bestIdx < 0 {
		return nil, kept, suppressed
	}
	best := kept[bestIdx]
	return &best, kept, suppressed
}

func buildCanonicalPrimaryEntity(season string, episode string, episodeRange string, episodeList string, spKind string, episodeIdx int, episodeRangeIdx int, episodeListIdx int) *model.EPEntity {
	season = strings.TrimSpace(season)
	episode = strings.TrimSpace(episode)
	episodeRange = strings.TrimSpace(episodeRange)
	episodeList = strings.TrimSpace(episodeList)
	spKind = strings.ToUpper(strings.TrimSpace(spKind))
	axis := model.EPAxisABS
	if season != "" {
		axis = model.EPAxisSE
	}
	switch {
	case episodeRange != "":
		return &model.EPEntity{
			Kind:         model.EPKindRange,
			Axis:         axis,
			Season:       season,
			EpisodeRange: episodeRange,
			TokenStart:   episodeRangeIdx,
			TokenEnd:     episodeRangeIdx + 1,
			Source:       model.EPSourceCanonical,
		}
	case episodeList != "":
		first := episode
		if first == "" {
			if parts := strings.Split(episodeList, ","); len(parts) > 0 {
				first = strings.TrimSpace(parts[0])
			}
		}
		return &model.EPEntity{
			Kind:        model.EPKindEPList,
			Axis:        axis,
			Season:      season,
			Episode:     first,
			EpisodeList: episodeList,
			TokenStart:  episodeListIdx,
			TokenEnd:    episodeListIdx + 1,
			Source:      model.EPSourceCanonical,
		}
	case episode != "":
		return &model.EPEntity{
			Kind:       model.EPKindEP,
			Axis:       axis,
			Season:     season,
			Episode:    episode,
			TokenStart: episodeIdx,
			TokenEnd:   episodeIdx + 1,
			Source:     model.EPSourceCanonical,
		}
	case spKind != "":
		return &model.EPEntity{
			Kind:   model.EPKindSP,
			Axis:   model.EPAxis(spKind),
			Season: season,
			SPKind: spKind,
			Source: model.EPSourceCanonical,
		}
	default:
		return nil
	}
}

func canonicalPrimaryMatches(c model.EPEntity, primary *model.EPEntity) bool {
	if primary == nil {
		return false
	}
	cRange := strings.TrimSpace(c.EpisodeRange)
	pRange := strings.TrimSpace(primary.EpisodeRange)
	if cRange != "" || pRange != "" {
		cRange = normalizeEpisodeRangeText(cRange)
		pRange = normalizeEpisodeRangeText(pRange)
	}
	return c.Kind.Canonical() == primary.Kind.Canonical() &&
		strings.TrimSpace(c.Season) == strings.TrimSpace(primary.Season) &&
		strings.TrimSpace(c.Episode) == strings.TrimSpace(primary.Episode) &&
		cRange == pRange &&
		strings.TrimSpace(c.EpisodeList) == strings.TrimSpace(primary.EpisodeList) &&
		strings.EqualFold(strings.TrimSpace(c.SPKind), strings.TrimSpace(primary.SPKind))
}

func syncEPSlotWithCanonicalSignals(epslot *model.EPSlot, season string, episode string, episodeRange string, episodeList string, spKind string, episodeIdx int, episodeRangeIdx int, episodeListIdx int) *model.EPSlot {
	primary := buildCanonicalPrimaryEntity(season, episode, episodeRange, episodeList, spKind, episodeIdx, episodeRangeIdx, episodeListIdx)
	if primary == nil {
		if epslot == nil || len(epslot.Candidates) == 0 {
			return nil
		}
		epslot.Primary = nil
		return epslot
	}
	if epslot == nil {
		return &model.EPSlot{
			Primary:    primary,
			Candidates: []model.EPEntity{*primary},
		}
	}
	resolvedPrimary := *primary
	if epslot.Primary != nil && canonicalPrimaryMatches(*epslot.Primary, primary) {
		resolvedPrimary = *epslot.Primary
		resolvedPrimary.Season = primary.Season
		resolvedPrimary.Episode = primary.Episode
		resolvedPrimary.EpisodeRange = primary.EpisodeRange
		resolvedPrimary.EpisodeList = primary.EpisodeList
		resolvedPrimary.SPKind = primary.SPKind
	}
	cands := make([]model.EPEntity, 0, len(epslot.Candidates)+1)
	seen := map[string]struct{}{}
	add := func(c model.EPEntity) {
		key := epslotCandidateKey(c)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		cands = append(cands, c)
	}
	add(resolvedPrimary)
	for _, c := range epslot.Candidates {
		if canonicalPrimaryMatches(c, primary) {
			continue
		}
		add(c)
	}
	epslot.Primary = &resolvedPrimary
	epslot.Candidates = cands
	return epslot
}

func adjudicateEPDecisionIntoContext(tokens []model.Token, ctx *parseContext) {
	if ctx == nil {
		return
	}
	if ctx.EPDecision == nil {
		ctx.EPDecision = newEPDecision()
	}
	seasonHint := selectBestSeasonHint(ctx.EPDecision.SeasonHints)
	if seasonHint != nil {
		ctx.Season = strings.TrimSpace(seasonHint.Season)
		ctx.SeasonIdx = seasonHint.TokenIdx
	}
	primary, kept, suppressed := choosePrimaryCandidate(tokens, ctx.EPDecision.Candidates, seasonHint)
	ctx.EPDecision.Primary = primary
	ctx.EPDecision.Suppressed = suppressed

	if primary == nil {
		ctx.Episode = ""
		ctx.EpisodeIdx = -1
		ctx.EpisodeRange = ""
		ctx.EpisodeRangeIdx = -1
		ctx.EpisodeList = ""
		ctx.EpisodeListIdx = -1
		ctx.EpisodeVersion = ""
		ctx.EpisodeVersionIdx = -1
		if len(kept) > 0 {
			ctx.EPSlot = &model.EPSlot{Candidates: kept}
		} else {
			ctx.EPSlot = nil
		}
		return
	}

	primaryCopy := *primary
	primaryCopy.Source = finalizeEPCandidateSource(primaryCopy)
	if strings.TrimSpace(primaryCopy.Season) == "" && seasonHint != nil {
		primaryCopy.Season = strings.TrimSpace(seasonHint.Season)
	}
	ctx.Episode = ""
	ctx.EpisodeIdx = -1
	ctx.EpisodeRange = ""
	ctx.EpisodeRangeIdx = -1
	ctx.EpisodeList = ""
	ctx.EpisodeListIdx = -1
	switch primaryCopy.Kind.Canonical() {
	case model.EPKindRange:
		ctx.EpisodeRange = strings.TrimSpace(primaryCopy.EpisodeRange)
		ctx.EpisodeRangeIdx = primaryCopy.TokenStart
		ctx.EpisodeVersion = ""
		ctx.EpisodeVersionIdx = -1
	case model.EPKindEPList:
		ctx.Episode = strings.TrimSpace(primaryCopy.Episode)
		ctx.EpisodeIdx = primaryCopy.TokenStart
		ctx.EpisodeList = strings.TrimSpace(primaryCopy.EpisodeList)
		ctx.EpisodeListIdx = primaryCopy.TokenStart
		ctx.EpisodeVersion = ""
		ctx.EpisodeVersionIdx = -1
	case model.EPKindEP:
		ctx.Episode = strings.TrimSpace(primaryCopy.Episode)
		ctx.EpisodeIdx = primaryCopy.TokenStart
		if ctx.EpisodeVersionIdx != ctx.EpisodeIdx {
			ctx.EpisodeVersion = ""
			ctx.EpisodeVersionIdx = -1
		}
	case model.EPKindSP:
		ctx.EpisodeVersion = ""
		ctx.EpisodeVersionIdx = -1
	}
	if strings.TrimSpace(primaryCopy.Season) != "" {
		ctx.Season = strings.TrimSpace(primaryCopy.Season)
	}
	if strings.TrimSpace(primaryCopy.SPKind) != "" {
		ctx.SPKind = strings.ToUpper(strings.TrimSpace(primaryCopy.SPKind))
	}
	ctx.EPSlot = &model.EPSlot{
		Primary:    &primaryCopy,
		Candidates: kept,
	}
}

func finalizeEPCandidateSource(c model.EPEntity) model.EPSource {
	switch c.Source.Canonical() {
	case model.EPSourceSxxExx:
		return model.EPSourceSxxExxFinal
	case model.EPSourceEpisodeList:
		return model.EPSourceEpisodeListFinal
	case model.EPSourceEpisodeRange:
		return model.EPSourceEpisodeRangeFinal
	case model.EPSourceEpisode:
		return model.EPSourceEpisodeFinal
	case model.EPSourceTaggedEP:
		return model.EPSourceTaggedEPFinal
	case model.EPSourceTaggedRange:
		return model.EPSourceTaggedRangeFinal
	case model.EPSourceSPKind, model.EPSourceSPToken:
		return model.EPSourceSPKindFinal
	default:
		if strings.TrimSpace(string(c.Source)) == "" {
			return model.EPSourceCanonical
		}
		return model.EPSource(strings.TrimSpace(string(c.Source)))
	}
}

func buildCanonicalEPSemantics(rawTitle string, tokens []model.Token, epslot *model.EPSlot, season string, seasonRange string, episode string, episodeList string, episodeRange string, spKind string) *model.EPSemantics {
	var primary *model.EPReference
	if epslot != nil {
		primary = buildEPReferenceFromPrimaryCandidate(epslot.Primary, season, seasonRange, episode, episodeList, episodeRange, spKind)
	} else {
		primary = buildEPReferenceFromPrimaryCandidate(nil, season, seasonRange, episode, episodeList, episodeRange, spKind)
	}
	if primary == nil {
		return nil
	}
	primary = overridePrimaryWithSummaryCatalogEpisode(rawTitle, primary)
	out := &model.EPSemantics{
		Primary:    primary,
		Alternates: collectAlternateEPReferences(rawTitle, tokens, primary),
	}
	return out
}

func splitEpisodeListCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func splitNormalizedRangeBounds(s string) (string, string) {
	s = normalizeEpisodeRangeText(strings.TrimSpace(s))
	if s == "" {
		return "", ""
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}
