package application

import (
	"context"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	catalogapplication "bansoko/internal/catalog/application"
)

// A one-shot browse is not bound by group selection: an unselected group lists
// its supplied snapshot and seeds, and its download action is not blocked by
// group_not_selected. The ordinary read of the same draft keeps the group empty.
func TestOneShotBrowseListsUnselectedGroupSeeds(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12", "13")
	for index := range effective.Selection.Entries {
		if effective.Selection.Entries[index].GroupRef == "13" {
			effective.Selection.Entries[index].EffectiveSelected = false
		}
	}
	source := &draftOffsetSnapshotSource{byGroup: map[string]SeedPreviewSnapshot{
		"12": draftOffsetSnapshot(t, "12", "[Group] Preview Show - 02 [1080p]"),
		"13": draftOffsetSnapshot(t, "13", "[Group] Preview Show - 02 [1080p]"),
	}}
	service := &Service{seedPreviews: source}
	seedPreviewBusiness(t, service, &draft)
	feed := []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12"), previewTestRef(animedomain.ProviderKindAnibt, "13")}

	ordinary, err := service.composeRSSListViewFor(context.Background(), "current_input", "draft:x", draft, effective, previewTestSelection(), feed, nil, nil, 0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if group := groupByRef(t, ordinary, "13"); len(group.Items) != 0 || len(ordinary.UnusedFeedIdentities) != 1 {
		t.Fatalf("ordinary read listed an unselected group's seeds: %+v", group)
	}

	browse, err := service.composeRSSListViewFor(context.Background(), "current_input", "draft:x", draft, oneShotBrowseSelection(draft, effective), previewTestSelection(), feed, nil, nil, 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	group := groupByRef(t, browse, "13")
	if len(group.Items) != 1 || len(browse.UnusedFeedIdentities) != 0 {
		t.Fatalf("one-shot browse hid an unselected group's seeds: %+v", group)
	}
	if code := group.Items[0].DownloadAction.ReasonCode; code == RSSListReasonGroupNotSelected {
		t.Fatalf("one-shot browse blocked a seed on group selection: %+v", group.Items[0].DownloadAction)
	}
}

// Every group of a one-shot browse previews with the request's offset, never a
// draft source's own offset.
func TestOneShotBrowseOffsetIsTheRequestOffset(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	draft.RssSources = []animedomain.RSSSourceDraft{draftProviderSource("12", 5)}
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12", "13")

	entries, err := rssListGroupEntriesFor(draft, effective, -2, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.offset.Value != -2 || entry.offset.Source != RSSListOffsetSourcePreviewInput {
			t.Fatalf("group %s offset = %+v, want the request's -2", entry.groupRef, entry.offset)
		}
	}
	entries, err = rssListGroupEntriesFor(draft, effective, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.offset.Value != 0 || entry.offset.Source != RSSListOffsetSourceDefault {
			t.Fatalf("group %s offset = %+v, want 0 (the source's 5 is not the one-shot's)", entry.groupRef, entry.offset)
		}
	}
}

// A one-shot custom browse binds the exact persisted snapshot its ref names
// (no saved revision scope exists for the one-shot's own draft) and previews
// with the request's offset instead of the custom source's.
func TestOneShotCustomBrowseBindsNamedSnapshotAndOffset(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	draft.Origin = animedomain.SubscriptionOriginCustom
	draft.RssSources = []animedomain.RSSSourceDraft{{SourceKind: "custom_rss", Locator: "https://example.test/feed", GroupName: "Only Group", EpisodeOffset: 4}}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	source := &readmodelPreviewSource{snapshot: manifestPreviewSnapshot(t, "12")}
	service := &Service{seedPreviews: source}

	entryRef, groupRef := catalogapplication.CustomSnapshotScope("candidate#1", draft.RssSources[0].Locator, "Only Group")
	ref := previewTestRef(animedomain.ProviderKindAnibt, "12")
	ref.Provider = animedomain.ProviderKind("custom")
	ref.ProviderEntryRef = entryRef
	ref.ProviderGroupRef = groupRef

	// The ordinary read of an unsaved draft has no scope this snapshot belongs to.
	if _, _, err := service.resolveCustomSnapshotProjectionFor(context.Background(), "draft:x", draft, effective, []PreviewFeedSnapshotRef{ref}, false, 0); err == nil {
		t.Fatal("ordinary read bound a snapshot outside the draft's scope")
	}
	previews, entries, err := service.resolveCustomSnapshotProjectionFor(context.Background(), "draft:x", draft, effective, []PreviewFeedSnapshotRef{ref}, true, 3)
	if err != nil || len(previews) != 1 || len(entries) != 1 || source.calls != 1 {
		t.Fatalf("one-shot custom read %v/%d", err, source.calls)
	}
	if entries[0].providerEntryRef != entryRef || entries[0].groupRef != groupRef {
		t.Fatalf("one-shot custom group scope = %s/%s, want the named snapshot's", entries[0].providerEntryRef, entries[0].groupRef)
	}
	if entries[0].offset.Value != 3 || entries[0].offset.Source != RSSListOffsetSourcePreviewInput {
		t.Fatalf("one-shot custom offset = %+v, want the request's 3", entries[0].offset)
	}
}

func TestOneShotBrowseRejectsNonCurrentInput(t *testing.T) {
	service := &Service{}
	if _, err := service.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "candidate_revision", CandidateID: "c", Revision: 1, OneShot: true}); err == nil {
		t.Fatal("one_shot accepted outside current_input")
	}
}

func groupByRef(t *testing.T, view RSSListView, ref string) RSSListGroup {
	t.Helper()
	for _, group := range view.Groups {
		if group.Scope.ProviderGroupRef == ref {
			return group
		}
	}
	t.Fatalf("group %s missing from %+v", ref, view.Groups)
	return RSSListGroup{}
}
