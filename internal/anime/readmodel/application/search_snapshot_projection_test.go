package application

import (
	"context"
	"fmt"
	"strings"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// The keyword-search projection of the one-shot browse. A search snapshot has
// no catalog entry and no subgroup layer, so the tests here pin the three
// things that make it a real member of the list-view machinery instead of a
// parallel renderer: the dispatch routes an origin=one_shot draft through the
// search projection and the shared evaluation renders its seeds, the refs keep
// the kind doctrine (a feed ref is refused, never answered from a feed row),
// and an identity that no longer resolves fails closed as the typed refresh
// outcome instead of re-searching.

func searchPreviewRef() PreviewFeedSnapshotRef {
	return PreviewFeedSnapshotRef{
		Provider:         animedomain.ProviderKindMikan,
		ProviderEntryRef: "search-scope:" + strings.Repeat("c", 16),
		ProviderGroupRef: "search-query:" + strings.Repeat("d", 16),
		SnapshotID:       "snap-search", SnapshotVersion: 3,
		ContentHash: "sha256:" + strings.Repeat("e", 64),
	}
}

// searchOneshotDraft is the draft form the admission path binds to a search
// snapshot: origin=one_shot, no entry ref, no RSS source.
func searchOneshotDraft() animedomain.AnimeDraft {
	label := "Preview Show"
	return animedomain.AnimeDraft{Origin: animedomain.SubscriptionOriginOneshot, Title: "Preview Show", InstanceLabel: &label}
}

// searchTestSource serves the exact search snapshot a ref names and counts the
// reads; the feed read stays the inherited double's.
type searchTestSource struct {
	readmodelPreviewSource
	searchSnapshot SeedPreviewSnapshot
	searchErr      error
	searchCalls    int
}

func (f *searchTestSource) FetchSearchSnapshotByID(_ context.Context, provider, scopeRef, queryRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (SeedPreviewSnapshot, error) {
	f.searchCalls++
	f.lastIdentity = identity
	f.lastProvider, f.lastEntryRef, f.lastGroupRef = provider, scopeRef, queryRef
	return f.searchSnapshot, f.searchErr
}

// A one-shot browse of a search snapshot renders the snapshot's whole result
// window as one group through the shared machinery: the seed is evaluated, the
// scope echoes the search refs, and the group previews with the request's
// offset like every other one-shot group.
func TestOneShotSearchBrowseRendersSnapshotThroughSharedMachinery(t *testing.T) {
	ref := searchPreviewRef()
	snapshot := manifestPreviewSnapshot(t, "12")
	snapshot.Provider = string(ref.Provider)
	snapshot.ProviderEntryRef = ref.ProviderEntryRef
	snapshot.ProviderGroupRef = ref.ProviderGroupRef
	source := &searchTestSource{searchSnapshot: snapshot}
	service := &Service{seedPreviews: source}
	draft := searchOneshotDraft()
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListViewFor(context.Background(), "current_input", "draft:x", draft,
		previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindMikan), previewTestSelection(), []PreviewFeedSnapshotRef{ref}, nil, nil, 3, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source.searchCalls != 1 || len(view.UnusedFeedIdentities) != 0 {
		t.Fatalf("search reads = %d, unused = %+v", source.searchCalls, view.UnusedFeedIdentities)
	}
	if len(view.Groups) != 1 {
		t.Fatalf("search browse rendered %d groups, want the one search group", len(view.Groups))
	}
	group := view.Groups[0]
	if group.Scope.ProviderEntryRef != ref.ProviderEntryRef || group.Scope.ProviderGroupRef != ref.ProviderGroupRef {
		t.Fatalf("search group scope = %+v, want the snapshot's refs", group.Scope)
	}
	if group.Feed.State != RSSListFeedFresh || group.Feed.SnapshotID != ref.SnapshotID {
		t.Fatalf("search group feed = %+v, want the exact snapshot identity", group.Feed)
	}
	if group.Offset.Value != 3 || group.Offset.Source != RSSListOffsetSourcePreviewInput {
		t.Fatalf("search group offset = %+v, want the request's 3", group.Offset)
	}
	if !group.Selection.EffectiveSelected || group.Selection.SelectionMode != string(animedomain.SelectionModeAuto) {
		t.Fatalf("search group selection = %+v, want auto-selected", group.Selection)
	}
	if len(group.Items) != 1 {
		t.Fatalf("search group items = %+v, want the snapshot's one expanded seed", group.Items)
	}
	if group.Items[0].Evaluation.State != RSSListEvaluationEvaluated {
		t.Fatalf("search seed evaluation = %+v, want the shared evaluation", group.Items[0].Evaluation)
	}
}

// The refs decide the kind: an origin=one_shot browse that names a feed
// snapshot is refused instead of answered from the feed row, and the projection
// itself never binds a non-one-shot draft or more than one identity.
func TestOneShotSearchBrowseRefusesFeedRefsAndWrongForms(t *testing.T) {
	service := &Service{seedPreviews: &searchTestSource{}}
	feedRef := previewTestRef(animedomain.ProviderKindMikan, "12")

	if _, err := service.composeRSSListViewFor(context.Background(), "current_input", "draft:x", searchOneshotDraft(),
		previewTestEffective(animedomain.ProviderKindMikan), previewTestSelection(), []PreviewFeedSnapshotRef{feedRef}, nil, nil, 0, true, nil); err == nil {
		t.Fatal("search browse accepted a feed snapshot identity")
	} else if code := businessCode(t, err); code != "FEED_SNAPSHOT_IDENTITY_INVALID" {
		t.Fatalf("feed-ref refusal code = %s", code)
	}

	for _, tc := range []struct {
		name  string
		draft animedomain.AnimeDraft
		refs  []PreviewFeedSnapshotRef
	}{
		{"provider draft", previewTestDraft(animedomain.ProviderKindMikan, "12"), []PreviewFeedSnapshotRef{searchPreviewRef()}},
		{"two identities", searchOneshotDraft(), []PreviewFeedSnapshotRef{searchPreviewRef(), searchPreviewRef()}},
	} {
		if _, _, err := service.resolveSearchSnapshotProjection(context.Background(), tc.draft, tc.refs, 0); err == nil {
			t.Fatalf("%s: projection accepted the form", tc.name)
		} else if code := businessCode(t, err); code != "FEED_SNAPSHOT_IDENTITY_INVALID" {
			t.Fatalf("%s: refusal code = %s", tc.name, code)
		}
	}
}

// A search identity that no longer resolves fails closed as the typed refresh
// outcome: the read never re-searches and never answers from another snapshot.
func TestOneShotSearchBrowseStaleFailsClosedAsRefreshRequired(t *testing.T) {
	source := &searchTestSource{searchErr: fmt.Errorf("%w: snapshot retired", ErrProviderFeedSnapshotRefreshRequired)}
	service := &Service{seedPreviews: source}
	draft := searchOneshotDraft()

	_, err := service.composeRSSListViewFor(context.Background(), "current_input", "draft:x", draft,
		previewTestEffective(animedomain.ProviderKindMikan), previewTestSelection(), []PreviewFeedSnapshotRef{searchPreviewRef()}, nil, nil, 0, true, nil)
	if err == nil {
		t.Fatal("search browse served a stale identity")
	}
	if code := businessCode(t, err); code != "FEED_SNAPSHOT_REFRESH_REQUIRED" {
		t.Fatalf("stale outcome code = %s, want FEED_SNAPSHOT_REFRESH_REQUIRED", code)
	}
}

// The manifest read mirrors the feed projection's rule: the snapshot's own
// projections stay the authority (the cache never overrides one), and the
// expansion cache fills in the seeds the snapshot does not cover.
func TestSearchSeedManifestsPreferSnapshotProjections(t *testing.T) {
	ref := searchPreviewRef()
	snapshot := manifestPreviewSnapshot(t, "12")
	seedKey := snapshot.Seeds[0].StableSeedKey
	expanded := singleFileManifest(t, "Cached Show S01/01.mkv")
	override := singleFileManifest(t, "Wrong Show S01/01.mkv")
	cache := &readmodelSeedManifestSource{search: map[string]*animecatalogdomain.TorrentManifest{
		seedKey:      override,
		"other-seed": expanded,
	}}
	service := &Service{seedTorrentManifests: cache}

	manifests, err := service.searchSeedManifests(context.Background(), ref, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if manifests[seedKey] != snapshot.Seeds[0].Manifest {
		t.Fatal("the expansion cache overrode a projection the snapshot already carries")
	}
	if manifests["other-seed"] != expanded {
		t.Fatal("the expansion cache did not fill in a seed the snapshot does not cover")
	}
	if cache.searchCalls != 1 {
		t.Fatalf("search manifest cache reads = %d", cache.searchCalls)
	}
}

// readmodelSeedManifestSource doubles the manifest source: the feed read is
// unused here, the search read serves a fixed map.
type readmodelSeedManifestSource struct {
	feed        map[string]*animecatalogdomain.TorrentManifest
	search      map[string]*animecatalogdomain.TorrentManifest
	err         error
	searchCalls int
}

func (f *readmodelSeedManifestSource) ReadSeedTorrentManifests(context.Context, string, string, string, animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error) {
	return f.feed, f.err
}

func (f *readmodelSeedManifestSource) ReadSearchSeedTorrentManifests(_ context.Context, _, _, _ string, _ animecatalogdomain.ProviderRSSPreviewIdentity) (map[string]*animecatalogdomain.TorrentManifest, error) {
	f.searchCalls++
	return f.search, f.err
}
