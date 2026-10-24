package tokenize

import (
	"strings"
	"unicode"
)

// Tokenize splits a raw release title into tokens: whitespace, brackets and
// separator runes become boundaries, and CJK/Latin transitions split words.
// The returned strings are the Text of the parser's model.Token values.
func Tokenize(rawTitle string) []string {
	var tokens []string
	var current strings.Builder
	hasCJK := false
	hasLatin := false

	for _, r := range rawTitle {
		if unicode.IsSpace(r) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
				hasCJK = false
				hasLatin = false
			}
		} else if isBracket(string(r)) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
				hasCJK = false
				hasLatin = false
			}
			tokens = append(tokens, string(r))
		} else if isSeparatorRune(r) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
				hasCJK = false
				hasLatin = false
			}
			tokens = append(tokens, string(r))
		} else {
			curIsCJK := isCJKRune(r)
			curIsLatin := isLatinRune(r)
			if current.Len() > 0 {
				if hasCJK && curIsLatin && !hasLatin {
					tokens = append(tokens, current.String())
					current.Reset()
					hasCJK = false
					hasLatin = false
				} else if hasLatin && curIsCJK && !hasCJK {
					tokens = append(tokens, current.String())
					current.Reset()
					hasCJK = false
					hasLatin = false
				}
			}
			current.WriteRune(r)
			if curIsCJK {
				hasCJK = true
			}
			if curIsLatin {
				hasLatin = true
			}
		}
	}

	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens
}

func isSeparatorRune(r rune) bool {
	switch r {
	case '.', '-', '－', '–', '—', '_', '/', '／', '|', '｜', '★', '☆', ';', '；', '+', '＋', ':', '：':
		return true
	default:
		return false
	}
}

func isLatinRune(r rune) bool {
	return unicode.Is(unicode.Latin, r)
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func isBracket(s string) bool {
	return s == "[" || s == "]" || s == "(" || s == ")" ||
		s == "{" || s == "}" || s == "【" || s == "】" ||
		s == "「" || s == "」" || s == "『" || s == "』"
}
