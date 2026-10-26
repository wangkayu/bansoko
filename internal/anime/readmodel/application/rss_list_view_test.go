package application

import (
	"context"
	"path"
	"reflect"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/templatefacts"
)

// The composed RSS list view is the one shape the workspace renders. These
// tests pin the join: one group per configured subgroup, one item per seed, one
// file per projected manifest entry, and a server-derived download reason when a
// row cannot be downloaded.

func composeRSSListViewForTest(t *testing.T, source SeedPreviewSource) (RSSListView, error) {
	t.Helper()
	draft := previewBoundDraft()
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	service := &Service{seedPreviews: source}
	seedPreviewBusiness(t, service, &draft)
	setDraftOwnerYear(&draft)
	return service.composeRSSListView(
		context.Background(), "current_input", "draft:test", draft, effective, previewTestSelection(),
		[]PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")},
		0,
	)
}

func TestComposeRSSListViewJoinsGroupSeedAndFiles(t *testing.T) {
	view, err := composeRSSListViewForTest(t, &readmodelPreviewSource{snapshot: manifestPreviewSnapshot(t, "12")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Mode != "current_input" || view.ConfigIdentity != "draft:test" {
		t.Fatalf("view identity = %q/%q", view.Mode, view.ConfigIdentity)
	}
	if len(view.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(view.Groups))
	}
	group := view.Groups[0]
	if group.Scope.Provider != "anibt" || group.Scope.ProviderEntryRef != "1001" || group.Scope.ProviderGroupRef != "12" {
		t.Fatalf("group scope is not the full identity: %+v", group.Scope)
	}
	if !group.Selection.EffectiveSelected {
		t.Fatalf("group must report its server-derived selection: %+v", group.Selection)
	}
	if group.Feed.State != RSSListFeedFresh || group.Feed.SnapshotID != "snap-12" || group.Feed.SnapshotVersion != 2 {
		t.Fatalf("group feed status = %+v", group.Feed)
	}
	if len(group.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(group.Items))
	}
	item := group.Items[0]
	if item.SnapshotID != "snap-12" || item.StableSeedKey != "seed-01" {
		t.Fatalf("item identity = %q/%q", item.SnapshotID, item.StableSeedKey)
	}
	if item.Seed.LocatorDisplay == "" {
		t.Fatal("item must expose the redacted locator display")
	}
	if item.Torrent.State != animecatalogdomain.TorrentAcquisitionAvailable || item.Torrent.Manifest == nil {
		t.Fatalf("item torrent state = %+v", item.Torrent)
	}
	if item.Torrent.Manifest.FileCount() != 3 {
		t.Fatalf("manifest file count = %d, want 3", item.Torrent.Manifest.FileCount())
	}
	if item.Evaluation.State != RSSListEvaluationEvaluated {
		t.Fatalf("item evaluation state = %q, want %q", item.Evaluation.State, RSSListEvaluationEvaluated)
	}
	// One row per manifest entry that passed the two hard gates: the video and
	// the subtitle it binds. The cover is a kind the product does not handle, so
	// it is not a candidate and takes no row at all (the plan still classifies
	// it as ignored_kind).
	if len(item.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(item.Files))
	}
	wantPaths := []string{
		"Preview Show S1/Preview Show - 01.mkv",
		"Preview Show S1/Preview Show - 01.zh-Hans.ass",
	}
	for index, want := range wantPaths {
		file := item.Files[index]
		if file.SourcePath != want {
			t.Fatalf("file %d path = %q, want %q", index, file.SourcePath, want)
		}
		if file.FileRef == "" || file.Filename == "" || file.MediaKind == nil || file.SizeBytes == nil {
			t.Fatalf("file %d lost its manifest identity: %+v", index, file)
		}
		if file.Ordinal != index {
			t.Fatalf("file %d ordinal = %d", index, file.Ordinal)
		}
		// The manifest file declares the size and kind the row must report.
		if want := item.Torrent.Manifest.Files[index]; *file.SizeBytes != want.SizeBytes || *file.MediaKind != want.MediaKind {
			t.Fatalf("file %d facts = %d/%q, want the manifest's %d/%q", index, *file.SizeBytes, *file.MediaKind, want.SizeBytes, want.MediaKind)
		}
		if file.RenderedTarget == "" {
			t.Fatalf("file %d did not render a target", index)
		}
	}
	if item.Files[1].Language != "zh-Hans" {
		t.Fatalf("subtitle language was not carried: %+v", item.Files[1])
	}
	// The subtitle lands beside its video under the video's basename: it keeps
	// the video's target up to the extension, gains the suffix the rules
	// rendered for it (sub.lang_tag: "zh-Hans" is no reltitle tag, so the
	// release title's SC stands, whose tag is zh-Hans) and its own extension
	// (MF design 4a).
	video, subtitle := item.Files[0].RenderedTarget, item.Files[1].RenderedTarget
	if want := strings.TrimSuffix(video, path.Ext(video)) + ".zh-Hans.ass"; subtitle != want {
		t.Fatalf("subtitle target = %q, want the video's basename with the rendered suffix %q", subtitle, want)
	}
	// The subtitle row carries the language that suffix came from; the video
	// row carries none.
	if got := item.Files[1]; got.SubLang != "SC" || got.SubLangTag != "zh-Hans" || got.SubLangSource != "release" {
		t.Fatalf("subtitle language facts = %q/%q/%q, want SC/zh-Hans/release", got.SubLang, got.SubLangTag, got.SubLangSource)
	}
	if got := item.Files[0]; got.SubLang != "" || got.SubLangTag != "" || got.SubLangSource != "" {
		t.Fatalf("a video row carries no subtitle language: %+v", got)
	}
	// The rendered video is the library layout of the bound works: the media
	// kind, the title with the year the provider observed, the bound season
	// folder and the episode the file itself carries.
	if want := "/media/TV/Preview Show (2025)/Season 01/Preview Show S01E01.mkv"; video != want {
		t.Fatalf("video target = %q, want the default TV layout %q", video, want)
	}
	// The manifest's own binding travels with the subtitle row, so a reader can
	// list its output under that video instead of binding it again.
	if bound := item.Files[1].BoundVideoSourcePath; bound != "Preview Show S1/Preview Show - 01.mkv" {
		t.Fatalf("subtitle binding = %q, want its video", bound)
	}
	if bound := item.Files[0].BoundVideoSourcePath; bound != "" {
		t.Fatalf("a video follows no video, got %q", bound)
	}
	// The row is downloadable, and the server says why when it is not.
	if !item.DownloadAction.Allowed || item.DownloadAction.Kind != RSSListDownloadOneShot {
		t.Fatalf("download action = %+v, want an allowed one_shot action", item.DownloadAction)
	}
}

func TestComposeRSSListViewAdvisoryIdentityForUnselectedGroup(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	effective.Selection.Entries[0].EffectiveSelected = false
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: manifestPreviewSnapshot(t, "12")}}
	seedPreviewBusiness(t, service, &draft)
	view, err := service.composeRSSListView(context.Background(), "current_input", "draft:test", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("unselected identity should be advisory: %v", err)
	}
	if len(view.UnusedFeedIdentities) != 1 {
		t.Fatalf("unused identities = %+v", view.UnusedFeedIdentities)
	}
	if len(view.Groups) != 1 || view.Groups[0].Selection.EffectiveSelected {
		t.Fatalf("group selection state = %+v", view.Groups)
	}
	if view.Groups[0].Feed.State != RSSListFeedMissing {
		t.Fatalf("feed state = %q, want %q", view.Groups[0].Feed.State, RSSListFeedMissing)
	}
}

func TestComposeRSSListViewReportsUnavailableTorrentRow(t *testing.T) {
	snapshot := manifestPreviewSnapshot(t, "12")
	// One unusable torrent keeps its row with a bounded reason and no files.
	snapshot.Seeds[0].Manifest = nil
	snapshot.Seeds[0].TorrentState = animecatalogdomain.TorrentAcquisitionUnavailable
	snapshot.Seeds[0].TorrentErrorCode = "torrent_metadata_unavailable"
	snapshot.Seeds[0].TorrentErrorReason = "the torrent payload could not be parsed"

	view, err := composeRSSListViewForTest(t, &readmodelPreviewSource{snapshot: snapshot})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	item := view.Groups[0].Items[0]
	if item.Torrent.State != animecatalogdomain.TorrentAcquisitionUnavailable {
		t.Fatalf("torrent state = %q", item.Torrent.State)
	}
	if item.Torrent.ErrorCode != "torrent_metadata_unavailable" || item.Torrent.ErrorReason == "" {
		t.Fatalf("unavailable row lost its bounded reason: %+v", item.Torrent)
	}
	if item.Torrent.Manifest != nil {
		t.Fatal("an unavailable row must not carry a manifest")
	}
	// The torrent's failure is still the snapshot's record of it, but a seed
	// with no manifest was never expanded, so nothing parsed its title: the row
	// is pending rather than a verdict the server never computed.
	if item.Evaluation.State != RSSListEvaluationPending {
		t.Fatalf("evaluation state = %q", item.Evaluation.State)
	}
	if len(item.Files) != 0 {
		t.Fatalf("files = %d, want no row without an acquired structure", len(item.Files))
	}
	if item.DownloadAction.Allowed || item.DownloadAction.ReasonCode != RSSListReasonTorrentUnavailable {
		t.Fatalf("download action = %+v, want a blocked torrent_payload_unavailable action", item.DownloadAction)
	}
}

func TestComposeRSSListViewStaleSnapshotBlocksDownload(t *testing.T) {
	snapshot := manifestPreviewSnapshot(t, "12")
	snapshot.Freshness = "stale"
	view, err := composeRSSListViewForTest(t, &readmodelPreviewSource{snapshot: snapshot})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	group := view.Groups[0]
	if group.Feed.State != RSSListFeedStale {
		t.Fatalf("feed state = %q, want %q", group.Feed.State, RSSListFeedStale)
	}
	// A stale snapshot is still inspectable, but its rows cannot be downloaded.
	if len(group.Items) != 1 || group.Items[0].Files == nil {
		t.Fatal("a stale snapshot must remain readable")
	}
	if group.Items[0].DownloadAction.Allowed || group.Items[0].DownloadAction.ReasonCode != RSSListReasonSnapshotRefreshReq {
		t.Fatalf("download action = %+v, want a blocked snapshot_refresh_required action", group.Items[0].DownloadAction)
	}
}

func TestComposeRSSListViewListsCustomOriginWithoutFabricatedStructure(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	draft.Origin = animedomain.SubscriptionOriginCustom
	draft.Subgroups = animedomain.SubgroupsSection{MaxRss: 3}
	locator := "https://feeds.example.test/rss.xml"
	draft.RssSources = []animedomain.RSSSourceDraft{{
		SourceKind: "custom_rss", Locator: locator,
		LocatorDisplay: &locator, GroupName: "自定义组", Title: "测试番",
	}}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	service := &Service{}

	view, err := service.composeRSSListView(context.Background(), "current_input", "draft:custom", draft, effective, previewTestSelection(), nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(view.Groups) != 1 || view.Groups[0].DisplayName != "自定义组" {
		t.Fatalf("custom group was not listed: %+v", view.Groups)
	}
	group := view.Groups[0]
	if group.Feed.State != RSSListFeedSnapshotRequired || group.Feed.ErrorCode != "" {
		t.Fatalf("custom group feed state = %+v, want snapshot_required without fabricated fetch error", group.Feed)
	}
	if len(group.Items) != 0 {
		t.Fatalf("a source with no persisted snapshot must not fabricate items: %+v", group.Items)
	}
}

// A repeated stable key inside one snapshot is evaluated once and keeps the
// first release's evidence, exactly as the refresh path merges duplicates. The
// seed rows themselves stay listed: dedup is about evaluation, not hiding.
func TestComposeRSSListViewDeduplicatesRepeatedStableKeyWithinOneSnapshot(t *testing.T) {
	adapter := registerPreviewParser(t, "test-rss-dedupe", "7")
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
	source := &previewSnapshotSource{seeds: []SeedPreviewFact{
		{StableSeedKey: "dup-1", Title: "[Group] Preview Show - 01 [1080p]", DownloadURL: "https://example.test/first.torrent", TorrentState: animecatalogdomain.TorrentAcquisitionAvailable, Manifest: titleOnlyManifest(t, "Preview Show - 01")},
		{StableSeedKey: "dup-1", Title: "[Group] Preview Show - 02 [1080p]", DownloadURL: "https://example.test/second.torrent", TorrentState: animecatalogdomain.TorrentAcquisitionAvailable, Manifest: titleOnlyManifest(t, "Preview Show - 02")},
	}}
	selection := acquisitiontarget.ParserSelectionInput{Kind: adapter.kind, MediaKind: "tv"}
	service := &Service{seedPreviews: source}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, selection, []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 2 {
		t.Fatalf("both snapshot rows must stay listed: %+v", view.Groups)
	}
	inputs := adapter.parsedInputs()
	if len(inputs) != 1 || inputs[0] != "[Group] Preview Show - 01 [1080p]" {
		t.Fatalf("parser inputs = %v, want exactly the first release for a repeated stable key", inputs)
	}
	for index, item := range view.Groups[0].Items {
		if item.Evaluation.State != RSSListEvaluationEvaluated || item.Evaluation.ParsedFacts == nil || item.Evaluation.ParsedFacts.Episode == nil || *item.Evaluation.ParsedFacts.Episode != 7 {
			t.Fatalf("row %d evaluation = %+v, want the single deduplicated release's evidence", index, item.Evaluation)
		}
	}
}

// A naming template that throws must surface on the file row as an unsafe
// target with the template's message, never as a normal-looking target and
// never as a failed read.
func TestComposeRSSListViewSurfacesRenderErrorOnTheFileRow(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	throwing := "${(() => { if (anime.title_base) { throw new Error('缺少标题'); } return ''; })()}"
	effective.Rules.Naming = animedomain.NamingRules{
		Language:               animedomain.NamingRulesLanguageJsNaming1,
		Tv:                     animedomain.NamingVariant{DirectoryTemplate: throwing, VideoFilenameTemplate: throwing},
		Movie:                  animedomain.NamingVariant{DirectoryTemplate: throwing, VideoFilenameTemplate: throwing},
		SubtitleSuffixTemplate: "",
	}
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: manifestPreviewSnapshot(t, "12")}}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("a render error must not fail the read: %v", err)
	}
	// The video and its bound subtitle stay listed with the render failure; the
	// cover is a kind the product does not handle and takes no row.
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 || len(view.Groups[0].Items[0].Files) != 2 {
		t.Fatalf("the manifest rows must still be listed: %+v", view.Groups)
	}
	video := view.Groups[0].Items[0].Files[0]
	if video.Safety != "invalid" && video.Safety != "unsafe" {
		t.Fatalf("render failure must mark the file invalid/unsafe: %+v", video)
	}
	if video.RenderedTarget != "" {
		t.Fatalf("render failure must not produce a normal-looking target: %+v", video)
	}
	if video.RenderError == "" {
		t.Fatalf("render error message must reach the file row: %+v", video)
	}
}

// A held configuration is reported on the view while its rows stay listed: the
// hold blocks admission, not the read. The shipped default TV directory keeps
// ${sf} as a folder level of its own, so the fixture states the owner-edited
// season — independent of the bindings — and a held draft still previews its
// target while the metadata hold stays (bgm and tmdb unbound).
func TestComposeRSSListViewListsHeldRowsWithTheirPreviewTarget(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	season := 1
	draft.Metadata = animedomain.AnimeMetadataDraft{Season: &season}
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: manifestPreviewSnapshot(t, "12")}}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !view.MetadataHold.Held || len(view.MetadataHold.Missing) != 2 {
		t.Fatalf("hold = %+v, want bgm and tmdb held", view.MetadataHold)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("groups = %+v", view.Groups)
	}
	item := view.Groups[0].Items[0]
	if item.Evaluation.Verdict != "waiting_metadata" || item.Evaluation.ReasonCode != "metadata_incomplete" {
		t.Fatalf("held row verdict = %+v, want waiting_metadata", item.Evaluation)
	}
	// The video and its bound subtitle stay listed with the refusal; the cover
	// is a kind the product does not handle and takes no row.
	if len(item.Files) != 2 {
		t.Fatalf("held row must still list its files: %+v", item.Files)
	}
	for index, file := range item.Files {
		if file.RenderedTarget == "" || file.Safety != "valid" || file.RenderError != "" {
			t.Fatalf("file %d = %+v, want a previewed target", index, file)
		}
		if !strings.Contains(file.RenderedTarget, "/Season 01/") || !strings.Contains(file.RenderedTarget, " S01E01.") || len(file.EmptyFacts) != 0 {
			t.Fatalf("file %d = %+v, want the season 01 folder, an S01E01 name and no empty facts", index, file)
		}
	}
}

func TestRSSListOffsetEditableFollowsTheLifecycleFence(t *testing.T) {
	// The offset patch locks the ManagedAnime and requires the active state, so
	// only that state is editable. A draft/preview row carries no lifecycle
	// fact (its group has no persisted link), which must stay read-only rather
	// than defaulting open.
	for state, want := range map[string]bool{"active": true, "retired": false, "": false} {
		if got := rssListOffsetEditable(state); got != want {
			t.Fatalf("lifecycle %q: editable=%v want %v", state, got, want)
		}
	}
}

// The snapshot is the only carrier of an AniBT item's structured tags: the
// provider sends them as namespaced XML, not as title text, so a read model
// that rebuilt the item without them would evaluate `anibt.*` against an empty
// set. The whitelist below is a release the subscribed path accepts; a preview
// row that dropped the tags would filter it as anibt_tag_missing.
func TestComposeRSSListViewEvaluatesTheSnapshotAniBTTags(t *testing.T) {
	anibtTags := acquisitiondomain.StoredAniBTTags{Schema: acquisitiondomain.AniBTTagSchemaV1, Tags: []string{"resolution:1080p", "language:CHS"}}
	for _, tc := range []struct {
		name    string
		tags    *acquisitiondomain.StoredAniBTTags
		verdict string
		reason  string
	}{
		{name: "snapshot tags satisfy the whitelist", tags: &anibtTags, verdict: "accepted_pending_download", reason: "accepted"},
		{
			name: "a tag-less AniBT item is still an AniBT release",
			tags: &acquisitiondomain.StoredAniBTTags{Tags: []string{}},
			// A non-empty tag row is what makes the difference observable: a
			// release with no tags fails the whitelist as missing, while a
			// release that was never an AniBT one at all would pass it.
			verdict: "filtered", reason: animedomain.ReasonAniBTTagMissing,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Bound metadata: the verdict under test is the tag filter's, and a
			// held draft would mask it as waiting_metadata.
			draft := previewBoundDraft()
			setDraftOwnerYear(&draft)
			effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
			effective.TagFilters = animedomain.DefaultResolvedTagFilters()
			effective.TagFilters.Rules.Anibt = animedomain.TagFilterRow{
				Require: []string{"resolution:1080p"}, Exclude: []string{},
			}
			snapshot := manifestPreviewSnapshot(t, "12")
			snapshot.Seeds[0].AniBTTags = tc.tags
			service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: snapshot}}
			seedPreviewBusiness(t, service, &draft)

			view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			item := view.Groups[0].Items[0]
			if item.Evaluation.Verdict != tc.verdict || item.Evaluation.ReasonCode != tc.reason {
				t.Fatalf("evaluation = %s/%s (%s), want %s/%s", item.Evaluation.Verdict, item.Evaluation.ReasonCode, item.Evaluation.ReasonDetail, tc.verdict, tc.reason)
			}
			// The row's frozen tag set is what an owner read shows beside it, and
			// it must be the snapshot's own set rather than one re-derived from
			// the title.
			if tc.tags != nil {
				if item.Seed.AniBTTags == nil || !reflect.DeepEqual(*item.Seed.AniBTTags, tc.tags.Tags) {
					t.Fatalf("row AniBT tags = %v, want the snapshot's %v", item.Seed.AniBTTags, tc.tags.Tags)
				}
			}
		})
	}
}

// A draft's edited title is what acceptance freezes as title_override, so the
// preview must rank it above the provider and metadata titles; ranking it as the
// lowest fallback showed a target the accepted task would never write.
func TestDraftVarsAnimeEvidenceRanksTheEditedTitleAsTheFixedTitle(t *testing.T) {
	override := "  覆盖标题 "
	for name, tc := range map[string]struct {
		draft animedomain.AnimeDraft
		want  string
	}{
		"edited title":            {draft: animedomain.AnimeDraft{Title: " 自定义标题 "}, want: "自定义标题"},
		"override wins the title": {draft: animedomain.AnimeDraft{Title: "自定义标题", TitleOverride: &override}, want: "覆盖标题"},
	} {
		t.Run(name, func(t *testing.T) {
			evidence, err := (&Service{}).draftVarsAnimeEvidence(context.Background(), tc.draft)
			if err != nil {
				t.Fatal(err)
			}
			if evidence.AnimeTitleOverride != tc.want {
				t.Fatalf("title override = %q, want %q", evidence.AnimeTitleOverride, tc.want)
			}
			title, _ := templatefacts.EffectiveTitle(templatefacts.TitleCandidates{
				Override: evidence.AnimeTitleOverride, Provider: "站点标题",
				TmdbCN: "TMDB 标题", BgmCN: "BGM 标题", Base: evidence.AnimeTitle,
			})
			if title != tc.want {
				t.Fatalf("effective title = %q, want %q", title, tc.want)
			}
		})
	}
}
