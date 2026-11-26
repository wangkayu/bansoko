package matcher

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
)

type bgmInfoboxField struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func (f bgmInfoboxField) values() []string {
	var scalar string
	if json.Unmarshal(f.Value, &scalar) == nil {
		return []string{scalar}
	}
	var list []struct {
		Value string `json:"v"`
	}
	if json.Unmarshal(f.Value, &list) != nil {
		return nil
	}
	values := make([]string, 0, min(64, len(list)))
	for _, entry := range list[:min(64, len(list))] {
		values = append(values, entry.Value)
	}
	return values
}

func bgmTitleKey(title string) string {
	return strings.ToLower(strings.TrimSpace(title))
}

func (s BGMSubject) matchesTitle(key string) bool {
	if key == bgmTitleKey(s.Name) || key == bgmTitleKey(s.NameCN) {
		return true
	}
	seen := make(map[string]bool)
	for _, field := range s.Infobox {
		switch strings.TrimSpace(field.Key) {
		case "别名", "別名", "又名", "其他名称", "其它名称", "英文名", "英文名称", "日文名", "日文名称", "中文名", "中文名称", "简体中文名", "繁体中文名":
			for _, value := range field.values() {
				if value = strings.TrimSpace(value); len(value) > 512 {
					continue
				}
				alias := bgmTitleKey(value)
				if strings.Trim(alias, "*{}[] \t\r\n") == "" || seen[alias] {
					continue
				}
				if alias == key {
					return true
				}
				seen[alias] = true
				if len(seen) == 64 {
					return false
				}
			}
		}
	}
	return false
}
func (s BGMSubject) matchesNormalizedTitle(key string) bool {
	if key == "" || normalizeTitle(s.Name) == key || normalizeTitle(s.NameCN) == key {
		return key != ""
	}
	for _, field := range s.Infobox {
		switch strings.TrimSpace(field.Key) {
		case "别名", "別名", "又名", "其他名称", "其它名称", "英文名", "英文名称", "日文名", "日文名称", "中文名", "中文名称", "简体中文名", "繁体中文名":
			for _, value := range field.values() {
				if len(value) <= 512 && normalizeTitle(value) == key {
					return true
				}
			}
		}
	}
	return false
}

type datePrecision uint8

const (
	dateUnknown datePrecision = iota
	dateYear
	dateSeason
	dateMonth
	dateDay
)

type dateClue struct {
	start, end time.Time // half-open interval; year is the provider's label, not always start.Year()
	precision  datePrecision
	year       int
	source     string
	conflict   bool
}

var (
	isoAiringDate = regexp.MustCompile(`^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?$`)
	cnAiringDate  = regexp.MustCompile(`^(\d{4})年(?:(\d{1,2})月(?:(\d{1,2})日)?|([春夏秋冬])季?)?$`)
)

func parseAiringDate(value, source string) dateClue {
	value = strings.TrimSpace(value)
	parts := isoAiringDate.FindStringSubmatch(value)
	if parts == nil {
		parts = cnAiringDate.FindStringSubmatch(value)
	}
	if parts == nil {
		return dateClue{}
	}
	year, _ := strconv.Atoi(parts[1])
	if identitymetadatadomain.NormalizeYear(&year) == nil {
		return dateClue{}
	}
	date := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }
	clue := dateClue{year: year, source: source}
	switch {
	case len(parts) == 5 && parts[4] != "":
		clue.precision = dateSeason
		switch parts[4] {
		case "冬":
			clue.start, clue.end = date(year-1, 12, 20), date(year, 3, 20)
		case "春":
			clue.start, clue.end = date(year, 3, 20), date(year, 6, 20)
		case "夏":
			clue.start, clue.end = date(year, 6, 20), date(year, 9, 20)
		case "秋":
			clue.start, clue.end = date(year, 9, 20), date(year, 12, 20)
		}
	case parts[2] == "":
		clue.precision = dateYear
		clue.start, clue.end = date(year, 1, 1), date(year+1, 1, 1)
	default:
		month, _ := strconv.Atoi(parts[2])
		if month < 1 || month > 12 {
			return dateClue{}
		}
		if parts[3] == "" {
			clue.precision = dateMonth
			clue.start, clue.end = date(year, month, 1), date(year, month+1, 1)
			break
		}
		day, _ := strconv.Atoi(parts[3])
		start := date(year, month, day)
		if day < 1 || start.Month() != time.Month(month) || start.Day() != day {
			return dateClue{}
		}
		clue.precision = dateDay
		clue.start, clue.end = start, start.AddDate(0, 0, 1)
	}
	return clue
}

func parseProviderClue(value, source string) dateClue {
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
		value = parsed.Format(time.DateOnly)
	}
	return parseAiringDate(value, source)
}

func (d dateClue) contains(day time.Time) bool {
	return !day.Before(d.start) && day.Before(d.end)
}

func (d dateClue) refine(next dateClue) dateClue {
	if next.precision == dateUnknown {
		return d
	}
	if d.precision == dateUnknown {
		return next
	}
	if !d.start.Before(next.end) || !next.start.Before(d.end) {
		d.conflict = true
		return d
	}
	if next.precision > d.precision {
		next.conflict = d.conflict || next.conflict
		return next
	}
	d.conflict = d.conflict || next.conflict
	return d
}

func (s BGMSubject) airingDate() dateClue {
	clue := parseAiringDate(s.AirDate, "bangumi.date")
	var annual, release dateClue
	for _, field := range s.Infobox {
		key := strings.TrimSpace(field.Key)
		switch key {
		case "放送开始", "放送開始", "首播时间", "开播日期", "首播", "开播", "播放日期", "播出日期", "开始", "上映年度", "发售日":
		default:
			continue
		}
		for _, value := range field.values() {
			date := parseAiringDate(value, "bangumi.infobox:"+key)
			switch key {
			case "上映年度":
				if date.precision != dateUnknown {
					annual = annual.refine(parseAiringDate(strconv.Itoa(date.year), date.source))
				}
			case "发售日":
				release = release.refine(date)
			default:
				clue = clue.refine(date)
			}
		}
	}
	clue = clue.refine(annual)
	if clue.precision == dateUnknown {
		if format := identitymetadatadomain.ReleaseFormatFromBangumiPlatform(s.Platform); format != nil &&
			(*format == identitymetadatadomain.ReleaseFormatOVA || *format == identitymetadatadomain.ReleaseFormatMovie) {
			return release
		}
	}
	return clue
}
