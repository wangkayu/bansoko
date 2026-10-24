package transport

import (
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func ProviderGroupDiscoveryResult(value catalogdomain.ProviderGroupDiscoveryResult) generated.ProviderGroupDiscoveryResult {
	groups := make([]generated.ProviderGroupDiscoveryGroup, 0, len(value.Groups))
	for _, group := range value.Groups {
		groups = append(groups, generated.ProviderGroupDiscoveryGroup{
			Provider:    generated.ProviderKind(group.Provider),
			GroupRef:    group.GroupRef,
			DisplayName: group.DisplayName,
			FeedUrl:     group.FeedURL,
			ObservedAt:  group.ObservedAt,
		})
	}
	return generated.ProviderGroupDiscoveryResult{
		Groups:           groups,
		ObservedAt:       value.ObservedAt,
		Provider:         generated.ProviderKind(value.Provider),
		ProviderEntryRef: value.ProviderEntryRef,
		SchemaVersion:    value.SchemaVersion,
		SnapshotVersion:  value.SnapshotVersion,
		SourceVersion:    value.SourceVersion,
	}
}

func ProviderRSSPreviewResult(value catalogdomain.ProviderRSSPreviewResult) generated.ProviderRSSPreviewResult {
	seeds := make([]generated.ProviderRSSPreviewSeed, 0, len(value.Seeds))
	for _, seed := range value.Seeds {
		mapped := generated.ProviderRSSPreviewSeed{
			DownloadUrl:           seed.DownloadURL,
			InfoHash:              seed.InfoHash,
			PublishedAt:           copyTimePtrValue(seed.PublishedAt),
			SizeBytes:             seed.SizeBytes,
			StableSeedKey:         seed.StableSeedKey,
			Title:                 seed.Title,
			ExpectedPayloadDigest: seed.ExpectedPayloadDigest,
		}
		seeds = append(seeds, mapped)
	}
	result := generated.ProviderRSSPreviewResult{
		ContentHash:      stringPtrOrNil(value.ContentHash),
		FetchedAt:        timePtrOrNil(value.FetchedAt),
		Freshness:        generated.ProviderRSSPreviewResultFreshness(value.Freshness),
		GroupRef:         value.GroupRef,
		ObservedAt:       timePtrOrNil(value.ObservedAt),
		Provider:         generated.ProviderRSSPreviewResultProvider(value.Provider),
		ProviderEntryRef: value.ProviderEntryRef,
		Seeds:            seeds,
	}
	if value.SnapshotID != "" {
		id := generated.ID(value.SnapshotID)
		result.SnapshotId = &id
	}
	if value.SnapshotVersion > 0 {
		version := generated.Version(value.SnapshotVersion)
		result.SnapshotVersion = &version
	}
	return result
}

func timePtrOrNil(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

func copyTimePtrValue(value time.Time) *time.Time {
	return timePtrOrNil(value)
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
