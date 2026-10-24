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

func buildApplicationEpisodeProgress(episodesStr sql.NullString, totalEpisodes, catalogTotal *int) *EpisodeProgress {
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
