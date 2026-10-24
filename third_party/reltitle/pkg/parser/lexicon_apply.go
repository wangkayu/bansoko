package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func (p *Parser) applyLexicon(tokens []model.Token, out *[]model.Attribute) {
	lower := make([]string, 0, len(tokens))
	for _, t := range tokens {
		lower = append(lower, strings.ToLower(t.Text))
	}

	brDepth, parDepth := computeBracketParenDepth(tokens)

	for _, e := range p.cfg.Lexicon.Entries {
		if len(e.AnyOf) > 0 {
			for _, clause := range e.AnyOf {
				scope := strings.TrimSpace(clause.Scope)
				if scope == "" {
					scope = strings.TrimSpace(e.Scope)
				}
				applyLexiconClause(tokens, lower, brDepth, parDepth, scope, clause.Tokens, clause.Emit, clause.Remove, clause.Confidence, out)
			}
			continue
		}
		applyLexiconClause(tokens, lower, brDepth, parDepth, strings.TrimSpace(e.Scope), e.Tokens, e.Emit, e.Remove, e.Confidence, out)
	}
}

func applyLexiconClause(tokens []model.Token, lower []string, brDepth []int, parDepth []int, scope string, pat []string, emits []config.LexiconEmit, remove bool, conf float64, out *[]model.Attribute) {
	if len(pat) == 0 {
		return
	}

	scope = strings.ToLower(strings.TrimSpace(scope))

	pl := make([]string, 0, len(pat))
	for _, t := range pat {
		pl = append(pl, strings.ToLower(t))
	}

	for i := 0; i+len(pl) <= len(lower); i++ {
		if !lexiconScopeOK(brDepth, parDepth, scope, i, i+len(pl)) {
			continue
		}
		// If any token in this window has been removed by earlier deterministic logic,
		// skip matching to avoid emitting attributes from consumed tokens.
		windowOK := true
		for k := i; k < i+len(pl); k++ {
			if k >= 0 && k < len(tokens) && tokens[k].Removed {
				windowOK = false
				break
			}
		}
		if !windowOK {
			continue
		}
		ok := true
		for j := 0; j < len(pl); j++ {
			expected := pl[j]
			actual := lower[i+j]
			if isInBracket(tokens, i+j) && strings.HasSuffix(actual, ",") {
				for _, em := range emits {
					if em.Type == "video_codec" || em.Type == "audio_codec" || em.Type == "source_type" || em.Type == "bit_depth" || em.Type == "edition" || em.Type == "content_rating" {
						actual = strings.TrimRight(actual, ",")
						expected = strings.TrimRight(expected, ",")
						break
					}
				}
			}
			if actual != expected {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}

		start := i
		end := i + len(pl)

		for _, em := range emits {
			idx := em.TokenIndex
			if idx < 0 {
				idx = len(pl) - 1
			}
			abs := start + idx
			if abs >= start && abs < end {
				*out = append(*out, model.Attribute{Type: model.AttrType(em.Type), Normalized: em.Normalized, TokenStart: abs, TokenEnd: abs + 1, Confidence: conf, Source: model.AttrSourceLexicon})
			}
		}

		if remove {
			for k := start; k < end; k++ {
				tokens[k].Removed = true
			}
		}
	}
}

func computeBracketParenDepth(tokens []model.Token) ([]int, []int) {
	br := make([]int, len(tokens))
	pr := make([]int, len(tokens))
	bd := 0
	pd := 0
	for i := 0; i < len(tokens); i++ {
		t := tokens[i].Text
		br[i] = bd
		pr[i] = pd
		if t == "[" || t == "【" {
			bd++
			continue
		}
		if t == "]" || t == "】" {
			if bd > 0 {
				bd--
			}
			continue
		}
		if t == "(" || t == "（" {
			pd++
			continue
		}
		if t == ")" || t == "）" {
			if pd > 0 {
				pd--
			}
			continue
		}
	}
	return br, pr
}

func lexiconScopeOK(brDepth []int, parDepth []int, scope string, start int, end int) bool {
	if scope == "" || scope == "any" || scope == "anywhere" {
		return true
	}
	if start < 0 {
		start = 0
	}
	if end > len(brDepth) {
		end = len(brDepth)
	}
	if start >= end {
		return false
	}

	switch scope {
	case "bracket", "brackets", "[]":
		for i := start; i < end; i++ {
			if brDepth[i] <= 0 {
				return false
			}
		}
		return true
	case "paren", "parens", "()":
		for i := start; i < end; i++ {
			if parDepth[i] <= 0 {
				return false
			}
		}
		return true
	default:
		return true
	}
}
