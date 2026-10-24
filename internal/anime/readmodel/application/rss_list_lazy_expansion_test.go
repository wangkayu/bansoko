package application

import (
	"context"
	"strings"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// Lazy seed expansion, read side. The list read never fetches a torrent: it
// renders the RSS-native facts of every seed and marks the ones the server has
// not expanded pending. "Has a manifest" is the single rule for "already
// expanded", and the expansion cache — not the snapshot — is where a lazy seed
// gets its manifest from. These tests pin that projection, the merge precedence
// between the snapshot's own manifests and the cache, and the two-level download
// gate.

// lazyExpansionManifestSource is a test double for the kernel's manifest port.
// It records the exact identity it was asked about, so a test can prove the read
// asks for the identity the caller named and never for another one.
type lazyExpansionManifestSource struct {
	manifests    map[string]*animecatalogdomain.TorrentManifest
	err          error
	calls        int
	lastProvider string
	lastEntryRef string
	lastGroupRef string
	lastIdentity animecatalogdomain.ProviderRSSPreviewIdentity
}

func (f *lazyExpansionManifestSource) ReadSeedTorrentManifests(_ context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error) {
	f.calls++
	f.lastProvider, f.lastEntryRef, f.lastGroupRef = provider, entryRef, groupRef
	f.lastIdentity = identity
	return f.manifests, f.err
}

// lazySeedSnapshot is a one-seed snapshot the server has not expanded: the
// persisted snapshot records not_applicable, exactly as a lazy refresh writes,
// and carries no manifest of its own.
func lazySeedSnapshot() SeedPreviewSnapshot {
	published := time.Date(2026, 7, 29, 16, 30, 0, 0, time.UTC)
	size := int64(1234)
	return SeedPreviewSnapshot{
		Provider: "anibt", ProviderEntryRef: "1001", ProviderGroupRef: "12",
		SnapshotID: "snap-12", SnapshotVersion: 2,
		ContentHash: "sha256:" + strings.Repeat("b", 64), Freshness: "fresh",
		Seeds: []SeedPreviewFact{{
			StableSeedKey:  "seed-01",
			Title:          "[RealGroup] Preview Show - 01 [1080p]",
			DownloadURL:    "https://provider.example.test/show-01.torrent",
			LocatorDisplay: "https://provider.example.test/show-01.torrent",
			InfoHash:       strings.Repeat("a", 40),
			PublishedAt:    published,
			SizeBytes:      &size,
			TorrentState:   animecatalogdomain.TorrentAcquisitionNotApplicable,
		}},
	}
}

// An unexpanded seed renders pending in both dimensions and delivers nothing:
// the row keeps its RSS-native facts, lists no file and blocks the download with
// seed_not_expanded while the kind stays none, because expanding is not a
// download.
func TestRSSListPendingItemKeepsRSSFactsAndBlocksDelivery(t *testing.T) {
	snapshot := lazySeedSnapshot()
	seed := snapshot.Seeds[0]
	feed := RSSListFeedStatus{State: RSSListFeedFresh, SnapshotID: snapshot.SnapshotID}

	item := rssListPendingItem(rssListSeedItem(snapshot.SnapshotID, seed, nil), true, feed)

	if item.Torrent.State != RSSListTorrentPending || item.Torrent.Manifest != nil {
		t.Fatalf("unexpanded torrent state = %+v, want pending without a manifest", item.Torrent)
	}
	if item.Torrent.ErrorCode != "" || item.Torrent.ErrorReason != "" {
		t.Fatalf("pending is not a failure, got %+v", item.Torrent)
	}
	if item.Evaluation.State != RSSListEvaluationPending {
		t.Fatalf("evaluation state = %q, want %q", item.Evaluation.State, RSSListEvaluationPending)
	}
	if item.Files == nil || len(item.Files) != 0 {
		t.Fatalf("files = %+v, want an empty row list", item.Files)
	}
	if item.DownloadAction.Allowed || item.DownloadAction.Kind != RSSListDownloadNone || item.DownloadAction.ReasonCode != RSSListReasonSeedNotExpanded {
		t.Fatalf("download action = %+v, want a blocked seed_not_expanded/none action", item.DownloadAction)
	}
	// The row is still identified and readable without the torrent: these are the
	// facts a human judges the seed by before deciding to expand it.
	if item.SnapshotID != "snap-12" || item.StableSeedKey != "seed-01" || item.Seed.Title == "" ||
		item.Seed.PublishedAt == nil || item.Seed.SizeBytes == nil || item.Seed.InfoHash == "" || item.Seed.LocatorDisplay == "" {
		t.Fatalf("pending row lost its RSS-native facts: %+v", item)
	}
}

// The torrent state is decided by the manifest alone; the snapshot's persisted
// state is pre-expansion evidence. A persisted failure still renders its bounded
// reason, because the snapshot is the record of it.
func TestRSSListTorrentStateFollowsTheManifestNotThePersistedState(t *testing.T) {
	manifest := singleFileManifest(t, "Preview Show - 01.mkv")
	seed := SeedPreviewFact{TorrentState: animecatalogdomain.TorrentAcquisitionNotApplicable}
	if got := rssListTorrentState(seed, manifest); got.State != animecatalogdomain.TorrentAcquisitionAvailable || got.Manifest != manifest {
		t.Fatalf("with a manifest the state = %+v, want available with that manifest", got)
	}
	seed.TorrentState = animecatalogdomain.TorrentAcquisitionUnavailable
	seed.TorrentErrorCode = "torrent_metadata_unavailable"
	seed.TorrentErrorReason = "the torrent payload could not be parsed"
	got := rssListTorrentState(seed, nil)
	if got.State != animecatalogdomain.TorrentAcquisitionUnavailable || got.ErrorCode != seed.TorrentErrorCode || got.ErrorReason != seed.TorrentErrorReason {
		t.Fatalf("persisted failure = %+v, want the snapshot's bounded reason", got)
	}
	seed.TorrentState = animecatalogdomain.TorrentAcquisitionNotApplicable
	seed.TorrentErrorCode, seed.TorrentErrorReason = "", ""
	if got := rssListTorrentState(seed, nil); got.State != RSSListTorrentPending {
		t.Fatalf("not_applicable without a manifest = %+v, want pending", got)
	}
}

// The download action matrix under lazy expansion. Expansion comes first: a
// pending seed is told to expand whether or not its feed published an
// infohash, because the fetched torrent's infohash becomes the identity of a
// seed whose feed has none. After expansion the identity is the feed's
// infohash when present (it must name the fetched torrent), else the
// manifest's own infohash; strong_identity_missing is left only for an
// expanded torrent whose infohash cannot be computed.
func TestRSSListDownloadActionIdentityMatrix(t *testing.T) {
	fresh := RSSListFeedStatus{State: RSSListFeedFresh}
	published := timePtr(time.Now().UTC())
	v1 := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	v2 := strings.Repeat("c", 64)
	digest := "sha256:" + strings.Repeat("d", 64)
	manifest := func(v1, v2, payload string) *animecatalogdomain.TorrentManifest {
		return &animecatalogdomain.TorrentManifest{InfoHashV1: v1, InfoHashV2: v2, PayloadDigest: payload}
	}
	pending := RSSListTorrentState{State: RSSListTorrentPending}
	available := func(m *animecatalogdomain.TorrentManifest) RSSListTorrentState {
		return RSSListTorrentState{State: animecatalogdomain.TorrentAcquisitionAvailable, Manifest: m}
	}
	evaluated := RSSListEvaluation{State: RSSListEvaluationEvaluated}
	for _, tc := range []struct {
		name  string
		seed  RSSListSeedFacts
		state RSSListTorrentState
		feed  RSSListFeedStatus
		want  string // empty = allowed
	}{
		{name: "pending with feed infohash", seed: RSSListSeedFacts{InfoHash: v1}, state: pending, feed: fresh, want: RSSListReasonSeedNotExpanded},
		{name: "pending without feed infohash", seed: RSSListSeedFacts{}, state: pending, feed: fresh, want: RSSListReasonSeedNotExpanded},
		{name: "pending on a stale snapshot", seed: RSSListSeedFacts{}, state: pending, feed: RSSListFeedStatus{State: RSSListFeedStale}, want: RSSListReasonSnapshotRefreshReq},
		{name: "expanded, feed infohash matches v1", seed: RSSListSeedFacts{InfoHash: v1}, state: available(manifest(v1, "", digest)), feed: fresh},
		{name: "expanded, feed infohash matches v2", seed: RSSListSeedFacts{InfoHash: v2}, state: available(manifest(v1, v2, digest)), feed: fresh},
		{name: "expanded, no feed infohash, manifest v1", seed: RSSListSeedFacts{}, state: available(manifest(v1, "", digest)), feed: fresh},
		{name: "expanded, no feed infohash, manifest v2 only", seed: RSSListSeedFacts{}, state: available(manifest("", v2, digest)), feed: fresh},
		{name: "expanded, feed infohash disagrees", seed: RSSListSeedFacts{InfoHash: other}, state: available(manifest(v1, "", digest)), feed: fresh, want: RSSListReasonSeedIdentityMismatch},
		{name: "expanded, published digest disagrees", seed: RSSListSeedFacts{ExpectedPayloadDigest: "sha256:" + strings.Repeat("e", 64)}, state: available(manifest(v1, "", digest)), feed: fresh, want: RSSListReasonSeedIdentityMismatch},
		{name: "expanded, published digest matches", seed: RSSListSeedFacts{ExpectedPayloadDigest: digest}, state: available(manifest("", "", digest)), feed: fresh},
		{name: "expanded, no infohash anywhere", seed: RSSListSeedFacts{}, state: available(manifest("", "", digest)), feed: fresh, want: RSSListReasonStrongIdentityMissing},
		{name: "torrent unavailable", seed: RSSListSeedFacts{}, state: RSSListTorrentState{State: animecatalogdomain.TorrentAcquisitionUnavailable}, feed: fresh, want: RSSListReasonTorrentUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := tc.seed
			seed.PublishedAt = published
			item := RSSListItem{Seed: seed, Torrent: tc.state, Evaluation: evaluated}
			if tc.state.State == RSSListTorrentPending {
				item.Evaluation = RSSListEvaluation{State: RSSListEvaluationPending}
			}
			got := rssListDownloadAction(true, tc.feed, item)
			if tc.want == "" {
				if !got.Allowed || got.Kind != RSSListDownloadOneShot || got.ReasonCode != "" {
					t.Fatalf("download action = %+v, want an allowed one_shot action", got)
				}
				return
			}
			if got.Allowed || got.Kind != RSSListDownloadNone || got.ReasonCode != tc.want || got.Reason == "" {
				t.Fatalf("download action = %+v, want blocked by %q", got, tc.want)
			}
		})
	}
}

// Only the seeds with a manifest become feed items. Evaluation, filtering and
// rendering all run per item, so an unexpanded seed is never parsed: there is no
// structure to judge, and a verdict the server did not compute would be a guess.
func TestExpandedFeedItemsOmitsUnexpandedSeeds(t *testing.T) {
	snapshot := lazySeedSnapshot()
	snapshot.Seeds = append(snapshot.Seeds, SeedPreviewFact{
		StableSeedKey: "seed-02", Title: "[RealGroup] Preview Show - 02 [1080p]",
		DownloadURL: "https://provider.example.test/show-02.torrent",
	})
	manifests := map[string]*animecatalogdomain.TorrentManifest{"seed-02": singleFileManifest(t, "Preview Show - 02.mkv")}

	items := expandedFeedItems(snapshot, manifests, "anibt")
	if len(items) != 1 || items[0].GUID != "seed-02" {
		t.Fatalf("items = %+v, want only the expanded seed", items)
	}
	empty := expandedFeedItems(snapshot, map[string]*animecatalogdomain.TorrentManifest{}, "anibt")
	if len(empty) != 0 {
		t.Fatalf("a fully pending group must produce no item to evaluate, got %+v", empty)
	}
}

// The merge is precedence, not union: a snapshot sealed before lazy expansion
// carries its own manifests and they stay the authority, while the cache fills
// only the seeds the snapshot does not cover. The cache is read once, for the
// exact identity the caller named, and never fetches or writes.
func TestExpandedSeedManifestsKeepsSnapshotManifestsOverTheCache(t *testing.T) {
	snapshot := manifestPreviewSnapshot(t, "12")
	snapshot.Seeds = append(snapshot.Seeds, SeedPreviewFact{StableSeedKey: "seed-02", Title: "[RealGroup] Preview Show - 02 [1080p]"})
	fromCache := singleFileManifest(t, "cached.torrent")
	source := &lazyExpansionManifestSource{manifests: map[string]*animecatalogdomain.TorrentManifest{
		// A different manifest for a seed the snapshot already sealed: it must lose.
		"seed-01": singleFileManifest(t, "cache-must-lose.torrent"),
		"seed-02": fromCache,
	}}
	service := &Service{seedTorrentManifests: source}
	ref := previewTestRef(animedomain.ProviderKindAnibt, "12")

	manifests, err := service.expandedSeedManifests(context.Background(), ref, snapshot)
	if err != nil {
		t.Fatalf("resolve manifests: %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("cache reads = %d, want exactly one batch read", source.calls)
	}
	if source.lastProvider != "anibt" || source.lastEntryRef != "1001" || source.lastGroupRef != "12" {
		t.Fatalf("cache read identity = %q/%q/%q", source.lastProvider, source.lastEntryRef, source.lastGroupRef)
	}
	if source.lastIdentity.SnapshotID != ref.SnapshotID || source.lastIdentity.SnapshotVersion != ref.SnapshotVersion || source.lastIdentity.ContentHash != ref.ContentHash {
		t.Fatalf("cache read identity = %+v, want the caller's %+v", source.lastIdentity, ref.Identity())
	}
	if got := manifests["seed-01"]; got == nil || got.TorrentName != "Preview Show S1" {
		t.Fatalf("the snapshot's own manifest must win, got %+v", got)
	}
	if got := manifests["seed-02"]; got != fromCache {
		t.Fatalf("the cache must fill a seed the snapshot does not cover, got %+v", got)
	}

	// An unwired cache is not an error: every lazy seed then renders pending.
	unwired, err := (&Service{}).expandedSeedManifests(context.Background(), ref, snapshot)
	if err != nil {
		t.Fatalf("an unwired cache must not fail the read: %v", err)
	}
	if unwired["seed-02"] != nil || unwired["seed-01"] == nil {
		t.Fatalf("unwired cache manifests = %+v, want snapshot-only", unwired)
	}
}

// An identity the cache can no longer resolve fails the read closed instead of
// rendering a different snapshot's structure.
func TestExpandedSeedManifestsFailsClosedOnStaleIdentity(t *testing.T) {
	source := &lazyExpansionManifestSource{err: ErrProviderFeedSnapshotRefreshRequired}
	service := &Service{seedTorrentManifests: source}
	_, err := service.expandedSeedManifests(context.Background(), previewTestRef(animedomain.ProviderKindAnibt, "12"), lazySeedSnapshot())
	if err == nil {
		t.Fatal("a stale cache identity must not be rendered")
	}
	if code := businessCode(t, err); code != "FEED_SNAPSHOT_REFRESH_REQUIRED" {
		t.Fatalf("error code = %q, want FEED_SNAPSHOT_REFRESH_REQUIRED", code)
	}

}

func timePtr(value time.Time) *time.Time { return &value }
