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
	"strconv"
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
const (
	// defaultSearchSeedPreviewLimit bounds the seed preview a fetch returns. The
	// snapshot identity still covers the complete result set; the window is only
	// how many rows this response carries.
	defaultSearchSeedPreviewLimit = 25
	maxSearchSeedPreviewLimit     = 100
)

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

// SearchSeedPreview is the fetch/read result of one search snapshot.
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
	Window       int
	NextCursor   string
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
	// Limit is the seed-preview window of this response, not of the snapshot
	// identity. It is clamped to the documented keyword window.
	Limit int
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
	if err := validateCatalogProvider(provider); err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_KIND_INVALID", Message: "Provider must be anibt or mikan."}
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
	preview := mapSearchSnapshot(snapshot, query, a.limit(input.Limit), 0)
	preview.TotalSeeds = len(seeds)
	a.Service.publishCatalogInvalidation()
	return preview, nil
}

// ReadWindow returns a bounded window of one exact search snapshot identity. The
// identity must be the fresh, exact search snapshot the caller named; a stale,
// missing, superseded or differently-kinded snapshot fails closed, exactly like
// an exact feed read.
func (a SeedSearchApplication) ReadWindow(ctx context.Context, provider, scopeRef, queryRef, snapshotID, contentHash string, version int64, limit int, cursor string) (SearchSeedPreview, error) {
	if a.Service == nil || a.Service.feedSnapshots == nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_SNAPSHOT_STORE_NOT_CONFIGURED", Message: "Provider feed snapshots are not configured on this server."}
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if err := validateCatalogProvider(provider); err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_KIND_INVALID", Message: "Provider must be anibt or mikan."}
	}
	if err := catalogdomain.ValidateSearchSnapshotRef(scopeRef, catalogdomain.SearchSnapshotScopePrefix); err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEARCH_SNAPSHOT_SCOPE_INVALID", Message: safeProviderReason(err.Error())}
	}
	if err := catalogdomain.ValidateSearchSnapshotRef(queryRef, catalogdomain.SearchSnapshotQueryPrefix); err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEARCH_SNAPSHOT_SCOPE_INVALID", Message: safeProviderReason(err.Error())}
	}
	identity := catalogdomain.ProviderRSSPreviewIdentity{SnapshotID: snapshotID, SnapshotVersion: version, ContentHash: contentHash}
	if err := identity.ValidateIdentityShape(); err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: safeProviderReason(err.Error())}
	}
	snapshot, err := a.Service.readSearchSnapshotIdentity(ctx, provider, scopeRef, queryRef, identity)
	if err != nil {
		return SearchSeedPreview{}, err
	}
	// The query text is not persisted in clear text: the snapshot stores its
	// digest, and the read reports that digest so a caller can prove it is the
	// same query without the server keeping a query log.
	offset, err := searchCursorOffset(cursor, len(snapshot.Seeds))
	if err != nil {
		return SearchSeedPreview{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "SEARCH_SNAPSHOT_CURSOR_INVALID", Message: "The search snapshot cursor is invalid for this result window."}
	}
	return mapSearchSnapshot(snapshot, "", a.limit(limit), offset), nil
}

// FetchSearchSnapshotByID is the exact search-snapshot read exposed to one-shot
// admission. It is the same read as ReadWindow: the identity must be the fresh
// exact `search` row, and the caller names the two derived refs it derived from
// its own query. A read never falls back to a feed snapshot.
func (a SeedSearchApplication) FetchSearchSnapshotByID(ctx context.Context, provider, scopeRef, queryRef string, identity catalogdomain.ProviderRSSPreviewIdentity) (catalogdomain.ProviderRSSPreviewResult, error) {
	if a.Service == nil || a.Service.feedSnapshots == nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_FEED_SNAPSHOT_STORE_NOT_CONFIGURED", Message: "Provider feed snapshots are not configured on this server."}
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if err := validateCatalogProvider(provider); err != nil {
		return catalogdomain.ProviderRSSPreviewResult{}, &apperrors.Business{Status: http.StatusUnprocessableEntity, Code: "PROVIDER_KIND_INVALID", Message: "Provider must be anibt or mikan."}
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

func (a SeedSearchApplication) limit(requested int) int {
	if requested <= 0 {
		return defaultSearchSeedPreviewLimit
	}
	if requested > maxSearchSeedPreviewLimit {
		return maxSearchSeedPreviewLimit
	}
	return requested
}

// mapSearchSnapshot projects one persisted search snapshot into the bounded read
// window. Seeds keep their stored order (published time, then stable key), so the
// window is stable for a given snapshot: paging a snapshot never reorders it.
func mapSearchSnapshot(snapshot catalogdomain.ProviderFeedSnapshot, query string, limit, offset int) SearchSeedPreview {
	preview := SearchSeedPreview{
		Provider: snapshot.Provider, Query: query,
		ScopeRef: snapshot.ProviderEntryRef, QueryRef: snapshot.ProviderGroupRef,
		SnapshotID: snapshot.ID, Version: snapshot.Version, ContentHash: snapshot.ContentHash,
		FetchedAt: snapshot.FetchedAt, FreshUntil: snapshot.FreshUntil,
		Freshness:  snapshot.FreshnessAt(time.Now().UTC()),
		TotalSeeds: len(snapshot.Seeds), Window: limit, Parser: snapshot.SearchParserVersion,
		RetainedTill: snapshot.RetentionUntil,
		Seeds:        make([]SearchSeedPreviewSeed, 0, len(snapshot.Seeds)),
	}
	for index := offset; index < len(snapshot.Seeds); index++ {
		if limit > 0 && len(preview.Seeds) >= limit {
			break
		}
		seed := snapshot.Seeds[index]
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
	if next := offset + len(preview.Seeds); next < len(snapshot.Seeds) {
		preview.NextCursor = strconv.Itoa(next)
	}
	return preview
}

// searchCursorOffset decodes the opaque cursor emitted by the previous window.
// The current encoding is an offset into the immutable, stably ordered snapshot;
// callers must not construct it, and an offset outside this result set is
// rejected instead of silently skipping or repeating rows.
func searchCursorOffset(cursor string, total int) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 || offset > total || strconv.Itoa(offset) != cursor {
		return 0, errors.New("invalid search snapshot cursor")
	}
	return offset, nil
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
