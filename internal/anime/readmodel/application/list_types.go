package application

import (
	"database/sql"

	subscriptiondomain "bansoko/internal/subscription/domain"
)

type AnimeListItem struct {
	BroadcastDay     string
	CoverURL         *string
	EpisodeProgress  *EpisodeProgress
	ManagedAnime     ManagedAnime
	Metadata         AnimeMetadata
	MetadataHold     MetadataHold
	PosterURL        *string
	ReleaseFormat    string
	RSSHealth        *string
	RSSHealthReason  *string
	SubscribedGroups *[]string
	Subscription     Subscription
}

type EpisodeProgress struct {
	Done     bool
	Episodes []int
	Total    *int
}

func subscriptionStateFromDB(value string) subscriptiondomain.SubscriptionState {
	return subscriptiondomain.SubscriptionState(value)
}

// Both projections aggregate only coverage held by active, verified placements
// that participate in the owner's identity. m supplies the owner ID and kind.
const applicationEpisodeProgressSQL = `
  SELECT ARRAY_TO_STRING(ARRAY_AGG(DISTINCT keys.episode ORDER BY keys.episode), ',') AS episodes
  FROM (
    SELECT CASE WHEN m.media_kind = 'movie' THEN 1
           ELSE substring(pc.observation_key FROM '^tv:(?:s[0-9]+:)?e([0-9]+)$')::int END AS episode
    FROM placements p
    JOIN placement_coverage pc ON pc.placement_id = p.id
    WHERE p.managed_anime_id = m.managed_anime_id
      AND p.state = 'active'
      AND p.verification_state = 'verified'
      AND p.replacement_eligible
      AND CASE WHEN m.media_kind = 'movie' THEN pc.observation_key = 'movie:0'
          ELSE pc.observation_key ~ '^tv:(?:s[0-9]+:)?e[0-9]+$' END
  ) keys`

func buildApplicationEpisodeProgress(episodesStr sql.NullString, mediaKind *string, totalEpisodes, catalogTotal *int) *EpisodeProgress {
	if mediaKind != nil && *mediaKind == "movie" {
		total := 1
		progress := &EpisodeProgress{Episodes: []int{}, Total: &total}
		if episodesStr.Valid && episodesStr.String == "1" {
			progress.Episodes = []int{1}
			progress.Done = true
		}
		return progress
	}
	episodeNumbers, ok := parseEpisodeArray(episodesStr.String)
	if !ok {
		return nil
	}
	progress := &EpisodeProgress{Episodes: episodeNumbers}
	total := totalEpisodes
	if total == nil || *total <= 0 {
		total = catalogTotal
	}
	if total != nil && *total > 0 {
		value := *total
		progress.Total = &value
		// Progress is count-based: the number of distinct imported episodes is
		// compared with the total. Episode numbers are not mapped to slots, so
		// a high episode number never completes a show on its own.
		progress.Done = distinctCount(episodeNumbers) >= value
	}
	return progress
}

func distinctCount(values []int) int {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	return len(seen)
}
