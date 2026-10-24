package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func passExtractTagCodeAudioLang(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	if len(tokens) == 0 || attrs == nil || ctx == nil {
		return
	}
	g := strings.ToLower(strings.TrimSpace(ctx.Group))
	allowErai := g != "" && strings.Contains(g, "erai")

	addAttr := func(t model.AttrType, val string, start int, end int, conf float64, source model.AttrSource) {
		val = strings.TrimSpace(val)
		if val == "" {
			return
		}
		*attrs = append(*attrs, model.Attribute{Type: t, Normalized: val, TokenStart: start, TokenEnd: end, Confidence: conf, Source: source})
	}

	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if tokens[i].Text != "(" && tokens[i].Text != "（" {
			continue
		}
		_, closeIdx, inner, ok := parseParenBlock(tokens, i, len(tokens))
		if !ok {
			continue
		}
		innerTrim := strings.TrimSpace(inner)
		up := strings.ToUpper(innerTrim)
		if up != "CA" && up != "JA" {
			i = closeIdx
			continue
		}

		addAttr(model.AttrNote, "tag_code:"+up, i, closeIdx+1, 0.7, model.AttrSourceTagCode)

		if allowErai {
			if up == "CA" {
				addAttr(model.AttrAudioLang, "YUE", i, closeIdx+1, 0.75, model.AttrSourceTagCodeAudioLang)
			}
			if up == "JA" {
				addAttr(model.AttrAudioLang, "JPN", i, closeIdx+1, 0.75, model.AttrSourceTagCodeAudioLang)
			}
		}

		for k := i; k <= closeIdx && k < len(tokens); k++ {
			tokens[k].Removed = true
		}
		i = closeIdx
	}
}
