package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// The keyword-search one-shot is a narrow exception to the human caller matrix:
// a search snapshot has no catalog entry, so the human carries a complete
// origin=oneshot draft of its own and must still prove the exact search
// snapshot. These tests pin the exception's boundaries: it never widens the
// provider/custom rules, never accepts a saved candidate revision, and never
// accepts a non-search seed.

func searchBindingSeed() SelectedSeed {
	return SelectedSeed{
		Provider:            animedomain.ProviderKindMikan,
		ProviderEntryRef:    "search-scope:" + strings.Repeat("a", 64),
		ProviderGroupRef:    "search-query:" + strings.Repeat("b", 64),
		SnapshotID:          "snap-1",
		SnapshotVersion:     2,
		SnapshotContentHash: "sha256:" + strings.Repeat("c", 64),
		StableSeedKey:       "seed-1",
		Locator:             "https://mikan.example.test/Download/20261025/" + strings.Repeat("d", 40) + ".torrent",
		Title:               "[示例字幕组] 示例作品 - 03 [1080p]",
		SnapshotKind:        string(animecatalogdomain.FeedSnapshotKindSearch),
	}
}

func searchBindingDraft() animedomain.AnimeDraft {
	label := "示例作品"
	return animedomain.AnimeDraft{
		Origin:        animedomain.SubscriptionOriginOneshot,
		Title:         "示例作品",
		InstanceLabel: &label,
		Business:      &animedomain.BusinessSnapshot{SchemaVersion: "anime-business-snapshot-v1"},
	}
}

func TestHumanSearchSeedBindingAcceptsOnlyTheNarrowShape(t *testing.T) {
	currentInput := AcquireSeedInput{InputSource: AcquireSeedInputSourceCurrentInput}
	if err := validateHumanSearchSeedBinding(searchBindingDraft(), searchBindingSeed(), currentInput); err != nil {
		t.Fatalf("the exact search one-shot shape was refused: %v", err)
	}

	cases := map[string]struct {
		draft animedomain.AnimeDraft
		seed  SelectedSeed
		input AcquireSeedInput
	}{
		"provider draft": {
			draft: func() animedomain.AnimeDraft {
				d := searchBindingDraft()
				d.Origin = animedomain.SubscriptionOriginMikan
				ref := "1001"
				d.OriginEntryRef = &ref
				return d
			}(),
			seed: searchBindingSeed(), input: currentInput,
		},
		"custom draft": {
			draft: func() animedomain.AnimeDraft {
				d := searchBindingDraft()
				d.Origin = animedomain.SubscriptionOriginCustom
				return d
			}(),
			seed: searchBindingSeed(), input: currentInput,
		},
		"saved revision": {
			draft: searchBindingDraft(), seed: searchBindingSeed(),
			input: AcquireSeedInput{CandidateID: "candidate-1", Revision: 1},
		},
		"custom provider": {
			draft: searchBindingDraft(),
			seed: func() SelectedSeed {
				s := searchBindingSeed()
				s.Provider = animedomain.ProviderKindCustom
				return s
			}(),
			input: currentInput,
		},
		"provider entry scope": {
			draft: searchBindingDraft(),
			seed:  func() SelectedSeed { s := searchBindingSeed(); s.ProviderEntryRef = "1001"; return s }(),
			input: currentInput,
		},
		"provider group scope": {
			draft: searchBindingDraft(),
			seed:  func() SelectedSeed { s := searchBindingSeed(); s.ProviderGroupRef = "612"; return s }(),
			input: currentInput,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateHumanSearchSeedBinding(tc.draft, tc.seed, tc.input); err == nil {
				t.Fatalf("%s was accepted by the search exception", name)
			}
		})
	}
}

// A non-search seed keeps the pre-existing matrix: an origin=oneshot draft is
// still the agent form, so a human cannot use it for a feed seed.
func TestHumanFeedSeedStillRejectsTheOneshotDraft(t *testing.T) {
	seed := SelectedSeed{
		Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "1001", ProviderGroupRef: "612",
		SnapshotID: "snap-1", SnapshotVersion: 1, SnapshotContentHash: "sha256:" + strings.Repeat("c", 64),
		StableSeedKey: "seed-1", Locator: "https://mikan.example.test/a.torrent",
		Title: "[示例字幕组] 示例作品 - 03", SnapshotKind: string(animecatalogdomain.FeedSnapshotKindProviderFeed),
	}
	err := validateAcquireSeedCaller(AcquireSeedCaller{Kind: AcquireSeedCallerHuman}, searchBindingDraft(), seed,
		AcquireSeedInput{InputSource: AcquireSeedInputSourceCurrentInput})
	if err == nil || !strings.Contains(err.Error(), "agent form") {
		t.Fatalf("a feed seed with an origin=oneshot draft: %v", err)
	}
}

// fakeSearchPreviewSource serves both feeds and search snapshots so admission can
// be driven without a database. It records which read was used, so a test can
// prove a search seed is never proven against a feed.
type fakeSearchPreviewSource struct {
	feedSnapshot   animeapplication.SeedPreviewSnapshot
	searchSnapshot animeapplication.SeedPreviewSnapshot
	feedErr        error
	searchErr      error
	feedReads      int
	searchReads    int
	payload        animeapplication.SeedPayload
}

func (f *fakeSearchPreviewSource) FetchEntryGroupSnapshotByID(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (animeapplication.SeedPreviewSnapshot, error) {
	f.feedReads++
	return f.feedSnapshot, f.feedErr
}

func (f *fakeSearchPreviewSource) FetchSearchSnapshotByID(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (animeapplication.SeedPreviewSnapshot, error) {
	f.searchReads++
	return f.searchSnapshot, f.searchErr
}

func (f *fakeSearchPreviewSource) ReadSeedPayload(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity, string) (animeapplication.SeedPayload, error) {
	return f.payload, nil
}

func (f *fakeSearchPreviewSource) ReadSearchSeedPayload(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity, string) (animecatalogdomain.ProviderFeedSnapshotSeed, error) {
	digest := f.payload.PayloadDigest
	return animecatalogdomain.ProviderFeedSnapshotSeed{
		StableSeedKey: f.payload.StableSeedKey, Locator: "https://mikan.example.test/expanded.torrent",
		LocatorDisplay: "mikan.example.test", Title: "[示例字幕组] 示例作品 - 03 [1080p]",
		TorrentState: animecatalogdomain.TorrentAcquisitionAvailable,
		Payload:      f.payload.Bytes, PayloadDigest: &digest, Manifest: f.payload.Manifest,
	}, nil
}

func searchBindingSnapshot(seed SelectedSeed) animeapplication.SeedPreviewSnapshot {
	published := seed.PublishedAt
	return animeapplication.SeedPreviewSnapshot{
		Provider: string(seed.Provider), ProviderEntryRef: seed.ProviderEntryRef, ProviderGroupRef: seed.ProviderGroupRef,
		SnapshotID: seed.SnapshotID, SnapshotVersion: seed.SnapshotVersion, ContentHash: seed.SnapshotContentHash,
		Freshness: "fresh",
		Seeds: []animeapplication.SeedPreviewFact{{
			DownloadURL: seed.Locator, LocatorDisplay: "mikan.example.test", StableSeedKey: seed.StableSeedKey,
			Title: seed.Title, PublishedAt: published, SizeBytes: seed.SizeBytes,
			InfoHash: optionalStringValue(seed.InfoHash), ExpectedPayloadDigest: optionalStringValue(seed.ExpectedPayloadDigest),
			TorrentState: animecatalogdomain.TorrentAcquisitionAvailable,
		}},
	}
}

// A search seed is proven against its search snapshot, and only that read is
// used. A refusal keeps zero writes.
func TestVerifyHumanSearchSeedBindingUsesTheSearchRead(t *testing.T) {
	seed := searchBindingSeed()
	seed.PublishedAt = searchBindingPublishedAt()
	source := &fakeSearchPreviewSource{searchSnapshot: searchBindingSnapshot(seed)}
	service := &Service{seedPreviews: source}

	fact, err := service.verifyHumanSeedPreviewBinding(context.Background(), seed)
	if err != nil {
		t.Fatalf("search binding: %v", err)
	}
	if fact.StableSeedKey != seed.StableSeedKey {
		t.Fatalf("bound fact = %+v", fact)
	}
	if source.searchReads != 1 || source.feedReads != 0 {
		t.Fatalf("search reads = %d, feed reads = %d: a search seed must never be proven against a feed", source.searchReads, source.feedReads)
	}

	// A search snapshot that no longer resolves is the typed stale refusal.
	stale := &fakeSearchPreviewSource{searchErr: animeapplication.ErrProviderFeedSnapshotRefreshRequired}
	_, err = (&Service{seedPreviews: stale}).verifyHumanSeedPreviewBinding(context.Background(), seed)
	if err == nil || !strings.Contains(err.Error(), "SEED_PREVIEW_STALE") {
		t.Fatalf("stale search snapshot: %v", err)
	}

	// A source that cannot serve search fails closed instead of using a feed.
	_, err = (&Service{seedPreviews: feedOnlyPreviewSource{snapshot: searchBindingSnapshot(seed)}}).
		verifyHumanSeedPreviewBinding(context.Background(), seed)
	if err == nil || !strings.Contains(err.Error(), "SEED_PREVIEW_UNAVAILABLE") {
		t.Fatalf("a feed-only source served a search binding: %v", err)
	}
}

// A reference-form search seed is resolved through the search read, not the
// feed read, and the resolved seed keeps the derived kind. This is the same
// builder DownloadSnapshotSeed uses, so both identity-only entries admit the
// same search one-shot instead of dropping its kind at admission.
func TestSearchReferenceResolvesThroughTheSearchRead(t *testing.T) {
	seed := searchBindingSeed()
	seed.PublishedAt = searchBindingPublishedAt()
	source := &fakeSearchPreviewSource{searchSnapshot: searchBindingSnapshot(seed)}
	resolved, err := (&Service{seedPreviews: source}).resolveSeedReference(context.Background(), seedReferenceOf(seed))
	if err != nil {
		t.Fatalf("resolve search reference: %v", err)
	}
	if resolved.SnapshotKind != string(animecatalogdomain.FeedSnapshotKindSearch) {
		t.Fatalf("resolved snapshot kind = %q, want search", resolved.SnapshotKind)
	}
	if source.searchReads != 1 || source.feedReads != 0 {
		t.Fatalf("search reads = %d, feed reads = %d: a search reference must never be proven against a feed", source.searchReads, source.feedReads)
	}
	if err := validateAcquireSeedCaller(AcquireSeedCaller{Kind: AcquireSeedCallerHuman}, searchBindingDraft(), resolved,
		AcquireSeedInput{InputSource: AcquireSeedInputSourceCurrentInput}); err != nil {
		t.Fatalf("the resolved search seed lost its admission binding: %v", err)
	}

	stale := &fakeSearchPreviewSource{searchErr: animeapplication.ErrProviderFeedSnapshotRefreshRequired}
	_, err = (&Service{seedPreviews: stale}).resolveSeedReference(context.Background(), seedReferenceOf(seed))
	if businessCode(err) != "SEED_PREVIEW_STALE" {
		t.Fatalf("stale search reference: %v", err)
	}
}

// feedOnlyPreviewSource implements the feed contract only.
type feedOnlyPreviewSource struct {
	snapshot animeapplication.SeedPreviewSnapshot
}

func (f feedOnlyPreviewSource) FetchEntryGroupSnapshotByID(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (animeapplication.SeedPreviewSnapshot, error) {
	return f.snapshot, nil
}

func (f feedOnlyPreviewSource) ReadSeedPayload(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity, string) (animeapplication.SeedPayload, error) {
	return animeapplication.SeedPayload{}, errors.New("not used")
}

func searchBindingPublishedAt() (published time.Time) {
	return time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)
}
