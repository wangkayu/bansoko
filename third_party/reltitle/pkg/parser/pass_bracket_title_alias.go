package parser

import "github.com/wangkayu/reltitle/pkg/model"

func passExtractBracketTitleAliases(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	if ctx == nil || ctx.AliasValidFunc == nil {
		return
	}
	ctx.ForcedTitle, ctx.ForcedAliases, ctx.ForcedTitleStart, ctx.ForcedTitleEnd = extractBracketTitleAliasesAndRemoveFromTokens(tokens, attrs, ctx.AliasValidFunc)
}
