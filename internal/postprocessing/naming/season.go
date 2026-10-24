package naming

import (
	"fmt"
	"strings"

	"bansoko/internal/templatefacts"
)

// AnimeSeason is the season every render path names: RSS and draft previews,
// the one-shot preview, acquisition, the typed planner and the maintenance
// re-render all reach it through renderFacts, whatever context builder
// produced tmdb.
//
// A season is bound when the TMDB binding carries a real season (≥ 1) or a
// preserved season 0 (SeasonBound, continuous-SP recognition). A bound season
// 0 that was not preserved, or no binding at all, is unbound: it names no
// season (nil), so anime.season, anime.season_folder and anime.season_path
// render empty, and ep.s00e00 renders its episode-only form (E07).
// naming-render-2
// named an
// unbound TV season 1 (unboundTVDefault, replay only). tmdb.season stays the
// raw binding.
//
// Since naming-render-4 the named season is the owner-edited season
// (OwnerSeason); AnimeSeason is the season naming-render-2 and
// naming-render-3 replay, and the season a frozen input that predates the
// owner season maps to (FrozenAnimeSeason).
func AnimeSeason(kind string, tmdb TMDBNamingFacts, unboundTVDefault bool) *int64 {
	if tmdb.Season >= 1 || (tmdb.SeasonPresent && tmdb.SeasonBound && tmdb.Season == 0) {
		return templatefacts.Int64(int64(tmdb.Season))
	}
	if unboundTVDefault && kind == string(MediaKindTV) {
		return templatefacts.Int64(1)
	}
	return nil
}

// OwnerSeason is the named season of naming-render-4: the owner-edited
// season as is (0 is the specials season), nil when unset or negative. There
// is no fallback to the TMDB binding and no default.
func OwnerSeason(season *int) *int64 {
	if season == nil || *season < 0 {
		return nil
	}
	return templatefacts.Int64(int64(*season))
}

// legacyTMDBSeason is the raw TMDB binding season the fact set has always
// carried as tmdb.season (and naming-render-1 read as anime.season): the
// bound season when present or positive, else nil.
func legacyTMDBSeason(tmdb TMDBNamingFacts) *int64 {
	if tmdb.SeasonPresent || tmdb.Season > 0 {
		return templatefacts.Int64(int64(tmdb.Season))
	}
	return nil
}

// SeasonPath is the anime.season_path composite: "/" plus the season folder of
// a TV item with a season ("/Season 01", "/Season 00" for season 0), and ""
// for a movie or an unbound season. A template appends it to the work folder,
// so an unbound season leaves no empty folder level behind.
func SeasonPath(kind string, season *int64) string {
	if kind != string(MediaKindTV) || season == nil {
		return ""
	}
	return "/" + templatefacts.SeasonFolder(season)
}

// SeasonFolderFact is the anime.season_folder value of the current naming
// render: the season folder of a TV item with a season ("/Season 00"
// specials without the slash), and "" for a movie or an unbound season. The
// normative row (naming-module.md §Fact catalog rows) and the registry copy
// both record movie → empty, and the season_path composite has always
// enforced it; the leaf now agrees.
func SeasonFolderFact(kind string, season *int64) string {
	if kind != string(MediaKindTV) || season == nil {
		return ""
	}
	return templatefacts.SeasonFolder(season)
}

// SeasonEpisode is the ep.s00e00 composite, in the spirit of FileBot's
// {s00e00}:
//
//   - S01E07 for season 1 episode 7; the episode is zero-padded to two digits
//     and a longer or non-numeric number is kept as is (S01E107);
//   - S01E07-E08 for a file holding episodes 7 to 8 (ep.number_end);
//   - S00E07 for season 0, the season continuous specials are bound to;
//   - E07 (E07-E08 for a multi-episode file) for a TV item with no bound
//     season;
//   - "" for a movie or without an episode number.
//
// The episode-only form is the composite's own defined output for an unbound
// season, not an implicit season: anime.season and the
// season folder stay empty. Without it every episode of an unbound show would
// target the same name.
func SeasonEpisode(kind string, season *int64, number, numberEnd string) string {
	number, numberEnd = strings.TrimSpace(number), strings.TrimSpace(numberEnd)
	if kind != string(MediaKindTV) || number == "" {
		return ""
	}
	out := "E" + padEpisode(number)
	if season != nil {
		out = fmt.Sprintf("S%02d", *season) + out
	}
	if numberEnd != "" && numberEnd != number {
		out += "-E" + padEpisode(numberEnd)
	}
	return out
}

func padEpisode(number string) string {
	if len(number) < 2 {
		return strings.Repeat("0", 2-len(number)) + number
	}
	return number
}
