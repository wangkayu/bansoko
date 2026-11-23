// Package draftfixture exposes draft setup for tests outside the draft owner.
package draftfixture

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/apperrors"
	"bansoko/internal/transport/httpapi/generated"
)

type Service struct {
	*draft.Service
}

type Dependencies = draft.Dependencies
type CandidateDraftInput = draft.CandidateDraftInput
type CandidateSubscribeInput = draft.CandidateSubscribeInput
type SubscribeResult = draft.SubscribeResult
type AnimeConfigCandidate = draft.AnimeConfigCandidate

func NewService(db *sql.DB, deps Dependencies) (*Service, error) {
	svc, err := draft.NewService(db, deps)
	if err != nil {
		return nil, err
	}
	return &Service{Service: svc}, nil
}

// SubscribeAnime maps a generated SubscribeAnimeInput onto SubscribeAnimeApplication for test callers.
func (s *Service) SubscribeAnime(ctx context.Context, input generated.SubscribeAnimeInput, idempotencyKey string) (generated.SubscribeResult, error) {
	return SubscribeAnime(ctx, s.Service, input, idempotencyKey)
}

// SubscribeAnime maps a generated SubscribeAnimeInput onto a *draft.Service for test callers.
func SubscribeAnime(ctx context.Context, svc *draft.Service, input generated.SubscribeAnimeInput, idempotencyKey string) (generated.SubscribeResult, error) {
	domainDraft, err := animetransport.Draft(input.Draft)
	if err != nil {
		field := "draft"
		return generated.SubscribeResult{}, &apperrors.Validation{
			Code:    "DRAFT_INVALID",
			Details: []apperrors.FieldViolation{{Field: &field, Code: "DRAFT_INVALID", Message: fmt.Sprintf("draft transport mapping failed: %v", err)}},
		}
	}
	requestHash, err := animeapplication.CanonicalJSONHash(input)
	if err != nil {
		return generated.SubscribeResult{}, fmt.Errorf("hash subscribe request: %w", err)
	}
	result, err := svc.SubscribeAnimeApplication(ctx, draft.SubscribeAnimeInput{
		Draft:       domainDraft,
		RequestHash: requestHash,
		Start:       input.Start != nil && *input.Start,
	}, idempotencyKey)
	if err != nil {
		return generated.SubscribeResult{}, err
	}
	return subscribeResultForHTTP(result), nil
}

func subscribeResultForHTTP(value draft.SubscribeResult) generated.SubscribeResult {
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
			Id:                       generated.ID(value.Subscription.ID),
			ManagedAnimeId:           generated.ID(value.Subscription.ManagedAnimeID),
			Origin:                   generated.SubscriptionOrigin(value.Subscription.Origin),
			OriginEntryRef:           cloneString(value.Subscription.OriginEntryRef),
			EntrySeason:              cloneString(value.Subscription.EntrySeason),
			InstanceLabel:            cloneString(value.Subscription.InstanceLabel),
			State:                    generated.SubscriptionState(value.Subscription.State),
			Version:                  generated.Version(value.Subscription.Version),
			LifecycleState:           generated.SubscriptionLifecycleState(value.Subscription.LifecycleState),
			LifecycleEpoch:           value.Subscription.LifecycleEpoch,
			RetiredAt:                cloneTime(value.Subscription.RetiredAt),
			NewReleasesOnly:          value.Subscription.NewReleasesOnly,
			StartBaselineAt:          cloneTime(value.Subscription.StartBaselineAt),
			IgnoreMultiVideoReleases: value.Subscription.IgnoreMultiVideoReleases,
			CreatedAt:                value.Subscription.CreatedAt,
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
			EpisodeSpan:           animedomain.NormalizeEpisodeSpan(source.EpisodeSpan),
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

func generatedBGMForHTTP(value *animeapplication.BGMBinding) *generated.BGMBinding {
	if value == nil {
		return nil
	}
	return &generated.BGMBinding{WorkId: value.WorkID}
}

func generatedTMDBForHTTP(value *animeapplication.TMDBBinding) *generated.TMDBBinding {
	if value == nil {
		return nil
	}
	return &generated.TMDBBinding{WorkId: value.WorkID, MediaKind: generated.TMDBMediaKind(value.MediaKind), Season: value.Season}
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cp := *value
	return &cp
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cp := *value
	return &cp
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cp := *value
	return &cp
}
