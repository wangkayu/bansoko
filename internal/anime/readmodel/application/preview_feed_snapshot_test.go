package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	rootapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// Focused pure unit tests for the RSSListView read model's feed identity
// handling and for the provider snapshot adapter it reads through. They require
// no database and no provider network. The retired flat preview's per-source
// identity DTO is gone; the advisory identity contract below is what survives.

func previewTestDraft(origin animedomain.ProviderKind, groups ...string) animedomain.AnimeDraft {
	entryRef := "1001"
	entries := make([]animedomain.SubgroupScoreEntry, 0, len(groups))
	for _, group := range groups {
		entries = append(entries, animedomain.SubgroupScoreEntry{
			GroupRef: group, DisplayName: "Group " + group, Provider: origin,
			SelectionMode: animedomain.SelectionModeAuto, Score: 50, 
		})
	}
	return animedomain.AnimeDraft{
		Origin:         animedomain.SubscriptionOrigin(origin),
		OriginEntryRef: &entryRef,
		Title:          "Preview Show",
		Subgroups:      animedomain.SubgroupsSection{MaxRss: 3, Entries: entries},
	}
}

// previewBoundDraft is previewTestDraft with complete metadata: one BGM subject
// and one TMDB tv work with season 1. The shipped default templates name
// ${anime.year} and ${anime.season_folder}, which only a binding provides, so a
// fixture whose rows must render binds the works and states their observed year
// (observeDraftBindingsYear).
func previewBoundDraft() animedomain.AnimeDraft {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	draft.Metadata = animedomain.AnimeMetadataDraft{
		Bgm:  &animedomain.BGMBinding{WorkId: "bgm-preview"},
		Tmdb: &animedomain.TMDBBinding{WorkId: "tmdb-preview", MediaKind: animedomain.TMDBMediaKindTv, Season: 1},
	}
	return draft
}

// previewTestEffective builds the resolved selection the saved revision would
// carry: every named group is effective_selected for the revision's origin.
func previewTestEffective(origin animedomain.ProviderKind, groups ...string) animedomain.EffectiveConfig {
	entries := make([]animedomain.SubgroupScoreEntry, 0, len(groups))
	for _, group := range groups {
		entries = append(entries, animedomain.SubgroupScoreEntry{
			GroupRef: group, DisplayName: "Group " + group, Provider: origin,
			SelectionMode: animedomain.SelectionModeAuto, Score: 50, 
			EffectiveSelected: true,
		})
	}
	return animedomain.EffectiveConfig{Selection: animedomain.SubgroupsSection{MaxRss: 3, Entries: entries}}
}

func previewTestRef(provider animedomain.ProviderKind, group string) PreviewFeedSnapshotRef {
	return PreviewFeedSnapshotRef{
		Provider: provider, ProviderEntryRef: "1001", ProviderGroupRef: group,
		SnapshotID: "snap-" + group, SnapshotVersion: 2,
		ContentHash: "sha256:" + strings.Repeat("a", 64),
	}
}

// previewTestSelection is the resolved parser selection a saved revision would
// carry for these pure identity tests: the registered regex parser with
// no offset and the unbound TV default binding.
func previewTestSelection() acquisitiontarget.ParserSelectionInput {
	return acquisitiontarget.ParserSelectionInput{Kind: "regex", MediaKind: "tv"}
}

func businessCode(t *testing.T, err error) string {
	t.Helper()
	business, ok := err.(*apperrors.Business)
	if !ok {
		t.Fatalf("error %v is not a typed business error", err)
	}
	return business.Code
}

// A malformed advisory identity is refused before any snapshot read: shape
// validation is identity integrity, not a claim about the configuration.
func TestRSSListViewReadRejectsMalformedFeedIdentity(t *testing.T) {
	service := &Service{seedPreviews: &readmodelPreviewSource{}}
	cases := []struct {
		name string
		ref  PreviewFeedSnapshotRef
	}{
		{name: "empty snapshot id", ref: func() PreviewFeedSnapshotRef {
			ref := previewTestRef(animedomain.ProviderKindAnibt, "12")
			ref.SnapshotID = ""
			return ref
		}()},
		{name: "non positive version", ref: func() PreviewFeedSnapshotRef {
			ref := previewTestRef(animedomain.ProviderKindAnibt, "12")
			ref.SnapshotVersion = 0
			return ref
		}()},
		{name: "malformed hash", ref: func() PreviewFeedSnapshotRef {
			ref := previewTestRef(animedomain.ProviderKindAnibt, "12")
			ref.ContentHash = "sha256:short"
			return ref
		}()},
		{name: "unknown provider", ref: func() PreviewFeedSnapshotRef {
			ref := previewTestRef(animedomain.ProviderKindAnibt, "12")
			ref.Provider = "unknown-provider"
			return ref
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
			effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
			_, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{tc.ref}, 0)
			if code := businessCode(t, err); code != "FEED_SNAPSHOT_IDENTITY_INVALID" {
				t.Fatalf("code = %q, want FEED_SNAPSHOT_IDENTITY_INVALID", code)
			}
		})
	}
}

// One group may be identified at most once per read, so a duplicated identity
// cannot silently pick one snapshot version over another.
func TestRSSListViewReadRejectsDuplicateGroupIdentity(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
	service := &Service{seedPreviews: &readmodelPreviewSource{}}
	_, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{
		previewTestRef(animedomain.ProviderKindAnibt, "12"),
		previewTestRef(animedomain.ProviderKindAnibt, "12"),
	}, 0)
	if code := businessCode(t, err); code != "FEED_SNAPSHOT_IDENTITY_INVALID" {
		t.Fatalf("code = %q, want FEED_SNAPSHOT_IDENTITY_INVALID", code)
	}
}

// A selected group with no supplied identity is an explicit snapshot_required
// state and costs no snapshot read: the read model never falls back to a live
// provider fetch to fill the gap.
func TestRSSListViewReadReportsSnapshotRequiredWithoutReading(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
	source := &readmodelPreviewSource{}
	service := &Service{seedPreviews: source}
	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), nil, 0)
	if err != nil {
		t.Fatalf("a missing advisory identity must not fail the read: %v", err)
	}
	if source.calls != 0 {
		t.Fatalf("snapshot reads = %d, want 0", source.calls)
	}
	if len(view.Groups) != 1 || view.Groups[0].Feed.State != RSSListFeedSnapshotRequired {
		t.Fatalf("group feed = %+v, want snapshot_required", view.Groups)
	}
	if len(view.Groups[0].Items) != 0 {
		t.Fatalf("a snapshot_required group must not fabricate items: %+v", view.Groups[0].Items)
	}
}

// An identity belonging to another entry or group is advisory only: it is
// echoed as unused instead of failing or being rendered.
func TestRSSListViewReadTreatsForeignIdentityAsUnused(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
	source := &readmodelPreviewSource{}
	service := &Service{seedPreviews: source}
	foreign := previewTestRef(animedomain.ProviderKindAnibt, "99")
	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{foreign}, 0)
	if err != nil {
		t.Fatalf("a foreign identity must not fail the read: %v", err)
	}
	if source.calls != 0 {
		t.Fatalf("snapshot reads = %d, want 0 for an unrelated identity", source.calls)
	}
	if len(view.UnusedFeedIdentities) != 1 || view.UnusedFeedIdentities[0].ProviderGroupRef != "99" {
		t.Fatalf("unused identities = %+v, want the foreign identity", view.UnusedFeedIdentities)
	}
	if len(view.Groups) != 1 || view.Groups[0].Feed.State != RSSListFeedSnapshotRequired {
		t.Fatalf("group feed = %+v, want snapshot_required", view.Groups)
	}
}

// Exactly one identity per selected group is read once and echoed on the group
// it belongs to.
func TestRSSListViewReadReadsEachSelectedGroupOnce(t *testing.T) {
	// The revision selects two groups; supplying exactly one identity per group
	// sends exactly two identity reads and never falls back to a live read.
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12", "13")
	source := &readmodelPreviewSource{}
	service := &Service{seedPreviews: source}
	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{
		previewTestRef(animedomain.ProviderKindAnibt, "12"),
		previewTestRef(animedomain.ProviderKindAnibt, "13"),
	}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source.calls != 2 {
		t.Fatalf("snapshot reads = %d, want exactly 2", source.calls)
	}
	if len(view.Groups) != 2 || len(view.UnusedFeedIdentities) != 0 {
		t.Fatalf("groups/unused = %+v / %+v", view.Groups, view.UnusedFeedIdentities)
	}
	for index, group := range []string{"12", "13"} {
		if view.Groups[index].Feed.SnapshotID != "snap-"+group {
			t.Fatalf("group %s did not echo its exact identity: %+v", group, view.Groups[index].Feed)
		}
	}
}

// An unwired snapshot store is a typed infrastructure refusal, never a silent
// empty read.
func TestRSSListViewReadFailsClosedWhenSnapshotStoreUnwired(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
	service := &Service{}
	_, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if code := businessCode(t, err); code != "FEED_SNAPSHOT_UNAVAILABLE" {
		t.Fatalf("code = %q, want FEED_SNAPSHOT_UNAVAILABLE", code)
	}
}

// A stale-or-missing snapshot store answer becomes the explicit refresh-required
// failure the caller can act on.
func TestRSSListViewReadPropagatesRefreshRequiredFromSource(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
	service := &Service{seedPreviews: &readmodelPreviewSource{err: fmt.Errorf("%w: removed", ErrProviderFeedSnapshotRefreshRequired)}}
	_, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if code := businessCode(t, err); code != "FEED_SNAPSHOT_REFRESH_REQUIRED" {
		t.Fatalf("code = %q, want FEED_SNAPSHOT_REFRESH_REQUIRED", code)
	}
}

func TestSeedPreviewSourceAdapterMapsRefreshRequiredSentinels(t *testing.T) {
	sentinels := map[string]error{
		"not found":    animecatalogdomain.ErrProviderFeedSnapshotNotFound,
		"mismatch":     animecatalogdomain.ErrProviderFeedSnapshotIdentityMismatch,
		"stale":        animecatalogdomain.ErrProviderFeedSnapshotStale,
		"infra outage": errors.New("database is down"),
	}
	for name, cause := range sentinels {
		t.Run(name, func(t *testing.T) {
			source := rootapplication.SeedPreviewSourceFromProviderFeedSnapshot(func(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error) {
				return animecatalogdomain.ProviderRSSPreviewResult{}, cause
			}, nil)
			_, err := source.FetchEntryGroupSnapshotByID(context.Background(), "anibt", "1001", "12", animecatalogdomain.ProviderRSSPreviewIdentity{SnapshotID: "snap-1", SnapshotVersion: 2, ContentHash: "sha256:" + strings.Repeat("a", 64)})
			if err == nil {
				t.Fatal("source error was swallowed")
			}
			wantRefresh := name != "infra outage"
			if got := errors.Is(err, ErrProviderFeedSnapshotRefreshRequired); got != wantRefresh {
				t.Fatalf("refresh_required = %v, want %v (err=%v)", got, wantRefresh, err)
			}
		})
	}
}

func TestSeedPreviewSourceAdapterForwardsTorrentFilename(t *testing.T) {
	source := rootapplication.SeedPreviewSourceFromProviderFeedSnapshot(func(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (animecatalogdomain.ProviderRSSPreviewResult, error) {
		infoHash := strings.Repeat("a", 40)
		digest := "sha256:" + strings.Repeat("b", 64)
		size := int64(1024)
		return animecatalogdomain.ProviderRSSPreviewResult{
			Provider: "anibt", ProviderEntryRef: "1001", GroupRef: "12",
			SnapshotID: "snap-1", SnapshotVersion: 2, ContentHash: "sha256:" + strings.Repeat("c", 64),
			Freshness: animecatalogdomain.FeedSnapshotFresh,
			Seeds: []animecatalogdomain.ProviderRSSPreviewSeed{{
				DownloadURL: "https://example.test/a.torrent", StableSeedKey: "seed-1", Title: "Release",
				PublishedAt: time.Unix(1700000000, 0).UTC(), SizeBytes: &size, InfoHash: &infoHash,
				ExpectedPayloadDigest: &digest, TorrentFilename: "Release - 01.mkv",
			}},
		}, nil
	}, nil)
	snapshot, err := source.FetchEntryGroupSnapshotByID(context.Background(), "anibt", "1001", "12", animecatalogdomain.ProviderRSSPreviewIdentity{SnapshotID: "snap-1", SnapshotVersion: 2, ContentHash: "sha256:" + strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Seeds) != 1 || snapshot.Seeds[0].TorrentFilename != "Release - 01.mkv" {
		t.Fatalf("torrent filename was not forwarded: %+v", snapshot.Seeds)
	}
	if snapshot.Seeds[0].InfoHash != strings.Repeat("a", 40) || snapshot.Seeds[0].ExpectedPayloadDigest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("strong identities were not forwarded: %+v", snapshot.Seeds[0])
	}
}

func TestFeedItemFromPreviewSeedRebuildsReplayableItem(t *testing.T) {
	size := int64(2048)
	v2Hash := strings.Repeat("d", 64)
	item := feedItemFromPreviewSeed(SeedPreviewFact{
		StableSeedKey: "seed-1", DownloadURL: "https://example.test/a.torrent",
		Title: "Release", PublishedAt: time.Unix(1700000000, 0).UTC(), SizeBytes: &size,
		InfoHash: v2Hash, TorrentFilename: "Release - 01.mkv",
	}, "anibt")
	if item.GUID != "seed-1" || item.Enclosure != "https://example.test/a.torrent" {
		t.Fatalf("identity fields not rebuilt: %+v", item)
	}
	if item.TorrentFilename != "Release - 01.mkv" || item.TorrentSizeBytes != size || item.EnclosureLength != size {
		t.Fatalf("naming/size facts not rebuilt: %+v", item)
	}
	if item.TorrentInfoHashV2 != v2Hash || item.TorrentInfoHashV1 != "" {
		t.Fatalf("v2 infohash not classified: %+v", item)
	}
	if _, stableKey := animedomain.StableItemKey(item); stableKey != "seed-1" {
		t.Fatalf("stable item key = %q, want the snapshot seed key", stableKey)
	}
}

func TestFeedItemFromPreviewSeedClassifiesV1Infohash(t *testing.T) {
	v1Hash := strings.Repeat("e", 40)
	item := feedItemFromPreviewSeed(SeedPreviewFact{StableSeedKey: "seed-1", DownloadURL: "https://example.test/a.torrent", Title: "Release", PublishedAt: time.Unix(1700000000, 0).UTC(), InfoHash: v1Hash}, "anibt")
	if item.TorrentInfoHashV1 != v1Hash || item.TorrentInfoHashV2 != "" {
		t.Fatalf("v1 infohash not classified: %+v", item)
	}
}

// The rebuilt item freezes the release title's parsed tag set, so a preview
// render reads the same value namespaces the release freezes at acceptance
// instead of refusing a fact the title carries. The shipped subtitle suffix
// (sub.lang_tag, which falls back to rt.sub_lang) reads it.
func TestFeedItemFromPreviewSeedFreezesTheTitleTagSet(t *testing.T) {
	item := feedItemFromPreviewSeed(SeedPreviewFact{
		StableSeedKey: "seed-1", DownloadURL: "https://example.test/a.torrent",
		Title: "[RealGroup] Preview Show - 01 [CHS] [1080p]", PublishedAt: time.Unix(1700000000, 0).UTC(),
	}, "anibt")
	if item.TitleTags == nil {
		t.Fatal("the rebuilt item carries no title tag set")
	}
	if item.TitleTags.Schema != acquisitiondomain.TitleTagSchema || item.TitleTags.Implementation == "" {
		t.Fatalf("tag set is not the frozen projection: %+v", item.TitleTags)
	}
	if !slices.Contains(item.TitleTags.Tags, "sub_lang:SC") || !slices.Contains(item.TitleTags.Tags, "resolution:1080p") {
		t.Fatalf("tags = %v, want the title's subtitle language and resolution", item.TitleTags.Tags)
	}
}

func TestSelectedProviderGroupEntriesIsOrderedSelectedAndProviderScoped(t *testing.T) {
	section := animedomain.SubgroupsSection{Entries: []animedomain.SubgroupScoreEntry{
		{GroupRef: "13", DisplayName: "B", Provider: animedomain.ProviderKindAnibt, EffectiveSelected: true},
		{GroupRef: "99", DisplayName: "Other", Provider: animedomain.ProviderKindMikan, EffectiveSelected: true},
		{GroupRef: "  ", DisplayName: "Blank", Provider: animedomain.ProviderKindAnibt, EffectiveSelected: true},
		{GroupRef: "12", DisplayName: "A", Provider: animedomain.ProviderKindAnibt, EffectiveSelected: true},
		{GroupRef: "13", DisplayName: "Duplicate", Provider: animedomain.ProviderKindAnibt, EffectiveSelected: true},
		{GroupRef: "11", DisplayName: "Unselected", Provider: animedomain.ProviderKindAnibt, EffectiveSelected: false},
	}}
	ordered, byGroup := selectedProviderGroupEntries(section, animedomain.SubscriptionOriginAnibt)
	if len(ordered) != 2 || ordered[0].GroupRef != "13" || ordered[1].GroupRef != "12" {
		t.Fatalf("ordered entries = %+v, want the provider-scoped deduplicated selected order", ordered)
	}
	if len(byGroup) != 2 {
		t.Fatalf("by-group size = %d, want 2", len(byGroup))
	}
	if _, exists := byGroup["99"]; exists {
		t.Fatal("another provider's group was indexed")
	}
	if _, exists := byGroup["11"]; exists {
		t.Fatal("an unselected group was indexed")
	}
}
