package parser

import "github.com/wangkayu/reltitle/pkg/model"

func passExtractTechSubs(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	extractTechSubsSemanticsAndRemoveFromTokens(tokens, attrs)
}

func passExtractTechAudio(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	ctx.AudioSlot = extractTechAudioSlotAndRemoveFromTokens(tokens, attrs, ctx.AudioCodecCanonical)
}
