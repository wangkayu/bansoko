package matcher

import (
	"regexp"
	"strconv"
	"strings"
)

var trimPunctuationSpace = regexp.MustCompile(`\s+([:：,，。！？!？】）)\]》【（(])`)
var trimOpeningSpace = regexp.MustCompile(`([【（(\[《])\s+`)
var seasonMarker = regexp.MustCompile(`(?i)(?:第(\d{1,2}|[零〇一二两三四五六七八九十]{1,3})(?:季|期)|\b(?:S|Season)\s*(\d{1,2})\b|\b(\d{1,2})(?:st|nd|rd|th)\s+Season\b)`)

const (
	movieCJK       = `(?:电影版|电影|電影版|電影|映画版|映画|新剧场版|剧场版|新劇場版|劇場版)`
	movieSeparator = `[：:·\-—/／]`
)

var (
	moviePrefix = regexp.MustCompile(`(?i)^(?:` + movieCJK + `(?:[\s　]+(?:` + movieSeparator + `)?|` + movieSeparator + `)|(?:the\s+)?movie[\s　]*` + movieSeparator + `)`)
	movieMarker = regexp.MustCompile(`(?i)(?:^|[\s　：:·\-—/／（(【\[）)】\]])` + movieCJK + `(?:$|[\s　：:·\-—/／（(【\[）)】\]])`)
	movieSuffix = regexp.MustCompile(`(?i)(?:^|[\s　：:·\-—/／])movie$|[（(【\[]\s*(?:the\s+)?movie\s*[）)】\]]|\bthe\s+movie(?:$|[\s：:·\-—/／）)】\]])`)
)

type mediaPreference uint8

const (
	mediaPrefNone mediaPreference = iota
	mediaPrefMovieOnly
	mediaPrefTVOnly
	mediaPrefMovieFirst
	mediaPrefTVFirst
)

// TitleTrimmer only removes explicit season markers and redundant whitespace.
type TitleTrimmer struct{}

func NewTitleTrimmer() *TitleTrimmer { return &TitleTrimmer{} }

func (*TitleTrimmer) TrimTitle(title string) string {
	trimmed := seasonMarker.ReplaceAllStringFunc(title, func(marker string) string {
		if seasonNumber(seasonMarker.FindStringSubmatch(marker)) != nil {
			return ""
		}
		return marker
	})
	trimmed = strings.Join(strings.Fields(trimmed), " ")
	trimmed = trimPunctuationSpace.ReplaceAllString(trimmed, "$1")
	trimmed = trimOpeningSpace.ReplaceAllString(trimmed, "$1")
	if trimmed != "" {
		return trimmed
	}
	return strings.Join(strings.Fields(title), " ")
}

// ExtractSeasonNumber recognizes season numbers, not split-part or cour markers.
func (*TitleTrimmer) ExtractSeasonNumber(title string) *int {
	for _, match := range seasonMarker.FindAllStringSubmatch(title, -1) {
		if n := seasonNumber(match); n != nil {
			return n
		}
	}
	return nil
}

func seasonNumber(match []string) *int {
	value := firstNonEmpty(match[1:]...)
	n, err := strconv.Atoi(value)
	if err != nil {
		digit := func(s string) int {
			switch s {
			case "〇", "零":
				return 0
			case "两":
				return 2
			}
			if len([]rune(s)) == 1 {
				return strings.Index("一二三四五六七八九", s)/len("一") + 1
			}
			return -1
		}
		switch {
		case value == "十":
			n = 10
		case value == "二十":
			n = 20
		case strings.HasPrefix(value, "十") && digit(strings.TrimPrefix(value, "十")) > 0:
			n = 10 + digit(strings.TrimPrefix(value, "十"))
		default:
			n = digit(value)
		}
	}
	if n < 0 || n > 20 {
		return nil
	}
	return &n
}

func parseInt(value string) *int {
	if value == "" {
		return nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &n
}

func mediaPreferenceForTitle(title string, trimmer *TitleTrimmer) mediaPreference {
	title = strings.TrimSpace(title)
	if moviePrefix.MatchString(title) || movieMarker.MatchString(title) || movieSuffix.MatchString(title) {
		return mediaPrefMovieOnly
	}
	if trimmer.ExtractSeasonNumber(title) != nil {
		return mediaPrefTVOnly
	}
	return mediaPrefNone
}

func stripMoviePrefix(title string) string {
	match := moviePrefix.FindStringIndex(title)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(title[match[1]:])
}
