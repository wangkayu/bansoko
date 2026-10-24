package domain

import "strings"

// Release formats a Bangumi metadata search result can show. They are
// display-only on the search result and never stored with anime metadata.
const (
	ReleaseFormatTV    = "tv"
	ReleaseFormatMovie = "movie"
	ReleaseFormatOVA   = "ova"
	ReleaseFormatONA   = "ona"
)

// bangumiPlatformFormats maps the Bangumi v0 subject `platform` of an anime
// subject (type 2) to a release format. The keys are the platform names the
// v0 API returns for anime (bangumi/common subject_platforms: 1 TV, 2 OVA,
// 3 movie, 5 WEB). WEB is a web release, recorded as ona. Every other
// platform (other, donghua, an empty value, anything new) has no format and
// stays unknown; nothing is inferred from a title or another provider.
var bangumiPlatformFormats = map[string]string{
	"TV":  ReleaseFormatTV,
	"OVA": ReleaseFormatOVA,
	"剧场版": ReleaseFormatMovie,
	"WEB": ReleaseFormatONA,
}

// ReleaseFormatFromBangumiPlatform returns the release format for a Bangumi
// subject platform, or nil when the platform has no exact mapping.
func ReleaseFormatFromBangumiPlatform(platform string) *string {
	format, ok := bangumiPlatformFormats[strings.TrimSpace(platform)]
	if !ok {
		return nil
	}
	return &format
}
