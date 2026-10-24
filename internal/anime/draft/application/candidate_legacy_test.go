package application

import (
	"context"

	animedomain "bansoko/internal/anime/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func subscriptionStateFromGenerated(value generated.SubscriptionState) subscriptiondomain.SubscriptionState {
	return subscriptiondomain.SubscriptionState(value)
}

func subscribeResultFromHTTP(value generated.SubscribeResult) SubscribeResult {
	result := SubscribeResult{
		EffectiveConfigVersions: EffectiveConfigVersions{
			FileEffect: int64(value.EffectiveConfigVersions.FileEffect), FileFilter: int64(value.EffectiveConfigVersions.FileFilter), Naming: int64(value.EffectiveConfigVersions.Naming),
			Parser: int64(value.EffectiveConfigVersions.Parser), ReleaseFilter: int64(value.EffectiveConfigVersions.ReleaseFilter), Subgroups: int64(value.EffectiveConfigVersions.Subgroups),
		},
		ManagedAnime: ManagedAnime{ID: string(value.ManagedAnime.Id), Title: value.ManagedAnime.Title, TitleOverride: cloneString(value.ManagedAnime.TitleOverride), Version: int64(value.ManagedAnime.Version)},
		Metadata:     AnimeMetadata{ManagedAnimeID: string(value.Metadata.ManagedAnimeId), BGM: applicationBGM(value.Metadata.Bgm), TMDB: applicationTMDB(value.Metadata.Tmdb), TitleOverride: cloneString(value.Metadata.TitleOverride), TotalEpisodes: cloneInt(value.Metadata.TotalEpisodes), Version: int64(value.Metadata.Version)},
		Subscription: Subscription{ID: string(value.Subscription.Id), ManagedAnimeID: string(value.Subscription.ManagedAnimeId), Origin: string(value.Subscription.Origin), OriginEntryRef: cloneString(value.Subscription.OriginEntryRef), InstanceLabel: cloneString(value.Subscription.InstanceLabel), State: subscriptiondomain.SubscriptionState(value.Subscription.State), LifecycleState: string(value.Subscription.LifecycleState), LifecycleEpoch: value.Subscription.LifecycleEpoch, RetiredAt: cloneTime(value.Subscription.RetiredAt), Version: int64(value.Subscription.Version)},
	}
	for _, source := range value.RssSources {
		result.RssSources = append(result.RssSources, RSSSource{AnibtAuxMode: applicationMode(source.AnibtAuxMode), GroupName: source.GroupName, HttpFeedConnectionRef: cloneString(source.HttpFeedConnectionRef), ID: string(source.Id), LocatorDisplay: source.LocatorDisplay, NetworkRouteRef: cloneString(source.NetworkRouteRef), EpisodeOffset: source.EpisodeOffset, ProviderProvenance: applicationProvenance(source.ProviderProvenance), SourceKind: animedomain.SourceKind(source.SourceKind), SubscriptionID: string(source.SubscriptionId), Title: source.Title, Version: int64(source.Version)})
	}
	return result
}

func applicationBGM(value *generated.NullableBGMBinding) *BGMBinding {
	if value == nil {
		return nil
	}
	return &BGMBinding{WorkID: value.WorkId}
}

func applicationTMDB(value *generated.NullableTMDBBinding) *TMDBBinding {
	if value == nil {
		return nil
	}
	return &TMDBBinding{WorkID: value.WorkId, MediaKind: string(value.MediaKind), Season: value.Season}
}

func applicationMode(value *generated.RSSSourceAnibtAuxMode) *animedomain.RSSSourceDraftAnibtAuxMode {
	if value == nil {
		return nil
	}
	mapped := animedomain.RSSSourceDraftAnibtAuxMode(*value)
	return &mapped
}

func applicationProvenance(value *generated.ProviderProvenance) *animedomain.ProviderProvenance {
	if value == nil {
		return nil
	}
	return &animedomain.ProviderProvenance{DiscoverySnapshotVersion: value.DiscoverySnapshotVersion, Provider: animedomain.ProviderKind(value.Provider), ProviderEntryRef: value.ProviderEntryRef, ProviderGroupRef: value.ProviderGroupRef}
}

func (s *Service) SubscribeAnimeConfigCandidate(ctx context.Context, candidateID string, expectedVersion int64, idempotencyKey string, input generated.CandidateSubscribeInput) (generated.SubscribeResult, error) {
	revision := input.Revision
	if revision < 1 {
		candidate, err := s.GetAnimeConfigCandidate(ctx, candidateID)
		if err != nil {
			return generated.SubscribeResult{}, err
		}
		if candidate.CurrentRevision != nil {
			revision = *candidate.CurrentRevision
		}
	}
	applicationInput := CandidateSubscribeInput{Revision: revision, Start: input.Start != nil && *input.Start}
	if input.InitialState != nil {
		state := subscriptionStateFromGenerated(*input.InitialState)
		applicationInput.InitialState = &state
	}
	result, err := s.ActivateAnimeConfigCandidate(ctx, candidateID, expectedVersion, idempotencyKey, applicationInput)
	if err != nil {
		return generated.SubscribeResult{}, err
	}
	return subscribeResultToGenerated(result), nil
}

func subscribeResultToGenerated(value SubscribeResult) generated.SubscribeResult {
	result := generated.SubscribeResult{
		EffectiveConfigVersions: generated.EffectiveConfigVersions{
			FileEffect:    generated.Version(value.EffectiveConfigVersions.FileEffect),
			FileFilter:    generated.Version(value.EffectiveConfigVersions.FileFilter),
			Naming:        generated.Version(value.EffectiveConfigVersions.Naming),
			Parser:        generated.Version(value.EffectiveConfigVersions.Parser),
			ReleaseFilter: generated.Version(value.EffectiveConfigVersions.ReleaseFilter),
			Subgroups:     generated.Version(value.EffectiveConfigVersions.Subgroups),
		},
		ManagedAnime: generated.ManagedAnime{
			Id: generated.ID(value.ManagedAnime.ID), Title: value.ManagedAnime.Title,
			TitleOverride: cloneString(value.ManagedAnime.TitleOverride), Version: generated.Version(value.ManagedAnime.Version),
		},
		Metadata: generated.AnimeMetadata{
			ManagedAnimeId: generated.ID(value.Metadata.ManagedAnimeID),
			Bgm:            generatedBGM(value.Metadata.BGM), Tmdb: generatedTMDB(value.Metadata.TMDB),
			TitleOverride: cloneString(value.Metadata.TitleOverride), TotalEpisodes: cloneInt(value.Metadata.TotalEpisodes),
			Version: generated.Version(value.Metadata.Version),
		},
		Subscription: generated.Subscription{
			Id: generated.ID(value.Subscription.ID), ManagedAnimeId: generated.ID(value.Subscription.ManagedAnimeID),
			Origin: generated.SubscriptionOrigin(value.Subscription.Origin), OriginEntryRef: cloneString(value.Subscription.OriginEntryRef),
			InstanceLabel: cloneString(value.Subscription.InstanceLabel), State: generated.SubscriptionState(value.Subscription.State),
			LifecycleState: generated.SubscriptionLifecycleState(value.Subscription.LifecycleState), LifecycleEpoch: value.Subscription.LifecycleEpoch,
			RetiredAt: cloneTime(value.Subscription.RetiredAt), Version: generated.Version(value.Subscription.Version),
		},
	}
	result.RssSources = make([]generated.RSSSource, 0, len(value.RssSources))
	for _, source := range value.RssSources {
		mapped := generated.RSSSource{
			GroupName: source.GroupName, HttpFeedConnectionRef: cloneString(source.HttpFeedConnectionRef), Id: generated.ID(source.ID),
			LocatorDisplay: source.LocatorDisplay, NetworkRouteRef: cloneString(source.NetworkRouteRef), EpisodeOffset: source.EpisodeOffset,
			SourceKind: generated.SourceKind(source.SourceKind), SubscriptionId: generated.ID(source.SubscriptionID), Title: source.Title,
			Version: generated.Version(source.Version), AnibtAuxMode: generatedMode(source.AnibtAuxMode), ProviderProvenance: generatedProvenance(source.ProviderProvenance),
		}
		result.RssSources = append(result.RssSources, mapped)
	}
	return result
}

func generatedBGM(value *BGMBinding) *generated.NullableBGMBinding {
	if value == nil {
		return nil
	}
	return &generated.NullableBGMBinding{WorkId: value.WorkID}
}

func generatedTMDB(value *TMDBBinding) *generated.NullableTMDBBinding {
	if value == nil {
		return nil
	}
	return &generated.NullableTMDBBinding{WorkId: value.WorkID, MediaKind: generated.TMDBMediaKind(value.MediaKind), Season: value.Season}
}

func generatedMode(value *animedomain.RSSSourceDraftAnibtAuxMode) *generated.RSSSourceAnibtAuxMode {
	if value == nil {
		return nil
	}
	mapped := generated.RSSSourceAnibtAuxMode(*value)
	return &mapped
}

func generatedProvenance(value *animedomain.ProviderProvenance) *generated.ProviderProvenance {
	if value == nil {
		return nil
	}
	return &generated.ProviderProvenance{
		DiscoverySnapshotVersion: value.DiscoverySnapshotVersion, Provider: generated.ProviderKind(value.Provider),
		ProviderEntryRef: value.ProviderEntryRef, ProviderGroupRef: value.ProviderGroupRef,
	}
}
