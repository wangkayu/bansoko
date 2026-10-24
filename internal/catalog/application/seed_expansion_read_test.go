package application

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/apperrors"
	catalogdomain "bansoko/internal/catalog/domain"
)

// The read side of lazy expansion: the list read answers from the cache and
// never fetches, while a download of an unexpanded seed expands it once and then
// hands the downloader the very bytes expansion verified.

// cachedSeedTorrentEntry is a cache row bound to the payload it stores: the
// digest covers the bytes and the manifest projects them, so the entry passes
// the same validation a written row does.
func cachedSeedTorrentEntry(t *testing.T, locator string, payload []byte) catalogdomain.SeedTorrentCache {
	t.Helper()
	digest := catalogdomain.DigestBytes(payload)
	file := catalogdomain.TorrentFile{
		Ordinal: 0, SourcePath: "Show S1/ep1.mkv", Filename: "ep1.mkv",
		SizeBytes: 1000, MediaKind: "video",
	}
	file.FileRef = catalogdomain.TorrentFileRef(digest, file.SourcePath)
	files := []catalogdomain.TorrentFile{file}
	projection, err := catalogdomain.CanonicalTorrentProjectionDigest(digest, "parser-v1", files)
	if err != nil {
		t.Fatalf("project the cached files: %v", err)
	}
	return catalogdomain.SeedTorrentCache{
		Locator: locator, Payload: append([]byte(nil), payload...), PayloadDigest: digest,
		FetchedAt: time.Now().UTC(),
		Manifest: catalogdomain.TorrentManifest{
			PayloadDigest: digest, ParserVersion: "parser-v1", TorrentVersion: "v1",
			TorrentName: "Show S1", InfoHashV1: strings.Repeat("c", 40),
			TotalSizeBytes: 1000, ProjectionDigest: projection, Files: files,
		},
	}
}

// lazySeedSnapshotWith returns a snapshot holding a pre-lazy seed (its own
// payload and manifest, as a snapshot published before lazy expansion carries)
// plus a lazy seed that has RSS facts only.
func lazySeedSnapshotWith(t *testing.T) (catalogdomain.ProviderFeedSnapshot, catalogdomain.ProviderFeedSnapshotSeed) {
	t.Helper()
	expanded := seedExpansionSeed(t, seedExpansionInfoHash, catalogdomain.TorrentAcquisitionAvailable)
	expanded.StableSeedKey = "seed-expanded"
	expanded.Locator = "https://mikanani.me/Download/20261017/expanded.torrent"
	sealSnapshotSeed(&expanded, seedExpansionPayload(t))
	lazy := seedExpansionSeed(t, "", catalogdomain.TorrentAcquisitionNotApplicable)
	lazy.StableSeedKey = "seed-lazy"
	lazy.Locator = "https://mikanani.me/Download/20261018/lazy.torrent"
	snapshot := seedExpansionSnapshot(expanded)
	snapshot.Seeds = append(snapshot.Seeds, lazy)
	return snapshot, lazy
}

// sealSnapshotSeed gives a seed its own payload, digest and manifest — the
// shape a snapshot published before previews stopped prefetching carries, where
// the bytes and the projection were sealed together.
func sealSnapshotSeed(seed *catalogdomain.ProviderFeedSnapshotSeed, payload acquisitionapplication.TorrentPayload) {
	digest := catalogdomain.DigestBytes(payload.Bytes)
	seed.TorrentState = catalogdomain.TorrentAcquisitionAvailable
	seed.Payload = append([]byte(nil), payload.Bytes...)
	seed.PayloadDigest = &digest
	seed.Manifest = &catalogdomain.TorrentManifest{
		PayloadDigest: digest, ParserVersion: "parser-v1", TorrentVersion: "v1",
		TorrentName: "show.mkv", InfoHashV1: payload.Metadata.InfoHashV1,
		TotalSizeBytes: 12345,
	}
}

func seedExpansionIdentity(snapshot catalogdomain.ProviderFeedSnapshot) catalogdomain.ProviderRSSPreviewIdentity {
	return catalogdomain.ProviderRSSPreviewIdentity{
		SnapshotID: snapshot.ID, SnapshotVersion: snapshot.Version, ContentHash: snapshot.ContentHash,
	}
}

// The list read asks the cache for the lazy seeds only, in one batch, and never
// writes: a seed the snapshot already sealed keeps its own manifest, so the
// cache can neither be consulted for it nor override it.
func TestReadSeedTorrentManifestsReadsTheCacheOnlyForLazySeeds(t *testing.T) {
	snapshot, lazy := lazySeedSnapshotWith(t)
	service, cache := seedExpansionService(t, snapshot, nil)
	cache.entries[lazy.Locator] = cachedSeedTorrentEntry(t, lazy.Locator, seedExpansionPayload(t).Bytes)

	manifests, err := service.ReadSeedTorrentManifests(context.Background(), "mikan", "123", "456", seedExpansionIdentity(snapshot))
	if err != nil {
		t.Fatalf("read seed torrent manifests: %v", err)
	}
	if len(manifests) != 1 || manifests["seed-lazy"] == nil {
		t.Fatalf("manifests = %+v, want only the lazy seed", manifests)
	}
	if manifests["seed-lazy"].TorrentName != "Show S1" {
		t.Fatalf("cached manifest = %+v, want the row's projection", manifests["seed-lazy"])
	}
	if manifests["seed-expanded"] != nil {
		t.Fatalf("the snapshot's own manifest must win over anything in the cache: %+v", manifests)
	}
	if len(cache.batches) != 1 || len(cache.batches[0]) != 1 || cache.batches[0][0] != lazy.Locator {
		t.Fatalf("batch reads = %v, want exactly one read of the lazy locator", cache.batches)
	}
	if cache.writeCount() != 0 {
		t.Fatalf("a list read wrote %d cache rows, want none", cache.writeCount())
	}
}

// A group whose snapshot already carried every manifest is answered without
// reaching the store at all.
func TestReadSeedTorrentManifestsTouchesNoStoreWhenEverySeedWasExpanded(t *testing.T) {
	snapshot, _ := lazySeedSnapshotWith(t)
	// Seal the lazy seed too: with every seed expanded by the snapshot itself,
	// there is nothing left for the cache to answer.
	sealSnapshotSeed(&snapshot.Seeds[1], seedExpansionPayload(t))
	service, cache := seedExpansionService(t, snapshot, nil)

	manifests, err := service.ReadSeedTorrentManifests(context.Background(), "mikan", "123", "456", seedExpansionIdentity(snapshot))
	if err != nil {
		t.Fatalf("read seed torrent manifests: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatalf("manifests = %+v, want none: the snapshot covered every seed", manifests)
	}
	if len(cache.batches) != 0 {
		t.Fatalf("cache reads = %v, want none", cache.batches)
	}
}

// An identity the store no longer retains fails closed before the cache is
// consulted, so a caller can never render one snapshot's cache against another
// snapshot's seeds. The boundary answers the typed 409 the endpoint declares —
// never a raw domain sentinel, which the transport would map onto unknown-500.
func TestReadSeedTorrentManifestsFailsClosedOnADriftedIdentity(t *testing.T) {
	snapshot, _ := lazySeedSnapshotWith(t)
	service, cache := seedExpansionService(t, snapshot, nil)
	identity := seedExpansionIdentity(snapshot)
	identity.SnapshotVersion++

	_, err := service.ReadSeedTorrentManifests(context.Background(), "mikan", "123", "456", identity)
	var business *apperrors.Business
	if err == nil || !errors.As(err, &business) || business.Status != 409 || business.Code != "FEED_SNAPSHOT_REFRESH_REQUIRED" {
		t.Fatalf("drift error = %v, want typed 409 FEED_SNAPSHOT_REFRESH_REQUIRED", err)
	}
	if len(cache.batches) != 0 {
		t.Fatalf("cache reads = %v, want none for a rejected identity", cache.batches)
	}
}

// Downloading an unexpanded seed completes: it expands through the shared use
// case, fetches exactly once, and hands back the cached bytes rather than a
// second, independent download.
func TestReadProviderEntrySeedPayloadExpandsAndReusesTheCachedBytes(t *testing.T) {
	snapshot, _ := lazySeedSnapshotWith(t)
	payload := seedExpansionPayload(t)
	// The lazy seed publishes the identity of the payload the provider serves.
	snapshot.Seeds[1].InfoHash = nil
	hash := payload.Metadata.InfoHashV1
	snapshot.Seeds[1].InfoHash = &hash
	// Expansion replaces the bytes, never the feed facts: the frozen AniBT tag
	// set must survive it, whichever branch of the read served the seed.
	snapshot.Seeds[1].AniBTTags = &acquisitiondomain.StoredAniBTTags{Schema: acquisitiondomain.AniBTTagSchemaV1, Tags: []string{"resolution:1080p"}}
	fetcher := &fakeSeedTorrentFetcher{payload: payload}
	service, cache := seedExpansionService(t, snapshot, fetcher)

	seed, err := service.ReadProviderEntrySeedPayload(context.Background(), "mikan", "123", "456", seedExpansionIdentity(snapshot), "seed-lazy")
	if err != nil {
		t.Fatalf("read the lazy seed's payload: %v", err)
	}
	if seed.AniBTTags == nil || !reflect.DeepEqual(seed.AniBTTags.Tags, []string{"resolution:1080p"}) {
		t.Fatalf("expanded seed tags = %+v, want the snapshot's frozen set", seed.AniBTTags)
	}
	if !bytes.Equal(seed.Payload, payload.Bytes) {
		t.Fatalf("downloaded bytes differ from the expanded bytes")
	}
	if seed.PayloadDigest == nil || *seed.PayloadDigest != catalogdomain.DigestBytes(payload.Bytes) {
		t.Fatalf("payload digest = %v, want the digest of the fetched bytes", seed.PayloadDigest)
	}
	if seed.Manifest == nil || seed.Manifest.FileCount() != 1 {
		t.Fatalf("manifest = %+v, want the expansion's projection", seed.Manifest)
	}
	if seed.TorrentState != catalogdomain.TorrentAcquisitionAvailable {
		t.Fatalf("torrent state = %q, want available", seed.TorrentState)
	}
	if fetcher.callCount() != 1 || cache.writeCount() != 1 {
		t.Fatalf("fetches = %d cache writes = %d, want one of each", fetcher.callCount(), cache.writeCount())
	}

	// A second download reuses the cached bytes instead of fetching again.
	again, err := service.ReadProviderEntrySeedPayload(context.Background(), "mikan", "123", "456", seedExpansionIdentity(snapshot), "seed-lazy")
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !bytes.Equal(again.Payload, payload.Bytes) || fetcher.callCount() != 1 {
		t.Fatalf("second download fetched %d times, want the cached bytes", fetcher.callCount())
	}
}

// A seed whose bytes cannot be verified is not downloadable: the download fails
// closed as refresh_required and the cache stays untouched, so no unverified
// payload ever reaches a downloader.
func TestReadProviderEntrySeedPayloadFailsClosedWhenTheSeedCannotBeExpanded(t *testing.T) {
	snapshot, _ := lazySeedSnapshotWith(t)
	fetcher := &fakeSeedTorrentFetcher{err: acquisitionapplication.ErrTorrentPayloadMissing}
	service, cache := seedExpansionService(t, snapshot, fetcher)

	_, err := service.ReadProviderEntrySeedPayload(context.Background(), "mikan", "123", "456", seedExpansionIdentity(snapshot), "seed-lazy")
	if err == nil {
		t.Fatal("an unexpandable seed must not be downloadable")
	}
	if !errors.Is(err, catalogdomain.ErrProviderFeedSnapshotNotFound) {
		t.Fatalf("error = %v, want a refresh-required outcome", err)
	}
	if !strings.Contains(err.Error(), SeedTorrentErrorMissing) {
		t.Fatalf("error = %v, want the bounded %q reason", err, SeedTorrentErrorMissing)
	}
	if cache.writeCount() != 0 {
		t.Fatalf("cache writes = %d, want none for unverified bytes", cache.writeCount())
	}
}
