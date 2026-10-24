package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

var reGroupSlot2PlatformCompactFilter = regexp.MustCompile(`(?i)[^a-z0-9\p{Han}]`)

func passGroupSlot2PlatformInfer(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	if attrs == nil || ctx == nil || len(ctx.GroupSlot2Rules) == 0 {
		return
	}
	groupLower := strings.ToLower(strings.TrimSpace(ctx.Group))
	if groupLower == "" {
		return
	}
	addAttr := func(t model.AttrType, val string, conf float64, src model.AttrSource) {
		val = strings.TrimSpace(val)
		if val == "" {
			return
		}
		*attrs = append(*attrs, model.Attribute{
			Type:       t,
			Normalized: val,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: conf,
			Source:     src,
		})
	}

	for _, r := range ctx.GroupSlot2Rules {
		if !groupSlot2RuleMatchesGroup(r, groupLower, ctx.Group) {
			continue
		}
		slotText := extractBracketSlot(ctx.RawTitle, r.slotIndex)
		if slotText == "" {
			continue
		}
		slotLower := strings.ToLower(slotText)
		hasYue := containsAnyKeyword(slotLower, r.yueKeywords)

		for _, w := range r.platformWhitelist {
			if !containsAnyKeyword(slotLower, w.containsAny) {
				continue
			}
			addAttr(model.AttrPlatform, w.normalized, 0.9, model.AttrSourceGroupSlot2Platform)
			if hasYue {
				addAttr(model.AttrAudioLang, r.emitAudioLang, 0.9, model.AttrSourceGroupSlot2Platform)
			}
			return
		}

		if hasYue {
			addAttr(model.AttrAudioLang, r.emitAudioLang, 0.85, model.AttrSourceGroupSlot2Platform)
		}
		if containsAnyKeyword(slotLower, r.distributorKeywords) {
			addAttr(model.AttrNote, r.noteOnDistributor, 0.75, model.AttrSourceGroupSlot2Platform)
			return
		}
		if !hasYue {
			return
		}
		if containsAnyKeyword(slotLower, r.languageMetaKeywords) {
			return
		}
		if !r.inferUnknownPlatform {
			return
		}

		compact := slotText
		for _, kw := range r.trimForInfer {
			if strings.TrimSpace(kw) == "" {
				continue
			}
			re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(kw))
			compact = re.ReplaceAllString(compact, "")
		}
		compact = strings.TrimSpace(reGroupSlot2PlatformCompactFilter.ReplaceAllString(compact, ""))
		if compact == "" {
			return
		}
		addAttr(model.AttrPlatform, strings.ToUpper(compact), r.unknownPlatformConfidence, model.AttrSourceGroupSlot2PlatformInferUnknown)
		return
	}
}

func groupSlot2RuleMatchesGroup(r groupSlot2PlatformRule, groupLower string, groupRaw string) bool {
	if len(r.groups) > 0 {
		if _, ok := r.groups[groupLower]; !ok {
			return false
		}
	}
	if r.groupRegex != nil && !r.groupRegex.MatchString(strings.TrimSpace(groupRaw)) {
		return false
	}
	return true
}

func containsAnyKeyword(textLower string, keywords []string) bool {
	if strings.TrimSpace(textLower) == "" || len(keywords) == 0 {
		return false
	}
	for _, kw := range keywords {
		kk := strings.TrimSpace(strings.ToLower(kw))
		if kk == "" {
			continue
		}
		if strings.Contains(textLower, kk) {
			return true
		}
	}
	return false
}

func extractBracketSlot(raw string, slotIndex int) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if slotIndex <= 0 {
		slotIndex = 2
	}
	ms := bracketSegmentPattern.FindAllStringSubmatch(raw, -1)
	if len(ms) < slotIndex {
		return ""
	}
	return strings.TrimSpace(ms[slotIndex-1][1])
}
