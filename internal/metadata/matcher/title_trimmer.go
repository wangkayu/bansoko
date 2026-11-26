package matcher

import (
	"regexp"
	"strings"
)

// defaultTrimRules are the title-trimming rules. These strip season
// markers, parenthetical remarks, and delimiter suffixes so that a search
// query targets the canonical title.
var defaultTrimRules = []string{
	`第(?:\d{1,2}|[一二三四五六七八九十])季`,
	`(?i)\b(?:S|Season\s*)\d+\b`,
	`\s*\(.*\)`,
	`\s*【.*】`,
	`\s*\[.*\]`,
	`\s*-\s*.*`,
	`\s*～.*`,
	`\s*~.*`,
	`\s*：.*`,
	`\s*:.*`,
}

var whitespacePattern = regexp.MustCompile(`\s+`)
var seasonPatterns = []*regexp.Regexp{
	regexp.MustCompile(`第(\d{1,2})季`),
	regexp.MustCompile(`(?i)\b(?:S|Season\s*)(\d{1,2})\b`),
}

// TitleTrimmer removes season markers and decorative text from anime titles
// before metadata API searches. The rules are fixed (not user-configurable)
// to keep the matching pipeline deterministic.
type TitleTrimmer struct {
	rules []*regexp.Regexp
}

// NewTitleTrimmer compiles the default trim rules.
func NewTitleTrimmer() *TitleTrimmer {
	rules := make([]*regexp.Regexp, len(defaultTrimRules))
	for i, pattern := range defaultTrimRules {
		rules[i] = regexp.MustCompile(pattern)
	}
	return &TitleTrimmer{rules: rules}
}

// TrimTitle applies all trim rules sequentially and collapses leftover
// whitespace. If trimming empties the title the original is returned.
func (t *TitleTrimmer) TrimTitle(title string) string {
	if title == "" {
		return title
	}
	original := title
	trimmed := strings.TrimSpace(title)
	for _, rule := range t.rules {
		trimmed = rule.ReplaceAllString(trimmed, "")
		trimmed = strings.TrimSpace(trimmed)
	}
	trimmed = whitespacePattern.ReplaceAllString(trimmed, " ")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return original
	}
	return trimmed
}

// ExtractSeasonNumber attempts to find a season number from patterns like
// "第2季", "S2", "Season 2". Returns nil if no pattern matches.
func (t *TitleTrimmer) ExtractSeasonNumber(title string) *int {
	for _, p := range seasonPatterns {
		m := p.FindStringSubmatch(title)
		if len(m) >= 2 {
			n := parseInt(m[1])
			if n != nil && *n >= 0 && *n <= 20 {
				return n
			}
		}
	}
	return nil
}

func parseInt(s string) *int {
	if s == "" {
		return nil
	}
	var result int
	for _, r := range s {
		if r < '0' || r > '9' {
			return nil
		}
		result = result*10 + int(r-'0')
	}
	return &result
}
