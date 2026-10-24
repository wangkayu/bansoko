package application

import (
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

func TestSubscribeResultFromHTTPProjectsAllActivationFacts(t *testing.T) {
	titleOverride := "override"
	totalEpisodes := 12
	originEntryRef := "entry-1"
	instanceLabel := "one-shot"
	feedRef := "feed-1"
	routeRef := "route-1"
	offset := 2
	mode := generated.RSSSourceAnibtAuxMode("fill_missing")
	result := subscribeResultFromHTTP(generated.SubscribeResult{
		EffectiveConfigVersions: generated.EffectiveConfigVersions{
			FileEffect:    11,
			FileFilter:    12,
			Naming:        13,
			Parser:        14,
			ReleaseFilter: 15,
			Subgroups:     16,
		},
		ManagedAnime: generated.ManagedAnime{
			Id:            "anime-1",
			Title:         "Example",
			TitleOverride: &titleOverride,
			Version:       3,
		},
		Metadata: generated.AnimeMetadata{
			ManagedAnimeId: "anime-1",
			Bgm:            &generated.BGMBinding{WorkId: "bgm-1"},
			Tmdb:           &generated.TMDBBinding{WorkId: "tmdb-1", MediaKind: "tv", Season: 2},
			TitleOverride:  &titleOverride,
			TotalEpisodes:  &totalEpisodes,
			Version:        4,
		},
		Subscription: generated.Subscription{
			Id:             "subscription-1",
			ManagedAnimeId: "anime-1",
			Origin:         "anibt",
			OriginEntryRef: &originEntryRef,
			InstanceLabel:  &instanceLabel,
			State:          "stopped",
			LifecycleState: "active",
			LifecycleEpoch: 1,
			Version:        5,
		},
		RssSources: []generated.RSSSource{{
			AnibtAuxMode:          &mode,
			GroupName:             "Group",
			HttpFeedConnectionRef: &feedRef,
			Id:                    "source-1",
			LocatorDisplay:        "https://example.invalid/feed",
			NetworkRouteRef:       &routeRef,
			EpisodeOffset:         offset,
			ProviderProvenance: &generated.ProviderProvenance{
				DiscoverySnapshotVersion: "snapshot-1",
				Provider:                 "anibt",
				ProviderEntryRef:         "entry-1",
				ProviderGroupRef:         "group-1",
			},
			SourceKind:     "provider_group",
			SubscriptionId: "subscription-1",
			Title:          "Group",
			Version:        6,
		}},
	})

	if result.EffectiveConfigVersions.Subgroups != 16 || result.ManagedAnime.ID != "anime-1" || result.ManagedAnime.Version != 3 {
		t.Fatalf("activation facts were not projected: %#v", result)
	}
	if result.Metadata.BGM == nil || result.Metadata.BGM.WorkID != "bgm-1" || result.Metadata.TMDB == nil || result.Metadata.TMDB.WorkID != "tmdb-1" {
		t.Fatalf("metadata was not projected: %#v", result.Metadata)
	}
	if result.Subscription.State != "stopped" || result.Subscription.OriginEntryRef == nil || *result.Subscription.OriginEntryRef != originEntryRef {
		t.Fatalf("subscription was not projected: %#v", result.Subscription)
	}
	if len(result.RssSources) != 1 || result.RssSources[0].ProviderProvenance == nil || result.RssSources[0].AnibtAuxMode == nil {
		t.Fatalf("rss source was not projected: %#v", result.RssSources)
	}
	if *result.RssSources[0].AnibtAuxMode != "fill_missing" || result.RssSources[0].ProviderProvenance.Provider != "anibt" || result.RssSources[0].EpisodeOffset != 2 {
		t.Fatalf("rss source details were not projected: %#v", result.RssSources[0])
	}
}

func TestSubscribeResultFromHTTPClonesNullableValues(t *testing.T) {
	value := "original"
	mode := generated.RSSSourceAnibtAuxMode("disabled")
	result := subscribeResultFromHTTP(generated.SubscribeResult{
		ManagedAnime: generated.ManagedAnime{TitleOverride: &value},
		Metadata:     generated.AnimeMetadata{TitleOverride: &value},
		Subscription: generated.Subscription{OriginEntryRef: &value},
		RssSources: []generated.RSSSource{{
			AnibtAuxMode:       &mode,
			EpisodeOffset:      1,
			ProviderProvenance: &generated.ProviderProvenance{ProviderEntryRef: "entry"},
		}},
	})

	value = "changed"
	mode = "override"
	if *result.ManagedAnime.TitleOverride != "original" || *result.Metadata.TitleOverride != "original" || *result.Subscription.OriginEntryRef != "original" {
		t.Fatal("nullable string values were not cloned")
	}
	if *result.RssSources[0].AnibtAuxMode != "disabled" || result.RssSources[0].EpisodeOffset != 1 || result.RssSources[0].ProviderProvenance.ProviderEntryRef != "entry" {
		t.Fatal("nullable RSS values were not cloned")
	}
}
