package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// A keyword search seed's bytes must come from the search snapshot path, never
// from the feed payload reader: a search row has no catalog entry and no provider
// group, so the feed reader would be asked for a scope it can never resolve. These
// tests exercise the production adapter the composition root wires
// (SeedPreviewSourceWithKeywordSearch), not a hand-written fake, so the wiring gap
// that answered SEED_PAYLOAD_READER_NOT_CONFIGURED cannot come back unnoticed.

const (
	searchPayloadScope = "search-scope:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	searchPayloadQuery = "search-query:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	searchPayloadKey   = "seed-1"
)

func searchPayloadIdentity() animecatalogdomain.ProviderRSSPreviewIdentity {
	return animecatalogdomain.ProviderRSSPreviewIdentity{
		SnapshotID: "search-snapshot-1", SnapshotVersion: 3,
		ContentHash: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
}

// searchPayloadSeed builds one persisted search seed with exact bytes, its
// digest and a manifest so the adapter has to hand every fact through.
func searchPayloadSeed(t *testing.T, mutate func(*animecatalogdomain.ProviderFeedSnapshotSeed)) animecatalogdomain.ProviderFeedSnapshotSeed {
	t.Helper()
	bytes := []byte("d4:infod4:name8:show.mkv6:lengthi12345eee")
	digest := animecatalogdomain.DigestBytes(bytes)
	filename := "show.mkv"
	seed := animecatalogdomain.ProviderFeedSnapshotSeed{
		StableSeedKey:   searchPayloadKey,
		Locator:         "https://anibt.example.test/api/torrent/" + searchPayloadKey + ".torrent",
		LocatorDisplay:  "https://anibt.example.test/api/torrent/" + searchPayloadKey + ".torrent",
		Title:           "[示例字幕组] 示例作品 - 03 [1080p]",
		TorrentState:    animecatalogdomain.TorrentAcquisitionAvailable,
		Payload:         bytes,
		PayloadDigest:   &digest,
		TorrentFilename: &filename,
		Manifest: &animecatalogdomain.TorrentManifest{
			PayloadDigest: digest, ParserVersion: "torrent-parser-v1", TorrentVersion: "v1",
			TorrentName: "show", TotalSizeBytes: int64(len(bytes)),
			ProjectionDigest: "sha256:" + strings.Repeat("d", 64),
		},
	}
	if mutate != nil {
		mutate(&seed)
	}
	return seed
}

// searchPayloadProbe records which reads the adapter uses, so a test can prove the
// search seed never travelled through the feed payload reader.
type searchPayloadProbe struct {
	feedSnapshotReads  int
	feedPayloadReads   int
	searchSnapshotRead int
	searchPayloadReads int
	searchScope        string
	searchQuery        string
	seed               animecatalogdomain.ProviderFeedSnapshotSeed
	err                error
}

func (p *searchPayloadProbe) source() SeedPreviewSource {
	return SeedPreviewSourceWithKeywordSearch(
		func(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error) {
			p.feedSnapshotReads++
			return animecatalogdomain.ProviderRSSPreviewResult{}, errors.New("a search seed must not read a feed snapshot")
		},
		func(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity, string) (animecatalogdomain.ProviderFeedSnapshotSeed, error) {
			p.feedPayloadReads++
			return animecatalogdomain.ProviderFeedSnapshotSeed{}, errors.New("a search seed must not read a feed payload")
		},
		func(_ context.Context, _, _, _ string, _ animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error) {
			p.searchSnapshotRead++
			return animecatalogdomain.ProviderRSSPreviewResult{}, nil
		},
		func(_ context.Context, _, scopeRef, queryRef string, _ animecatalogdomain.ProviderRSSPreviewIdentity, _ string) (animecatalogdomain.ProviderFeedSnapshotSeed, error) {
			p.searchPayloadReads++
			p.searchScope, p.searchQuery = scopeRef, queryRef
			return p.seed, p.err
		},
	)
}

// The production adapter forwards a search payload read to the search path, with
// the derived refs, and hands back the exact bytes and facts.
func TestSeedPreviewSourceForwardsSearchPayloadThroughTheSearchRead(t *testing.T) {
	probe := &searchPayloadProbe{seed: searchPayloadSeed(t, nil)}
	source := probe.source()

	payloadSource, ok := source.(SearchSeedPayloadSource)
	if !ok {
		t.Fatal("the wired source does not serve search seed payloads: a search one-shot would fail closed at admission")
	}
	seed, err := payloadSource.ReadSearchSeedPayload(context.Background(), "anibt", searchPayloadScope, searchPayloadQuery, searchPayloadIdentity(), searchPayloadKey)
	if err != nil {
		t.Fatalf("search payload read: %v", err)
	}
	if probe.searchPayloadReads != 1 || probe.feedPayloadReads != 0 || probe.feedSnapshotReads != 0 {
		t.Fatalf("search payload reads = %d, feed payload reads = %d, feed snapshot reads = %d: the search seed must never use the feed path",
			probe.searchPayloadReads, probe.feedPayloadReads, probe.feedSnapshotReads)
	}
	if probe.searchScope != searchPayloadScope || probe.searchQuery != searchPayloadQuery {
		t.Fatalf("the search read received %q / %q, want the derived search refs", probe.searchScope, probe.searchQuery)
	}
	// The adapter must not silently drop the identity it read.
	if seed.PayloadDigest == nil || *seed.PayloadDigest != animecatalogdomain.DigestBytes(seed.Payload) {
		t.Fatalf("returned seed digest does not bind its bytes: %+v", seed)
	}
	if seed.Manifest == nil || seed.Manifest.PayloadDigest != *seed.PayloadDigest {
		t.Fatalf("returned manifest does not match the payload: %+v", seed.Manifest)
	}
	if seed.TorrentFilename == nil || *seed.TorrentFilename != "show.mkv" {
		t.Fatalf("returned filename = %v", seed.TorrentFilename)
	}
}

// A search seed is never served by the feed payload reader, and conversely the
// feed payload read keeps its own path: the two reads stay disjoint.
func TestSeedPreviewSourceKeepsSearchAndFeedPayloadReadsDisjoint(t *testing.T) {
	probe := &searchPayloadProbe{seed: searchPayloadSeed(t, nil)}
	source := probe.source()
	payloadSource := source.(SearchSeedPayloadSource)

	if _, err := source.ReadSeedPayload(context.Background(), "anibt", "1001", "612", searchPayloadIdentity(), searchPayloadKey); err != nil {
		// The probe's feed reader returns an error on purpose; what matters is that
		// it was the one called.
		t.Logf("feed payload read refused by the probe: %v", err)
	}
	if probe.feedPayloadReads != 1 || probe.searchPayloadReads != 0 {
		t.Fatalf("feed payload reads = %d, search payload reads = %d", probe.feedPayloadReads, probe.searchPayloadReads)
	}

	if _, err := payloadSource.ReadSearchSeedPayload(context.Background(), "anibt", searchPayloadScope, searchPayloadQuery, searchPayloadIdentity(), searchPayloadKey); err != nil {
		t.Fatalf("search payload read: %v", err)
	}
	if probe.searchPayloadReads != 1 || probe.feedPayloadReads != 1 {
		t.Fatalf("after the search read: search = %d, feed = %d", probe.searchPayloadReads, probe.feedPayloadReads)
	}
}

// The search payload read keeps every fail-closed rule the feed payload read has.
func TestSeedPreviewSourceSearchPayloadFailsClosed(t *testing.T) {
	cases := map[string]struct {
		mutate  func(*animecatalogdomain.ProviderFeedSnapshotSeed)
		err     error
		want    error
		wantBiz string
	}{
		"no bytes": {
			mutate: func(seed *animecatalogdomain.ProviderFeedSnapshotSeed) {
				seed.Payload = nil
			},
			want: ErrProviderFeedSnapshotRefreshRequired,
		},
		"no digest": {
			mutate: func(seed *animecatalogdomain.ProviderFeedSnapshotSeed) {
				seed.PayloadDigest = nil
			},
			want: ErrProviderFeedSnapshotRefreshRequired,
		},
		"digest does not cover the bytes": {
			mutate: func(seed *animecatalogdomain.ProviderFeedSnapshotSeed) {
				other := animecatalogdomain.DigestBytes([]byte("different bytes"))
				seed.PayloadDigest = &other
			},
			want: ErrProviderFeedSnapshotRefreshRequired,
		},
		"stale snapshot": {
			err:  animecatalogdomain.ErrProviderFeedSnapshotStale,
			want: ErrProviderFeedSnapshotRefreshRequired,
		},
		"missing snapshot": {
			err:  animecatalogdomain.ErrProviderFeedSnapshotNotFound,
			want: ErrProviderFeedSnapshotRefreshRequired,
		},
		"mismatching snapshot": {
			err:  animecatalogdomain.ErrProviderFeedSnapshotIdentityMismatch,
			want: ErrProviderFeedSnapshotRefreshRequired,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probe := &searchPayloadProbe{seed: searchPayloadSeed(t, tc.mutate), err: tc.err}
			_, err := probe.source().(SearchSeedPayloadSource).
				ReadSearchSeedPayload(context.Background(), "anibt", searchPayloadScope, searchPayloadQuery, searchPayloadIdentity(), searchPayloadKey)
			if err == nil {
				t.Fatal("an unusable search payload was returned")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// A source wired without the search payload read (the gap this adapter change
// closes) refuses with a typed 503 instead of returning empty bytes; the failure is
// loud at the adapter, not only at the caller.
func TestSeedPreviewSourceWithoutSearchPayloadReadIsTyped503(t *testing.T) {
	source := SeedPreviewSourceFromProviderFeedSnapshot(
		func(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error) {
			return animecatalogdomain.ProviderRSSPreviewResult{}, nil
		},
		func(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity, string) (animecatalogdomain.ProviderFeedSnapshotSeed, error) {
			return animecatalogdomain.ProviderFeedSnapshotSeed{}, nil
		},
	)
	payloadSource, ok := source.(SearchSeedPayloadSource)
	if !ok {
		t.Fatal("the feed-only source lost its search payload method; callers would fail with an unknown error")
	}
	_, err := payloadSource.ReadSearchSeedPayload(context.Background(), "anibt", searchPayloadScope, searchPayloadQuery, searchPayloadIdentity(), searchPayloadKey)
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 503 || business.Code != "SEED_PAYLOAD_READER_NOT_CONFIGURED" {
		t.Fatalf("error = %v, want a typed 503 SEED_PAYLOAD_READER_NOT_CONFIGURED", err)
	}
}

// The search snapshot read is still refused when the source was wired without it,
// so the pair of search reads stays explicit.
func TestSeedPreviewSourceWithoutSearchSnapshotReadFailsClosed(t *testing.T) {
	source := SeedPreviewSourceFromProviderFeedSnapshot(
		func(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error) {
			return animecatalogdomain.ProviderRSSPreviewResult{}, nil
		},
		func(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity, string) (animecatalogdomain.ProviderFeedSnapshotSeed, error) {
			return animecatalogdomain.ProviderFeedSnapshotSeed{}, nil
		},
	)
	_, err := source.(SearchSeedPreviewSource).
		FetchSearchSnapshotByID(context.Background(), "anibt", searchPayloadScope, searchPayloadQuery, searchPayloadIdentity())
	if !errors.Is(err, ErrProviderFeedSnapshotRefreshRequired) {
		t.Fatalf("error = %v, want the typed refresh refusal", err)
	}
}
