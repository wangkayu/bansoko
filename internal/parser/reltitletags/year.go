package reltitletags

import (
	"strconv"
	"strings"

	reltitlemodel "github.com/wangkayu/reltitle/pkg/model"
)

// WeakYear returns an unambiguous standalone year token, never a year that
// came from a full date. It does not add to the persisted title-tag projection.
func WeakYear(title string) *int {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > MaxTitleBytes {
		return nil
	}
	parsed, err := parseActive(title)
	if err != nil || parsed == nil {
		return nil
	}
	return standaloneYear(parsed.Attributes)
}

func standaloneYear(attrs []reltitlemodel.Attribute) *int {
	var year *int
	for _, attr := range attrs {
		if attr.Type != reltitlemodel.AttrYear || attr.Source != reltitlemodel.AttrSourceYearToken ||
			attr.TokenStart < 0 || attr.TokenEnd <= attr.TokenStart {
			continue
		}
		fromDate := false
		for _, date := range attrs {
			if date.Type == reltitlemodel.AttrDate && date.Source == reltitlemodel.AttrSourceDateToken &&
				attr.TokenStart < date.TokenEnd && date.TokenStart < attr.TokenEnd {
				fromDate = true
				break
			}
		}
		if fromDate {
			continue
		}
		n, err := strconv.Atoi(attr.Normalized)
		if err != nil || len(attr.Normalized) != 4 || n < 1900 || n > 2099 {
			continue
		}
		if year != nil && *year != n {
			return nil
		}
		year = &n
	}
	return year
}
