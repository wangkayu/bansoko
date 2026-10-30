package application

import (
	"context"
	"errors"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/postprocessing/naming"
	rssapplication "bansoko/internal/rss/application"
)

// PreviewFeedSnapshotRef identifies an exact provider snapshot. It is the
// advisory feed identity the RSSListView read model accepts and echoes; the
// retired flat preview DTOs no longer exist.
type PreviewFeedSnapshotRef struct {
	Provider         animedomain.ProviderKind
	ProviderEntryRef string
	ProviderGroupRef string
	SnapshotID       string
	SnapshotVersion  int64
	ContentHash      string
}

func (r PreviewFeedSnapshotRef) Identity() animecatalogdomain.ProviderRSSPreviewIdentity {
	return animecatalogdomain.ProviderRSSPreviewIdentity{SnapshotID: r.SnapshotID, SnapshotVersion: r.SnapshotVersion, ContentHash: r.ContentHash}
}

func (s *Service) draftParserSelectionInput(_ context.Context, draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig) (acquisitiontarget.ParserSelectionInput, error) {
	// The same selection a saved revision of this draft freezes
	// (CandidateRevision.ParserSelectionInput): the snapshot's parser envelope,
	// never the current Global parser policy. A draft without its business
	// snapshot fails closed, as candidate save does.
	if draft.Business == nil {
		return acquisitiontarget.ParserSelectionInput{}, &apperrors.Business{Status: 422, Code: "BUSINESS_SNAPSHOT_INVALID", Message: "draft must carry a complete business snapshot"}
	}
	policy := draft.Business.Parser
	mediaKind, season, seasonBound := animedomain.FrozenMediaBinding(draft.Metadata.Tmdb, draft.Metadata.BackfilledMediaKind())
	return acquisitiontarget.ParserSelectionInput{
		Kind: policy.Kind, EpisodePatterns: effective.Rules.Parser.EpisodePatterns,
		MediaKind: mediaKind, Season: season, SeasonBound: seasonBound,
		PolicyVersion: policy.PolicyVersion, ProfileVersion: policy.ProfileVersion, RulesOwnerVersion: policy.RulesOwnerVersion,
	}, nil
}

type providerGroupPreview struct {
	Ref       PreviewFeedSnapshotRef
	GroupName string
	Snapshot  SeedPreviewSnapshot
	Items     []acquisitiondomain.FeedItem
	Manifests map[string]*animecatalogdomain.TorrentManifest
	// ItemProvider is the provider every feed item of this snapshot is rebuilt
	// with (feedItemFromPreviewSeed): it decides whether the AniBT tag row
	// applies.
	ItemProvider string
}

// resolveRSSListProviderGroupPreviews resolves the advisory feed identities the
// read model was handed. Identities that do not belong to this configuration's
// selected provider groups are returned as unused rather than refused: a read
// never fails because an advisory identity was stale, and a selected group with
// no identity is reported as snapshot_required instead of being fetched live.
func (s *Service) resolveRSSListProviderGroupPreviews(ctx context.Context, draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig, refs []PreviewFeedSnapshotRef) ([]providerGroupPreview, []PreviewFeedSnapshotRef, error) {
	if draft.OriginEntryRef == nil || strings.TrimSpace(*draft.OriginEntryRef) == "" {
		return nil, nil, feedSnapshotNotApplicableError("provider draft has no catalog entry reference")
	}
	ordered, selected := selectedProviderGroupEntries(effective.Selection, draft.Origin)
	if len(refs) > maxPreviewFeedSnapshots {
		return nil, nil, &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "at most 32 provider feed snapshot identities may be previewed at once"}
	}
	by := make(map[string]PreviewFeedSnapshotRef, len(refs))
	unused := make([]PreviewFeedSnapshotRef, 0)
	for _, ref := range refs {
		if err := validatePreviewFeedRefShape(ref); err != nil {
			return nil, nil, err
		}
		if string(ref.Provider) != string(draft.Origin) || ref.ProviderEntryRef != strings.TrimSpace(*draft.OriginEntryRef) {
			unused = append(unused, ref)
			continue
		}
		if _, ok := selected[ref.ProviderGroupRef]; !ok {
			unused = append(unused, ref)
			continue
		}
		if _, ok := by[ref.ProviderGroupRef]; ok {
			return nil, nil, &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "a provider group may appear at most once per preview request"}
		}
		by[ref.ProviderGroupRef] = ref
	}
	if len(by) > 0 && s.seedPreviews == nil {
		return nil, nil, &apperrors.Business{Status: 503, Code: "FEED_SNAPSHOT_UNAVAILABLE", Message: "Provider feed snapshots are not configured on this server."}
	}
	out := make([]providerGroupPreview, 0, len(by))
	for _, entry := range ordered {
		group := strings.TrimSpace(entry.GroupRef)
		ref, ok := by[group]
		if !ok {
			continue
		}
		snapshot, err := s.seedPreviews.FetchEntryGroupSnapshotByID(ctx, string(ref.Provider), ref.ProviderEntryRef, ref.ProviderGroupRef, ref.Identity())
		if err != nil {
			if errors.Is(err, ErrProviderFeedSnapshotRefreshRequired) {
				return nil, nil, feedSnapshotRefreshRequiredError(safeReason(err.Error()))
			}
			return nil, nil, err
		}
		name := strings.TrimSpace(entry.DisplayName)
		if name == "" {
			name = group
		}
		manifests, err := s.expandedSeedManifests(ctx, ref, snapshot)
		if err != nil {
			return nil, nil, err
		}
		items := expandedFeedItems(snapshot, manifests, string(ref.Provider))
		out = append(out, providerGroupPreview{Ref: ref, GroupName: name, Snapshot: snapshot, Items: items, Manifests: manifests, ItemProvider: string(ref.Provider)})
	}
	return out, unused, nil
}

func rssListVersions() RSSListViewVersions {
	return RSSListViewVersions{Parser: animedomain.ParserVersion, Evaluator: animedomain.EvaluatorVersion, Renderer: naming.Language, Safety: animedomain.SafetyVersion}
}

func feedItemFromPreviewSeed(seed SeedPreviewFact, provider string) acquisitiondomain.FeedItem {
	item := acquisitiondomain.FeedItem{GUID: seed.StableSeedKey, Title: seed.Title, Enclosure: seed.DownloadURL, Published: seed.PublishedAt, Provider: provider, TorrentFilename: seed.TorrentFilename,
		TorrentErrorCode: seed.TorrentErrorCode, TorrentErrorReason: seed.TorrentErrorReason}
	if seed.SizeBytes != nil {
		item.TorrentSizeBytes = *seed.SizeBytes
		item.EnclosureLength = *seed.SizeBytes
	}
	if seed.InfoHash != "" {
		item.TorrentInfoHash = seed.InfoHash
		if len(seed.InfoHash) == 64 {
			item.TorrentInfoHashV2 = seed.InfoHash
		} else {
			item.TorrentInfoHashV1 = seed.InfoHash
		}
	}
	// The rebuilt item freezes the release title's parsed tag set, exactly as a
	// fetched item does before its release persists one. A render reads the
	// value namespaces only from the item's frozen sets, so a seed whose title
	// is never parsed renders no rt.* fact at all — the shipped subtitle suffix
	// (sub.lang_tag, which falls back to rt.sub_lang for a subtitle whose
	// own name names no language) would then differ from the eventual
	// release, which renders with the language its own title names.
	//
	// The AniBT set is not derivable from the title at all: the provider carries
	// it as namespaced XML fields, so it is taken from the snapshot's frozen
	// block. Without it `anibt.*` renders empty and a tag whitelist fails a
	// release the subscribed path accepts from its source facts.
	item.AniBTTags = acquisitiondomain.AniBTTagSetFromStored(seed.AniBTTags)
	return rssapplication.FreezeTitleTags(item)
}

// expandedSeedManifests resolves the manifests one exact snapshot identity can
// already prove. The snapshot's own projections come first: a pre-lazy snapshot
// was sealed with them and they stay the authority for those seeds. The lazy
// expansion cache then fills in the seeds the snapshot does not cover and never
// overwrites a projection the snapshot already carries.
//
// "Has a manifest" is the single rule for "already expanded": a seed with
// neither is rendered pending, and a failed expansion leaves no trace here by
// design (the cache stores verified bytes only), so a retry simply renders
// pending again.
//
// The call reads only: no provider request and no write.
func (s *Service) expandedSeedManifests(ctx context.Context, ref PreviewFeedSnapshotRef, snapshot SeedPreviewSnapshot) (map[string]*animecatalogdomain.TorrentManifest, error) {
	manifests := make(map[string]*animecatalogdomain.TorrentManifest, len(snapshot.Seeds))
	for _, seed := range snapshot.Seeds {
		if seed.Manifest != nil {
			manifests[seed.StableSeedKey] = seed.Manifest
		}
	}
	if s.seedTorrentManifests == nil {
		return manifests, nil
	}
	cached, err := s.seedTorrentManifests.ReadSeedTorrentManifests(ctx, string(ref.Provider), ref.ProviderEntryRef, ref.ProviderGroupRef, ref.Identity())
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

// expandedFeedItems projects only the seeds that have a manifest. Evaluation,
// filtering and rendering all run per item, so an unexpanded seed is never
// parsed: there is no structure to judge it against, and reporting a verdict
// the server never computed would be a guess. Omitting it here is what makes the
// row render as pending instead of as an unavailable torrent.
func expandedFeedItems(snapshot SeedPreviewSnapshot, manifests map[string]*animecatalogdomain.TorrentManifest, provider string) []acquisitiondomain.FeedItem {
	items := make([]acquisitiondomain.FeedItem, 0, len(manifests))
	for _, seed := range snapshot.Seeds {
		if _, expanded := manifests[seed.StableSeedKey]; !expanded {
			continue
		}
		items = append(items, feedItemFromPreviewSeed(seed, provider))
	}
	return items
}

func validatePreviewFeedRefShape(ref PreviewFeedSnapshotRef) error {
	if ref.Provider != animedomain.ProviderKindAnibt && ref.Provider != animedomain.ProviderKindMikan && ref.Provider != animedomain.ProviderKindNyaa && ref.Provider != animedomain.ProviderKindJackett {
		return &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "feed snapshot provider must be anibt, mikan, nyaa or jackett"}
	}
	if strings.TrimSpace(ref.ProviderEntryRef) == "" || len(ref.ProviderEntryRef) > 256 {
		return &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "feed snapshot provider_entry_ref is required and bounded"}
	}
	if strings.TrimSpace(ref.ProviderGroupRef) == "" || len(ref.ProviderGroupRef) > 256 {
		return &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "feed snapshot provider_group_ref is required and bounded"}
	}
	if err := ref.Identity().ValidateIdentityShape(); err != nil {
		return &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: safeReason(err.Error())}
	}
	return nil
}

func feedSnapshotRefreshRequiredError(detail string) error {
	if strings.TrimSpace(detail) == "" {
		detail = "the requested provider feed snapshot is missing, stale or superseded"
	}
	return &apperrors.Business{Status: 409, Code: "FEED_SNAPSHOT_REFRESH_REQUIRED", Message: detail}
}

func feedSnapshotNotApplicableError(detail string) error {
	return &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_NOT_APPLICABLE", Message: detail}
}

const maxPreviewFeedSnapshots = 32
