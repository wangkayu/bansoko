package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/apperrors"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/catalog/projection"
	configurationdomain "bansoko/internal/configuration/domain"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/redact"
)

// GroupReader discovers provider groups for one catalog entry. The concrete
// implementation is provider/adapter.ConfiguredGroupReader, wired by the
// composition root.
type GroupReader interface {
	ReadGroups(context.Context, string) (providerdomain.GroupReadResult, error)
}

// ProviderFeedFetcher fetches the published RSS seeds for one provider entry
// and group. The concrete implementation is
// acquisition/application.ConfiguredProviderFeedFetcher.
type ProviderFeedFetcher interface {
	FetchProviderFeed(ctx context.Context, locator providerdomain.FeedLocator) ([]acquisitiondomain.FeedItem, error)
}

// ProviderConnectionSource provides the current user-configured provider
// connections so the service can resolve the provider endpoint for feed URL
// construction.
type ProviderConnectionSource interface {
	ListConnections(context.Context) ([]configurationdomain.Connection, error)
}

// FeedSnapshotStore is the catalog-owned persistence boundary for provider RSS
// preview evidence. It never exposes database rows or transaction handles.
type FeedSnapshotStore interface {
	ReadLatestProviderFeedSnapshot(context.Context, string, string, string) (catalogdomain.ProviderFeedSnapshot, error)
	ReadProviderFeedSnapshotByID(context.Context, string) (catalogdomain.ProviderFeedSnapshot, error)
	PersistProviderFeedSnapshot(context.Context, catalogdomain.ProviderFeedSnapshotWrite) (catalogdomain.ProviderFeedSnapshot, error)
}

// SetGroupReaders wires the subscription-free provider group discovery
// readers. It mirrors the composition-root setter pattern of other services
// so the catalog Service stays free of concrete adapter imports.
func (s *Service) SetGroupReaders(readers map[string]GroupReader) {
	if s == nil {
		return
	}
	copyReaders := make(map[string]GroupReader, len(readers))
	for provider, reader := range readers {
		if reader != nil {
			copyReaders[strings.ToLower(strings.TrimSpace(provider))] = reader
		}
	}
	s.groupReaders = copyReaders
}

// SetProviderFeedFetcher wires the subscription-free RSS seed preview fetcher.
func (s *Service) SetProviderFeedFetcher(fetcher ProviderFeedFetcher) {
	if s != nil {
		s.feedFetcher = fetcher
	}
}

// SetFeedSnapshotStore wires the catalog-owned feed snapshot persistence port.
func (s *Service) SetFeedSnapshotStore(store FeedSnapshotStore) {
	if s != nil {
		s.feedSnapshots = store
	}
}

// SetProviderConnectionSource wires the connection source used to resolve
// provider endpoints for feed URL construction in group discovery results.
func (s *Service) SetProviderConnectionSource(connections ProviderConnectionSource) {
	if s != nil {
		s.providerConnections = connections
	}
}

// DiscoverProviderEntryGroups runs subscription-free read-only group discovery
// for one provider catalog entry. It reads groups via the configured provider
// adapter, persists a subscription-independent snapshot, and returns the group
// list. Provider and configuration failures are typed transport errors. It
// never creates an RSS source, schedules a WorkItem, or mutates any
// subscription/owner row.
func (s *Service) DiscoverProviderEntryGroups(ctx context.Context, provider, catalogEntryRef string) (catalogdomain.ProviderGroupDiscoveryResult, error) {
	if err := validateCatalogProvider(provider); err != nil {
		return catalogdomain.ProviderGroupDiscoveryResult{}, &apperrors.Business{
			Status:  http.StatusUnprocessableEntity,
			Code:    "PROVIDER_KIND_INVALID",
			Message: "Provider must be anibt or mikan.",
		}
	}
	if err := validateProviderEntryRef(provider, catalogEntryRef); err != nil {
		return catalogdomain.ProviderGroupDiscoveryResult{}, err
	}
	reader, err := s.groupReader(provider)
	if err != nil {
		return catalogdomain.ProviderGroupDiscoveryResult{}, &apperrors.Business{
			Status:  http.StatusServiceUnavailable,
			Code:    "PROVIDER_GROUP_READER_NOT_CONFIGURED",
			Message: "Provider group discovery is not configured for this provider.",
		}
	}
	result, err := reader.ReadGroups(ctx, catalogEntryRef)
	if err != nil {
		return catalogdomain.ProviderGroupDiscoveryResult{}, &apperrors.Business{
			Status:  http.StatusServiceUnavailable,
			Code:    "PROVIDER_GROUP_DISCOVERY_FAILED",
			Message: safeProviderReason(err.Error()),
		}
	}
	snapshot, err := s.persistProviderGroupSnapshot(ctx, result)
	if err != nil {
		return catalogdomain.ProviderGroupDiscoveryResult{}, fmt.Errorf("persist provider group snapshot: %w", err)
	}
	endpoint, endpointErr := s.resolveProviderEndpoint(ctx, provider)
	if endpointErr != nil {
		// Endpoint resolution failure is non-fatal for discovery: the snapshot
		// is already persisted. Surface an empty feed_url per group.
		endpoint = ""
	}
	return mapProviderGroupDiscoveryResult(snapshot, endpoint), nil
}

// ReadProviderEntryRSS reads only the latest persisted feed snapshot. It never
// invokes the provider fetcher and never writes a row.
func (s *Service) ReadProviderEntryRSS(ctx context.Context, provider, catalogEntryRef, groupRef string) (catalogdomain.ProviderRSSPreviewResult, error) {
	if err := validateFeedSnapshotScope(provider, catalogEntryRef, groupRef); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	if s == nil || s.feedSnapshots == nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_SNAPSHOT_STORE_NOT_CONFIGURED", Message: "Provider RSS snapshots are not configured on this server."}
	}
	snapshot, err := s.feedSnapshots.ReadLatestProviderFeedSnapshot(ctx, provider, catalogEntryRef, groupRef)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogdomain.ProviderRSSPreviewResult{Provider: provider, ProviderEntryRef: catalogEntryRef, GroupRef: groupRef, Freshness: catalogdomain.FeedSnapshotMissing}, nil
	}
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, fmt.Errorf("read provider feed snapshot: %w", err)
	}
	return mapProviderFeedSnapshot(snapshot), nil
}

// ReadProviderEntryRSSByIdentity reads one exact persisted feed snapshot. It
// never invokes the provider fetcher and never falls back to the latest
// snapshot: a missing, superseded, mismatching or stale identity is returned as
// a domain sentinel so the caller can fail closed with refresh_required instead
// of silently rendering a different snapshot.
func (s *Service) ReadProviderEntryRSSByIdentity(ctx context.Context, provider, catalogEntryRef, groupRef string, identity catalogdomain.ProviderRSSPreviewIdentity) (catalogdomain.ProviderRSSPreviewResult, error) {
	if err := validateFeedSnapshotScope(provider, catalogEntryRef, groupRef); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	if err := identity.ValidateIdentityShape(); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: safeProviderReason(err.Error())}
	}
	if s == nil || s.feedSnapshots == nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_SNAPSHOT_STORE_NOT_CONFIGURED", Message: "Provider RSS snapshots are not configured on this server."}
	}
	snapshot, err := s.feedSnapshots.ReadProviderFeedSnapshotByID(ctx, identity.SnapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogdomain.ProviderRSSPreviewResult{}, fmt.Errorf("%w: snapshot %s is not retained", catalogdomain.ErrProviderFeedSnapshotNotFound, identity.SnapshotID)
	}
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, fmt.Errorf("read provider feed snapshot by id: %w", err)
	}
	if snapshot.Provider != provider || snapshot.ProviderEntryRef != catalogEntryRef || snapshot.ProviderGroupRef != groupRef {
		return catalogdomain.ProviderRSSPreviewResult{}, fmt.Errorf("%w: snapshot %s belongs to %s/%s/%s", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, snapshot.ID, snapshot.Provider, snapshot.ProviderEntryRef, snapshot.ProviderGroupRef)
	}
	// A feed read accepts a published provider feed or a custom feed and nothing
	// else: a search snapshot authorizes a keyword result, not a subscription
	// surface, even when a caller names it by an exact identity.
	if err := snapshot.VerifyKind(feedSnapshotKindForProvider(provider)); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	if err := snapshot.VerifyIdentity(identity); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	if snapshot.FreshnessAt(time.Now().UTC()) != catalogdomain.FeedSnapshotFresh {
		return catalogdomain.ProviderRSSPreviewResult{}, fmt.Errorf("%w: snapshot %s v%d is stale", catalogdomain.ErrProviderFeedSnapshotStale, snapshot.ID, snapshot.Version)
	}
	return mapProviderFeedSnapshot(snapshot), nil
}

// ReadProviderEntrySeedPayload reads the exact verified torrent bytes of one
// seed inside one persisted snapshot. For a pre-lazy snapshot the stored blob
// is returned as is; for a seed published after previews stopped prefetching it
// resolves the same locator through the expansion cache, fetching and verifying
// once when the seed was never expanded. It never falls back to the latest
// snapshot: the stored blob must still reproduce its declared payload digest and
// the snapshot identity must still match, so the bytes a caller receives are the
// same bytes the read model rendered. A legacy, stale, missing or mismatching
// identity fails closed with refresh_required instead of silently substituting
// different content.
func (s *Service) ReadProviderEntrySeedPayload(ctx context.Context, provider, catalogEntryRef, groupRef string, identity catalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (catalogdomain.ProviderFeedSnapshotSeed, error) {
	if err := validateFeedSnapshotScope(provider, catalogEntryRef, groupRef); err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, err
	}
	if err := identity.ValidateIdentityShape(); err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: safeProviderReason(err.Error())}
	}
	if strings.TrimSpace(stableSeedKey) == "" {
		return catalogdomain.ProviderFeedSnapshotSeed{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEED_IDENTITY_REQUIRED", Message: "The seed identity is required."}
	}
	if s == nil || s.feedSnapshots == nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_SNAPSHOT_STORE_NOT_CONFIGURED", Message: "Provider RSS snapshots are not configured on this server."}
	}
	snapshot, err := s.feedSnapshots.ReadProviderFeedSnapshotByID(ctx, identity.SnapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: snapshot %s is not retained", catalogdomain.ErrProviderFeedSnapshotNotFound, identity.SnapshotID)
	}
	if err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("read provider feed snapshot by id: %w", err)
	}
	if snapshot.Provider != provider || snapshot.ProviderEntryRef != catalogEntryRef || snapshot.ProviderGroupRef != groupRef {
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: snapshot %s belongs to %s/%s/%s", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, snapshot.ID, snapshot.Provider, snapshot.ProviderEntryRef, snapshot.ProviderGroupRef)
	}
	// The bytes a downloader receives come only from a feed snapshot of the kind
	// its provider owns. A search snapshot's bytes are reached exclusively through
	// the search expansion path, which checks the same kind itself.
	if err := snapshot.VerifyKind(feedSnapshotKindForProvider(provider)); err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, err
	}
	if err := snapshot.VerifyIdentity(identity); err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, err
	}
	if snapshot.FreshnessAt(time.Now().UTC()) != catalogdomain.FeedSnapshotFresh {
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: snapshot %s v%d is stale", catalogdomain.ErrProviderFeedSnapshotStale, snapshot.ID, snapshot.Version)
	}
	for _, seed := range snapshot.Seeds {
		if seed.StableSeedKey != stableSeedKey {
			continue
		}
		if seed.PayloadDigest == nil && len(seed.Payload) == 0 {
			// A snapshot published after previews stopped prefetching carries RSS
			// facts only, so its bytes are not here. Expansion resolves the same
			// locator: the cache serves them when this seed was expanded before, and
			// otherwise they are fetched once, verified against the seed's published
			// identity and cached. Either way the downloader receives exactly the
			// bytes the list renders, and a locator whose bytes cannot be verified
			// fails closed instead of being downloaded unchecked.
			return s.expandedSeedTorrentPayload(ctx, provider, catalogEntryRef, groupRef, identity, seed)
		}
		if len(seed.Payload) == 0 || seed.PayloadDigest == nil {
			return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s has no retained torrent payload", catalogdomain.ErrProviderFeedSnapshotNotFound, stableSeedKey)
		}
		// The exact read already recomputed the digest; recompute once more here
		// because this path hands the bytes to a downloader.
		if got := catalogdomain.DigestBytes(seed.Payload); got != *seed.PayloadDigest {
			return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s payload digest does not match its bytes", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, stableSeedKey)
		}
		if seed.Manifest != nil {
			if err := seed.Manifest.Validate(); err != nil {
				return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s manifest is not a valid projection: %v", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, stableSeedKey, err)
			}
		}
		return seed, nil
	}
	return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s is not part of snapshot %s", catalogdomain.ErrProviderFeedSnapshotNotFound, stableSeedKey, snapshot.ID)
}

// expandedSeedTorrentPayload serves one lazy snapshot seed through the same
// expansion the list read and the expand endpoint use, then returns the cached
// bytes rather than a second, independent fetch. The downloader therefore
// receives the exact payload expansion verified, and a seed that cannot be
// expanded (unreachable, corrupt, upstream 404 or an identity the snapshot
// never published) fails closed with no bytes to download at all.
func (s *Service) expandedSeedTorrentPayload(ctx context.Context, provider, catalogEntryRef, groupRef string, identity catalogdomain.ProviderRSSPreviewIdentity, seed catalogdomain.ProviderFeedSnapshotSeed) (catalogdomain.ProviderFeedSnapshotSeed, error) {
	fact, err := s.EnsureSeedTorrent(ctx, EnsureSeedTorrentInput{
		Provider: provider, ProviderEntryRef: catalogEntryRef, ProviderGroupRef: groupRef,
		SnapshotID: identity.SnapshotID, SnapshotVersion: identity.SnapshotVersion, ContentHash: identity.ContentHash,
		StableSeedKey: seed.StableSeedKey,
	})
	if err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, err
	}
	if fact.State != SeedTorrentStateAvailable && fact.ErrorCode == SeedTorrentErrorIdentityMismatch {
		// The locator's torrent is not the seed the feed named: a typed refusal,
		// never a "refresh and retry" that would fetch the same wrong bytes.
		return catalogdomain.ProviderFeedSnapshotSeed{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEED_IDENTITY_MISMATCH", Message: "The torrent at the seed's locator does not match the infohash its feed published."}
	}
	if fact.State != SeedTorrentStateAvailable {
		// Expansion already classified why and wrote nothing, so the caller gets
		// the same refresh-required outcome a seed absent from the snapshot gets:
		// admission must not download bytes the server could not verify.
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s could not be expanded (%s)", catalogdomain.ErrProviderFeedSnapshotNotFound, seed.StableSeedKey, fact.ErrorCode)
	}
	// The locator the snapshot persisted is the one expansion just resolved, so
	// the cache read below cannot silently look up a different seed.
	locator := strings.TrimSpace(seed.Locator)
	entry, found, err := s.seedTorrentCache.ReadSeedTorrentCache(ctx, locator)
	if err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, err
	}
	if !found || len(entry.Payload) == 0 {
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s has no retained torrent payload", catalogdomain.ErrProviderFeedSnapshotNotFound, seed.StableSeedKey)
	}
	// The cached digest covers exactly these bytes (the cache validates it on
	// read), so it is the digest the returned payload declares.
	digest := entry.PayloadDigest
	return catalogdomain.ProviderFeedSnapshotSeed{
		StableSeedKey: seed.StableSeedKey, Locator: locator, LocatorDisplay: seed.LocatorDisplay,
		Title: seed.Title, PublishedAt: seed.PublishedAt, SizeBytes: seed.SizeBytes,
		InfoHash: seed.InfoHash, TorrentFilename: seed.TorrentFilename,
		TorrentState:  catalogdomain.TorrentAcquisitionAvailable,
		Payload:       entry.Payload,
		PayloadDigest: &digest,
		Manifest:      fact.Manifest,
		// Expansion replaces the bytes and the projection, not the feed facts:
		// the tag set is provider XML the snapshot froze, and a caller of this
		// read must see the same seed facts whichever branch served it.
		AniBTTags: seed.AniBTTags,
	}, nil
}

// RefreshProviderEntryRSS explicitly fetches the provider once and atomically
// persists the resulting snapshot, seed facts, and bounded torrent blobs.
func (s *Service) RefreshProviderEntryRSS(ctx context.Context, provider, catalogEntryRef, groupRef string) (catalogdomain.ProviderRSSPreviewResult, error) {
	if err := validateFeedSnapshotScope(provider, catalogEntryRef, groupRef); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	if s == nil || s.feedFetcher == nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_FETCHER_NOT_CONFIGURED", Message: "Provider RSS preview is not configured on this server."}
	}
	if s.feedSnapshots == nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_SNAPSHOT_STORE_NOT_CONFIGURED", Message: "Provider RSS snapshots are not configured on this server."}
	}
	normalizedGroup, err := providerdomain.NewGroupRef(providerdomain.Kind(provider), groupRef)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_GROUP_REF_INVALID", Message: "The provider group reference is invalid."}
	}
	locator := providerdomain.FeedLocator{Provider: providerdomain.Kind(provider), CatalogEntryRef: catalogEntryRef, Group: normalizedGroup}
	if err := locator.Validate(); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_FEED_LOCATOR_INVALID", Message: "The provider feed locator is invalid."}
	}
	items, err := s.feedFetcher.FetchProviderFeed(ctx, locator)
	if err != nil {
		if business := feedFailureBusinessError(err); business != nil {
			return catalogdomain.ProviderRSSPreviewResult{}, business
		}
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_FETCH_FAILED", Message: safeProviderReason(err.Error())}
	}
	fetchedAt := time.Now().UTC()
	seeds, err := mapFeedItemsToSnapshotSeeds(items)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_FEED_SNAPSHOT_INVALID", Message: safeProviderReason(err.Error())}
	}
	contentHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_FEED_SNAPSHOT_INVALID", Message: safeProviderReason(err.Error())}
	}
	snapshot, err := s.feedSnapshots.PersistProviderFeedSnapshot(ctx, catalogdomain.ProviderFeedSnapshotWrite{
		Provider: provider, ProviderEntryRef: catalogEntryRef, ProviderGroupRef: groupRef,
		FetchedAt: fetchedAt, FreshUntil: fetchedAt.Add(s.previewFreshWindow(ctx)), ContentHash: contentHash,
		HashSchema:       catalogdomain.ProviderFeedSnapshotContentHashSchema,
		ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema,
		Seeds:            seeds,
	})
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, fmt.Errorf("persist provider feed snapshot: %w", err)
	}
	return mapProviderFeedSnapshot(snapshot), nil
}

// PreviewProviderEntryRSS remains a compatibility name for explicit refresh
// callers. Reads use ReadProviderEntryRSS directly.
func (s *Service) PreviewProviderEntryRSS(ctx context.Context, provider, catalogEntryRef, groupRef string) (catalogdomain.ProviderRSSPreviewResult, error) {
	return s.RefreshProviderEntryRSS(ctx, provider, catalogEntryRef, groupRef)
}

// feedSnapshotKindForProvider is the kind a feed-shaped read requires for one
// provider: the frozen custom feed for a custom source, the published provider
// feed for a discovery provider. A search snapshot never matches it.
func feedSnapshotKindForProvider(provider string) catalogdomain.FeedSnapshotKind {
	if provider == "custom" {
		return catalogdomain.FeedSnapshotKindCustom
	}
	return catalogdomain.FeedSnapshotKindProviderFeed
}

// validateFeedSnapshotScope accepts the two feed-shaped scopes and refuses a
// search scope outright: search snapshots are addressed by derived hashed refs,
// never by a provider entry/group, so a request cannot smuggle one in here.
func validateFeedSnapshotScope(provider, entryRef, groupRef string) error {
	if strings.HasPrefix(entryRef, catalogdomain.SearchSnapshotScopePrefix) || strings.HasPrefix(groupRef, catalogdomain.SearchSnapshotQueryPrefix) {
		return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEARCH_SNAPSHOT_SCOPE_INVALID", Message: "A keyword search snapshot is not a provider feed scope."}
	}
	if provider == "custom" {
		if len(entryRef) != 78 || len(groupRef) != 77 || !strings.HasPrefix(entryRef, "custom-source:") || !strings.HasPrefix(groupRef, "custom-group:") {
			return &apperrors.Business{Status: 422, Code: "CUSTOM_SNAPSHOT_SCOPE_INVALID", Message: "Exact custom snapshot scope is required."}
		}
		for _, ref := range []string{entryRef[14:], groupRef[13:]} {
			for _, c := range ref {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					return &apperrors.Business{Status: 422, Code: "CUSTOM_SNAPSHOT_SCOPE_INVALID", Message: "Exact custom snapshot scope is required."}
				}
			}
		}
		return nil
	}
	if err := validateCatalogProvider(provider); err != nil {
		return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_KIND_INVALID", Message: "Provider must be anibt or mikan."}
	}
	if err := validateProviderEntryRef(provider, entryRef); err != nil {
		return err
	}
	if _, err := providerdomain.NewGroupRef(providerdomain.Kind(provider), groupRef); err != nil {
		return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_GROUP_REF_INVALID", Message: "The provider group reference is invalid."}
	}
	return nil
}

func mapFeedItemsToSnapshotSeeds(items []acquisitiondomain.FeedItem) ([]catalogdomain.ProviderFeedSnapshotSeed, error) {
	seeds := make([]catalogdomain.ProviderFeedSnapshotSeed, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		locator := strings.TrimSpace(item.DownloadURL())
		if locator == "" {
			return nil, fmt.Errorf("feed item %d has no download URL", index)
		}
		// The published identity names the seed whenever it can be stored as a
		// key. A missing identity, and one longer than the key bound, falls back
		// to the item's own facts: Mikan publishes the release title as its
		// guid, and a long CJK title passes 256 bytes, so without the fallback
		// one such row would make the whole page unusable.
		key := strings.TrimSpace(item.GUID)
		if key == "" || len(key) > catalogdomain.MaxProviderFeedStableKeyLength {
			key = feedItemDigestKey(item, locator)
		}
		if _, exists := seen[key]; exists {
			key = fmt.Sprintf("%s#%d", key, index)
			// A repeated identity at the very top of the bound leaves it again
			// once the disambiguating suffix is appended.
			if len(key) > catalogdomain.MaxProviderFeedStableKeyLength {
				key = catalogdomain.DigestBytes([]byte(key))
			}
		}
		seen[key] = struct{}{}
		seed := catalogdomain.ProviderFeedSnapshotSeed{
			StableSeedKey: key, Locator: locator, LocatorDisplay: safeFeedLocatorDisplay(locator),
			Title: strings.TrimSpace(item.Title),
			// Acquisition state is stated explicitly so one unusable torrent
			// stays visible in its group instead of vanishing from the read model.
			TorrentState: catalogdomain.TorrentAcquisitionNotApplicable,
		}
		if seed.Title == "" {
			return nil, fmt.Errorf("feed item %d has no title", index)
		}
		if !item.Published.IsZero() {
			published := item.Published.UTC()
			seed.PublishedAt = &published
		}
		if item.TorrentSizeBytes > 0 {
			size := item.TorrentSizeBytes
			seed.SizeBytes = &size
		}
		if hash := snapshotSeedInfoHash(item); hash != "" {
			seed.InfoHash = &hash
		}
		if filename := strings.TrimSpace(item.TorrentFilename); filename != "" {
			value := filename
			seed.TorrentFilename = &value
		}
		// The item's tag set is transient parse output (`Extensions` are gone
		// once the item is mapped), so it is frozen into the seed here. A nil set
		// means the provider has no tag block; an AniBT item is always non-nil.
		seed.AniBTTags = acquisitiondomain.StoredAniBTTagsFromSet(item.AniBTTags)
		seed.SourceSite = item.SourceSite
		if len(item.TorrentPayload) > 0 {
			payloadDigest := catalogdomain.DigestBytes(item.TorrentPayload)
			seed.Payload = append([]byte(nil), item.TorrentPayload...)
			seed.PayloadDigest = &payloadDigest
			if seed.SizeBytes == nil {
				size := int64(len(item.TorrentPayload))
				seed.SizeBytes = &size
			}
		}
		switch acquisitionStateFor(item) {
		case catalogdomain.TorrentAcquisitionUnavailable:
			seed.TorrentState = catalogdomain.TorrentAcquisitionUnavailable
			code := strings.TrimSpace(item.TorrentErrorCode)
			if code == "" {
				code = "torrent_unavailable"
			}
			seed.TorrentErrorCode = &code
			if reason := strings.TrimSpace(item.TorrentErrorReason); reason != "" {
				bounded := safeProviderReason(reason)
				seed.TorrentErrorReason = &bounded
			}
		case catalogdomain.TorrentAcquisitionAvailable:
			manifest, err := torrentManifestForSeed(item, *seed.PayloadDigest)
			if err != nil {
				return nil, fmt.Errorf("feed item %d: %w", index, err)
			}
			seed.TorrentState = catalogdomain.TorrentAcquisitionAvailable
			seed.Manifest = manifest
		}
		if err := seed.Validate(); err != nil {
			return nil, fmt.Errorf("feed item %d: %w", index, err)
		}
		seeds = append(seeds, seed)
	}
	return seeds, nil
}

// feedItemDigestKey is the content-derived name of one item: the same locator,
// title and published time always produce the same key, so an item without a
// storable published identity still keeps a stable name inside its snapshot and
// across snapshots of the same answer.
func feedItemDigestKey(item acquisitiondomain.FeedItem, locator string) string {
	return catalogdomain.DigestBytes([]byte(locator + "\x00" + item.Title + "\x00" + item.Published.UTC().Format(time.RFC3339Nano)))
}

// acquisitionStateFor derives one item's torrent state from the facts the
// shared enricher already produced: a bounded error means unavailable, a parsed
// file tree means available, and an item that was never opted in (or produced
// no torrent metadata at all) stays not_applicable.
func acquisitionStateFor(item acquisitiondomain.FeedItem) string {
	if strings.TrimSpace(item.TorrentErrorCode) != "" {
		return catalogdomain.TorrentAcquisitionUnavailable
	}
	if len(item.ReleaseFiles) > 0 && len(item.TorrentPayload) > 0 {
		return catalogdomain.TorrentAcquisitionAvailable
	}
	return catalogdomain.TorrentAcquisitionNotApplicable
}

// torrentManifestForSeed projects one parsed torrent into the immutable
// manifest the read model renders. Every file comes from the parser's own file
// tree, and each file_ref is bound to the payload digest, so a stored manifest
// can always be proven to belong to the stored bytes.
func torrentManifestForSeed(item acquisitiondomain.FeedItem, payloadDigest string) (*catalogdomain.TorrentManifest, error) {
	return projection.ManifestForItem(item, payloadDigest)
}

// safeFeedLocatorDisplay is the seed's display location: the URL without
// userinfo, query or fragment, and with every path credential (a UNIT3D
// /torrents/download/<id>.<key>) replaced by the shared URL redactor. It is
// applied when the seed is stored and again when it is read, so a row stored
// before path redaction existed never shows its key.
func safeFeedLocatorDisplay(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "opaque locator"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return redact.URL(parsed.String())
}

// storedSeedLocatorDisplay re-applies the display rule to a stored value. A
// stored display is always an absolute URL; anything else is passed through
// the free-text URL redactor unchanged in shape.
func storedSeedLocatorDisplay(display string) string {
	if strings.Contains(display, "://") {
		return safeFeedLocatorDisplay(display)
	}
	return redact.URLsInText(display)
}

func mapProviderFeedSnapshot(snapshot catalogdomain.ProviderFeedSnapshot) catalogdomain.ProviderRSSPreviewResult {
	seeds := make([]catalogdomain.ProviderRSSPreviewSeed, 0, len(snapshot.Seeds))
	for _, seed := range snapshot.Seeds {
		mapped := catalogdomain.ProviderRSSPreviewSeed{DownloadURL: seed.Locator, LocatorDisplay: storedSeedLocatorDisplay(seed.LocatorDisplay), StableSeedKey: seed.StableSeedKey, Title: seed.Title}
		if seed.PublishedAt != nil {
			mapped.PublishedAt = *seed.PublishedAt
		}
		if seed.SizeBytes != nil {
			value := *seed.SizeBytes
			mapped.SizeBytes = &value
		}
		if seed.InfoHash != nil {
			value := *seed.InfoHash
			mapped.InfoHash = &value
		}
		if seed.PayloadDigest != nil {
			value := *seed.PayloadDigest
			mapped.ExpectedPayloadDigest = &value
		}
		if seed.TorrentFilename != nil {
			mapped.TorrentFilename = *seed.TorrentFilename
		}
		state := seed.TorrentState
		if state == "" {
			state = catalogdomain.TorrentAcquisitionNotApplicable
		}
		mapped.TorrentState = state
		mapped.TorrentErrorCode = seed.TorrentErrorCode
		if seed.TorrentErrorReason != nil {
			// A reason stored before URL redaction existed is redacted on read.
			reason := safeProviderReason(*seed.TorrentErrorReason)
			mapped.TorrentErrorReason = &reason
		}
		mapped.Manifest = seed.Manifest
		mapped.AniBTTags = seed.AniBTTags
		mapped.SourceSite = seed.SourceSite
		seeds = append(seeds, mapped)
	}
	return catalogdomain.ProviderRSSPreviewResult{
		ContentHash: snapshot.ContentHash, FetchedAt: snapshot.FetchedAt, Freshness: snapshot.FreshnessAt(time.Now().UTC()),
		GroupRef: snapshot.ProviderGroupRef, ObservedAt: snapshot.FetchedAt, Provider: snapshot.Provider,
		ProviderEntryRef: snapshot.ProviderEntryRef, SnapshotID: snapshot.ID, SnapshotVersion: snapshot.Version, Seeds: seeds,
	}
}

func (s *Service) groupReader(provider string) (GroupReader, error) {
	if s == nil || s.groupReaders == nil {
		return nil, fmt.Errorf("provider group readers are not configured")
	}
	reader, ok := s.groupReaders[strings.ToLower(strings.TrimSpace(provider))]
	if !ok || reader == nil {
		return nil, fmt.Errorf("provider group reader for %q is not configured", provider)
	}
	return reader, nil
}

// resolveProviderEndpoint returns the current configured provider endpoint,
// falling back to the default public endpoint when no connection is saved.
func (s *Service) resolveProviderEndpoint(ctx context.Context, provider string) (string, error) {
	if s.providerConnections == nil {
		return "", errors.New("provider connection source is not configured")
	}
	connections, err := s.providerConnections.ListConnections(ctx)
	if err != nil {
		return "", fmt.Errorf("list provider connections: %w", err)
	}
	selected, err := configurationdomain.SelectSingleEnabledConnection(connections, configurationdomain.ConnectionKind(provider))
	if err != nil {
		if !errors.Is(err, configurationdomain.ErrNoEnabledConnection) {
			return "", err
		}
		fallback, ok := configurationdomain.DefaultConnectionForKind(configurationdomain.ConnectionKind(provider))
		if !ok {
			return "", err
		}
		return fallback.Endpoint, nil
	}
	return selected.Endpoint, nil
}

// persistProviderGroupSnapshot commits one catalog-owned whole observation.
func (s *Service) persistProviderGroupSnapshot(ctx context.Context, result providerdomain.GroupReadResult) (providerdomain.GroupSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return providerdomain.GroupSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := PersistGroupObservationTx(ctx, tx, result)
	if err != nil {
		return snapshot, err
	}
	if err := tx.Commit(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// mapProviderGroupDiscoveryResult converts the persisted snapshot into the
// generated contract result, resolving each group's feed URL through the
// provider endpoint when available.
func mapProviderGroupDiscoveryResult(snapshot providerdomain.GroupSnapshot, endpoint string) catalogdomain.ProviderGroupDiscoveryResult {
	groups := make([]catalogdomain.ProviderGroupDiscoveryGroup, 0, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		feedURL := ""
		if endpoint != "" {
			if url, err := providerdomain.BuildProviderRSSURL(snapshot.Provider, endpoint, snapshot.CatalogEntryRef, group.Ref.ExternalID); err == nil {
				feedURL = url
			}
		}
		groups = append(groups, catalogdomain.ProviderGroupDiscoveryGroup{
			Provider:    string(group.Ref.Provider),
			GroupRef:    group.Ref.ExternalID,
			DisplayName: group.DisplayName,
			FeedURL:     feedURL,
			ObservedAt:  group.ObservedAt,
		})
	}
	return catalogdomain.ProviderGroupDiscoveryResult{
		Provider:         string(snapshot.Provider),
		ProviderEntryRef: snapshot.CatalogEntryRef,
		ObservedAt:       snapshot.ObservedAt,
		SourceVersion:    snapshot.SourceVersion,
		SchemaVersion:    snapshot.SchemaVersion,
		SnapshotVersion:  snapshot.Version,
		Groups:           groups,
	}
}

func validateProviderEntryRef(provider, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return &apperrors.Business{
			Status:  http.StatusUnprocessableEntity,
			Code:    "PROVIDER_ENTRY_REF_INVALID",
			Message: "Provider entry reference is required.",
		}
	}
	// The domain validates positive-integer normalization for both providers.
	// Reuse BuildProviderRSSURL's validation by constructing a throwaway URL;
	// it returns an error for non-numeric or malformed refs.
	if _, err := providerdomain.BuildProviderRSSURL(providerdomain.Kind(provider), "https://example.test", ref, "1"); err != nil {
		return &apperrors.Business{
			Status:  http.StatusUnprocessableEntity,
			Code:    "PROVIDER_ENTRY_REF_INVALID",
			Message: "Provider entry reference must be a normalized positive integer.",
		}
	}
	return nil
}

// safeProviderReason truncates a raw error message to a bounded length so it
// can be surfaced safely in a typed outcome or error message.
// feedFailureBusinessError names a feed that was read but yields nothing
// usable: every item was skipped, or a listed site's format changed. Both are
// properties of the feed, not a transient fetch failure, so they are 422.
func feedFailureBusinessError(err error) *apperrors.Business {
	var failure *acquisitiondomain.FeedFailure
	if !errors.As(err, &failure) {
		return nil
	}
	code := "FEED_NO_USABLE_ITEMS"
	if failure.Code == acquisitiondomain.FeedFailureSiteFormatChanged {
		code = "FEED_SITE_FORMAT_CHANGED"
	}
	return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: code, Message: safeProviderReason(failure.Error())}
}

// safeProviderReason bounds an upstream reason for a response or a stored
// seed. A feed or torrent URL inside it (a fetch error names its request URL)
// is redacted first, so the bound never cuts a URL open around its key.
func safeProviderReason(raw string) string {
	const maxReason = 512
	message := redact.URLsInText(raw)
	if len(message) > maxReason {
		message = strings.ToValidUTF8(message[:maxReason], "")
	}
	return message
}

// snapshotSeedInfoHash picks the strongest content identity one RSS item
// carries, in the order the snapshot prefers: the parser's v2 then v1 hash,
// then the feed's own infohash field, which is the only identity a Mikan item
// has once preview no longer prefetches torrents.
//
// The feed field is admitted only in canonical form. AniBT publishes an opaque
// anibt:infohash there (not a hex infohash), and the snapshot's info_hash
// column CHECK accepts only 40 or 64 lowercase hex characters, so passing it
// through would fail the whole snapshot refresh.
func snapshotSeedInfoHash(item acquisitiondomain.FeedItem) string {
	for _, candidate := range []string{item.TorrentInfoHashV2, item.TorrentInfoHashV1} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return strings.ToLower(trimmed)
		}
	}
	feedHash := strings.ToLower(strings.TrimSpace(item.TorrentInfoHash))
	if !acquisitiondomain.IsCanonicalInfoHash(feedHash) {
		return ""
	}
	return feedHash
}
