package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

var parenSummaryAltSxxExxPattern = regexp.MustCompile(`(?i)(\d{1,4}(?:\.\d{1,2})?)\s*(?:总集篇|總集篇|总集|總集|recap|special)\s*[\(（]\s*S0*(\d{1,2})E0*(\d{1,4})\s*[\)）]`)

func buildEPReferenceFromPrimaryCandidate(p *model.EPEntity, season string, seasonRange string, episode string, episodeList string, episodeRange string, spKind string) *model.EPReference {
	season = strings.TrimSpace(season)
	seasonRange = strings.TrimSpace(seasonRange)
	episode = strings.TrimSpace(episode)
	episodeList = strings.TrimSpace(episodeList)
	episodeRange = strings.TrimSpace(episodeRange)
	spKind = strings.TrimSpace(spKind)

	if p != nil {
		kind := p.Kind.Canonical()
		if kind != "" && kind != model.EPKindNone {
			out := &model.EPReference{
				Kind:       kind,
				Axis:       p.Axis.Canonical(),
				Season:     firstNonEmpty(strings.TrimSpace(p.Season), season),
				SPKind:     firstNonEmpty(strings.TrimSpace(p.SPKind), spKind),
				Source:     model.EPSource(strings.TrimSpace(string(p.Source))),
				TokenStart: p.TokenStart,
				TokenEnd:   p.TokenEnd,
			}
			switch out.Kind {
			case model.EPKindEP:
				out.Episode = strings.TrimSpace(p.Episode)
			case model.EPKindEPList:
				out.Episode = strings.TrimSpace(p.Episode)
				out.EpisodeList = splitEpisodeListCSV(strings.TrimSpace(p.EpisodeList))
			case model.EPKindRange:
				start, end := splitNormalizedRangeBounds(firstNonEmpty(strings.TrimSpace(p.EpisodeRange), episodeRange))
				out.RangeStart = start
				out.RangeEnd = end
			case model.EPKindSeasonRange:
				out.SeasonRange = firstNonEmpty(strings.TrimSpace(p.EpisodeRange), seasonRange)
			}
			return out
		}
	}

	if seasonRange != "" {
		return &model.EPReference{
			Kind:        model.EPKindSeasonRange,
			Axis:        model.EPAxisSE,
			Season:      season,
			SeasonRange: seasonRange,
			Source:      model.EPSourceCanonical,
			TokenStart:  -1,
			TokenEnd:    -1,
		}
	}
	if season != "" && episode == "" && episodeList == "" && episodeRange == "" && spKind == "" {
		return &model.EPReference{
			Kind:       model.EPKindSeason,
			Axis:       model.EPAxisSE,
			Season:     season,
			Source:     model.EPSourceCanonical,
			TokenStart: -1,
			TokenEnd:   -1,
		}
	}
	switch {
	case episodeRange != "":
		start, end := splitNormalizedRangeBounds(episodeRange)
		axis := model.EPAxisABS
		if season != "" {
			axis = model.EPAxisSE
		}
		return &model.EPReference{
			Kind:       model.EPKindRange,
			Axis:       axis,
			Season:     season,
			RangeStart: start,
			RangeEnd:   end,
			Source:     model.EPSourceCanonical,
			TokenStart: -1,
			TokenEnd:   -1,
		}
	case episodeList != "":
		axis := model.EPAxisABS
		if season != "" {
			axis = model.EPAxisSE
		}
		return &model.EPReference{
			Kind:        model.EPKindEPList,
			Axis:        axis,
			Season:      season,
			Episode:     episode,
			EpisodeList: splitEpisodeListCSV(episodeList),
			Source:      model.EPSourceCanonical,
			TokenStart:  -1,
			TokenEnd:    -1,
		}
	case episode != "":
		axis := model.EPAxisABS
		if season != "" {
			axis = model.EPAxisSE
		}
		return &model.EPReference{
			Kind:       model.EPKindEP,
			Axis:       axis,
			Season:     season,
			Episode:    episode,
			SPKind:     strings.ToUpper(spKind),
			Source:     model.EPSourceCanonical,
			TokenStart: -1,
			TokenEnd:   -1,
		}
	case spKind != "":
		return &model.EPReference{
			Kind:       model.EPKindSP,
			Axis:       model.EPAxis(strings.ToUpper(spKind)),
			Season:     season,
			SPKind:     strings.ToUpper(spKind),
			Source:     model.EPSourceCanonical,
			TokenStart: -1,
			TokenEnd:   -1,
		}
	default:
		return nil
	}
}

func collectAlternateEPReferences(rawTitle string, tokens []model.Token, primary *model.EPReference) []model.EPReference {
	if primary == nil {
		return nil
	}
	out := make([]model.EPReference, 0, 2)
	seen := map[string]struct{}{}
	add := func(ref model.EPReference) {
		ref.Kind = ref.Kind.Canonical()
		ref.Axis = ref.Axis.Canonical()
		ref.Season = strings.TrimSpace(ref.Season)
		ref.SeasonRange = strings.TrimSpace(ref.SeasonRange)
		ref.Episode = strings.TrimSpace(ref.Episode)
		ref.RangeStart = strings.TrimSpace(ref.RangeStart)
		ref.RangeEnd = strings.TrimSpace(ref.RangeEnd)
		ref.SPKind = strings.ToUpper(strings.TrimSpace(ref.SPKind))
		ref.Relation = strings.TrimSpace(ref.Relation)
		ref.Source = model.EPSource(strings.TrimSpace(string(ref.Source)))
		if ref.Kind == "" || ref.Kind == model.EPKindNone {
			return
		}
		key := strings.Join([]string{
			string(ref.Kind),
			string(ref.Axis),
			ref.Season,
			ref.SeasonRange,
			ref.Episode,
			ref.RangeStart,
			ref.RangeEnd,
			ref.SPKind,
			ref.Relation,
			string(ref.Source),
		}, "|")
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}

	if alt, ok := detectOverallBracketAlternate(rawTitle, primary); ok {
		add(alt)
	}
	if alt, ok := detectParenAbsoluteEpisodeAlternate(tokens, primary); ok {
		add(alt)
	}
	if alt, ok := detectSummaryCatalogEpisodeAlternate(rawTitle, primary); ok {
		add(alt)
	}
	if alt, ok := detectParenRangeAlternateFromRaw(rawTitle, primary); ok {
		add(alt)
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

func overridePrimaryWithSummaryCatalogEpisode(rawTitle string, primary *model.EPReference) *model.EPReference {
	if primary == nil || primary.Kind.Canonical() != model.EPKindEP {
		return primary
	}
	m := parenSummaryAltSxxExxPattern.FindStringSubmatch(rawTitle)
	if len(m) != 4 {
		return primary
	}
	altSeason := normalizeEpisodeIdentity(m[2])
	altEpisode := normalizeEpisodeIdentity(m[3])
	if strings.TrimSpace(primary.Season) != altSeason || strings.TrimSpace(primary.Episode) != altEpisode {
		return primary
	}
	cp := *primary
	cp.Axis = model.EPAxisABS
	cp.Season = ""
	cp.Episode = normalizeEpisodeIdentity(m[1])
	cp.Source = model.EPSourceSummaryEpisodePrimary
	return &cp
}

func detectParenAbsoluteEpisodeAlternate(tokens []model.Token, primary *model.EPReference) (model.EPReference, bool) {
	if primary == nil || primary.Kind.Canonical() != model.EPKindEP {
		return model.EPReference{}, false
	}
	if primary.TokenEnd < 0 || primary.TokenEnd+2 >= len(tokens) {
		return model.EPReference{}, false
	}
	open := strings.TrimSpace(tokens[primary.TokenEnd].Text)
	inner := strings.TrimSpace(tokens[primary.TokenEnd+1].Text)
	close := strings.TrimSpace(tokens[primary.TokenEnd+2].Text)
	paren := (open == "(" || open == "（") && (close == ")" || close == "）")
	// "[03_51]": the underscore form carries the overall episode without parens.
	underscore := open == "_" && (close == "]" || close == "】") && isStandaloneNumericBracketBlock(tokens, primary.TokenStart)
	if !paren && !underscore {
		return model.EPReference{}, false
	}
	if !episodeOfTotalPattern.MatchString(inner) && !reDetectParenAbsoluteEpisodeAlternate1.MatchString(inner) {
		return model.EPReference{}, false
	}
	norm := strings.TrimLeft(inner, "0")
	if strings.Contains(inner, ".") {
		parts := strings.SplitN(inner, ".", 2)
		left := strings.TrimLeft(parts[0], "0")
		if left == "" {
			left = "0"
		}
		norm = left
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			norm += "." + strings.TrimSpace(parts[1])
		}
	}
	if norm == "" {
		norm = "0"
	}
	if norm == strings.TrimSpace(primary.Episode) {
		return model.EPReference{}, false
	}
	return model.EPReference{
		Kind:       model.EPKindEP,
		Axis:       model.EPAxisABS,
		Episode:    norm,
		Relation:   "overall",
		Source:     model.EPSourceParenAltEpisode,
		TokenStart: primary.TokenEnd + 1,
		TokenEnd:   primary.TokenEnd + 2,
	}, true
}

func detectSummaryCatalogEpisodeAlternate(rawTitle string, primary *model.EPReference) (model.EPReference, bool) {
	if primary == nil || primary.Kind.Canonical() != model.EPKindEP {
		return model.EPReference{}, false
	}
	if strings.TrimSpace(primary.Episode) == "" {
		return model.EPReference{}, false
	}
	m := parenSummaryAltSxxExxPattern.FindStringSubmatch(rawTitle)
	if len(m) != 4 {
		return model.EPReference{}, false
	}
	if normalizeEpisodeIdentity(m[1]) != normalizeEpisodeIdentity(primary.Episode) {
		return model.EPReference{}, false
	}
	return model.EPReference{
		Kind:       model.EPKindEP,
		Axis:       model.EPAxisSE,
		Season:     normalizeEpisodeIdentity(m[2]),
		Episode:    normalizeEpisodeIdentity(m[3]),
		Relation:   "catalog",
		Source:     model.EPSourceSummaryCatalogParen,
		TokenStart: -1,
		TokenEnd:   -1,
	}, true
}

func detectParenRangeAlternateFromRaw(rawTitle string, primary *model.EPReference) (model.EPReference, bool) {
	if primary == nil || primary.Kind.Canonical() != model.EPKindRange {
		return model.EPReference{}, false
	}
	start := strings.TrimSpace(primary.RangeStart)
	end := strings.TrimSpace(primary.RangeEnd)
	if start == "" || end == "" {
		return model.EPReference{}, false
	}
	pat := regexp.MustCompile(`(?i)` +
		`0*` + regexp.QuoteMeta(start) + `\s*[-－–—~～〜]\s*0*` + regexp.QuoteMeta(end) +
		`\s*[\(（]\s*(\d{1,4}(?:\.\d{1,2})?)\s*[-－–—~～〜]\s*(\d{1,4}(?:\.\d{1,2})?)\s*[\)）]`)
	m := pat.FindStringSubmatch(rawTitle)
	if len(m) != 3 {
		return model.EPReference{}, false
	}
	altStart := normalizeEpisodeIdentity(m[1])
	altEnd := normalizeEpisodeIdentity(m[2])
	if altStart == "" || altEnd == "" {
		return model.EPReference{}, false
	}
	if altStart == start && altEnd == end {
		return model.EPReference{}, false
	}
	return model.EPReference{
		Kind:       model.EPKindRange,
		Axis:       model.EPAxisABS,
		RangeStart: altStart,
		RangeEnd:   altEnd,
		Relation:   "overall",
		Source:     model.EPSourceParenAltRange,
		TokenStart: -1,
		TokenEnd:   -1,
	}, true
}

func normalizeEpisodeIdentity(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, ".") {
		parts := strings.SplitN(s, ".", 2)
		left := strings.TrimLeft(strings.TrimSpace(parts[0]), "0")
		if left == "" {
			left = "0"
		}
		if len(parts) == 1 || strings.TrimSpace(parts[1]) == "" {
			return left
		}
		return left + "." + strings.TrimSpace(parts[1])
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	return s
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reDetectParenAbsoluteEpisodeAlternate1 = regexp.MustCompile(`^\d{1,4}(?:\.\d{1,2})?$`)
)
