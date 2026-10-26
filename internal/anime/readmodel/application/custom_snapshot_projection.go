package application

import (
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	"context"
	"strings"
)

func (s *Service) resolveCustomSnapshotProjection(ctx context.Context, configIdentity string, draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig, refs []PreviewFeedSnapshotRef) ([]providerGroupPreview, []rssListGroupEntry, error) {
	return s.resolveCustomSnapshotProjectionFor(ctx, configIdentity, draft, effective, refs, false, 0)
}

// resolveCustomSnapshotProjectionFor is resolveCustomSnapshotProjection with
// the one-shot browse switch. A one-shot names its seed directly: its draft is
// this task's own unsaved configuration, so no saved revision scope exists to
// derive. It binds the exact persisted custom snapshot the ref names (which
// must exist under that identity) and previews with the request's offset.
func (s *Service) resolveCustomSnapshotProjectionFor(ctx context.Context, configIdentity string, draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig, refs []PreviewFeedSnapshotRef, oneShot bool, previewOffset int) ([]providerGroupPreview, []rssListGroupEntry, error) {
	if len(draft.RssSources) != 1 {
		return nil, nil, &apperrors.Business{Status: 422, Code: "CUSTOM_SINGLE_SOURCE_REQUIRED", Message: "Custom preview requires exactly one frozen RSS source."}
	}
	source := draft.RssSources[0]
	entryID, groupID := catalogapplication.CustomSnapshotScope(configIdentity, source.Locator, source.GroupName)
	headers := s.customOriginGroups(draft)
	header := headers[0]
	entry := rssListGroupEntry{provider: animedomain.ProviderKindCustom, providerEntryRef: entryID, groupRef: groupID, displayName: source.GroupName, selected: true, selectionMode: string(animedomain.SelectionModeAuto), offset: header.Offset}
	if oneShot {
		entry.offset = oneShotOffset(previewOffset)
		if len(refs) == 1 {
			entryID, groupID = refs[0].ProviderEntryRef, refs[0].ProviderGroupRef
			entry.providerEntryRef, entry.groupRef = entryID, groupID
		}
	}
	if len(refs) == 0 {
		return nil, []rssListGroupEntry{entry}, nil
	}
	if len(refs) != 1 {
		return nil, nil, &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "Custom preview accepts exactly one snapshot identity."}
	}
	ref := refs[0]
	if string(ref.Provider) != "custom" || ref.ProviderEntryRef != entryID || ref.ProviderGroupRef != groupID || ref.Identity().ValidateIdentityShape() != nil {
		return nil, nil, &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "Snapshot does not belong to this frozen custom source."}
	}
	if s.seedPreviews == nil {
		return nil, nil, &apperrors.Business{Status: 503, Code: "FEED_SNAPSHOT_UNAVAILABLE", Message: "Custom snapshots are not configured."}
	}
	snapshot, err := s.seedPreviews.FetchEntryGroupSnapshotByID(ctx, "custom", entryID, groupID, ref.Identity())
	if err != nil {
		return nil, nil, err
	}
	manifests, err := s.expandedSeedManifests(ctx, ref, snapshot)
	if err != nil {
		return nil, nil, err
	}
	items := expandedFeedItems(snapshot, manifests, "custom")
	return []providerGroupPreview{{Ref: ref, GroupName: strings.TrimSpace(source.GroupName), Snapshot: snapshot, Items: items, Manifests: manifests}}, []rssListGroupEntry{entry}, nil
}
