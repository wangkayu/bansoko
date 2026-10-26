package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	"bansoko/internal/apperrors"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/catalog/projection"
)

// Seed torrent expansion outcomes. The read model renders these as a state, so
// an unreachable or unusable upstream torrent is an expected result rather than
// a transport error; only a wrong snapshot identity stays a typed error.
const (
	SeedTorrentStateAvailable   = "available"
	SeedTorrentStateUnavailable = "unavailable"

	SeedTorrentErrorFetchFailed      = "torrent_fetch_failed"
	SeedTorrentErrorParseFailed      = "torrent_parse_failed"
	SeedTorrentErrorMissing          = "torrent_missing"
	SeedTorrentErrorIdentityMismatch = "torrent_identity_mismatch"
)

// SeedTorrentCacheStore is the catalog-owned persistence boundary for the lazy
// expansion cache. It is keyed by locator and holds content facts only; the
// concrete implementation is catalog/adapter/persistence.Repository.
type SeedTorrentCacheStore interface {
	ReadSeedTorrentCache(ctx context.Context, locator string) (catalogdomain.SeedTorrentCache, bool, error)
	// ReadSeedTorrentCaches is the batch form the read model uses to resolve a
	// whole group's manifests in one query; an empty locator list must not
	// reach the database at all.
	ReadSeedTorrentCaches(ctx context.Context, locators []string) (map[string]catalogdomain.SeedTorrentCache, error)
	UpsertSeedTorrentCache(ctx context.Context, entry catalogdomain.SeedTorrentCache) error
}

// SeedTorrentFetcher fetches one locator's exact bytes and parsed facts. The
// concrete implementation is
// acquisition/application.HTTPTorrentMetadataReader, which owns the SSRF-safe
// transport, the redirect limit and the size limit, so a locator can never
// reach an internal host through this port.
type SeedTorrentFetcher interface {
	FetchPayload(ctx context.Context, locator string) (acquisitionapplication.TorrentPayload, error)
}

// EnsureSeedTorrentInput names one seed inside one exact persisted snapshot.
// The locator is resolved server-side from that snapshot, so a caller never
// supplies a fetch target.
type EnsureSeedTorrentInput struct {
	Provider string
	// SnapshotKind names the kind of snapshot the identity belongs to. An empty
	// value is the ordinary provider feed; a keyword search names "search" and
	// carries its two derived refs in ProviderEntryRef/ProviderGroupRef.
	SnapshotKind     catalogdomain.FeedSnapshotKind
	ProviderEntryRef string
	ProviderGroupRef string
	SnapshotID       string
	SnapshotVersion  int64
	ContentHash      string
	StableSeedKey    string
}

// SeedTorrentFact is one seed's expansion outcome. Manifest is present exactly
// when State is available; otherwise the bounded ErrorCode/ErrorReason explain
// why the seed cannot be expanded. Cached reports that the bytes were already
// persisted, so no provider request was made.
type SeedTorrentFact struct {
	Manifest      *catalogdomain.TorrentManifest
	PayloadDigest string
	State         string
	ErrorCode     string
	ErrorReason   string
	Cached        bool
}

// SetSeedTorrentCache wires the long-lived locator cache.
func (s *Service) SetSeedTorrentCache(store SeedTorrentCacheStore) {
	if s != nil {
		s.seedTorrentCache = store
	}
}

// SetSeedTorrentFetcher wires the on-demand torrent fetcher. It is deliberately
// separate from ProviderFeedFetcher: the preview fetch must never touch a
// torrent, and only expansion may.
func (s *Service) SetSeedTorrentFetcher(fetcher SeedTorrentFetcher) {
	if s != nil {
		s.seedTorrentFetcher = fetcher
	}
}

// EnsureSeedTorrent expands one seed of one exact snapshot on demand: it
// verifies the snapshot identity (the exact-identity read fails closed on a
// stale, superseded or mismatching snapshot), resolves the seed's locator, and
// returns the parsed file manifest, fetching and caching the bytes only on a
// cache miss. Concurrent expansions of the same locator share one fetch, which
// keeps a burst of clicks inside the provider's rate limit.
func (s *Service) EnsureSeedTorrent(ctx context.Context, input EnsureSeedTorrentInput) (SeedTorrentFact, error) {
	// An unnamed kind is derived from the two refs, never assumed: a caller that
	// omits it cannot choose which kind its identity is read as, and a caller
	// that names one still has to match the persisted row (VerifyKind).
	if input.SnapshotKind == "" {
		input.SnapshotKind = SnapshotKindFromRefs(input.ProviderEntryRef, input.ProviderGroupRef)
	}
	// The kind, not the provider, decides which scope shape is legal: a feed
	// expansion carries a provider entry/group scope through the feed validator,
	// while a search expansion carries the two derived hashed refs. Both then go
	// through exactly the same exact-identity read and identity checks.
	if err := validateExpansionScope(input); err != nil {
		return SeedTorrentFact{}, err
	}
	if err := validateFeedSnapshotScope(input.Provider, input.ProviderEntryRef, input.ProviderGroupRef); err != nil && input.SnapshotKind != catalogdomain.FeedSnapshotKindSearch {
		return SeedTorrentFact{}, err
	}
	if strings.TrimSpace(input.StableSeedKey) == "" {
		return SeedTorrentFact{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEED_IDENTITY_REQUIRED", Message: "The seed identity is required."}
	}
	preview, err := s.readSnapshotIdentityForKind(ctx, input.Provider, input.ProviderEntryRef, input.ProviderGroupRef, input.SnapshotKind, catalogdomain.ProviderRSSPreviewIdentity{
		SnapshotID: input.SnapshotID, SnapshotVersion: input.SnapshotVersion, ContentHash: input.ContentHash,
	})
	if err != nil {
		return SeedTorrentFact{}, feedSnapshotRefreshRequired(err)
	}
	seed, ok := ensureSeedTorrentSeed(preview.Seeds, input.StableSeedKey)
	if !ok {
		// A seed that is not in the named snapshot is not a distinct failure
		// class from a stale snapshot: the caller must refresh and choose again.
		return SeedTorrentFact{}, feedSnapshotRefreshRequired(fmt.Errorf("%w: seed %s is not in snapshot %s", catalogdomain.ErrProviderFeedSnapshotStale, input.StableSeedKey, preview.SnapshotID))
	}
	// A snapshot that still carries its own manifest (the pre-lazy shape) was
	// already reviewed and sealed with its bytes; re-fetching its locator would
	// risk substituting different content, so it is served directly.
	if fact, ok := persistedSeedTorrentFact(seed); ok {
		return fact, nil
	}
	if s == nil || s.seedTorrentCache == nil {
		return SeedTorrentFact{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "SEED_TORRENT_CACHE_NOT_CONFIGURED", Message: "Seed torrent caching is not configured on this server."}
	}
	if s.seedTorrentFetcher == nil {
		return SeedTorrentFact{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "SEED_TORRENT_FETCHER_NOT_CONFIGURED", Message: "Seed torrent fetching is not configured on this server."}
	}
	locator := strings.TrimSpace(seed.DownloadURL)
	if locator == "" {
		return SeedTorrentFact{}, fmt.Errorf("%w: seed %s has no locator", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, input.StableSeedKey)
	}
	if cached, hit, err := s.seedTorrentCache.ReadSeedTorrentCache(ctx, locator); err != nil {
		return SeedTorrentFact{}, err
	} else if hit {
		return cachedSeedTorrentFact(seed, cached), nil
	}
	// One fetch per locator even under concurrent expansion. The shared call
	// runs under the first caller's context, so a cancelled first request also
	// cancels the shared fetch; that keeps the failure honest for every waiter
	// instead of leaving a fetch nobody observes.
	value, err, _ := s.seedFlights.Do(locator, func() (any, error) {
		return s.fetchAndCacheSeedTorrent(ctx, seed)
	})
	if err != nil {
		return SeedTorrentFact{}, err
	}
	fact, ok := value.(SeedTorrentFact)
	if !ok {
		return SeedTorrentFact{}, errors.New("seed torrent expansion produced an unexpected result type")
	}
	return fact, nil
}

// ReadSeedTorrentManifests returns the manifests the lazy expansion cache can
// already prove for one exact persisted snapshot, keyed by stable seed key. It
// is the read-model half of expansion: it verifies the snapshot identity
// exactly like the expansion itself, then answers from the cache alone. It
// never invokes the provider fetcher and never writes, so a list read stays
// side-effect free while still rendering every seed an earlier expansion
// already fetched and verified.
//
// A seed whose snapshot still carries its own manifest is left out of the
// result: that projection was sealed with the snapshot's bytes and is the
// caller's first choice, and the cache must never override it.
func (s *Service) ReadSeedTorrentManifests(ctx context.Context, provider, catalogEntryRef, groupRef string, identity catalogdomain.ProviderRSSPreviewIdentity) (map[string]*catalogdomain.TorrentManifest, error) {
	preview, err := s.ReadProviderEntryRSSByIdentity(ctx, provider, catalogEntryRef, groupRef, identity)
	if err != nil {
		return nil, feedSnapshotRefreshRequired(err)
	}
	if s == nil || s.seedTorrentCache == nil {
		return nil, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "SEED_TORRENT_CACHE_NOT_CONFIGURED", Message: "Seed torrent caching is not configured on this server."}
	}
	// One batch read for the whole group: the locators are collected first so a
	// group whose seeds are all still pending still issues exactly one query
	// (and none at all when the snapshot already carried every manifest).
	locators := make([]string, 0, len(preview.Seeds))
	keysByLocator := make(map[string][]string, len(preview.Seeds))
	for _, seed := range preview.Seeds {
		if seed.Manifest != nil {
			continue
		}
		locator := strings.TrimSpace(seed.DownloadURL)
		if locator == "" {
			continue
		}
		if _, seen := keysByLocator[locator]; !seen {
			locators = append(locators, locator)
		}
		keysByLocator[locator] = append(keysByLocator[locator], seed.StableSeedKey)
	}
	if len(locators) == 0 {
		// Every seed was already expanded by the snapshot itself, so there is
		// nothing to look up and the read touches no store at all.
		return map[string]*catalogdomain.TorrentManifest{}, nil
	}
	cached, err := s.seedTorrentCache.ReadSeedTorrentCaches(ctx, locators)
	if err != nil {
		return nil, err
	}
	manifests := make(map[string]*catalogdomain.TorrentManifest, len(cached))
	for locator, entry := range cached {
		for _, key := range keysByLocator[locator] {
			manifest := entry.Manifest
			manifests[key] = &manifest
		}
	}
	return manifests, nil
}

// fetchAndCacheSeedTorrent runs inside the single flight. It re-checks the cache
// so a row another instance wrote after the caller's first read is still a hit,
// fetches exactly once, verifies the bytes against every published candidate
// identity, and only then caches them. A mismatching payload is reported but
// never written: the cache must not claim bytes that are not the seed's.
func (s *Service) fetchAndCacheSeedTorrent(ctx context.Context, seed catalogdomain.ProviderRSSPreviewSeed) (SeedTorrentFact, error) {
	locator := strings.TrimSpace(seed.DownloadURL)
	if cached, hit, err := s.seedTorrentCache.ReadSeedTorrentCache(ctx, locator); err != nil {
		return SeedTorrentFact{}, err
	} else if hit {
		return cachedSeedTorrentFact(seed, cached), nil
	}
	payload, err := s.seedTorrentFetcher.FetchPayload(ctx, locator)
	if err != nil {
		return unavailableSeedTorrentFact(err), nil
	}
	digest := catalogdomain.DigestBytes(payload.Bytes)
	if seedTorrentIdentityMismatch(seed, payload.Metadata, digest) {
		return identityMismatchSeedTorrentFact(), nil
	}
	// ApplyTorrentMetadata records the parser's authoritative facts and the
	// per-file tree on the item; the projection then derives the immutable
	// manifest, exactly as the preview publication path does. Sharing that path
	// keeps a cached manifest byte-identical to a snapshot seed's manifest.
	item := acquisitiondomain.FeedItem{}
	acquisitionapplication.ApplyTorrentMetadata(&item, payload.Metadata)
	manifest, err := projection.ManifestForItem(item, digest)
	if err != nil {
		return SeedTorrentFact{}, fmt.Errorf("project expanded seed torrent: %w", err)
	}
	entry := catalogdomain.SeedTorrentCache{
		Locator: locator, Payload: payload.Bytes, PayloadDigest: digest, Manifest: *manifest, FetchedAt: s.seedTorrentNow(),
	}
	if err := s.seedTorrentCache.UpsertSeedTorrentCache(ctx, entry); err != nil {
		return SeedTorrentFact{}, err
	}
	return SeedTorrentFact{Manifest: manifest, PayloadDigest: digest, State: SeedTorrentStateAvailable}, nil
}

// validateExpansionScope enforces the kind's scope shape before any read. A feed
// expansion is the feed scope validator above; a search expansion must carry the
// two derived hashed refs, which is the only form a search snapshot has.
// readSnapshotIdentityForKind resolves the exact snapshot an expansion names and
// proves it is the kind that expansion is allowed to act on. The search kind is
// read through the search-scope path, so a search seed can never be expanded from
// a feed identity and a feed seed can never be expanded from a search one.
func (s *Service) readSnapshotIdentityForKind(ctx context.Context, provider, entryRef, groupRef string, kind catalogdomain.FeedSnapshotKind, identity catalogdomain.ProviderRSSPreviewIdentity) (catalogdomain.ProviderRSSPreviewResult, error) {
	if kind == catalogdomain.FeedSnapshotKindSearch {
		snapshot, err := s.readSearchSnapshotIdentity(ctx, provider, entryRef, groupRef, identity)
		if err != nil {
			return catalogdomain.ProviderRSSPreviewResult{}, feedSnapshotRefreshRequired(err)
		}
		return mapProviderFeedSnapshot(snapshot), nil
	}
	return s.ReadProviderEntryRSSByIdentity(ctx, provider, entryRef, groupRef, identity)
}

func validateExpansionScope(input EnsureSeedTorrentInput) error {
	switch input.SnapshotKind {
	case "", catalogdomain.FeedSnapshotKindProviderFeed, catalogdomain.FeedSnapshotKindCustom:
		return nil
	case catalogdomain.FeedSnapshotKindSearch:
		if err := catalogdomain.ValidateSearchSnapshotRef(input.ProviderEntryRef, catalogdomain.SearchSnapshotScopePrefix); err != nil {
			return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEARCH_SNAPSHOT_SCOPE_INVALID", Message: safeProviderReason(err.Error())}
		}
		if err := catalogdomain.ValidateSearchSnapshotRef(input.ProviderGroupRef, catalogdomain.SearchSnapshotQueryPrefix); err != nil {
			return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEARCH_SNAPSHOT_SCOPE_INVALID", Message: safeProviderReason(err.Error())}
		}
		if err := validateSearchProvider(input.Provider); err != nil {
			return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_KIND_INVALID", Message: "Provider must be anibt, mikan or nyaa."}
		}
		return nil
	default:
		return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEED_KIND_INVALID", Message: "The snapshot kind is unknown."}
	}
}

// ensureSeedTorrentSeed locates one seed by its stable key.
func ensureSeedTorrentSeed(seeds []catalogdomain.ProviderRSSPreviewSeed, stableSeedKey string) (catalogdomain.ProviderRSSPreviewSeed, bool) {
	for index := range seeds {
		if seeds[index].StableSeedKey == stableSeedKey {
			return seeds[index], true
		}
	}
	return catalogdomain.ProviderRSSPreviewSeed{}, false
}

// persistedSeedTorrentFact serves a seed whose snapshot already carries an
// available manifest. It never touches the network: the bytes were sealed with
// the snapshot, and the locator may legitimately have moved since.
func persistedSeedTorrentFact(seed catalogdomain.ProviderRSSPreviewSeed) (SeedTorrentFact, bool) {
	if seed.TorrentState != catalogdomain.TorrentAcquisitionAvailable || seed.Manifest == nil {
		return SeedTorrentFact{}, false
	}
	digest := ""
	if seed.ExpectedPayloadDigest != nil {
		digest = *seed.ExpectedPayloadDigest
	}
	return SeedTorrentFact{Manifest: seed.Manifest, PayloadDigest: digest, State: SeedTorrentStateAvailable, Cached: true}, true
}

func seedTorrentFactFromCache(entry catalogdomain.SeedTorrentCache) SeedTorrentFact {
	manifest := entry.Manifest
	return SeedTorrentFact{Manifest: &manifest, PayloadDigest: entry.PayloadDigest, State: SeedTorrentStateAvailable, Cached: true}
}

// cachedSeedTorrentFact serves a cache hit for one seed. The cache is keyed by
// locator, so a later snapshot can name the same locator with a different
// published identity: the cached bytes are checked against this seed's
// identity exactly like freshly fetched bytes, and a mismatch is reported as
// the same unavailable state rather than served.
func cachedSeedTorrentFact(seed catalogdomain.ProviderRSSPreviewSeed, entry catalogdomain.SeedTorrentCache) SeedTorrentFact {
	metadata := torrent.Metadata{InfoHashV1: entry.Manifest.InfoHashV1, InfoHashV2: entry.Manifest.InfoHashV2}
	if seedTorrentIdentityMismatch(seed, metadata, entry.PayloadDigest) {
		return identityMismatchSeedTorrentFact()
	}
	return seedTorrentFactFromCache(entry)
}

func identityMismatchSeedTorrentFact() SeedTorrentFact {
	return SeedTorrentFact{
		State:       SeedTorrentStateUnavailable,
		ErrorCode:   SeedTorrentErrorIdentityMismatch,
		ErrorReason: "the fetched torrent does not match the seed identity published by the snapshot",
	}
}

// unavailableSeedTorrentFact maps a typed fetch failure onto the state the read
// model renders. The reason is bounded and carries no locator of its own beyond
// what the transport error already mentions.
func unavailableSeedTorrentFact(cause error) SeedTorrentFact {
	code := SeedTorrentErrorFetchFailed
	switch {
	case errors.Is(cause, acquisitionapplication.ErrTorrentPayloadMissing):
		code = SeedTorrentErrorMissing
	case errors.Is(cause, acquisitionapplication.ErrTorrentPayloadInvalid):
		code = SeedTorrentErrorParseFailed
	}
	return SeedTorrentFact{State: SeedTorrentStateUnavailable, ErrorCode: code, ErrorReason: safeProviderReason(cause.Error())}
}

// seedTorrentIdentityMismatch compares the fetched payload against every
// candidate identity the snapshot published. A published digest must cover the
// bytes, and a published infohash is accepted when it equals the parser's v1
// OR v2 hash — the exact rule the downloader applies in
// verifySeedStrongIdentities, so one payload can never expand and then refuse to
// download (or the reverse). A value whose length is neither v1 nor v2 is not an
// infohash at all (the downloader rejects it before fetching) and stays a
// mismatch rather than a skip: caching bytes whose identity cannot be checked
// would let the downloader accept content the snapshot never named.
func seedTorrentIdentityMismatch(seed catalogdomain.ProviderRSSPreviewSeed, metadata torrent.Metadata, digest string) bool {
	if seed.ExpectedPayloadDigest != nil && *seed.ExpectedPayloadDigest != digest {
		return true
	}
	if seed.InfoHash == nil {
		return false
	}
	expected := strings.ToLower(strings.TrimSpace(*seed.InfoHash))
	if len(expected) != 40 && len(expected) != 64 {
		return true
	}
	return metadata.InfoHashV1 != expected && metadata.InfoHashV2 != expected
}

func (s *Service) seedTorrentNow() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// feedSnapshotRefreshRequired elevates the exact-identity read sentinels onto
// the typed 409 this endpoint's contract already declares. The identity read
// returns a raw domain sentinel so each caller can fail closed its own way;
// this is the boundary the transport sees, and an unmapped sentinel would
// answer a routine client state — a page holding an identity that expired or
// was superseded while it was open — from the unknown-500 bucket.
func feedSnapshotRefreshRequired(err error) error {
	if errors.Is(err, catalogdomain.ErrProviderFeedSnapshotNotFound) ||
		errors.Is(err, catalogdomain.ErrProviderFeedSnapshotIdentityMismatch) ||
		errors.Is(err, catalogdomain.ErrProviderFeedSnapshotStale) {
		return &apperrors.Business{Status: http.StatusConflict, Code: "FEED_SNAPSHOT_REFRESH_REQUIRED", Message: "The named feed snapshot is no longer current. Refresh the feed list and retry."}
	}
	return err
}
