package domain

import "time"

type DetailsStatus string

const (
	DetailsStatusAvailable     DetailsStatus = "available"
	DetailsStatusEmpty         DetailsStatus = "empty"
	DetailsStatusError         DetailsStatus = "error"
	DetailsStatusNotApplicable DetailsStatus = "not_applicable"
)

type SearchSeason struct {
	SeasonNumber int    `json:"season_number"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date"`
	IsSpecial    bool   `json:"is_special"`
}

// SearchCandidate is a normalized result from an external metadata provider.
// Returning a candidate never changes a binding; callers must explicitly apply
// a complete metadata selection.
type SearchCandidate struct {
	Provider        string         `json:"provider"`
	ProviderWorkID  string         `json:"provider_work_id"`
	Title           string         `json:"title"`
	OriginalTitle   string         `json:"original_title,omitempty"`
	Overview        string         `json:"overview,omitempty"`
	PosterURL       string         `json:"poster_url,omitempty"`
	MediaKind       string         `json:"media_kind"`
	DetailsStatus   DetailsStatus  `json:"details_status"`
	Seasons         []SearchSeason `json:"seasons,omitempty"`
	SuggestedSeason *int           `json:"suggested_season,omitempty"`
	Year            int            `json:"year"`
	// ReleaseFormat is set for Bangumi results whose subject platform maps
	// exactly (ReleaseFormatFromBangumiPlatform); empty otherwise.
	ReleaseFormat string  `json:"release_format,omitempty"`
	GenreIDs      []int   `json:"genre_ids,omitempty"`
	Score         float64 `json:"score"`
	// Native holds the provider's own names for the work; it is recorded for
	// acceptance and never sent to clients.
	Native WorkTitles `json:"-"`
}

// WorkTitles are the native titles a provider reports for one work. For
// Bangumi that is the subject name (Title) and name_cn (TitleCN). For TMDB
// TitleCN is the title the request language returned — every TMDB request
// sends language=zh-CN, so it is the localized title, not necessarily a
// Chinese one — and OriginalTitle is the native title. A blank value is
// unknown. MediaKind is "subject" for Bangumi, tv or movie for TMDB.
//
// Year is the work's first air/release year as the provider reported it
// (Bangumi subject date, TMDB first_air_date/release_date); nil when the
// provider gave none. It is recorded from the provider's own value only.
//
// Overview is the work summary the provider reported (TMDB overview only;
// Bangumi has no equivalent). It is recorded alongside the titles and frozen
// into the acceptance input as the tmdb.summary fact.
type WorkTitles struct {
	Provider      string
	MediaKind     string
	WorkID        string
	Title         string
	TitleCN       string
	OriginalTitle string
	Overview      string
	Year          *int
}

// Provider year bounds. A year outside them is not an air/release year: the
// write path stores NULL rather than a value the database CHECK would refuse.
const (
	MinProviderYear = 1900
	MaxProviderYear = 2100
)

// NormalizeYear returns the observed year when it lies in the supported range,
// or nil. It never invents a value.
func NormalizeYear(year *int) *int {
	if year == nil || *year < MinProviderYear || *year > MaxProviderYear {
		return nil
	}
	value := *year
	return &value
}

// SearchSnapshot records the result set returned for a query. It is evidence
// for a later explicit choice, not a metadata binding.
type SearchSnapshot struct {
	Provider   string            `json:"provider"`
	Query      string            `json:"query"`
	Candidates []SearchCandidate `json:"candidates"`
	Version    int64             `json:"version"`
	ObservedAt time.Time         `json:"observed_at"`
}
