package matcher

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// splitTrailingTitleYear recognizes only bracketed years, not digits that may
// be part of the work's name.
func splitTrailingTitleYear(title string) (string, int) {
	title = strings.TrimSpace(title)
	close, size := utf8.DecodeLastRuneInString(title)
	var open rune
	switch close {
	case ')':
		open = '('
	case '\uFF09':
		open = '\uFF08'
	case ']':
		open = '['
	case '\u3011':
		open = '\u3010'
	default:
		return "", 0
	}
	end := len(title) - size
	start := strings.LastIndex(title[:end], string(open))
	if start < 0 {
		return "", 0
	}
	base := strings.TrimSpace(title[:start])
	digits := strings.TrimSpace(title[start+utf8.RuneLen(open) : end])
	if base == "" || len(digits) != 4 {
		return "", 0
	}
	year, err := strconv.Atoi(digits)
	if err != nil || year < 1900 || year > 2099 {
		return "", 0
	}
	return base, year
}
