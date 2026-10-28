package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	animeapplication "bansoko/internal/anime/application"
)

// readTitleSuggestions lists the title candidates of a binding: the cached
// native titles of the bound works (metadata_work_titles, never a network
// call) and the provider catalog title. Every non-empty source is returned in
// the order tmdb_cn, tmdb_original, bgm_cn, bgm, provider; deduplication is the
// caller's.
func (s *Service) readTitleSuggestions(ctx context.Context, bgmWorkID, tmdbWorkID, tmdbMediaKind, providerTitle string) ([]TitleSuggestion, error) {
	read := func(provider, kind, id string) (titleCN, title, original string, err error) {
		if strings.TrimSpace(id) == "" {
			return "", "", "", nil
		}
		var nullTitle, nullCN, nullOriginal sql.NullString
		err = s.db.QueryRowContext(ctx,
			`SELECT title, title_cn, original_title FROM metadata_work_titles WHERE provider=$1 AND media_kind=$2 AND work_id=$3`,
			provider, kind, id).Scan(&nullTitle, &nullCN, &nullOriginal)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", nil
		}
		return nullCN.String, nullTitle.String, nullOriginal.String, err
	}
	tmdbCN, _, tmdbOriginal, err := read("tmdb", tmdbMediaKind, tmdbWorkID)
	if err != nil {
		return nil, err
	}
	bgmCN, bgmTitle, _, err := read("bgm", "subject", bgmWorkID)
	if err != nil {
		return nil, err
	}
	var out []TitleSuggestion
	for _, candidate := range []TitleSuggestion{
		{Source: animeapplication.TitleSourceTMDBCN, Text: tmdbCN},
		{Source: animeapplication.TitleSourceTMDBOriginal, Text: tmdbOriginal},
		{Source: animeapplication.TitleSourceBGMCN, Text: bgmCN},
		{Source: animeapplication.TitleSourceBGM, Text: bgmTitle},
		{Source: animeapplication.TitleSourceProvider, Text: providerTitle},
	} {
		if text := strings.TrimSpace(candidate.Text); text != "" {
			out = append(out, TitleSuggestion{Source: candidate.Source, Text: text})
		}
	}
	return out, nil
}
