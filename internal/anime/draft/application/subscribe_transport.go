package application

import (
	"context"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// SubscribeAnime is the generated-contract compatibility facade.
func (s *Service) SubscribeAnime(ctx context.Context, input generated.SubscribeAnimeInput, idempotencyKey string) (generated.SubscribeResult, error) {
	draft, err := domainDraft(input.Draft)
	if err != nil {
		return generated.SubscribeResult{}, draftInvalidError([]animedomain.FieldViolation{{Field: "draft", Code: "DRAFT_INVALID", Message: fmt.Sprintf("draft transport mapping failed: %v", err)}})
	}
	requestHash, err := canonicalJSONHash(input)
	if err != nil {
		return generated.SubscribeResult{}, fmt.Errorf("hash subscribe request: %w", err)
	}
	result, err := s.SubscribeAnimeApplication(ctx, SubscribeAnimeInput{Draft: draft, RequestHash: requestHash, Start: input.Start != nil && *input.Start}, idempotencyKey)
	if err != nil {
		return generated.SubscribeResult{}, err
	}
	return subscribeResultForHTTP(result), nil
}

// subscribeResultForHTTP is the compatibility projection for the legacy direct
// SubscribeAnime public signature. The shared transaction never constructs
// generated transport values.
func subscribeResultForHTTP(value SubscribeResult) generated.SubscribeResult {
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
			Id:            generated.ID(value.ManagedAnime.ID),
			Title:         value.ManagedAnime.Title,
			TitleOverride: cloneString(value.ManagedAnime.TitleOverride),
			Version:       generated.Version(value.ManagedAnime.Version),
		},
		Metadata: generated.AnimeMetadata{
			ManagedAnimeId: generated.ID(value.Metadata.ManagedAnimeID),
			Bgm:            generatedBGMForHTTP(value.Metadata.BGM),
			Tmdb:           generatedTMDBForHTTP(value.Metadata.TMDB),
			TitleOverride:  cloneString(value.Metadata.TitleOverride),
			TotalEpisodes:  cloneInt(value.Metadata.TotalEpisodes),
			MediaKind:      (*generated.AnimeMetadataMediaKind)(cloneString(value.Metadata.MediaKind)),
			Year:           cloneInt(value.Metadata.Year),
			Season:         cloneInt(value.Metadata.Season),
			Version:        generated.Version(value.Metadata.Version),
		},
		Subscription: generated.Subscription{
			Id:              generated.ID(value.Subscription.ID),
			ManagedAnimeId:  generated.ID(value.Subscription.ManagedAnimeID),
			Origin:          generated.SubscriptionOrigin(value.Subscription.Origin),
			OriginEntryRef:  cloneString(value.Subscription.OriginEntryRef),
			EntrySeason:     cloneString(value.Subscription.EntrySeason),
			InstanceLabel:   cloneString(value.Subscription.InstanceLabel),
			State:           generated.SubscriptionState(value.Subscription.State),
			Version:         generated.Version(value.Subscription.Version),
			LifecycleState:  generated.SubscriptionLifecycleState(value.Subscription.LifecycleState),
			LifecycleEpoch:  value.Subscription.LifecycleEpoch,
			RetiredAt:       cloneTime(value.Subscription.RetiredAt),
			NewReleasesOnly: value.Subscription.NewReleasesOnly,
			StartBaselineAt: cloneTime(value.Subscription.StartBaselineAt),
			CreatedAt:       value.Subscription.CreatedAt,
		},
	}
	result.RssSources = make([]generated.RSSSource, 0, len(value.RssSources))
	for _, source := range value.RssSources {
		mapped := generated.RSSSource{
			Id:                    generated.ID(source.ID),
			SubscriptionId:        generated.ID(source.SubscriptionID),
			SourceKind:            generated.SourceKind(source.SourceKind),
			LocatorDisplay:        source.LocatorDisplay,
			GroupName:             source.GroupName,
			Title:                 source.Title,
			EpisodeOffset:         source.EpisodeOffset,
			HttpFeedConnectionRef: cloneString(source.HttpFeedConnectionRef),
			NetworkRouteRef:       cloneString(source.NetworkRouteRef),
			Version:               generated.Version(source.Version),
		}
		if source.AnibtAuxMode != nil {
			mode := generated.RSSSourceAnibtAuxMode(*source.AnibtAuxMode)
			mapped.AnibtAuxMode = &mode
		}
		if source.ProviderProvenance != nil {
			provenance := generated.ProviderProvenance{
				DiscoverySnapshotVersion: source.ProviderProvenance.DiscoverySnapshotVersion,
				Provider:                 generated.ProviderKind(source.ProviderProvenance.Provider),
				ProviderEntryRef:         source.ProviderProvenance.ProviderEntryRef,
				ProviderGroupRef:         source.ProviderProvenance.ProviderGroupRef,
			}
			mapped.ProviderProvenance = &provenance
		}
		result.RssSources = append(result.RssSources, mapped)
	}
	return result
}

func generatedBGMForHTTP(value *BGMBinding) *generated.BGMBinding {
	if value == nil {
		return nil
	}
	return &generated.BGMBinding{WorkId: value.WorkID}
}

func generatedTMDBForHTTP(value *TMDBBinding) *generated.TMDBBinding {
	if value == nil {
		return nil
	}
	return &generated.TMDBBinding{WorkId: value.WorkID, MediaKind: generated.TMDBMediaKind(value.MediaKind), Season: value.Season}
}
