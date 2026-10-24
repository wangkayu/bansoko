package parser

import (
	"strings"
	"unicode"

	"github.com/wangkayu/reltitle/pkg/model"
)

// restoreRepeatedTitlePrefix uses the original title slot as evidence: both
// slash-separated names must start with the same non-metadata Latin prefix and
// colon. Only surviving title text is restored, so season/pack cleanup remains.
func restoreRepeatedTitlePrefix(tokens []model.Token, start, end int, title string, aliases []string) (string, []string) {
	if start < 0 || end <= start || end > len(tokens) {
		return title, aliases
	}
	texts := make([]string, 0, end-start)
	for _, token := range tokens[start:end] {
		texts = append(texts, token.Text)
	}
	slot := strings.TrimSpace(strings.Join(texts, " "))
	// A title slot may itself be enclosed in a pair of brackets.
	slot = strings.TrimSpace(strings.Trim(slot, "[]【】"))
	parts := splitByStrongTitleDelims(slot)
	if len(parts) != 2 || !strings.ContainsAny(slot, "/／") {
		return title, aliases
	}
	prefixes := [2]string{}
	bodies := [2]string{}
	for i, part := range parts {
		colon := strings.IndexAny(part, ":：")
		if colon <= 0 {
			return title, aliases
		}
		prefix := strings.TrimSpace(part[:colon])
		if len(prefix) > 24 || !hasLatin(prefix) || isLikelyMetaBracketText(prefix) {
			return title, aliases
		}
		for _, r := range prefix {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '-' {
				return title, aliases
			}
		}
		prefixes[i] = prefix
		bodies[i] = aliasSemanticKey(part[colon+1:])
		if bodies[i] == "" {
			return title, aliases
		}
	}
	if !strings.EqualFold(prefixes[0], prefixes[1]) {
		return title, aliases
	}
	prefix := prefixes[0]
	prefixKey := aliasSemanticKey(prefix)
	restore := func(surface string) (string, bool) {
		body := strings.TrimSpace(surface)
		if len(body) >= len(prefix) && strings.EqualFold(body[:len(prefix)], prefix) {
			tail := body[len(prefix):]
			if tail != "" && (strings.HasPrefix(tail, ":") || strings.HasPrefix(tail, "：") || strings.HasPrefix(tail, " ")) {
				body = strings.TrimSpace(strings.TrimLeft(tail, ":： "))
			}
		}
		key := aliasSemanticKey(body)
		if key == "" || key == prefixKey {
			return surface, false
		}
		for _, rawBody := range bodies {
			if strings.HasPrefix(rawBody, key) {
				return prefix + ":" + body, true
			}
		}
		return surface, false
	}
	restoredTitle, ok := restore(title)
	if !ok {
		return title, aliases
	}
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if aliasSemanticKey(alias) == prefixKey {
			continue
		}
		if restored, ok := restore(alias); ok {
			alias = restored
		}
		out = append(out, alias)
	}
	return restoredTitle, out
}
