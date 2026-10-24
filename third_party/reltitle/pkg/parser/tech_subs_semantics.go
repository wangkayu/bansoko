package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func hasSplitSCTCMarker(s string) bool {
	noSpace := strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if noSpace == "" {
		return false
	}
	for _, pat := range []string{
		"简/繁", "简／繁", "簡/繁", "簡／繁",
		"繁/简", "繁／简", "繁/簡", "繁／簡",
		"简體/繁體", "简体/繁体", "簡體/繁體", "繁體/簡體",
		"简中/繁中", "簡中/繁中", "繁中/简中", "繁中/簡中",
	} {
		if strings.Contains(noSpace, pat) {
			return true
		}
	}
	return false
}

func containsASCIIWordLike(s string, markers ...string) bool {
	if strings.TrimSpace(s) == "" || len(markers) == 0 {
		return false
	}
	low := strings.ToLower(s)
	isAlphaNum := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
	}
	for _, marker := range markers {
		marker = strings.ToLower(strings.TrimSpace(marker))
		if marker == "" {
			continue
		}
		searchFrom := 0
		for searchFrom < len(low) {
			idx := strings.Index(low[searchFrom:], marker)
			if idx < 0 {
				break
			}
			idx += searchFrom
			beforeOK := idx == 0 || !isAlphaNum(low[idx-1])
			after := idx + len(marker)
			afterOK := after >= len(low) || !isAlphaNum(low[after])
			if beforeOK && afterOK {
				return true
			}
			searchFrom = idx + 1
		}
	}
	return false
}

// extractTechSubsSemanticsAndRemoveFromTokens scans bracket/paren blocks and
// plain token segments for subtitle semantics, emits subtitle attributes, and
// removes the consumed tokens. The extraction machinery (segment scanning,
// flag/language/format detection) lives in tech_subs_extract.go.
func isToonsHubCN(tokens []model.Token) bool {
	if len(tokens) < 3 || tokens[0].Text != "[" || !strings.EqualFold(tokens[1].Text, "ToonsHub") || tokens[2].Text != "]" {
		return false
	}
	for i := 3; i < len(tokens); i++ {
		if tokens[i].Text == "[" {
			break
		}
		if hasCJK(tokens[i].Text) {
			return true
		}
	}
	return false
}

// langCodePart reports whether part is a known subtitle language code.
func langCodePart(part string) bool {
	switch part {
	case "chs", "cht", "gb", "big5", "sc", "tc", "zh",
		"jp", "jpn", "eng", "kor", "esp", "por", "fre", "rus":
		return true
	}
	return false
}

// underscoreJoinedLangCombo reports whether s is exactly two language codes
// joined by an underscore, e.g. "chs_jp", "gb_jpn". Like the lexicon's
// code-combo entries (GB&JP, CHS&CHT), it is just two subtitle languages in
// compact notation: split into independent sub_langs, no group, no sub_flag.
func underscoreJoinedLangCombo(s string) bool {
	parts := strings.Split(strings.TrimSpace(s), "_")
	if len(parts) != 2 {
		return false
	}
	return langCodePart(strings.TrimSpace(strings.ToLower(parts[0]))) && langCodePart(strings.TrimSpace(strings.ToLower(parts[1])))
}

// ampersandJoinedLangEnumeration reports whether s is two or more language
// codes joined by "&", e.g. "chs&jp", "chs&cht&eng". Same convention as the
// underscore combos: a pure language enumeration carries no enclosure
// evidence, so it must not infer sub_flag soft.
func ampersandJoinedLangEnumeration(s string) bool {
	parts := strings.Split(strings.TrimSpace(s), "&")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !langCodePart(strings.TrimSpace(strings.ToLower(p))) {
			return false
		}
	}
	return true
}

func extractTechSubsSemanticsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if len(tokens) == 0 || attrs == nil {
		return
	}
	e := &techSubsSemanticsExtract{toonshubCN: isToonsHubCN(tokens), tokens: tokens, attrs: attrs, headGroupStart: -1, headGroupEnd: -1}
	e.techStart, e.hasTechStart = firstTechTokenIndexFromAttrs(*attrs)
	if !e.hasTechStart {
		e.techStart = 0
	}
	if g, span := extractHeadBracketGroup(tokens); strings.TrimSpace(g) != "" {
		e.headGroupStart = span[0]
		e.headGroupEnd = span[1]
	}
	e.run()
}
