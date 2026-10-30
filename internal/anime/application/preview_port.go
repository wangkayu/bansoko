package application

import (
	"context"
	"errors"
	"fmt"

	"bansoko/internal/apperrors"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// This file holds the persisted provider-feed preview port and its production
// adapter. Both the read model's saved preview and oneshot's human admission
// consume the port, and the composition root wires the adapter, so the port
// belongs to the kernel rather than to either bucket.

// ErrProviderFeedSnapshotRefreshRequired is the typed outcome when the exact
// feed snapshot identity a caller supplied is missing, stale, superseded or does
// not match the persisted snapshot. Callers must require an explicit provider
// refresh; they must never fall back to the latest snapshot.
var ErrProviderFeedSnapshotRefreshRequired = errors.New("provider feed snapshot requires an explicit refresh")

// SeedPreviewSource reads one exact persisted provider feed snapshot identity
// for one provider entry/group. It never reads "latest": a missing, stale or
// mismatching identity is reported as ErrProviderFeedSnapshotRefreshRequired so
// human admission and saved preview fail closed. The production adapter wraps
// the catalog ReadProviderEntryRSSByIdentity service (see
// SeedPreviewSourceFromProviderFeedSnapshot); the composition root wires it.
type SeedPreviewSource interface {
	FetchEntryGroupSnapshotByID(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (SeedPreviewSnapshot, error)

	// ReadSeedPayload reads the exact verified torrent bytes of one seed in one
	// persisted snapshot. Admission uses it instead of fetching the locator
	// again, so the bytes the user reviewed are the bytes the downloader
	// receives. A missing, stale, legacy or mismatching identity fails closed.
	ReadSeedPayload(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (SeedPayload, error)
}

// SearchSeedPreviewSource is the optional extension that serves exact keyword
// search snapshots. It is an extension instead of a member of SeedPreviewSource
// so a source that never serves search keeps its narrow contract, and so a
// consumer that needs search has to ask for it explicitly.
type SearchSeedPreviewSource interface {
	FetchSearchSnapshotByID(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (SeedPreviewSnapshot, error)
}

// SearchSeedPayloadSource is the optional extension that serves one search seed's
// exact verified bytes. Admission uses it for a search seed exactly like
// SeedPreviewSource.ReadSeedPayload serves a feed seed: the bytes a user expanded
// are the bytes the downloader receives.
type SearchSeedPayloadSource interface {
	ReadSearchSeedPayload(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error)
}

// SeedPayload is one seed's exact persisted torrent bytes with the identity
// that binds them.
type SeedPayload struct {
	Bytes           []byte
	PayloadDigest   string
	StableSeedKey   string
	TorrentFilename string
	Manifest        *animecatalogdomain.TorrentManifest
}

// SeedTorrentManifestSource reads the manifests the lazy expansion cache can
// already prove for one exact persisted snapshot identity, keyed by stable seed
// key. It is read-only: no provider request and no write, so a list read stays
// side-effect free. A locator that was never expanded is simply absent from the
// map (the seed stays pending), and an identity that no longer resolves fails
// closed as ErrProviderFeedSnapshotRefreshRequired rather than rendering a
// different snapshot.
type SeedTorrentManifestSource interface {
	ReadSeedTorrentManifests(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error)
}

// SearchSeedTorrentManifestSource is the optional extension that serves the
// expansion-cache manifest read for an exact keyword search snapshot. It is an
// extension for the same reason SearchSeedPreviewSource is: a source that never
// serves search keeps its narrow contract, and a consumer that needs the search
// read has to ask for it explicitly.
type SearchSeedTorrentManifestSource interface {
	ReadSearchSeedTorrentManifests(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error)
}

// SeedTorrentManifestsFromProviderFeedSnapshot adapts the catalog
// exact-identity cache read into the port without importing the catalog
// application package.
func SeedTorrentManifestsFromProviderFeedSnapshot(
	read func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error),
) SeedTorrentManifestSource {
	return seedTorrentManifestsByIDFunc(read)
}

type seedTorrentManifestsByIDFunc func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error)

func (f seedTorrentManifestsByIDFunc) ReadSeedTorrentManifests(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error) {
	if f == nil {
		return nil, &apperrors.Business{Status: 503, Code: "SEED_TORRENT_CACHE_NOT_CONFIGURED", Message: "The seed torrent cache reader is not configured."}
	}
	manifests, err := f(ctx, provider, entryRef, groupRef, identity)
	if err != nil {
		return nil, seedPreviewRefreshRequired(err)
	}
	return manifests, nil
}

// seedTorrentManifests is the struct adapter behind both manifest reads: the
// feed read is required, the search read is optional, and a nil search read
// fails closed exactly like a source that never serves search snapshots.
type seedTorrentManifests struct {
	read       func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error)
	readSearch func(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error)
}

// SeedTorrentManifestsWithKeywordSearch adds the exact keyword-search snapshot
// cache read to the same manifest source: the composition root wires it where
// the catalog can serve search snapshots, so the one-shot browse over a search
// snapshot renders the manifests its expansions already proved.
func SeedTorrentManifestsWithKeywordSearch(
	read func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error),
	readSearch func(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error),
) SeedTorrentManifestSource {
	return seedTorrentManifests{read: read, readSearch: readSearch}
}

func (s seedTorrentManifests) ReadSeedTorrentManifests(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error) {
	if s.read == nil {
		return nil, &apperrors.Business{Status: 503, Code: "SEED_TORRENT_CACHE_NOT_CONFIGURED", Message: "The seed torrent cache reader is not configured."}
	}
	manifests, err := s.read(ctx, provider, entryRef, groupRef, identity)
	if err != nil {
		return nil, seedPreviewRefreshRequired(err)
	}
	return manifests, nil
}

func (s seedTorrentManifests) ReadSearchSeedTorrentManifests(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error) {
	if s.readSearch == nil {
		return nil, &apperrors.Business{Status: 503, Code: "SEED_TORRENT_CACHE_NOT_CONFIGURED", Message: "The keyword search seed torrent cache reader is not configured."}
	}
	manifests, err := s.readSearch(ctx, provider, scopeRef, queryRef, identity)
	if err != nil {
		return nil, seedPreviewRefreshRequired(err)
	}
	return manifests, nil
}

// SeedPreviewSourceFromProviderFeedSnapshot adapts the catalog exact-identity
// feed snapshot reads into the SeedPreviewSource port without importing the
// catalog application package.
func SeedPreviewSourceFromProviderFeedSnapshot(
	fetch func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error),
	readPayload func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error),
) SeedPreviewSource {
	return seedPreviewByIDFunc{fetch: fetch, readPayload: readPayload}
}

// SeedPreviewSourceWithKeywordSearch adds the exact keyword-search snapshot reads
// to the same source: the snapshot read (SearchSeedPreviewSource) and the seed's
// verified bytes (SearchSeedPayloadSource). The composition root wires it where
// the catalog service can serve search snapshots; a one-shot search admission then
// proves the seed belongs to that exact search snapshot instead of a feed, and
// receives the bytes expansion already verified rather than a fresh fetch.
//
// Both search reads are required arguments: a source built without the payload
// read would answer SEED_PAYLOAD_READER_NOT_CONFIGURED at admission time, which is
// exactly the production gap this constructor exists to close.
func SeedPreviewSourceWithKeywordSearch(
	fetch func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error),
	readPayload func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error),
	fetchSearch func(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error),
	readSearchPayload func(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error),
) SeedPreviewSource {
	return seedPreviewByIDFunc{
		fetch: fetch, readPayload: readPayload,
		fetchSearch: fetchSearch, readSearchPayload: readSearchPayload,
	}
}

type seedPreviewByIDFunc struct {
	fetch       func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error)
	readPayload func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error)
	// fetchSearch is optional: a source that never serves search snapshots
	// leaves it nil, and a search read then fails closed.
	fetchSearch func(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error)
	// readSearchPayload is optional for the same reason: a search seed's bytes are
	// only reachable when the source serves search snapshots at all.
	readSearchPayload func(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error)
}

// seedPreviewRefreshRequired maps the catalog exact-read sentinels onto the one
// typed outcome callers fail closed on.
func seedPreviewRefreshRequired(err error) error {
	if errors.Is(err, animecatalogdomain.ErrProviderFeedSnapshotNotFound) ||
		errors.Is(err, animecatalogdomain.ErrProviderFeedSnapshotIdentityMismatch) ||
		errors.Is(err, animecatalogdomain.ErrProviderFeedSnapshotStale) {
		return fmt.Errorf("%w: %v", ErrProviderFeedSnapshotRefreshRequired, err)
	}
	return err
}

func (f seedPreviewByIDFunc) ReadSeedPayload(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (SeedPayload, error) {
	if f.readPayload == nil {
		return SeedPayload{}, &apperrors.Business{Status: 503, Code: "SEED_PAYLOAD_READER_NOT_CONFIGURED", Message: "The persisted seed payload reader is not configured."}
	}
	seed, err := f.readPayload(ctx, provider, entryRef, groupRef, identity, stableSeedKey)
	if err != nil {
		return SeedPayload{}, seedPreviewRefreshRequired(err)
	}
	if len(seed.Payload) == 0 || seed.PayloadDigest == nil {
		return SeedPayload{}, fmt.Errorf("%w: seed %s has no retained torrent payload", ErrProviderFeedSnapshotRefreshRequired, stableSeedKey)
	}
	payload := SeedPayload{
		Bytes:         append([]byte(nil), seed.Payload...),
		PayloadDigest: *seed.PayloadDigest,
		StableSeedKey: seed.StableSeedKey,
		Manifest:      seed.Manifest,
	}
	if seed.TorrentFilename != nil {
		payload.TorrentFilename = *seed.TorrentFilename
	}
	return payload, nil
}

// ReadSearchSeedPayload reads a search seed's exact verified bytes through the
// search snapshot's own path, so a search seed is never served by the feed
// payload reader (which would resolve a provider entry/group scope a search row
// does not have). The same fail-closed rules as ReadSeedPayload apply: a missing
// read is 503, an exact-identity refusal is the typed refresh outcome, and bytes
// without a digest are not a payload.
func (f seedPreviewByIDFunc) ReadSearchSeedPayload(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error) {
	if f.readSearchPayload == nil {
		return animecatalogdomain.ProviderFeedSnapshotSeed{}, &apperrors.Business{Status: 503, Code: "SEED_PAYLOAD_READER_NOT_CONFIGURED", Message: "The persisted keyword search seed payload reader is not configured."}
	}
	seed, err := f.readSearchPayload(ctx, provider, scopeRef, queryRef, identity, stableSeedKey)
	if err != nil {
		return animecatalogdomain.ProviderFeedSnapshotSeed{}, seedPreviewRefreshRequired(err)
	}
	if len(seed.Payload) == 0 || seed.PayloadDigest == nil {
		return animecatalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s has no retained torrent payload", ErrProviderFeedSnapshotRefreshRequired, stableSeedKey)
	}
	if got := animecatalogdomain.DigestBytes(seed.Payload); got != *seed.PayloadDigest {
		return animecatalogdomain.ProviderFeedSnapshotSeed{}, fmt.Errorf("%w: seed %s payload digest does not match its bytes", ErrProviderFeedSnapshotRefreshRequired, stableSeedKey)
	}
	return seed, nil
}

// FetchSearchSnapshotByID reads one exact search snapshot. A source without a
// search read fails closed: the search kind must never be answered from a feed
// snapshot.
func (f seedPreviewByIDFunc) FetchSearchSnapshotByID(ctx context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (SeedPreviewSnapshot, error) {
	if f.fetchSearch == nil {
		return SeedPreviewSnapshot{}, fmt.Errorf("%w: keyword search snapshots are not served by this source", ErrProviderFeedSnapshotRefreshRequired)
	}
	result, err := f.fetchSearch(ctx, provider, scopeRef, queryRef, identity)
	if err != nil {
		return SeedPreviewSnapshot{}, seedPreviewRefreshRequired(err)
	}
	return f.seedPreviewFacts(result), nil
}

func (f seedPreviewByIDFunc) FetchEntryGroupSnapshotByID(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (SeedPreviewSnapshot, error) {
	result, err := f.fetch(ctx, provider, entryRef, groupRef, identity)
	if err != nil {
		return SeedPreviewSnapshot{}, seedPreviewRefreshRequired(err)
	}
	return f.seedPreviewFacts(result), nil
}

func (f seedPreviewByIDFunc) seedPreviewFacts(result animecatalogdomain.ProviderRSSPreviewResult) SeedPreviewSnapshot {
	facts := make([]SeedPreviewFact, 0, len(result.Seeds))
	for _, seed := range result.Seeds {
		fact := SeedPreviewFact{DownloadURL: seed.DownloadURL, LocatorDisplay: seed.LocatorDisplay, PublishedAt: seed.PublishedAt, SizeBytes: seed.SizeBytes, StableSeedKey: seed.StableSeedKey, Title: seed.Title, TorrentFilename: seed.TorrentFilename}
		if seed.InfoHash != nil {
			fact.InfoHash = *seed.InfoHash
		}
		if seed.ExpectedPayloadDigest != nil {
			fact.ExpectedPayloadDigest = *seed.ExpectedPayloadDigest
		}
		fact.TorrentState = seed.TorrentState
		if seed.TorrentErrorCode != nil {
			fact.TorrentErrorCode = *seed.TorrentErrorCode
		}
		if seed.TorrentErrorReason != nil {
			fact.TorrentErrorReason = *seed.TorrentErrorReason
		}
		fact.Manifest = seed.Manifest
		fact.AniBTTags = seed.AniBTTags
		fact.SourceSite = seed.SourceSite
		facts = append(facts, fact)
	}
	return SeedPreviewSnapshot{
		Provider: result.Provider, ProviderEntryRef: result.ProviderEntryRef, ProviderGroupRef: result.GroupRef,
		SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, ContentHash: result.ContentHash,
		Freshness: string(result.Freshness), Seeds: facts,
	}
}
