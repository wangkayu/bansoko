package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func stripLegacyEPAttributes(attrs []model.Attribute) []model.Attribute {
	attrs = removeAttributeType(attrs, model.AttrEpisode)
	attrs = removeAttributeType(attrs, model.AttrEpisodeList)
	attrs = removeAttributeType(attrs, model.AttrEpisodeRange)
	attrs = removeAttributeType(attrs, model.AttrSeason)
	attrs = removeAttributeType(attrs, model.AttrSeasonRange)
	attrs = removeAttributeType(attrs, model.AttrSPKind)
	return attrs
}

func pruneCanonicalEpisodeSignalsForOutput(rawTitle string, contentKind model.ContentKind, packKind string, season string, episode string, episodeList string, episodeRange string, spKind string, movieIndex string, movieIndexRange string, epslot *model.EPSlot) (model.ContentKind, string, string, string, *model.EPSlot) {
	kind := contentKind.Canonical()
	if strings.TrimSpace(movieIndex) != "" && strings.TrimSpace(movieIndexRange) == "" && strings.TrimSpace(season) == "" && strings.TrimSpace(episodeRange) != "" && movieDashEpisodePattern.MatchString(rawTitle) {
		return model.ContentKindMovie, "", "", "", nil
	}
	if kind != model.ContentKindBatch {
		return contentKind, episode, episodeList, episodeRange, epslot
	}
	pk := strings.TrimSpace(packKind)
	if pk == "" {
		return contentKind, episode, episodeList, episodeRange, epslot
	}
	if strings.TrimSpace(spKind) != "" || strings.TrimSpace(season) != "" || strings.TrimSpace(movieIndexRange) != "" {
		return contentKind, episode, episodeList, episodeRange, epslot
	}
	if strings.TrimSpace(episode) != "" && strings.TrimSpace(episodeList) == "" && strings.TrimSpace(episodeRange) == "" && strings.TrimSpace(movieIndex) == "" {
		return contentKind, "", "", "", nil
	}
	if strings.TrimSpace(episodeRange) != "" && hasAmbiguousExtraNumberAfterPrimaryRange(rawTitle, episodeRange) {
		return contentKind, "", "", "", nil
	}
	return contentKind, episode, episodeList, episodeRange, epslot
}

func hasAmbiguousExtraNumberAfterPrimaryRange(rawTitle string, episodeRange string) bool {
	bounds := strings.SplitN(normalizeEpisodeRangeText(strings.TrimSpace(episodeRange)), "-", 2)
	if len(bounds) != 2 {
		return false
	}
	left := strings.TrimSpace(bounds[0])
	right := strings.TrimSpace(bounds[1])
	if left == "" || right == "" {
		return false
	}
	pat := regexp.MustCompile(`(?i)\b0*` + regexp.QuoteMeta(left) + `\s*[-~]\s*0*` + regexp.QuoteMeta(right) + `\b`)
	loc := pat.FindStringIndex(rawTitle)
	if loc == nil {
		return false
	}
	tail := rawTitle[loc[1]:]
	if !reHasAmbiguousExtraNumberAfterPrimaryRange1.MatchString(tail) {
		return false
	}
	return reHasAmbiguousExtraNumberAfterPrimaryRange2.MatchString(tail)
}

func pruneBatchEPSlotForConservativeOutput(epslot *model.EPSlot, contentKind model.ContentKind, episode string, episodeRange string, spKind string) *model.EPSlot {
	if epslot == nil {
		return nil
	}
	kind := contentKind.Canonical()
	if kind != model.ContentKindBatch && kind != model.ContentKindMovie {
		return epslot
	}
	// For BATCH/MOVIE outputs, keep epslot only when we have a confirmed primary or
	// explicit canonical episode-like fields. Otherwise candidate-only epslot is
	// often noisy (e.g. digits inside title fragments) and should be suppressed.
	if epslot.Primary != nil {
		if shouldPruneWeakBatchRangePrimary(epslot, episodeRange, spKind) {
			return nil
		}
		return epslot
	}
	if strings.TrimSpace(episode) != "" || strings.TrimSpace(episodeRange) != "" || strings.TrimSpace(spKind) != "" {
		return epslot
	}
	return nil
}

func shouldPruneWeakBatchRangePrimary(epslot *model.EPSlot, episodeRange string, spKind string) bool {
	if epslot == nil || epslot.Primary == nil {
		return false
	}
	if epslot.Primary.Kind.Canonical() != model.EPKindRange {
		return false
	}
	if strings.TrimSpace(spKind) != "" {
		return false
	}
	rangeText := strings.TrimSpace(episodeRange)
	if rangeText == "" {
		rangeText = strings.TrimSpace(epslot.Primary.EpisodeRange)
	}
	if rangeText == "" {
		return false
	}
	source := epslot.Primary.Source.Canonical()
	if source != model.EPSourceEpisodeRange && source != model.EPSourceEpisodeRangeFinal {
		return false
	}
	bounds := strings.SplitN(normalizeEpisodeRangeText(rangeText), "-", 2)
	if len(bounds) != 2 {
		return false
	}
	left := strings.TrimSpace(bounds[0])
	right := strings.TrimSpace(bounds[1])
	for _, c := range epslot.Candidates {
		kind := c.Kind.Canonical()
		if kind != model.EPKindEP && kind != model.EPKindEPList {
			continue
		}
		ep := strings.TrimSpace(c.Episode)
		if ep == "" || ep == left || ep == right {
			continue
		}
		return true
	}
	return false
}

func shouldPreferSPPrimaryForSpecial(contentKind model.ContentKind, spKind string, epslot *model.EPSlot) bool {
	if contentKind.Canonical() != model.ContentKindSpecial {
		return false
	}
	if strings.TrimSpace(spKind) == "" || epslot == nil || epslot.Primary == nil {
		return false
	}
	if epslot.Primary.Kind.Canonical() != model.EPKindEP {
		return false
	}
	return epslot.Primary.Source.Canonical() == model.EPSourceEPDigitToken
}

func forceSPPrimaryFromSPKind(epslot *model.EPSlot, season string, spKind string) *model.EPSlot {
	k := strings.ToUpper(strings.TrimSpace(spKind))
	if k == "" {
		return epslot
	}
	primary := model.EPEntity{
		Kind:   model.EPKindSP,
		Axis:   model.EPAxis(k),
		Season: strings.TrimSpace(season),
		SPKind: k,
		Source: model.EPSourceSPKindFinal,
	}
	if epslot == nil {
		return &model.EPSlot{Primary: &primary, Candidates: []model.EPEntity{{Kind: model.EPKindSP, Axis: model.EPAxis(k), Season: strings.TrimSpace(season), SPKind: k, Source: model.EPSourceSPKind}}}
	}
	candidates := make([]model.EPEntity, 0, len(epslot.Candidates)+1)
	candidates = append(candidates, model.EPEntity{Kind: model.EPKindSP, Axis: model.EPAxis(k), Season: strings.TrimSpace(season), SPKind: k, Source: model.EPSourceSPKind})
	for _, c := range epslot.Candidates {
		if c.Kind.Canonical() == model.EPKindSP && strings.EqualFold(strings.TrimSpace(c.SPKind), k) {
			continue
		}
		candidates = append(candidates, c)
	}
	epslot.Primary = &primary
	epslot.Candidates = candidates
	return epslot
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reHasAmbiguousExtraNumberAfterPrimaryRange1 = regexp.MustCompile(`(?i)\b(?:and|&|\+|plus)\b`)
	reHasAmbiguousExtraNumberAfterPrimaryRange2 = regexp.MustCompile(`\b\d{1,4}\b`)
)
