package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// protectRepeatedTitleBrackets recognizes matching non-metadata bracket blocks
// on opposite sides of a slash, inside the title slot before a known episode.
// It deliberately needs no shared title prefix or title dictionary entry.
func protectRepeatedTitleBrackets(tokens []model.Token, start, end int) {
	if start < 0 {
		start = 0
	}
	if end <= start || end > len(tokens) {
		return
	}
	type block struct{ start, end int }
	segments := []map[string][]block{{}}
	words := []bool{false}
	segment := 0
	for i := start; i < end; i++ {
		t := strings.TrimSpace(tokens[i].Text)
		if t == "/" || t == "／" {
			segments = append(segments, map[string][]block{})
			words = append(words, false)
			segment++
			continue
		}
		if t == "[" || t == "【" {
			close := "]"
			if t == "【" {
				close = "】"
			}
			j := i + 1
			parts := []string{}
			valid := !tokens[i].Removed
			for ; j < end && tokens[j].Text != close; j++ {
				text := tokens[j].Text
				if tokens[j].Removed || strings.ContainsAny(text, "[]【】/／") {
					valid = false
				}
				parts = append(parts, text)
			}
			if j == end {
				return
			}
			inner := strings.TrimSpace(strings.Join(parts, " "))
			if tokens[j].Removed || inner == "" || isLikelyMetaBracketText(inner) || epSlotBlockPattern.MatchString(inner) {
				valid = false
			}
			if !valid {
				// A metadata block ends the candidate title slot.
				break
			}
			key := strings.ToLower(inner)
			segments[segment][key] = append(segments[segment][key], block{i, j + 1})
			i = j
			continue
		}
		if t == "(" || t == "（" || t == "|" || t == "｜" {
			break
		}
		if !tokens[i].Removed && !isTitleNoiseToken(t) && (hasLatin(t) || hasCJK(t) || hasKana(t)) {
			words[segment] = true
		}
	}
	for s := 0; s+1 < len(segments); s++ {
		if !words[s] || !words[s+1] {
			continue
		}
		for key, left := range segments[s] {
			right := segments[s+1][key]
			if len(right) == 0 {
				continue
			}
			for _, b := range append(left, right...) {
				for i := b.start; i < b.end; i++ {
					tokens[i].Protected = true
				}
			}
		}
	}
}
