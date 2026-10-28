package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	"bansoko/internal/observability/businesslog"
	"bansoko/internal/persistence/txscope"
)

// PatchAnimeMetadataApplication replaces the metadata owner with expected-version CAS.
func (s *Service) PatchAnimeMetadataApplication(ctx context.Context, managedAnimeID string, patch MetadataPatchInput) (AnimeMetadata, error) {
	domainBGM, domainTMDB := metadataPatchBindingsForDomain(patch)
	violations := animedomain.ValidateMetadataPatch(domainBGM, domainTMDB, patch.TitleOverride, patch.MediaKindOverride, patch.TotalEpisodes, patch.Year)
	violations = append(violations, animedomain.ValidateAnimeSeason("season", patch.Season)...)
	if len(violations) > 0 {
		return AnimeMetadata{}, animeapplication.DraftInvalidError(violations)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AnimeMetadata{}, fmt.Errorf("begin metadata patch: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockActiveLifecycleTx(ctx, tx, managedAnimeID); err != nil {
		return AnimeMetadata{}, err
	}
	var subscriptionID, title string
	if err := tx.QueryRowContext(ctx, `
SELECT s.id, ma.title
FROM subscriptions s
JOIN managed_anime ma ON ma.id = s.managed_anime_id
WHERE s.managed_anime_id = $1
FOR UPDATE OF s, ma`, managedAnimeID).Scan(&subscriptionID, &title); errors.Is(err, sql.ErrNoRows) {
		return AnimeMetadata{}, &apperrors.NotFound{Resource: "subscription", ID: managedAnimeID}
	} else if err != nil {
		return AnimeMetadata{}, fmt.Errorf("lock metadata subscription: %w", err)
	}

	var version int64
	var currentBgm, currentTmdbWorkID, currentTmdbKind, currentMediaKindOverride sql.NullString
	var currentTmdbSeason, currentTotalEpisodes sql.NullInt64
	err = tx.QueryRowContext(ctx, `
SELECT bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season,
       total_episodes, media_kind_override, version
FROM anime_metadata
WHERE managed_anime_id = $1
FOR UPDATE`, managedAnimeID).Scan(&currentBgm, &currentTmdbWorkID, &currentTmdbKind, &currentTmdbSeason,
		&currentTotalEpisodes, &currentMediaKindOverride, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return AnimeMetadata{}, &apperrors.NotFound{Resource: "anime_metadata", ID: managedAnimeID}
	}
	if err != nil {
		return AnimeMetadata{}, fmt.Errorf("lock anime metadata: %w", err)
	}
	if version != patch.ExpectedVersion {
		return AnimeMetadata{}, &apperrors.VersionConflict{Owner: "anime_metadata", CurrentVersion: version}
	}

	changed := animeapplication.NullableValueChanged(currentBgm, animeapplication.BGMWorkID(domainBGM)) ||
		animeapplication.NullableValueChanged(currentTmdbWorkID, animeapplication.TMDBWorkID(domainTMDB)) ||
		animeapplication.NullableValueChanged(currentTmdbKind, animeapplication.TMDBMediaKind(domainTMDB)) ||
		animeapplication.NullableIntChanged(currentTmdbSeason, animeapplication.TMDBSeason(domainTMDB)) ||
		animeapplication.NullableIntPtrChanged(currentTotalEpisodes, patch.TotalEpisodes)
	// The owner media kind is backfilled from the TMDB tuple this patch binds
	// while the submitted kind is unset; a submitted kind is kept whatever
	// the tuple.
	nextMediaKind := animedomain.BackfilledMediaKind(patch.MediaKindOverride, domainTMDB)
	nextMediaKindOverride := ""
	if nextMediaKind != nil {
		nextMediaKindOverride = *nextMediaKind
	}
	changed = changed || animeapplication.NullableValueChanged(currentMediaKindOverride, nextMediaKindOverride)
	// The effective kind (unset is tv) decides the episode key shape; it
	// changes only while no file or unsettled acquisition holds the identity.
	var currentMediaKind *string
	if currentMediaKindOverride.Valid {
		currentMediaKind = &currentMediaKindOverride.String
	}
	allowed, blockers, err := animeapplication.IdentityKindChangeAllowedTx(ctx, tx, managedAnimeID, currentMediaKind, nextMediaKind)
	if err != nil {
		return AnimeMetadata{}, err
	}
	if !allowed {
		return AnimeMetadata{}, animeapplication.IdentityKindChangeBlockedError(blockers)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_metadata
SET bgm_work_id = $2, tmdb_work_id = $3, tmdb_media_kind = $4, tmdb_season = $5,
    total_episodes = $6, media_kind_override = $7, year = $8, season = $9,
    version = version + 1, updated_at = NOW()
WHERE managed_anime_id = $1`,
		managedAnimeID, animeapplication.NullableTrimmed(animeapplication.BGMWorkID(domainBGM)), animeapplication.NullableTrimmed(animeapplication.TMDBWorkID(domainTMDB)),
		animeapplication.NullableString(animeapplication.TMDBMediaKind(domainTMDB)), animeapplication.NullableInt(animeapplication.TMDBSeason(domainTMDB)),
		animeapplication.NullableIntPtr(patch.TotalEpisodes), animeapplication.NullableString(nextMediaKindOverride), animeapplication.NullableIntPtr(patch.Year), animeapplication.NullableIntPtr(patch.Season)); err != nil {
		return AnimeMetadata{}, fmt.Errorf("save anime metadata: %w", err)
	}
	// title_override is the anime's fixed title: a value replaces it, and nil
	// (JSON null or an omitted field) keeps the stored title. It is never
	// cleared, so nothing falls back to a provider or metadata title later.
	var titleOverrideValue any
	if patch.TitleOverride != nil {
		titleOverrideValue = *patch.TitleOverride
	}
	var storedTitleOverride sql.NullString
	if err := tx.QueryRowContext(ctx, `
UPDATE managed_anime SET title_override = COALESCE($2, title_override) WHERE id = $1
RETURNING title_override`,
		managedAnimeID, titleOverrideValue).Scan(&storedTitleOverride); err != nil {
		return AnimeMetadata{}, fmt.Errorf("save anime title: %w", err)
	}
	if patch.BGM != nil && patch.TMDB != nil && changed {
		if err := s.scheduleMetadataReleaseEvaluation(ctx, tx, managedAnimeID); err != nil {
			return AnimeMetadata{}, err
		}
		if err := businesslog.WriteMetadataAppliedTx(ctx, txscope.Wrap(tx), s.activityWriter, subscriptionID, managedAnimeID, version+1); err != nil {
			return AnimeMetadata{}, fmt.Errorf("write metadata applied log: %w", err)
		}
	}
	// Any metadata write may complete the configuration an import waits for.
	if err := animeapplication.WakeConfigWaitingPlansTx(ctx, tx, managedAnimeID); err != nil {
		return AnimeMetadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return AnimeMetadata{}, fmt.Errorf("commit metadata patch: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetRSS)
	return AnimeMetadata{
		ManagedAnimeID:    managedAnimeID,
		BGM:               animeapplication.ApplicationBGMFromDomain(domainBGM),
		TMDB:              animeapplication.ApplicationTMDBFromDomain(domainTMDB),
		TitleOverride:     nullStringPtr(storedTitleOverride),
		TotalEpisodes:     animeapplication.CloneInt(patch.TotalEpisodes),
		MediaKindOverride: animeapplication.CloneString(nextMediaKind),
		Year:              animeapplication.CloneInt(patch.Year),
		Season:            animeapplication.CloneInt(patch.Season),
		Version:           version + 1,
	}, nil
}

// metadataPatchBindingsForDomain converts the submitted owner bindings through
// the shared kernel projection, so the ownership write path and the read-model
// hold projection can never disagree about the same binding values.
func metadataPatchBindingsForDomain(patch MetadataPatchInput) (*animedomain.NullableBGMBinding, *animedomain.NullableTMDBBinding) {
	return animeapplication.NullableBindingsForDomain(patch.BGM, patch.TMDB)
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}
