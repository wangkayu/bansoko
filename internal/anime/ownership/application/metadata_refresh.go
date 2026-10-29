package application

import (
	"context"
	"database/sql"
	"fmt"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/apperrors"
)

// MetadataWorkRefresher re-pulls one bound work straight from its provider,
// bypassing every cache row — the positive-cache bypass of the 刷新元数据
// action (owner decree 2026-10-22) and its only caller. The adapter at the
// composition root implements it over the provider connections, the work
// title store (metadata_work_titles) and the persistent episode title store
// (episode_title_cache). The rename and alignment renders stay cache-first.
type MetadataWorkRefresher interface {
	RefreshWork(ctx context.Context, provider, mediaKind, workID string, season int) error
}

// SetMetadataWorkRefresher wires the re-pull port. Nil means the refresh
// action fails closed as a typed configuration error.
func (s *Service) SetMetadataWorkRefresher(refresher MetadataWorkRefresher) {
	if s != nil {
		s.metadataRefresher = refresher
	}
}

// RefreshAnimeMetadataApplication re-pulls every bound work from its provider
// and updates the cached rows the whitelisted naming renders read. It never
// edits the binding or the owner title records — correcting a binding stays
// the match flow's job — and it never touches subscription or work-item state,
// so it is read-only for the owner rows and write-only for the caches.
//
// Failure semantics: an anime with no binding at all is METADATA_NOT_BOUND; a
// re-pull that cannot reach its provider is a typed METADATA_REFRESH_FAILED
// (or TMDB_CREDENTIAL_NOT_CONFIGURED) and the cache rows stay as they were.
// The answer is the unchanged metadata owner view.
func (s *Service) RefreshAnimeMetadataApplication(ctx context.Context, managedAnimeID string) (animeapplication.AnimeMetadata, error) {
	if s == nil || s.db == nil {
		return animeapplication.AnimeMetadata{}, fmt.Errorf("ownership service is not configured")
	}
	if s.metadataRefresher == nil {
		return animeapplication.AnimeMetadata{}, &apperrors.Business{Status: 409, Code: "METADATA_REFRESH_UNAVAILABLE", Message: "The metadata re-pull is not wired; nothing was refreshed."}
	}
	var bgmWorkID, tmdbWorkID, tmdbKind, titleOverride sql.NullString
	var tmdbSeason, totalEpisodes, year, season sql.NullInt64
	var version int64
	var ownerMediaKind sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT ma.title_override, m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season,
       m.total_episodes, m.media_kind, m.year, m.season, m.version
FROM managed_anime ma
JOIN anime_metadata m ON m.managed_anime_id = ma.id
WHERE ma.id = $1`, managedAnimeID).Scan(&titleOverride, &bgmWorkID, &tmdbWorkID, &tmdbKind, &tmdbSeason,
		&totalEpisodes, &ownerMediaKind, &year, &season, &version)
	if err == sql.ErrNoRows {
		return animeapplication.AnimeMetadata{}, &apperrors.NotFound{Resource: "anime_metadata", ID: managedAnimeID}
	}
	if err != nil {
		return animeapplication.AnimeMetadata{}, fmt.Errorf("read anime metadata for refresh: %w", err)
	}
	if bgmWorkID.String == "" && tmdbWorkID.String == "" {
		return animeapplication.AnimeMetadata{}, &apperrors.Business{Status: 409, Code: "METADATA_NOT_BOUND", Message: "The anime has no metadata binding to refresh."}
	}
	// The bound Bangumi subject is the whole scope (season 0); the TMDB
	// episode list is re-pulled for its bound season, defaulting to season 1
	// when the binding records none.
	if bgmWorkID.String != "" {
		if err := s.metadataRefresher.RefreshWork(ctx, "bgm", "subject", bgmWorkID.String, 0); err != nil {
			return animeapplication.AnimeMetadata{}, err
		}
	}
	if tmdbWorkID.String != "" {
		refreshSeason := 1
		if tmdbSeason.Valid && tmdbSeason.Int64 > 0 {
			refreshSeason = int(tmdbSeason.Int64)
		}
		if err := s.metadataRefresher.RefreshWork(ctx, "tmdb", tmdbKind.String, tmdbWorkID.String, refreshSeason); err != nil {
			return animeapplication.AnimeMetadata{}, err
		}
	}
	return animeapplication.AnimeMetadata{
		ManagedAnimeID: managedAnimeID,
		BGM:            animeApplicationBGM(bgmWorkID.String),
		TMDB:           animeApplicationTMDB(tmdbWorkID.String, tmdbKind.String, tmdbSeason),
		TitleOverride:  nullStringPtr(titleOverride),
		TotalEpisodes:  nullInt64Ptr(totalEpisodes),
		// MediaKind is the naming branch pin, unchanged here.
		MediaKind: nullStringPtr(ownerMediaKind),
		Year:      nullInt64Ptr(year),
		Season:    nullInt64Ptr(season),
		Version:   version,
	}, nil
}

// animeApplicationBGM projects the stored BGM binding onto the owner view.
func animeApplicationBGM(workID string) *animeapplication.BGMBinding {
	if workID == "" {
		return nil
	}
	return &animeapplication.BGMBinding{WorkID: workID}
}

// animeApplicationTMDB projects the stored TMDB tuple onto the owner view.
func animeApplicationTMDB(workID, kind string, tmdbSeason sql.NullInt64) *animeapplication.TMDBBinding {
	if workID == "" {
		return nil
	}
	binding := &animeapplication.TMDBBinding{WorkID: workID, MediaKind: kind}
	if tmdbSeason.Valid {
		binding.Season = int(tmdbSeason.Int64)
	}
	return binding
}

// nullInt64Ptr projects a nullable column onto the owner view pointer.
func nullInt64Ptr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}
