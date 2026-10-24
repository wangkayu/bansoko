package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"github.com/wangkayu/reltitle/pkg/tokenize"
	"strings"
)

func normalizeKnownTitleToken(s string) string {
	// Full-width slash/dash variants must match their half-width forms:
	// "22／7" (full-width) is the same title as "22/7".
	s = strings.ReplaceAll(s, "／", "/")
	s = strings.ReplaceAll(s, "－", "-")
	return s
}

func (p *Parser) protectKnownTitleSpans(rawTitle string, tokens []model.Token) {
	if p == nil || p.cfg == nil || len(tokens) == 0 {
		return
	}
	rawLower := strings.ToLower(rawTitle)
	for _, entry := range p.cfg.TitleKnowledge.Entries {
		if !entry.Enabled {
			continue
		}
		matched := true
		for _, required := range entry.RawMustContainAll {
			if !strings.Contains(rawLower, strings.ToLower(strings.TrimSpace(required))) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		candidates := append([]string{entry.CanonicalTitle}, entry.Aliases...)
		for _, cand := range candidates {
			cand = strings.TrimSpace(cand)
			if cand == "" {
				continue
			}
			ctoks := tokenize.Tokenize(cand)
			if len(ctoks) == 0 {
				continue
			}
			// Find every contiguous token run that matches the known title form.
			// These tokens belong to the title; season/episode/range detection must
			// never reinterpret numbers inside them (e.g. "藤本树 17-26", "22／7").
			for i := 0; i+len(ctoks) <= len(tokens); i++ {
				ok := true
				for k, ct := range ctoks {
					if normalizeKnownTitleToken(tokens[i+k].Text) != normalizeKnownTitleToken(ct) {
						ok = false
						break
					}
				}
				if !ok {
					continue
				}
				for k := 0; k < len(ctoks); k++ {
					tokens[i+k].Protected = true
				}
			}
		}
	}
}

func (p *Parser) applyTitleKnowledge(rawTitle string, title string, aliases []string) (string, []string) {
	if p == nil || p.cfg == nil {
		return title, aliases
	}
	for _, entry := range p.cfg.TitleKnowledge.Entries {
		if !entry.Enabled || strings.TrimSpace(entry.CanonicalTitle) == "" || len(entry.RawMustContainAll) == 0 {
			continue
		}
		matched := true
		for _, required := range entry.RawMustContainAll {
			if !strings.Contains(strings.ToLower(rawTitle), strings.ToLower(strings.TrimSpace(required))) {
				matched = false
				break
			}
		}
		if matched {
			canonicalTitle := strings.TrimSpace(entry.CanonicalTitle)
			// Only keep knowledge aliases that actually appear in the raw title, so
			// script variants not present in this release are not injected into output.
			rawLower := strings.ToLower(rawTitle)
			var presentAliases []string
			for _, a := range entry.Aliases {
				a = strings.TrimSpace(a)
				if a == "" {
					continue
				}
				if strings.Contains(rawLower, strings.ToLower(a)) {
					presentAliases = append(presentAliases, a)
				}
			}
			mergedAliases := mergeAliases(presentAliases, aliases)
			filteredAliases := mergedAliases[:0]
			canonicalKey := aliasSemanticKey(canonicalTitle)
			presentKeys := make(map[string]bool, len(presentAliases))
			for _, a := range presentAliases {
				presentKeys[aliasSemanticKey(a)] = true
			}
			for _, alias := range mergedAliases {
				aliasKey := aliasSemanticKey(alias)
				if aliasKey != "" && strings.Contains(canonicalKey, aliasKey) {
					continue
				}
				// A fragment of a known alias that is present in the raw title ("Ranma 1" and
				// "Ranma" of "Ranma 1/2") is what splitting the title at its own slash leaves
				// behind, not a real alias.
				if aliasKey != "" && !presentKeys[aliasKey] && isFragmentOfKnownKey(aliasKey, presentKeys) {
					continue
				}
				filteredAliases = append(filteredAliases, alias)
			}
			return canonicalTitle, filteredAliases
		}
	}
	return title, aliases
}

// isFragmentOfKnownKey reports whether key is a strict part of one of the known keys.
func isFragmentOfKnownKey(key string, known map[string]bool) bool {
	for k := range known {
		if k != key && strings.Contains(k, key) {
			return true
		}
	}
	return false
}
