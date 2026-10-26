package application

import (
	"context"
	"errors"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// The keyword-search projection of the one-shot browse. A search snapshot has
// no catalog entry and no subgroup layer, so it never goes through the provider
// projection's entry/group-selection binding: the browse names one exact search
// snapshot identity, and the projection renders that snapshot's whole result
// window as a single group. Everything after the projection — evaluation,
// filtering, rendering, the library overlap pass — is the shared list-view
// machinery, so a search seed renders exactly like a feed seed.

// resolveSearchSnapshotProjection binds the one exact search snapshot the
// browse named. The draft must be the search one-shot form the admission path
// already defines (origin=oneshot: no entry, no RSS source), the single ref
// must be a search identity of a supported provider, and the read is the exact
// persisted snapshot — a missing, superseded, stale or retention-expired
// identity fails closed as the typed refresh outcome instead of re-searching.
//
// The group's display name is the query_ref: the query text itself is only
// stored as a digest, and the caller that ran the search renders its own query
// label; the name is a stable placeholder the contract can carry, not prose.
func (s *Service) resolveSearchSnapshotProjection(
	ctx context.Context,
	draft animedomain.AnimeDraft,
	refs []PreviewFeedSnapshotRef,
	previewOffset int,
) ([]providerGroupPreview, []rssListGroupEntry, error) {
	if draft.Origin != animedomain.SubscriptionOriginOneshot {
		return nil, nil, &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "a search snapshot identity pairs only with the one-shot draft form"}
	}
	if len(refs) != 1 {
		return nil, nil, &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "a one-shot browse of a search snapshot names exactly one snapshot identity"}
	}
	ref := refs[0]
	if err := validatePreviewFeedRefShape(ref); err != nil {
		return nil, nil, err
	}
	if catalogapplication.SnapshotKindFromRefs(ref.ProviderEntryRef, ref.ProviderGroupRef) != animecatalogdomain.FeedSnapshotKindSearch {
		return nil, nil, &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "the named snapshot identity is not a keyword search snapshot"}
	}
	if s.seedPreviews == nil {
		return nil, nil, &apperrors.Business{Status: 503, Code: "FEED_SNAPSHOT_UNAVAILABLE", Message: "Keyword search snapshots are not configured on this server."}
	}
	searchSource, ok := s.seedPreviews.(animeapplication.SearchSeedPreviewSource)
	if !ok {
		return nil, nil, &apperrors.Business{Status: 503, Code: "FEED_SNAPSHOT_UNAVAILABLE", Message: "Keyword search snapshots are not served by this server."}
	}
	snapshot, err := searchSource.FetchSearchSnapshotByID(ctx, string(ref.Provider), ref.ProviderEntryRef, ref.ProviderGroupRef, ref.Identity())
	if err != nil {
		if errors.Is(err, ErrProviderFeedSnapshotRefreshRequired) {
			return nil, nil, feedSnapshotRefreshRequiredError(safeReason(err.Error()))
		}
		return nil, nil, err
	}
	manifests, err := s.searchSeedManifests(ctx, ref, snapshot)
	if err != nil {
		return nil, nil, err
	}
	items := expandedFeedItems(snapshot, manifests, string(ref.Provider))
	entry := rssListGroupEntry{
		provider: ref.Provider, providerEntryRef: ref.ProviderEntryRef, groupRef: ref.ProviderGroupRef,
		displayName: ref.ProviderGroupRef, selected: true,
		selectionMode: string(animedomain.SelectionModeAuto),
		offset:        oneShotOffset(previewOffset),
	}
	return []providerGroupPreview{{Ref: ref, GroupName: entry.displayName, Snapshot: snapshot, Items: items, Manifests: manifests}}, []rssListGroupEntry{entry}, nil
}

// searchSeedManifests mirrors expandedSeedManifests over a search snapshot: the
// snapshot's own projections come first, and the lazy expansion cache fills in
// the seeds it already proved, never overriding a snapshot projection. The read
// touches no network and writes nothing.
func (s *Service) searchSeedManifests(ctx context.Context, ref PreviewFeedSnapshotRef, snapshot SeedPreviewSnapshot) (map[string]*animecatalogdomain.TorrentManifest, error) {
	manifests := make(map[string]*animecatalogdomain.TorrentManifest, len(snapshot.Seeds))
	for _, seed := range snapshot.Seeds {
		if seed.Manifest != nil {
			manifests[seed.StableSeedKey] = seed.Manifest
		}
	}
	if s.seedTorrentManifests == nil {
		return manifests, nil
	}
	manifestSource, ok := s.seedTorrentManifests.(animeapplication.SearchSeedTorrentManifestSource)
	if !ok {
		return manifests, nil
	}
	cached, err := manifestSource.ReadSearchSeedTorrentManifests(ctx, string(ref.Provider), ref.ProviderEntryRef, ref.ProviderGroupRef, ref.Identity())
	if err != nil {
		if errors.Is(err, ErrProviderFeedSnapshotRefreshRequired) {
			return nil, feedSnapshotRefreshRequiredError(safeReason(err.Error()))
		}
		return nil, err
	}
	for key, manifest := range cached {
		if manifest == nil {
			continue
		}
		if _, exists := manifests[key]; exists {
			continue
		}
		manifests[key] = manifest
	}
	return manifests, nil
}
