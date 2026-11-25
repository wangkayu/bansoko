package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/postprocessing/naming"
)

type Querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// LoadAnimeFacts reads the current owner and metadata binding for a pre-download render.
func LoadAnimeFacts(ctx context.Context, q Querier, animeID string) (naming.AnimeNamingFacts, error) {
	facts := naming.AnimeNamingFacts{ID: animeID}
	err := q.QueryRowContext(ctx, `SELECT a.title, COALESCE(a.title_override,''), COALESCE(a.provider_title,''),
       m.year, m.season, COALESCE(m.bgm_work_id,''), COALESCE(m.tmdb_work_id,''),
       COALESCE(m.tmdb_media_kind,''), COALESCE(m.tmdb_season,0), m.tmdb_season IS NOT NULL
FROM managed_anime a LEFT JOIN anime_metadata m ON m.managed_anime_id=a.id WHERE a.id=$1`, animeID).
		Scan(&facts.Title, &facts.TitleOverride, &facts.ProviderTitle, &facts.Year, &facts.Season,
			&facts.BGM.WorkID, &facts.TMDB.WorkID, &facts.TMDB.MediaKind, &facts.TMDB.Season, &facts.TMDB.SeasonPresent)
	if err != nil {
		return facts, fmt.Errorf("read naming metadata: %w", err)
	}
	return facts, FillWorkTitles(ctx, q, &facts)
}

// FillWorkTitles reads observed facts for the current binding without a network lookup.
func FillWorkTitles(ctx context.Context, q Querier, facts *naming.AnimeNamingFacts) error {
	facts.BGM.Title, facts.BGM.TitleCN, facts.BGM.Year = "", "", nil
	facts.TMDB.TitleCN, facts.TMDB.OriginalTitle, facts.TMDB.Year = "", "", nil
	if q == nil {
		return nil
	}
	if facts.BGM.WorkID != "" {
		err := q.QueryRowContext(ctx, `SELECT COALESCE(title,''), COALESCE(title_cn,''), year
FROM metadata_work_titles WHERE provider='bgm' AND media_kind='subject' AND work_id=$1`, facts.BGM.WorkID).
			Scan(&facts.BGM.Title, &facts.BGM.TitleCN, &facts.BGM.Year)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read BGM naming metadata: %w", err)
		}
	}
	if facts.TMDB.WorkID != "" && facts.TMDB.MediaKind != "" {
		err := q.QueryRowContext(ctx, `SELECT COALESCE(title_cn,''), COALESCE(original_title,''), year
FROM metadata_work_titles WHERE provider='tmdb' AND media_kind=$1 AND work_id=$2`, facts.TMDB.MediaKind, facts.TMDB.WorkID).
			Scan(&facts.TMDB.TitleCN, &facts.TMDB.OriginalTitle, &facts.TMDB.Year)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read TMDB naming metadata: %w", err)
		}
	}
	return nil
}
