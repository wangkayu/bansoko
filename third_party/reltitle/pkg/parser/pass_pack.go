package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func passExtractPack(tokens []model.Token, attrs *[]model.Attribute, ctx *parseContext) {
	plusAttrs := extractPackPlusContentsAndRemoveFromTokens(tokens)
	if len(plusAttrs) > 0 {
		*attrs = append(*attrs, plusAttrs...)
		if strings.TrimSpace(ctx.PackKind) == "" {
			hasTV := false
			hasOther := false
			for _, a := range plusAttrs {
				if a.Type != model.AttrContents {
					continue
				}
				n := strings.ToUpper(strings.TrimSpace(a.Normalized))
				if n == "TV" {
					hasTV = true
				} else if n != "" {
					hasOther = true
				}
			}
			if hasTV && hasOther {
				ctx.PackKind = "mixed_contents"
			} else {
				ctx.PackKind = "contents"
			}
		}
	}
	if strings.TrimSpace(ctx.PackKind) == "" {
		return
	}
	*attrs = append(*attrs, extractPackContentsAndRemoveFromTokens(tokens)...)
	*attrs = append(*attrs, extractPackAudioSubMetaAndRemoveFromTokens(tokens)...)
}
