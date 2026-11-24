package application

import (
	"context"
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

func TestRSSListGroupEntriesResolveEpisodeSpan(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	own := draftProviderSource("12", 0)
	own.EpisodeSpan = 2
	draft.RssSources = []animedomain.RSSSourceDraft{own}
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12", "13")

	entries, err := rssListGroupEntriesForSpan(draft, effective, 0, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	byGroup := make(map[string]RSSListOffset, len(entries))
	for _, entry := range entries {
		byGroup[entry.groupRef] = entry.offset
	}
	if got := byGroup["12"]; got.Span != 2 || got.Source != RSSListOffsetSourceDraftSource {
		t.Fatalf("configured group span = %+v, want its own 2", got)
	}
	if got := byGroup["13"]; got.Span != 4 || got.Source != RSSListOffsetSourcePreviewInput {
		t.Fatalf("unconfigured group span = %+v, want preview 4", got)
	}

	entries, err = rssListGroupEntriesForSpan(draft, effective, 0, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.offset.Span != 3 || entry.offset.Source != RSSListOffsetSourcePreviewInput {
			t.Fatalf("one-shot group %s span = %+v, want request 3", entry.groupRef, entry.offset)
		}
	}
}

func TestComposeRSSListViewAppliesDraftProviderSpan(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	own := draftProviderSource("12", 0)
	own.EpisodeSpan = 2
	draft.RssSources = []animedomain.RSSSourceDraft{own}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	source := &draftOffsetSnapshotSource{byGroup: map[string]SeedPreviewSnapshot{
		"12": draftOffsetSnapshot(t, "12", "[Group] Preview Show - 02 [1080p]"),
	}}
	service := &Service{seedPreviews: source}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListViewForSpan(
		context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(),
		[]PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, nil, nil, 0, 0, false, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	group := groupByRef(t, view, "12")
	if group.Offset.Span != 2 || draftOffsetEpisode(t, group) != 3 {
		t.Fatalf("combined group offset/episode = %+v/%v, want span 2 and episode 3", group.Offset, draftOffsetEpisode(t, group))
	}
	if len(group.Items[0].Files) != 1 || group.Items[0].Files[0].Episode != "3-4" {
		t.Fatalf("combined file projection = %+v, want episode range 3-4", group.Items[0].Files)
	}
}

func TestOneShotBrowseSpanIsTheRequestSpan(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	own := draftProviderSource("12", 0)
	own.EpisodeSpan = 2
	draft.RssSources = []animedomain.RSSSourceDraft{own}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	source := &draftOffsetSnapshotSource{byGroup: map[string]SeedPreviewSnapshot{
		"12": draftOffsetSnapshot(t, "12", "[Group] Preview Show - 02 [1080p]"),
	}}
	service := &Service{seedPreviews: source}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListViewForSpan(
		context.Background(), "current_input", "draft:x", draft, effective, previewTestSelection(),
		[]PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, nil, nil, 0, 3, true, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	group := groupByRef(t, view, "12")
	if group.Offset.Span != 3 || group.Offset.Source != RSSListOffsetSourcePreviewInput || draftOffsetEpisode(t, group) != 4 {
		t.Fatalf("one-shot combined group = offset %+v episode %v, want request span 3 and episode 4", group.Offset, draftOffsetEpisode(t, group))
	}
}

func TestReadAnimeRSSListViewRejectsOutOfRangePreviewSpan(t *testing.T) {
	for _, span := range []int{-1, 13} {
		_, err := (&Service{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
			InputSource: "current_input", PreviewEpisodeSpan: span,
		})
		if code := businessCode(t, err); code != "SPAN_OUT_OF_RANGE" {
			t.Fatalf("span %d code = %q, want SPAN_OUT_OF_RANGE", span, code)
		}
	}
}
