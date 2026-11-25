package domain

import "bansoko/internal/postprocessing/naming"

func previewFixtureAnime() naming.AnimeNamingFacts {
	year, season := 2025, 1
	return naming.AnimeNamingFacts{
		Title: "Preview Show", Year: &year, Season: &season,
		TMDB: naming.TMDBNamingFacts{WorkID: "1", MediaKind: "tv", Season: season, SeasonPresent: true},
	}
}
