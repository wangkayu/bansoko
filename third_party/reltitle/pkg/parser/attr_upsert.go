package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"strings"
)

func upsertTitleAttribute(attrs []model.Attribute, title string, source model.AttrSource) []model.Attribute {
	title = strings.TrimSpace(title)
	if title == "" {
		return attrs
	}
	out := make([]model.Attribute, len(attrs))
	copy(out, attrs)
	updated := false
	for i := range out {
		if out[i].Type != model.AttrTitle {
			continue
		}
		out[i].Normalized = title
		if strings.TrimSpace(string(source)) != "" {
			out[i].Source = source
		}
		if out[i].Confidence <= 0 {
			out[i].Confidence = 0.7
		}
		updated = true
	}
	if !updated {
		out = append(out, model.Attribute{
			Type:       model.AttrTitle,
			Normalized: title,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.7,
			Source:     source,
		})
	}
	return out
}

func upsertScalarAttribute(attrs []model.Attribute, t model.AttrType, value string, source model.AttrSource) []model.Attribute {
	value = strings.TrimSpace(value)
	if value == "" {
		return attrs
	}
	out := make([]model.Attribute, len(attrs))
	copy(out, attrs)
	updated := false
	for i := range out {
		if out[i].Type != t {
			continue
		}
		out[i].Normalized = value
		if strings.TrimSpace(string(source)) != "" {
			out[i].Source = source
		}
		if out[i].Confidence <= 0 {
			out[i].Confidence = 0.7
		}
		updated = true
	}
	if !updated {
		out = append(out, model.Attribute{
			Type:       t,
			Normalized: value,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.7,
			Source:     source,
		})
	}
	return out
}

func removeAttributeType(attrs []model.Attribute, t model.AttrType) []model.Attribute {
	if len(attrs) == 0 {
		return attrs
	}
	out := make([]model.Attribute, 0, len(attrs))
	for _, a := range attrs {
		if a.Type == t {
			continue
		}
		out = append(out, a)
	}
	return out
}

func upsertGroupAttribute(attrs []model.Attribute, group string, source model.AttrSource) []model.Attribute {
	group = strings.TrimSpace(group)
	if group == "" {
		return attrs
	}
	out := make([]model.Attribute, len(attrs))
	copy(out, attrs)
	updated := false
	for i := range out {
		if out[i].Type != model.AttrGroup {
			continue
		}
		out[i].Normalized = group
		if strings.TrimSpace(string(source)) != "" {
			out[i].Source = source
		}
		if out[i].Confidence <= 0 {
			out[i].Confidence = 0.8
		}
		updated = true
	}
	if !updated {
		out = append(out, model.Attribute{
			Type:       model.AttrGroup,
			Normalized: group,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.8,
			Source:     source,
		})
	}
	return out
}

func appendTitleAliasAttributes(attrs []model.Attribute, aliases []string, title string, source model.AttrSource) []model.Attribute {
	if len(aliases) == 0 {
		return attrs
	}
	titleKey := aliasSemanticKey(title)
	out := make([]model.Attribute, len(attrs), len(attrs)+len(aliases))
	copy(out, attrs)
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if titleKey != "" && aliasSemanticKey(a) == titleKey {
			continue
		}
		out = append(out, model.Attribute{
			Type:       model.AttrTitleAlias,
			Normalized: a,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.7,
			Source:     source,
		})
	}
	return out
}

func dedupeAttributes(attrs []model.Attribute) []model.Attribute {
	if len(attrs) <= 1 {
		return attrs
	}
	keyOf := func(a model.Attribute) string {
		t := strings.TrimSpace(string(a.Type))
		n := strings.TrimSpace(a.Normalized)
		return t + "\t" + n
	}
	bestIdx := make(map[string]int, len(attrs))
	keep := make([]bool, len(attrs))
	for i := range attrs {
		a := attrs[i]
		k := keyOf(a)
		if prev, ok := bestIdx[k]; ok {
			pa := attrs[prev]
			if a.Confidence > pa.Confidence {
				bestIdx[k] = i
				continue
			}
			if a.Confidence == pa.Confidence {
				plen := pa.TokenEnd - pa.TokenStart
				alen := a.TokenEnd - a.TokenStart
				if alen > plen {
					bestIdx[k] = i
				}
			}
			continue
		}
		bestIdx[k] = i
	}
	for _, idx := range bestIdx {
		if idx >= 0 && idx < len(keep) {
			keep[idx] = true
		}
	}
	out := make([]model.Attribute, 0, len(bestIdx))
	for i := range attrs {
		if keep[i] {
			out = append(out, attrs[i])
		}
	}
	return out
}

func pruneGroupSpanTechArtifacts(attrs []model.Attribute, groupSpan [2]int) []model.Attribute {
	if len(attrs) == 0 || groupSpan[1] <= groupSpan[0] {
		return attrs
	}
	isGroupSpan := func(a model.Attribute) bool {
		return a.TokenStart >= groupSpan[0] && a.TokenEnd > a.TokenStart && a.TokenEnd <= groupSpan[1]
	}
	shouldPrune := func(a model.Attribute) bool {
		if !isGroupSpan(a) {
			return false
		}
		switch a.Type {
		case model.AttrSubFlag,
			model.AttrSubLang,
			model.AttrSubFormat,
			model.AttrSubLangGroup,
			model.AttrAudioLang,
			model.AttrAudioFlag,
			model.AttrAudioCodec,
			model.AttrAudioChannels,
			model.AttrAudioObject,
			model.AttrSourceType,
			model.AttrResolution,
			model.AttrVideoCodec,
			model.AttrContainer,
			model.AttrPlatform,
			model.AttrTech:
			return true
		default:
			return false
		}
	}
	out := make([]model.Attribute, 0, len(attrs))
	for _, a := range attrs {
		if shouldPrune(a) {
			continue
		}
		out = append(out, a)
	}
	return out
}
