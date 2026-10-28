package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// A provider draft has no persisted RSS link yet, so its per-group offset must
// come from the draft source the subscribe pipeline will turn into that link.
// The draft side matches a group entry to its source by the same provider
// provenance triple the persisted link row derives its identity from
// (provider + provider_entry_ref + provider_group_ref).

// draftOffsetSnapshotSource serves one snapshot per provider group ref, so a
// test can give each group its own seeds.
type draftOffsetSnapshotSource struct {
	byGroup map[string]SeedPreviewSnapshot
	calls   int
}

func (s *draftOffsetSnapshotSource) FetchEntryGroupSnapshotByID(_ context.Context, _, _, groupRef string, _ animecatalogdomain.ProviderRSSPreviewIdentity) (SeedPreviewSnapshot, error) {
	s.calls++
	return s.byGroup[groupRef], nil
}

func (s *draftOffsetSnapshotSource) ReadSeedPayload(_ context.Context, _, _, _ string, _ animecatalogdomain.ProviderRSSPreviewIdentity, _ string) (SeedPayload, error) {
	return SeedPayload{}, errors.New("preview must not read seed payloads")
}

// draftOffsetSnapshot carries a manifest because only an expanded seed is
// evaluated: the lazy read model parses no title for a seed that was never
// expanded, so an offset could not be observed on one.
func draftOffsetSnapshot(t *testing.T, group, title string) SeedPreviewSnapshot {
	t.Helper()
	return SeedPreviewSnapshot{
		Provider: "anibt", ProviderEntryRef: "1001", ProviderGroupRef: group,
		SnapshotID: "snap-" + group, SnapshotVersion: 2,
		ContentHash: "sha256:" + strings.Repeat("b", 64), Freshness: "fresh",
		Seeds: []SeedPreviewFact{{
			StableSeedKey: "seed-" + group, Title: title,
			DownloadURL:  "https://provider.example.test/" + group + ".torrent",
			TorrentState: animecatalogdomain.TorrentAcquisitionAvailable,
			Manifest:     singleFileManifest(t, "Preview Show - 02.mkv"),
		}},
	}
}

func draftProviderSource(groupRef string, offset int) animedomain.RSSSourceDraft {
	return animedomain.RSSSourceDraft{
		SourceKind:    animedomain.SourceKindProviderGroup,
		Locator:       "https://provider.example.test/rss.xml",
		GroupName:     "Group " + groupRef,
		Title:         "Preview Show",
		EpisodeOffset: offset,
		ProviderProvenance: &animedomain.ProviderProvenance{
			Provider:                 animedomain.ProviderKindAnibt,
			ProviderEntryRef:         "1001",
			ProviderGroupRef:         groupRef,
			DiscoverySnapshotVersion: "disc-1",
		},
	}
}

// The group entry offset is the matching draft source's episode_offset; a group
// with no matching source falls back to 0, including when the only candidate
// source belongs to another entry ref or provider.
func TestRSSListGroupEntriesReadOffsetFromMatchingDraftSource(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	mismatchedEntry := draftProviderSource("13", 7)
	mismatchedEntry.ProviderProvenance.ProviderEntryRef = "9999"
	mismatchedProvider := draftProviderSource("13", 8)
	mismatchedProvider.ProviderProvenance.Provider = animedomain.ProviderKindMikan
	draft.RssSources = []animedomain.RSSSourceDraft{
		draftProviderSource("12", 1),
		mismatchedEntry,
		mismatchedProvider,
	}
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12", "13")

	entries, err := rssListGroupEntries(draft, effective, 0)
	if err != nil {
		t.Fatal(err)
	}
	offsets := make(map[string]int, len(entries))
	for _, entry := range entries {
		offsets[entry.groupRef] = entry.offset.Value
	}
	if offsets["12"] != 1 {
		t.Fatalf("group 12 offset = %d, want the matching source's 1", offsets["12"])
	}
	if offsets["13"] != 0 {
		t.Fatalf("group 13 offset = %d, want 0 when no source matches the provenance triple", offsets["13"])
	}
}

// A provider draft's offset reaches the preview evaluation: the group's own
// offset shifts the parsed episode, groups keep independent offsets, and a
// group with no matching source still evaluates at 0.
func TestComposeRSSListViewAppliesDraftProviderGroupOffset(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	draft.RssSources = []animedomain.RSSSourceDraft{draftProviderSource("12", 1)}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12", "13")
	source := &draftOffsetSnapshotSource{byGroup: map[string]SeedPreviewSnapshot{
		"12": draftOffsetSnapshot(t, "12", "[Group] Preview Show - 02 [1080p]"),
		"13": draftOffsetSnapshot(t, "13", "[Group] Preview Show - 02 [1080p]"),
	}}
	service := &Service{seedPreviews: source}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{
		previewTestRef(animedomain.ProviderKindAnibt, "12"),
		previewTestRef(animedomain.ProviderKindAnibt, "13"),
	}, 0)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(view.Groups) != 2 {
		t.Fatalf("groups = %+v, want one per draft group", view.Groups)
	}
	byGroup := make(map[string]RSSListGroup, len(view.Groups))
	for _, group := range view.Groups {
		byGroup[group.Scope.ProviderGroupRef] = group
	}
	shifted := byGroup["12"]
	unmatched := byGroup["13"]
	if shifted.Offset.Value != 1 {
		t.Fatalf("group 12 offset = %d, want the draft source's 1", shifted.Offset.Value)
	}
	if unmatched.Offset.Value != 0 {
		t.Fatalf("group 13 offset = %d, want 0 without a matching source", unmatched.Offset.Value)
	}
	if got := draftOffsetEpisode(t, shifted); got != 3 {
		t.Fatalf("group 12 episode = %v, want 3 (title 02 shifted by offset 1)", got)
	}
	if got := draftOffsetEpisode(t, unmatched); got != 2 {
		t.Fatalf("group 13 episode = %v, want 2 (title 02, offset 0)", got)
	}
}

func draftOffsetEpisode(t *testing.T, group RSSListGroup) float32 {
	t.Helper()
	if len(group.Items) != 1 {
		t.Fatalf("group %s items = %+v, want exactly one seed", group.Scope.ProviderGroupRef, group.Items)
	}
	facts := group.Items[0].Evaluation.ParsedFacts
	if group.Items[0].Evaluation.State != RSSListEvaluationEvaluated || facts == nil || facts.Episode == nil {
		t.Fatalf("group %s evaluation = %+v, want a resolved episode", group.Scope.ProviderGroupRef, group.Items[0].Evaluation)
	}
	return *facts.Episode
}

// The read input's preview-only offset is the last resort: it shifts a group
// that has no offset of its own, and a group whose own source states an offset
// keeps that offset, including an explicit 0. The provenance field is what a
// reader uses to tell the two apart, so this pins it alongside the value.
func TestRSSListGroupEntriesPreviewOffsetOnlyAppliesWithoutAnOwnOffset(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	draft.RssSources = []animedomain.RSSSourceDraft{draftProviderSource("12", 0)}
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12", "13")

	byGroup := func(entries []rssListGroupEntry) map[string]RSSListOffset {
		t.Helper()
		offsets := make(map[string]RSSListOffset, len(entries))
		for _, entry := range entries {
			offsets[entry.groupRef] = entry.offset
		}
		return offsets
	}

	shiftedEntries, err := rssListGroupEntries(draft, effective, -3)
	if err != nil {
		t.Fatal(err)
	}
	shifted := byGroup(shiftedEntries)
	if own := shifted["12"]; own.Value != 0 || own.Source != RSSListOffsetSourceDraftSource {
		t.Fatalf("group 12 offset = %+v, want the source's own 0 from draft_source", own)
	}
	if preview := shifted["13"]; preview.Value != -3 || preview.Source != RSSListOffsetSourcePreviewInput {
		t.Fatalf("group 13 offset = %+v, want the preview input's -3", preview)
	}

	// No preview offset means no shift at all, and the provenance says the 0 is
	// a default rather than the input.
	unshiftedEntries, err := rssListGroupEntries(draft, effective, 0)
	if err != nil {
		t.Fatal(err)
	}
	unshifted := byGroup(unshiftedEntries)
	if preview := unshifted["13"]; preview.Value != 0 || preview.Source != RSSListOffsetSourceDefault {
		t.Fatalf("group 13 offset = %+v, want the default 0", preview)
	}
	if own := unshifted["12"]; own.Value != 0 || own.Source != RSSListOffsetSourceDraftSource {
		t.Fatalf("group 12 offset = %+v, want the source's own 0 from draft_source", own)
	}
}

// An out-of-range preview offset is refused before any read happens, exactly as
// the one-shot request refuses the same value.
func TestReadAnimeRSSListViewRejectsOutOfRangePreviewOffset(t *testing.T) {
	for _, offset := range []int{1001, -1001} {
		_, err := (&Service{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
			InputSource: "current_input", PreviewEpisodeOffset: offset,
		})
		if code := businessCode(t, err); code != "OFFSET_OUT_OF_RANGE" {
			t.Fatalf("offset %d code = %q, want OFFSET_OUT_OF_RANGE", offset, code)
		}
	}
}

// The preview offset reaches the file-level parse: a group without its own
// offset evaluates shifted by the input, while a group whose source states its
// own offset keeps that one. This is what makes a non-zero offset previewable
// before a link exists, without the read ever rewriting a stored offset.
func TestComposeRSSListViewPreviewOffsetShiftsAnUnownedGroupOnly(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	draft.RssSources = []animedomain.RSSSourceDraft{draftProviderSource("12", 1)}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12", "13")
	source := &draftOffsetSnapshotSource{byGroup: map[string]SeedPreviewSnapshot{
		"12": draftOffsetSnapshot(t, "12", "[Group] Preview Show - 02 [1080p]"),
		"13": draftOffsetSnapshot(t, "13", "[Group] Preview Show - 02 [1080p]"),
	}}
	service := &Service{seedPreviews: source}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{
		previewTestRef(animedomain.ProviderKindAnibt, "12"),
		previewTestRef(animedomain.ProviderKindAnibt, "13"),
	}, -1)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	byGroup := make(map[string]RSSListGroup, len(view.Groups))
	for _, group := range view.Groups {
		byGroup[group.Scope.ProviderGroupRef] = group
	}
	owned := byGroup["12"]
	if owned.Offset.Value != 1 || owned.Offset.Source != RSSListOffsetSourceDraftSource {
		t.Fatalf("group 12 offset = %+v, want its own 1 from draft_source", owned.Offset)
	}
	if got := draftOffsetEpisode(t, owned); got != 3 {
		t.Fatalf("group 12 episode = %v, want 3 (title 02, its own offset 1)", got)
	}
	unowned := byGroup["13"]
	if unowned.Offset.Value != -1 || unowned.Offset.Source != RSSListOffsetSourcePreviewInput {
		t.Fatalf("group 13 offset = %+v, want the preview input's -1", unowned.Offset)
	}
	if got := draftOffsetEpisode(t, unowned); got != 1 {
		t.Fatalf("group 13 episode = %v, want 1 (title 02 shifted by the preview input)", got)
	}
}

// A provider draft carries no link rows, so its groups take their offsets from
// the draft's provider_group_offsets; a group without an entry keeps 0.
func TestRSSListGroupEntriesReadOffsetFromDraftProviderGroupOffsets(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12", "13")
	draft.ProviderGroupOffsets = []animedomain.ProviderGroupOffsetDraft{{ProviderGroupRef: "12", EpisodeOffset: -48}}
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12", "13")

	entries, err := rssListGroupEntries(draft, effective, 0)
	if err != nil {
		t.Fatal(err)
	}
	offsets := make(map[string]int, len(entries))
	for _, entry := range entries {
		offsets[entry.groupRef] = entry.offset.Value
	}
	if offsets["12"] != -48 || offsets["13"] != 0 {
		t.Fatalf("offsets = %+v, want 12 at -48 and 13 at 0", offsets)
	}
}
