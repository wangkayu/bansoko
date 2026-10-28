package application

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	catalogapplication "bansoko/internal/catalog/application"
)

func parseEpisodeArray(raw string) ([]int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	parts := strings.Split(raw, ",")
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return nil, false
		}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, false
	}
	return result, true
}

// ListAnimeApplication returns the managed anime list with the sole metadata hold and a
// lightweight summary (subscribed group names + worst RSS health). It is
// side-effect free.
func (s *Service) ListAnimeApplication(ctx context.Context) ([]AnimeListItem, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.id, a.title, a.title_override, a.version,
       sub.id, sub.origin, sub.origin_entry_ref, sub.instance_label, sub.state, sub.version,
       sub.new_releases_only, sub.start_baseline_at,
       a.lifecycle_state, a.fence_epoch, a.retired_at,
       m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season, m.media_kind_override,
       m.total_episodes, m.year, m.season, m.version,
       groups_agg.group_names,
       health_agg.worst_health,
       health_agg.health_reason,
       progress_agg.episodes,
       totals_agg.total_episodes,
       totals_agg.weekday_label,
       totals_agg.weekday,
       totals_agg.release_format,
       totals_agg.cover_url
FROM managed_anime a
JOIN subscriptions sub ON sub.managed_anime_id = a.id
JOIN anime_metadata m ON m.managed_anime_id = a.id
LEFT JOIN LATERAL (
  SELECT COALESCE(array_to_string(array_agg(src.group_name) FILTER (WHERE src.group_name IS NOT NULL), ','), '') AS group_names
  FROM rss_sources src
  WHERE src.subscription_id = sub.id
) groups_agg ON TRUE
LEFT JOIN LATERAL (
  SELECT CASE
    WHEN bool_or(rt.health = 'failed') THEN 'failed'
    WHEN bool_or(rt.health = 'degraded') THEN 'degraded'
    WHEN bool_or(rt.health = 'ready') THEN 'ready'
    ELSE NULL
  END AS worst_health,
  (ARRAY_AGG(
    COALESCE(NULLIF(btrim(rt.reason_detail), ''), NULLIF(btrim(rt.reason_code), ''))
    ORDER BY CASE rt.health WHEN 'failed' THEN 0 WHEN 'degraded' THEN 1 ELSE 2 END, rt.updated_at DESC
  ) FILTER (WHERE rt.health IN ('failed', 'degraded')))[1] AS health_reason
  FROM rss_source_runtime rt
  JOIN rss_sources src ON src.id = rt.rss_source_id
  WHERE src.subscription_id = sub.id
) health_agg ON TRUE
LEFT JOIN LATERAL (
  -- Progress is what the library holds: the episode coverage keys of the
  -- anime's active verified placements (a batch counts every placed file;
  -- every episode key counts, specials included; unknown files carry none).
  SELECT ARRAY_TO_STRING(ARRAY_AGG(DISTINCT keys.episode ORDER BY keys.episode), ',') AS episodes
  FROM (
    SELECT substring(pc.observation_key FROM '^tv:(?:s[0-9]+:)?e([0-9]+)$')::int AS episode
    FROM placements p
    JOIN placement_coverage pc ON pc.placement_id = p.id
    WHERE p.managed_anime_id = a.id
      AND p.state = 'active'
      AND p.verification_state = 'verified'
      AND pc.observation_key ~ '^tv:(?:s[0-9]+:)?e[0-9]+$'
  ) keys
) progress_agg ON TRUE
LEFT JOIN LATERAL (
  SELECT (ce.source_hint->>'episodes')::int AS total_episodes,
         ce.source_hint->>'weekdayLabel' AS weekday_label,
         ce.source_hint->>'weekday' AS weekday,
         ce.source_hint->>'format' AS release_format,
         NULLIF(ce.source_hint->>'cover', '') AS cover_url
  FROM provider_catalog_entries ce
  WHERE ce.provider = sub.origin
    AND ce.provider_entry_ref = sub.origin_entry_ref
  ORDER BY ce.observed_at DESC
  LIMIT 1
) totals_agg ON TRUE
WHERE NOT EXISTS (
  SELECT 1
  FROM one_shot_acquisitions osa
  WHERE osa.managed_anime_id = a.id
)
ORDER BY a.created_at, a.id`)
	if err != nil {
		return nil, fmt.Errorf("list anime: %w", err)
	}
	defer rows.Close()
	result := []AnimeListItem{}
	for rows.Next() {
		var item AnimeListItem
		var titleOverride, originEntryRef, instanceLabel sql.NullString
		var origin, state string
		var retiredAt sql.NullTime
		var startBaselineAt sql.NullTime
		var bgmWorkID, tmdbWorkID, tmdbMediaKind, mediaKindOverride sql.NullString
		var tmdbSeason, metadataTotalEpisodes, metadataYear, metadataSeason sql.NullInt32
		var metadataVersion int64
		var groupNames sql.NullString
		var worstHealth sql.NullString
		var healthReason sql.NullString
		var episodes sql.NullString
		var catalogTotalEpisodes sql.NullInt32
		var weekdayLabel, weekday, providerFormat sql.NullString
		var coverURL sql.NullString
		if err := rows.Scan(&item.ManagedAnime.ID, &item.ManagedAnime.Title, &titleOverride, &item.ManagedAnime.Version,
			&item.Subscription.ID, &origin, &originEntryRef, &instanceLabel, &state, &item.Subscription.Version,
			&item.Subscription.NewReleasesOnly, &startBaselineAt,
			&item.Subscription.LifecycleState, &item.Subscription.LifecycleEpoch, &retiredAt,
			&bgmWorkID, &tmdbWorkID, &tmdbMediaKind, &tmdbSeason, &mediaKindOverride,
			&metadataTotalEpisodes, &metadataYear, &metadataSeason, &metadataVersion,
			&groupNames, &worstHealth, &healthReason, &episodes, &catalogTotalEpisodes, &weekdayLabel, &weekday, &providerFormat, &coverURL); err != nil {
			return nil, fmt.Errorf("scan anime list item: %w", err)
		}
		item.ManagedAnime.TitleOverride = nullableStringPointer(titleOverride)
		item.Subscription.ManagedAnimeID = item.ManagedAnime.ID
		item.Subscription.Origin = origin
		item.Subscription.OriginEntryRef = nullableStringPointer(originEntryRef)
		item.Subscription.InstanceLabel = nullableStringPointer(instanceLabel)
		item.Subscription.State = subscriptionStateFromDB(state)
		if startBaselineAt.Valid {
			item.Subscription.StartBaselineAt = &startBaselineAt.Time
		}
		if retiredAt.Valid {
			item.Subscription.RetiredAt = &retiredAt.Time
		}
		bgm, tmdb := applicationBindingsFromRow(bgmWorkID, tmdbWorkID, tmdbMediaKind, tmdbSeason)
		item.Metadata = AnimeMetadata{ManagedAnimeID: item.ManagedAnime.ID, BGM: bgm, TMDB: tmdb, Version: metadataVersion}
		item.Metadata.MediaKindOverride = nullableStringPointer(mediaKindOverride)
		item.Metadata.TotalEpisodes = nullableIntPointer(metadataTotalEpisodes)
		item.Metadata.Year = nullableIntPointer(metadataYear)
		item.Metadata.Season = nullableIntPointer(metadataSeason)
		item.MetadataHold = metadataHoldFromApplication(bgm, tmdb)
		if coverURL.Valid {
			cover := strings.TrimSpace(coverURL.String)
			if cover != "" {
				item.CoverURL = &cover
			}
		}
		if bgm != nil && strings.TrimSpace(bgm.WorkID) != "" {
			poster := "/api/v1/images/poster/" + strings.TrimSpace(bgm.WorkID)
			item.PosterURL = &poster
		}
		var catalogTotal *int
		if catalogTotalEpisodes.Valid && catalogTotalEpisodes.Int32 > 0 {
			t := int(catalogTotalEpisodes.Int32)
			catalogTotal = &t
		}
		item.EpisodeProgress = buildApplicationEpisodeProgress(episodes, item.Metadata.TotalEpisodes, catalogTotal)
		// The schedule and release format are provider catalog facts read from
		// the entry the subscription came from; a custom subscription has none.
		hint := map[string]any{"weekdayLabel": weekdayLabel.String, "weekday": weekday.String, "format": providerFormat.String}
		item.BroadcastDay = catalogapplication.BroadcastDayKey(origin, hint)
		item.ReleaseFormat = catalogapplication.ReleaseFormatKey(origin, hint)
		if groupNames.Valid && groupNames.String != "" {
			names := strings.Split(groupNames.String, ",")
			item.SubscribedGroups = &names
		}
		if worstHealth.Valid {
			health := worstHealth.String
			item.RSSHealth = &health
		}
		if healthReason.Valid && healthReason.String != "" {
			item.RSSHealthReason = &healthReason.String
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
