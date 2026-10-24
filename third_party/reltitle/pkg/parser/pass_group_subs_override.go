package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

var subsX2TokenPattern = regexp.MustCompile(`(?i)^(ass|srt)x2$`)

func passGroupSubsX2Override(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	if len(tokens) == 0 || attrs == nil || ctx == nil {
		return
	}
	g := strings.ToLower(strings.TrimSpace(ctx.Group))
	if g == "" {
		return
	}
	// v1-compatible: only apply to LoliHouse family.
	if !strings.Contains(g, "lolihouse") {
		return
	}

	addAttr := func(t model.AttrType, val string, start int, end int, conf float64) {
		val = strings.TrimSpace(val)
		if val == "" {
			return
		}
		*attrs = append(*attrs, model.Attribute{Type: t, Normalized: val, TokenStart: start, TokenEnd: end, Confidence: conf, Source: model.AttrSourceGroupSubsX2})
	}

	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if !subsX2TokenPattern.MatchString(txt) {
			continue
		}
		// Restrict to bracket/paren blocks to avoid accidentally deleting title tokens.
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}

		up := strings.ToUpper(txt)
		format := ""
		if strings.HasPrefix(up, "ASS") {
			format = "ASS"
		}
		if strings.HasPrefix(up, "SRT") {
			format = "SRT"
		}
		if format != "" {
			addAttr(model.AttrSubFormat, format, i, i+1, 0.75)
		}
		// SRTx2/ASSx2 => SC + TC softsubs.
		addAttr(model.AttrSubLang, "SC", i, i+1, 0.75)
		addAttr(model.AttrSubLang, "TC", i, i+1, 0.75)
		addAttr(model.AttrSubFlag, "soft", i, i+1, 0.75)

		tokens[i].Removed = true
	}
}
