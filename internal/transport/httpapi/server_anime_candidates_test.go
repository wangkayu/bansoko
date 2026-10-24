package httpapi

import (
	"testing"
	"time"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
)

func TestMapCandidateSubscribeResultProjectsNeutralResult(t *testing.T) {
	titleOverride := "override"
	mode := animedomain.RSSSourceDraftAnibtAuxModeFillMissing
	result := mapCandidateSubscribeResult(draftapplication.SubscribeResult{
		EffectiveConfigVersions: draftapplication.EffectiveConfigVersions{Parser: 7},
		ManagedAnime: animeapplication.ManagedAnime{
			ID:            "anime-1",
			Title:         "Example",
			TitleOverride: &titleOverride,
			Version:       2,
		},
		Metadata: animeapplication.AnimeMetadata{
			ManagedAnimeID: "anime-1",
			BGM:            &animeapplication.BGMBinding{WorkID: "bgm-1"},
			TMDB:           &animeapplication.TMDBBinding{WorkID: "tmdb-1", MediaKind: "tv", Season: 1},
		},
		Subscription: animeapplication.Subscription{
			ID:             "subscription-1",
			ManagedAnimeID: "anime-1",
			Origin:         "custom",
			State:          "active",
			Version:        3,
		},
		RssSources: []animeapplication.RSSSource{{
			ID:             "source-1",
			AnibtAuxMode:   &mode,
			SourceKind:     animedomain.SourceKindProviderGroup,
			SubscriptionID: "subscription-1",
			LocatorDisplay: "https://example.invalid/feed",
			ProviderProvenance: &animedomain.ProviderProvenance{
				Provider: animedomain.ProviderKindMikan,
			},
		}},
	})

	if result.EffectiveConfigVersions.Parser != 7 || result.ManagedAnime.Id != "anime-1" || result.ManagedAnime.TitleOverride == nil || *result.ManagedAnime.TitleOverride != titleOverride {
		t.Fatalf("unexpected managed anime mapping: %#v", result.ManagedAnime)
	}
	if result.Metadata.Bgm == nil || result.Metadata.Bgm.WorkId != "bgm-1" || result.Metadata.Tmdb == nil || result.Metadata.Tmdb.WorkId != "tmdb-1" {
		t.Fatalf("unexpected metadata mapping: %#v", result.Metadata)
	}
	if result.Subscription.State != "active" || len(result.RssSources) != 1 || result.RssSources[0].AnibtAuxMode == nil || *result.RssSources[0].AnibtAuxMode != "fill_missing" {
		t.Fatalf("unexpected activation mapping: %#v", result)
	}
	if result.RssSources[0].ProviderProvenance == nil || result.RssSources[0].ProviderProvenance.Provider != "mikan" {
		t.Fatalf("unexpected provider provenance mapping: %#v", result.RssSources[0])
	}
}

func TestMapCandidateSubscribeResultPreservesNilAndEmptySources(t *testing.T) {
	for name, sources := range map[string][]animeapplication.RSSSource{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			mapped := mapCandidateSubscribeResult(draftapplication.SubscribeResult{RssSources: sources})
			if (mapped.RssSources == nil) != (sources == nil) {
				t.Fatalf("sources nil = %v, want %v", mapped.RssSources == nil, sources == nil)
			}
		})
	}
}

func TestMapAnimeConfigCandidateClonesPointers(t *testing.T) {
	activated := "anime-1"
	instance := "instance"
	entry := "entry"
	title := "title"
	cleanupReason := "provider_owner_retired"
	cleanupAt := time.Unix(30, 0).UTC()
	mapped, err := mapAnimeConfigCandidate(draftapplication.AnimeConfigCandidate{
		ActivatedManagedAnimeId: &activated,
		CleanupEligibleAt:       &cleanupAt,
		CleanupReason:           &cleanupReason,
		InstanceLabel:           &instance,
		OriginEntryRef:          &entry,
		TitleOverride:           &title,
	})
	if err != nil {
		t.Fatal(err)
	}
	activated, instance, entry, title, cleanupReason = "changed", "changed", "changed", "changed", "changed"
	cleanupAt = time.Unix(40, 0).UTC()
	if *mapped.ActivatedManagedAnimeId != "anime-1" || *mapped.InstanceLabel != "instance" || *mapped.OriginEntryRef != "entry" || *mapped.TitleOverride != "title" || *mapped.CleanupReason != "provider_owner_retired" || !mapped.CleanupEligibleAt.Equal(time.Unix(30, 0).UTC()) {
		t.Fatalf("candidate pointers were not cloned: %#v", mapped)
	}
}
