package httpapi

import (
	animeapplication "bansoko/internal/anime/application"
	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapAnimeListItems(values []readmodelapplication.AnimeListItem) []generated.AnimeListItem {
	result := make([]generated.AnimeListItem, 0, len(values))
	for _, value := range values {
		item := generated.AnimeListItem{
			ManagedAnime: generated.ManagedAnime{
				Id: value.ManagedAnime.ID, Title: value.ManagedAnime.Title,
				TitleOverride: mapAnimeListString(value.ManagedAnime.TitleOverride), Version: value.ManagedAnime.Version,
			},
			Metadata:        mapAnimeListMetadata(value.Metadata),
			MetadataHold:    mapAnimeListMetadataHold(value.MetadataHold),
			Subscription:    mapAnimeListSubscription(value.Subscription),
			BroadcastDay:    generated.BroadcastDay(value.BroadcastDay),
			CoverUrl:        mapAnimeListString(value.CoverURL),
			PosterUrl:       mapAnimeListString(value.PosterURL),
			RssHealthReason: mapAnimeListString(value.RSSHealthReason),
		}
		if value.EpisodeProgress != nil {
			item.EpisodeProgress = &struct {
				Done     bool  `json:"done"`
				Episodes []int `json:"episodes"`
				Total    *int  `json:"total,omitempty"`
			}{Done: value.EpisodeProgress.Done, Episodes: append([]int(nil), value.EpisodeProgress.Episodes...), Total: mapAnimeListInt(value.EpisodeProgress.Total)}
		}
		if value.ReleaseFormat != "" {
			format := generated.ReleaseFormat(value.ReleaseFormat)
			item.ReleaseFormat = &format
		}
		if value.SubscribedGroups != nil {
			groups := append([]string(nil), (*value.SubscribedGroups)...)
			item.SubscribedGroups = &groups
		}
		if value.RSSHealth != nil {
			health := generated.AnimeListItemRssHealth(*value.RSSHealth)
			item.RssHealth = &health
		}
		result = append(result, item)
	}
	return result
}

func mapAnimeListMetadata(value animeapplication.AnimeMetadata) generated.AnimeMetadata {
	result := generated.AnimeMetadata{
		ManagedAnimeId: value.ManagedAnimeID,
		TitleOverride:  mapAnimeListString(value.TitleOverride),
		TotalEpisodes:  mapAnimeListInt(value.TotalEpisodes),
		MediaKind:      (*generated.AnimeMetadataMediaKind)(mapAnimeListString(value.MediaKind)),
		Year:           mapAnimeListInt(value.Year),
		Season:         mapAnimeListInt(value.Season),
		Version:        value.Version,
	}
	if value.BGM != nil {
		result.Bgm = &generated.NullableBGMBinding{WorkId: value.BGM.WorkID}
	}
	if value.TMDB != nil {
		result.Tmdb = &generated.NullableTMDBBinding{WorkId: value.TMDB.WorkID, MediaKind: generated.TMDBMediaKind(value.TMDB.MediaKind), Season: value.TMDB.Season}
	}
	return result
}

func mapAnimeListMetadataHold(value animeapplication.MetadataHold) generated.MetadataHold {
	result := generated.MetadataHold{Held: value.Held, Missing: make([]generated.MetadataHoldMissing, len(value.Missing))}
	for i, missing := range value.Missing {
		result.Missing[i] = generated.MetadataHoldMissing(missing)
	}
	return result
}

func mapAnimeListSubscription(value animeapplication.Subscription) generated.Subscription {
	return generated.Subscription{
		Id: value.ID, ManagedAnimeId: value.ManagedAnimeID,
		Origin:         generated.SubscriptionOrigin(value.Origin),
		OriginEntryRef: mapAnimeListString(value.OriginEntryRef),
		InstanceLabel:  mapAnimeListString(value.InstanceLabel),
		State:          generated.SubscriptionState(value.State), LifecycleState: generated.SubscriptionLifecycleState(value.LifecycleState),
		LifecycleEpoch: value.LifecycleEpoch, RetiredAt: value.RetiredAt, Version: value.Version,
		NewReleasesOnly: value.NewReleasesOnly, StartBaselineAt: value.StartBaselineAt,
	}
}

func mapAnimeListString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func mapAnimeListInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
