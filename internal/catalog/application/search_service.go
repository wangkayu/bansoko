package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	acquisitionapplication "bansoko/internal/acquisition/application"
	"bansoko/internal/apperrors"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/events"
	"bansoko/internal/parser/reltitletags"
)

// Keyword search is the one non-subscribable catalog surface. Its product rule is
// narrow on purpose:
//
//   - The caller supplies a free-text query and a provider, nothing else. The
//     upstream endpoint, the query normalization, the result scope and the
//     stable identities are all derived server-side, so a request can never
//     name a URL, a catalog entry, a group, a stable key or torrent bytes.
//   - The result is persisted as an immutable `search` feed snapshot: the same
//     append-only store, the same canonical content hash and the same stable
//     seed keys as a provider feed, with its own short retention.
//   - A search writes no subscription, no ownership, no group and no torrent
//     byte: only expanding a seed fetches a torrent, exactly like a feed.

// SearchSeedPreviewSeed is one seed row of a search snapshot as the read model
// renders it. It is the same fact set a feed preview seed carries, minus the raw
// locator: a search result is identified by its snapshot identity plus its stable
// seed key, so the browser never has to hold (or echo) a download URL.
type SearchSeedPreviewSeed struct {
	StableSeedKey  string
	Title          string
	PublishedAt    time.Time
	SizeBytes      *int64
	InfoHash       *string
	LocatorDisplay string
	// Display-only facts parsed from the release title. They never identify a
	// provider group or a work: a search row has neither, and the parse below
	// exists so the list can show a resolution or a group name.
	GroupName string
	// TitleHint is the parsed work-title candidate. It only ever pre-fills the
	// editable title of a one-shot confirmation; it is not a work identity.
	TitleHint  string
	Resolution string
}

// SearchSeedPreview is the fetch result of one search snapshot: the snapshot
// identity plus its complete stored seed window.
type SearchSeedPreview struct {
	Provider     string
	Query        string
	ScopeRef     string
	QueryRef     string
	SnapshotID   string
	Version      int64
	ContentHash  string
	FetchedAt    time.Time
	FreshUntil   time.Time
	Freshness    catalogdomain.FeedSnapshotFreshness
	TotalSeeds   int
	Parser       string
	Seeds        []SearchSeedPreviewSeed
	RetainedTill time.Time
}

// CatalogSearchEndpoints resolves the typed upstream endpoint and client for one
// provider search. It is the single place that knows how a provider search is
// addressed, so a request can never choose an endpoint.
type CatalogSearchEndpoints interface {
	ResolveSearchEndpoint(ctx context.Context, provider string) (endpoint string, client *http.Client, err error)
}

// CatalogSearchEndpointsFunc adapts one function to the port.
type CatalogSearchEndpointsFunc func(ctx context.Context, provider string) (string, *http.Client, error)

func (f CatalogSearchEndpointsFunc) ResolveSearchEndpoint(ctx context.Context, provider string) (string, *http.Client, error) {
	return f(ctx, provider)
}

// SeedSearchApplication owns keyword seed search end to end: provider query,
// normalized feed items, the immutable search snapshot and its bounded read.
// It reuses the catalog Service's snapshot store, so it needs no second store and
// no search-specific download path.
type SeedSearchApplication struct {
	Service   *Service
	Endpoints CatalogSearchEndpoints
	// AniBT and Mikan are the readers. They are explicit fields instead of a
	// factory so a test can inject one without a network.
	AniBT HTTPAniBTTorznabSearcher
	Mikan HTTPMikanSearcher
	Nyaa  HTTPNyaaSearcher
	// Now is the clock; nil is the wall clock in UTC.
	Now func() time.Time
	// Resolver and AllowPrivateHosts are the shared SSRF policy knobs the
	// composition root already owns. Production leaves both at their defaults.
	Resolver          func(context.Context, string) ([]net.IP, error)
	AllowPrivateHosts []string
}

// FetchSearchSnapshotInput is one keyword seed search. Only Query and the
// provider path segment are caller input.
type FetchSearchSnapshotInput struct {
	Provider string
	Query    string
}

// ErrSeedSearchNotConfigured is the typed 503 of a server whose search endpoint
// resolution is not wired.
var ErrSeedSearchNotConfigured = errors.New("provider keyword search is not configured")

// SearchScopeRef derives the scope ref of one query. It binds the provider, the
// resolved upstream base and the normalized query, so two providers, two saved
// endpoints and two queries never share a snapshot (and therefore never
// supersede each other).
func SearchScopeRef(provider, endpoint, query string) string {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	return catalogdomain.SearchSnapshotScopePrefix + digestText(provider+"\x00"+base+"\x00"+NormalizeSearchQuery(query))
}

// SearchQueryRef derives the display-safe identity of one normalized query.
func SearchQueryRef(query string) string {
	return catalogdomain.SearchSnapshotQueryPrefix + digestText(NormalizeSearchQuery(query))
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// Fetch runs one search and persists its snapshot. There is no cached read path:
// a search is always an explicit upstream read (which is why its endpoint is a
// POST with the write scope), and only the returned identity is reused.
func (a SeedSearchApplication) Fetch(ctx context.Context, input FetchSearchSnapshotInput) (SearchSeedPreview, error) {
	if a.Service == nil || a.Service.feedSnapshots == nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_SNAPSHOT_STORE_NOT_CONFIGURED", Message: "Provider feed snapshots are not configured on this server."}
	}
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	if err := validateSearchProvider(provider); err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_KIND_INVALID", Message: "Provider must be anibt, mikan or nyaa."}
	}
	query := NormalizeSearchQuery(input.Query)
	if query == "" || utf8.RuneCountInString(query) > 256 {
		return SearchSeedPreview{}, fmt.Errorf("%w: query is required and must be at most 256 characters", ErrSearchQueryInvalid)
	}
	if a.Endpoints == nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_SEARCH_NOT_CONFIGURED", Message: ErrSeedSearchNotConfigured.Error()}
	}
	endpoint, client, err := a.Endpoints.ResolveSearchEndpoint(ctx, provider)
	if err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_SEARCH_ENDPOINT_UNAVAILABLE", Message: safeProviderReason(err.Error())}
	}
	search, err := a.search(ctx, provider, endpoint, client, query)
	if err != nil {
		return SearchSeedPreview{}, mapSeedSearchError(err)
	}
	seeds, err := mapFeedItemsToSnapshotSeeds(search.Items)
	if err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "FEED_SNAPSHOT_INVALID", Message: safeProviderReason(err.Error())}
	}
	contentHash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "FEED_SNAPSHOT_INVALID", Message: safeProviderReason(err.Error())}
	}
	now := a.now()
	scopeRef, queryRef := SearchScopeRef(provider, endpoint, query), SearchQueryRef(query)
	snapshot, err := a.Service.feedSnapshots.PersistProviderFeedSnapshot(ctx, catalogdomain.ProviderFeedSnapshotWrite{
		Provider: provider, ProviderEntryRef: scopeRef, ProviderGroupRef: queryRef,
		SnapshotKind: catalogdomain.FeedSnapshotKindSearch, SearchParserVersion: search.ParserVersion,
		FetchedAt: now, FreshUntil: now.Add(a.Service.searchFreshWindow()), ContentHash: contentHash,
		HashSchema:       catalogdomain.ProviderFeedSnapshotContentHashSchema,
		ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema,
		Seeds:            seeds,
	})
	if err != nil {
		return SearchSeedPreview{}, fmt.Errorf("persist search snapshot: %w", err)
	}
	preview := mapSearchSnapshot(snapshot, query)
	preview.TotalSeeds = len(seeds)
	a.Service.publishCatalogInvalidation()
	return preview, nil
}

// FetchSearchSnapshotByID is the exact search-snapshot read exposed to one-shot
// admission. The identity must be the fresh
// exact `search` row, and the caller names the two derived refs it derived from
// its own query. A read never falls back to a feed snapshot.
func (a SeedSearchApplication) FetchSearchSnapshotByID(ctx context.Context, provider, scopeRef, queryRef string, identity catalogdomain.ProviderRSSPreviewIdentity) (catalogdomain.ProviderRSSPreviewResult, error) {
	if a.Service == nil || a.Service.feedSnapshots == nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_SNAPSHOT_STORE_NOT_CONFIGURED", Message: "Provider feed snapshots are not configured on this server."}
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if err := validateSearchProvider(provider); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_KIND_INVALID", Message: "Provider must be anibt, mikan or nyaa."}
	}
	if err := catalogdomain.ValidateSearchSnapshotRef(scopeRef, catalogdomain.SearchSnapshotScopePrefix); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, fmt.Errorf("%w: %v", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, err)
	}
	if err := catalogdomain.ValidateSearchSnapshotRef(queryRef, catalogdomain.SearchSnapshotQueryPrefix); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, fmt.Errorf("%w: %v", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, err)
	}
	snapshot, err := a.Service.readSearchSnapshotIdentity(ctx, provider, scopeRef, queryRef, identity)
	if err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, err
	}
	return mapProviderFeedSnapshot(snapshot), nil
}

// ReadSearchSeedPayload reads the exact verified torrent bytes of one seed of an
// exact search snapshot, through the same lazy expansion the expand endpoint
// uses. It is the search counterpart of ReadProviderEntrySeedPayload: the bytes
// are the ones expansion verified and cached, never a fresh unverified fetch.
func (a SeedSearchApplication) ReadSearchSeedPayload(ctx context.Context, provider, scopeRef, queryRef string, identity catalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (catalogdomain.ProviderFeedSnapshotSeed, error) {
	preview, err := a.FetchSearchSnapshotByID(ctx, provider, scopeRef, queryRef, identity)
	if err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, err
	}
	if strings.TrimSpace(stableSeedKey) == "" {
		return catalogdomain.ProviderFeedSnapshotSeed{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEED_IDENTITY_REQUIRED", Message: "The seed identity is required."}
	}
	seed, ok := ensureSeedTorrentSeed(preview.Seeds, stableSeedKey)
	if !ok {
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s is not part of snapshot %s", catalogdomain.ErrProviderFeedSnapshotNotFound, stableSeedKey, preview.SnapshotID)
	}
	fact, err := a.Service.EnsureSeedTorrent(ctx, EnsureSeedTorrentInput{
		Provider: provider, ProviderEntryRef: scopeRef, ProviderGroupRef: queryRef,
		SnapshotKind: catalogdomain.FeedSnapshotKindSearch,
		SnapshotID:   identity.SnapshotID, SnapshotVersion: identity.SnapshotVersion, ContentHash: identity.ContentHash,
		StableSeedKey: stableSeedKey,
	})
	if err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, err
	}
	if fact.State != SeedTorrentStateAvailable {
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s could not be expanded (%s)", catalogdomain.ErrProviderFeedSnapshotNotFound, stableSeedKey, fact.ErrorCode)
	}
	// Expansion resolved exactly this seed's locator; the cached bytes are the
	// bytes the row rendered, so the downloader receives what expansion verified.
	entry, found, err := a.Service.seedTorrentCache.ReadSeedTorrentCache(ctx, strings.TrimSpace(seed.DownloadURL))
	if err != nil {
		return catalogdomain.ProviderFeedSnapshotSeed{}, err
	}
	if !found || len(entry.Payload) == 0 {
		return catalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s has no retained torrent payload", catalogdomain.ErrProviderFeedSnapshotNotFound, stableSeedKey)
	}
	digest := entry.PayloadDigest
	var published *time.Time
	if !seed.PublishedAt.IsZero() {
		value := seed.PublishedAt
		published = &value
	}
	var filename *string
	if strings.TrimSpace(seed.TorrentFilename) != "" {
		value := seed.TorrentFilename
		filename = &value
	}
	return catalogdomain.ProviderFeedSnapshotSeed{
		StableSeedKey: seed.StableSeedKey, Locator: strings.TrimSpace(seed.DownloadURL), LocatorDisplay: seed.LocatorDisplay,
		Title: seed.Title, PublishedAt: published, SizeBytes: seed.SizeBytes,
		InfoHash: seed.InfoHash, TorrentFilename: filename,
		TorrentState: catalogdomain.TorrentAcquisitionAvailable,
		Payload:      entry.Payload, PayloadDigest: &digest, Manifest: fact.Manifest,
		AniBTTags: seed.AniBTTags,
	}, nil
}

// ReadSearchSeedTorrentManifests returns the manifests the lazy expansion cache
// can already prove for one exact search snapshot, keyed by stable seed key. It
// is the search counterpart of ReadSeedTorrentManifests: the snapshot identity
// is verified exactly like the search read itself, the seed locators come from
// that snapshot, and the answer comes from the cache alone — no provider request
// and no write, so a list read stays side-effect free. A seed whose snapshot
// still carries its own manifest is left out: that projection was sealed with
// the snapshot's bytes and stays the caller's first choice.
func (a SeedSearchApplication) ReadSearchSeedTorrentManifests(ctx context.Context, provider, scopeRef, queryRef string, identity catalogdomain.ProviderRSSPreviewIdentity) (map[string]*catalogdomain.TorrentManifest, error) {
	preview, err := a.FetchSearchSnapshotByID(ctx, provider, scopeRef, queryRef, identity)
	if err != nil {
		return nil, err
	}
	if a.Service == nil || a.Service.seedTorrentCache == nil {
		return nil, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "SEED_TORRENT_CACHE_NOT_CONFIGURED", Message: "Seed torrent caching is not configured on this server."}
	}
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
		return map[string]*catalogdomain.TorrentManifest{}, nil
	}
	cached, err := a.Service.seedTorrentCache.ReadSeedTorrentCaches(ctx, locators)
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

func (a SeedSearchApplication) search(ctx context.Context, provider, endpoint string, client *http.Client, query string) (SeedSearchResult, error) {
	switch provider {
	case ProviderAniBT:
		reader := a.AniBT
		reader.Endpoint = endpoint
		reader.Client = client
		if err := a.attachSSRFTransport(&reader.Client); err != nil {
			return SeedSearchResult{}, err
		}
		return reader.SearchAniBTKeywords(ctx, query)
	case ProviderMikan:
		reader := a.Mikan
		reader.Endpoint = endpoint
		reader.Client = client
		if err := a.attachSSRFTransport(&reader.Client); err != nil {
			return SeedSearchResult{}, err
		}
		return reader.SearchMikanSeeds(ctx, query)
	case ProviderNyaa:
		reader := a.Nyaa
		reader.Endpoint = endpoint
		reader.Client = client
		if err := a.attachSSRFTransport(&reader.Client); err != nil {
			return SeedSearchResult{}, err
		}
		return reader.SearchNyaaSeeds(ctx, query)
	default:
		return SeedSearchResult{}, fmt.Errorf("%w: %s", ErrSearchNotSupported, provider)
	}
}

// attachSSRFTransport wraps the resolved client with the shared SSRF-safe
// provider transport, so a keyword search cannot dial an internal host whatever
// endpoint resolution returned.
func (a SeedSearchApplication) attachSSRFTransport(client **http.Client) error {
	secured, err := acquisitionapplication.SSRFClientForProvider(*client, a.Resolver, a.AllowPrivateHosts, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProviderUpstream, err)
	}
	*client = secured
	return nil
}

func (a SeedSearchApplication) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// mapSearchSnapshot projects one persisted search snapshot into its fetch read:
// the complete stored seed window. Seeds keep their stored order (published
// time, then stable key), so the response is stable for a given snapshot.
func mapSearchSnapshot(snapshot catalogdomain.ProviderFeedSnapshot, query string) SearchSeedPreview {
	preview := SearchSeedPreview{
		Provider: snapshot.Provider, Query: query,
		ScopeRef: snapshot.ProviderEntryRef, QueryRef: snapshot.ProviderGroupRef,
		SnapshotID: snapshot.ID, Version: snapshot.Version, ContentHash: snapshot.ContentHash,
		FetchedAt: snapshot.FetchedAt, FreshUntil: snapshot.FreshUntil,
		Freshness:  snapshot.FreshnessAt(time.Now().UTC()),
		TotalSeeds: len(snapshot.Seeds), Parser: snapshot.SearchParserVersion,
		RetainedTill: snapshot.RetentionUntil,
		Seeds:        make([]SearchSeedPreviewSeed, 0, len(snapshot.Seeds)),
	}
	for _, seed := range snapshot.Seeds {
		row := SearchSeedPreviewSeed{
			StableSeedKey: seed.StableSeedKey, Title: seed.Title,
			LocatorDisplay: storedSeedLocatorDisplay(seed.LocatorDisplay),
		}
		if seed.PublishedAt != nil {
			row.PublishedAt = *seed.PublishedAt
		}
		row.SizeBytes, row.InfoHash = seed.SizeBytes, seed.InfoHash
		// Display-only title facts. They are read from the release title, never
		// used as identity and never persisted as a provider group.
		tags := reltitletags.Extract(seed.Title)
		for _, tag := range tags.Tags {
			if value, ok := strings.CutPrefix(tag, "resolution:"); ok && row.Resolution == "" {
				row.Resolution = value
			}
		}
		for _, value := range tags.ValueTags {
			if name, ok := strings.CutPrefix(value, "group:"); ok && row.GroupName == "" {
				row.GroupName = name
			}
			if name, ok := strings.CutPrefix(value, "title:"); ok && row.TitleHint == "" {
				row.TitleHint = name
			}
		}
		preview.Seeds = append(preview.Seeds, row)
	}
	return preview
}

// mapSeedSearchError keeps the upstream failure classes distinguishable at the
// boundary: a rate limit carries the upstream's own retry time, a Torznab
// protocol error is a provider outcome, and an unusable feed stays the existing
// typed feed error.
func mapSeedSearchError(err error) error {
	// A rate limit stays the typed upstream refusal it is: the transport is the
	// only layer that can answer with a 429 and the upstream's own Retry-After,
	// and it needs the duration, which a generic business error cannot carry.
	var limited *ProviderRateLimited
	if errors.As(err, &limited) {
		return err
	}
	if errors.Is(err, ErrSearchQueryInvalid) {
		return err
	}
	// A feed whose every row was unusable is a property of the answer, not a
	// transport failure.
	if business := feedFailureBusinessError(err); business != nil {
		return business
	}
	var protocol *TorznabError
	if errors.As(err, &protocol) {
		return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEARCH_UPSTREAM_FAILED", Message: safeProviderReason(protocol.Error())}
	}
	if errors.Is(err, ErrProviderUpstream) {
		return &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEARCH_UPSTREAM_FAILED", Message: safeProviderReason(err.Error())}
	}
	return err
}

// readSearchSnapshotIdentity is the exact search-snapshot read: the id, version
// and content hash must still describe one fresh `search` row whose derived scope
// refs are the ones the caller named. It never falls back to "latest" and never
// accepts a feed or custom snapshot, so a search identity can never be used to
// read a subscription surface.
func (s *Service) readSearchSnapshotIdentity(ctx context.Context, provider, scopeRef, queryRef string, identity catalogdomain.ProviderRSSPreviewIdentity) (catalogdomain.ProviderFeedSnapshot, error) {
	snapshot, err := s.feedSnapshots.ReadProviderFeedSnapshotByID(ctx, identity.SnapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("%w: snapshot %s is not retained", catalogdomain.ErrProviderFeedSnapshotNotFound, identity.SnapshotID)
	}
	if err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("read search snapshot by id: %w", err)
	}
	if err := snapshot.VerifyKind(catalogdomain.FeedSnapshotKindSearch); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	if snapshot.Provider != provider || snapshot.ProviderEntryRef != scopeRef || snapshot.ProviderGroupRef != queryRef {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("%w: snapshot %s belongs to another search scope", catalogdomain.ErrProviderFeedSnapshotIdentityMismatch, snapshot.ID)
	}
	if err := snapshot.VerifyIdentity(identity); err != nil {
		return catalogdomain.ProviderFeedSnapshot{}, err
	}
	// Freshness is checked against the persisted window *and* the stored state: a
	// store may state a freshness derived from its own clock, and an exact search
	// read must never serve a snapshot whose window has passed.
	now := time.Now().UTC()
	if snapshot.FreshnessAt(now) != catalogdomain.FeedSnapshotFresh || !now.Before(snapshot.FreshUntil) {
		return catalogdomain.ProviderFeedSnapshot{}, fmt.Errorf("%w: snapshot %s v%d is stale", catalogdomain.ErrProviderFeedSnapshotStale, snapshot.ID, snapshot.Version)
	}
	return snapshot, nil
}

// publishCatalogInvalidation notifies the in-process read model that a catalog
// write happened. It is the same event every other catalog write publishes and
// never creates a WorkItem.
func (s *Service) publishCatalogInvalidation() {
	if s == nil || s.eventHub == nil {
		return
	}
	s.eventHub.Publish(events.Event{Target: events.TargetCatalog})
}

// SnapshotKindFromRefs derives the snapshot kind a caller's two refs name, from
// the refs alone. It is a routing hint, never an authorization: every read,
// expansion, download and admission proves the persisted row's own kind
// afterwards (VerifyKind), so a forged or stale ref pair can never widen access
// to another kind's snapshot.
func SnapshotKindFromRefs(entryRef, groupRef string) catalogdomain.FeedSnapshotKind {
	switch {
	case strings.HasPrefix(entryRef, catalogdomain.SearchSnapshotScopePrefix) && strings.HasPrefix(groupRef, catalogdomain.SearchSnapshotQueryPrefix):
		return catalogdomain.FeedSnapshotKindSearch
	case strings.HasPrefix(entryRef, "custom-source:") && strings.HasPrefix(groupRef, "custom-group:"):
		return catalogdomain.FeedSnapshotKindCustom
	default:
		return catalogdomain.FeedSnapshotKindProviderFeed
	}
}
