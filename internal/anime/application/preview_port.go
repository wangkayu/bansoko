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

// SeedPreviewSourceFromProviderFeedSnapshot adapts the catalog exact-identity
// feed snapshot reads into the SeedPreviewSource port without importing the
// catalog application package.
func SeedPreviewSourceFromProviderFeedSnapshot(
	fetch func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error),
	readPayload func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error),
) SeedPreviewSource {
	return seedPreviewByIDFunc{fetch: fetch, readPayload: readPayload}
}

type seedPreviewByIDFunc struct {
	fetch       func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error)
	readPayload func(ctx context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animecatalogdomain.ProviderFeedSnapshotSeed, error)
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
		facts = append(facts, fact)
	}
	return SeedPreviewSnapshot{
		Provider: result.Provider, ProviderEntryRef: result.ProviderEntryRef, ProviderGroupRef: result.GroupRef,
		SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, ContentHash: result.ContentHash,
		Freshness: string(result.Freshness), Seeds: facts,
	}
}
