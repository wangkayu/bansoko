package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"strings"
)

func (p *Parser) promoteNoteAttributes(attrs []model.Attribute) []model.Attribute {
	if p == nil {
		return attrs
	}
	if len(p.notePromotionRules) > 0 {
		for i := range attrs {
			if attrs[i].Type != model.AttrNote {
				continue
			}
			n := strings.TrimSpace(attrs[i].Normalized)
			if n == "" {
				continue
			}
			low := strings.ToLower(n)
			for _, rule := range p.notePromotionRules {
				if _, ok := rule.values[low]; !ok {
					continue
				}
				attrs[i].Type = rule.targetType
				if rule.normalizedFromToken {
					switch rule.targetType {
					case model.AttrPlatform:
						attrs[i].Normalized = p.normalizePlatformCode(n)
					case model.AttrSourceType:
						attrs[i].Normalized = p.normalizeSourceType(n)
					default:
						attrs[i].Normalized = strings.TrimSpace(strings.ToUpper(n))
					}
				} else if strings.TrimSpace(rule.normalized) != "" {
					attrs[i].Normalized = strings.TrimSpace(rule.normalized)
				}
				if attrs[i].Source == "" {
					attrs[i].Source = model.AttrSourceNotePromoteDict
				}
				break
			}
		}
	}
	// Contextual promotion: ESP is ambiguous between audio/subtitle language.
	// Only promote to subtitle language when subtitle signal already exists.
	hasSubSignal := false
	for _, a := range attrs {
		if a.Type == model.AttrSubFlag {
			n := strings.ToUpper(strings.TrimSpace(a.Normalized))
			if n == "SUBS" || n == "MULTI_SUBS" || n == "CC" || n == "SOFT" || n == "HARD" || n == "EX" {
				hasSubSignal = true
				break
			}
		}
	}
	if hasSubSignal {
		for i := range attrs {
			if attrs[i].Type != model.AttrNote {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(attrs[i].Normalized), "ESP") {
				continue
			}
			attrs[i].Type = model.AttrSubLang
			attrs[i].Normalized = "SPA"
			if attrs[i].Source == "" {
				attrs[i].Source = model.AttrSourceNotePromoteContext
			}
		}
	}
	// Composite subtitle/audio notes -> structured language attributes.
	hasAttr := func(typ model.AttrType, norm string) bool {
		for _, a := range attrs {
			if a.Type != typ {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(a.Normalized), strings.TrimSpace(norm)) {
				return true
			}
		}
		return false
	}
	appendAttr := func(typ model.AttrType, norm string, ref model.Attribute) {
		if hasAttr(typ, norm) {
			return
		}
		attrs = append(attrs, model.Attribute{
			Type:       typ,
			Normalized: norm,
			TokenStart: ref.TokenStart,
			TokenEnd:   ref.TokenEnd,
			Confidence: ref.Confidence,
			Source:     model.AttrSourceNotePromoteContext,
		})
	}
	for i := range attrs {
		if attrs[i].Type != model.AttrNote {
			continue
		}
		n := strings.ToUpper(strings.TrimSpace(attrs[i].Normalized))
		switch n {
		case "CHS_SUBS":
			attrs[i].Type = model.AttrSubLang
			attrs[i].Normalized = "SC"
			appendAttr(model.AttrSubFlag, "soft", attrs[i])
		case "JPN_AUDIO_CHS_SUBS":
			attrs[i].Type = model.AttrAudioLang
			attrs[i].Normalized = "JPN"
			appendAttr(model.AttrSubLang, "SC", attrs[i])
			appendAttr(model.AttrSubFlag, "soft", attrs[i])
		case "ENG_AUDIO_CHS_SUBS":
			attrs[i].Type = model.AttrAudioLang
			attrs[i].Normalized = "ENG"
			appendAttr(model.AttrSubLang, "SC", attrs[i])
			appendAttr(model.AttrSubFlag, "soft", attrs[i])
		case "CHN_AUDIO_CHS_SUBS":
			attrs[i].Type = model.AttrAudioLang
			attrs[i].Normalized = "CHN"
			appendAttr(model.AttrSubLang, "SC", attrs[i])
			appendAttr(model.AttrSubFlag, "soft", attrs[i])
		case "CHN_YUE_AUDIO":
			attrs[i].Type = model.AttrAudioLang
			attrs[i].Normalized = "CHN"
			appendAttr(model.AttrAudioLang, "YUE", attrs[i])
			appendAttr(model.AttrAudioFlag, "DUAL_AUDIO", attrs[i])
		case "CHN_YUE_AUDIO_SUBS":
			attrs[i].Type = model.AttrAudioLang
			attrs[i].Normalized = "CHN"
			appendAttr(model.AttrAudioLang, "YUE", attrs[i])
			appendAttr(model.AttrAudioFlag, "DUAL_AUDIO", attrs[i])
			appendAttr(model.AttrSubFlag, "soft", attrs[i])
		case "CHT_HARDSUB":
			attrs[i].Type = model.AttrSubLang
			attrs[i].Normalized = "TC"
			appendAttr(model.AttrSubFlag, "hard", attrs[i])
		}
	}
	return attrs
}

func normalizeMetaCompareKey(v string) string {
	s := strings.ToUpper(strings.TrimSpace(v))
	if s == "" {
		return ""
	}
	repl := strings.NewReplacer(
		" ", "",
		"-", "",
		"_", "",
		".", "",
		"／", "/",
		"/", "",
		"·", "",
		":", "",
		"：", "",
	)
	return repl.Replace(s)
}

func dropRedundantNoteAttributes(attrs []model.Attribute, season string, seasonRange string) []model.Attribute {
	if len(attrs) == 0 {
		return attrs
	}
	hasPlatform := map[string]struct{}{}
	hasResolution := map[string]struct{}{}
	hasSourceType := map[string]struct{}{}
	packKindPresent := false
	movieSignalPresent := false
	canonicalSeason := normalizeSeasonMarkerValue(season)
	canonicalSeasonRange := normalizeSeasonRangeMarkerValue(seasonRange)

	for _, a := range attrs {
		n := strings.TrimSpace(a.Normalized)
		if n == "" {
			continue
		}
		switch a.Type {
		case model.AttrPlatform:
			hasPlatform[normalizeMetaCompareKey(n)] = struct{}{}
		case model.AttrResolution:
			hasResolution[normalizeMetaCompareKey(n)] = struct{}{}
		case model.AttrSourceType:
			hasSourceType[normalizeMetaCompareKey(n)] = struct{}{}
		case model.AttrPackKind:
			packKindPresent = true
		case model.AttrContents:
			if strings.EqualFold(strings.TrimSpace(n), "MOVIE") {
				movieSignalPresent = true
			}
		case model.AttrMovieFlag:
			movieSignalPresent = true
		}
	}

	out := make([]model.Attribute, 0, len(attrs))
	for _, a := range attrs {
		if a.Type != model.AttrNote {
			out = append(out, a)
			continue
		}
		n := strings.TrimSpace(a.Normalized)
		if n == "" {
			continue
		}
		nk := normalizeMetaCompareKey(n)
		up := strings.ToUpper(n)

		if _, ok := hasPlatform[nk]; ok {
			continue
		}
		if _, ok := hasResolution[nk]; ok {
			continue
		}
		if _, ok := hasSourceType[nk]; ok {
			continue
		}
		if canonicalSeason != "" && normalizeSeasonMarkerValue(n) == canonicalSeason {
			continue
		}
		if canonicalSeasonRange != "" && normalizeSeasonRangeMarkerValue(n) == canonicalSeasonRange {
			continue
		}
		if packKindPresent {
			if up == "TV_PACK" || up == "BATCH" || n == "全集" || n == "合集" {
				continue
			}
		}
		if movieSignalPresent && up == "MOVIE" {
			continue
		}
		out = append(out, a)
	}
	return out
}
