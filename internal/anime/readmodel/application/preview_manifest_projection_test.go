package application

import (
	"context"
	"strings"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// The vertical slice this file pins: a saved revision renders its files from
// the seed's exact persisted torrent manifest, not from a single name derived
// from the torrent's folder. Before the manifest existed, adding a three-file
// torrent to the snapshot changed nothing in the preview output.

func manifestPreviewSnapshot(t *testing.T, group string) SeedPreviewSnapshot {
	t.Helper()
	// Only first-level files are planned: a video whose own filename carries
	// the episode, a subtitle bound to it by strict filename prefix, and a
	// cover image that is not a product-handled kind.
	return manifestPreviewSnapshotWithFiles(t, group, "Preview Show S1", []animecatalogdomain.TorrentFile{
		{SourcePath: "Preview Show S1/Preview Show - 01.mkv", Filename: "Preview Show - 01.mkv", SizeBytes: 1234, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Preview Show S1/Preview Show - 01.zh-Hans.ass", Filename: "Preview Show - 01.zh-Hans.ass", SizeBytes: 20, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle, Language: "zh-Hans"},
		{SourcePath: "Preview Show S1/cover.jpg", Filename: "cover.jpg", SizeBytes: 7, MediaKind: animecatalogdomain.TorrentFileMediaOther},
	})
}

// manifestPreviewSnapshotWithFiles builds a one-seed snapshot whose exact
// persisted manifest is the given file list (ordinals are assigned in order).
func manifestPreviewSnapshotWithFiles(t *testing.T, group, torrentName string, files []animecatalogdomain.TorrentFile) SeedPreviewSnapshot {
	t.Helper()
	payloadDigest := animecatalogdomain.DigestBytes([]byte("torrent"))
	var total int64
	for index := range files {
		files[index].Ordinal = index
		files[index].FileRef = animecatalogdomain.TorrentFileRef(payloadDigest, files[index].SourcePath)
		total += files[index].SizeBytes
	}
	projectionDigest, err := animecatalogdomain.CanonicalTorrentProjectionDigest(payloadDigest, "torrent-parser-v1", files)
	if err != nil {
		t.Fatal(err)
	}
	published := time.Date(2026, 7, 29, 16, 30, 0, 0, time.UTC)
	size := total
	return SeedPreviewSnapshot{
		Provider: "anibt", ProviderEntryRef: "1001", ProviderGroupRef: group,
		SnapshotID: "snap-" + group, SnapshotVersion: 2,
		ContentHash: "sha256:" + strings.Repeat("b", 64), Freshness: "fresh",
		Seeds: []SeedPreviewFact{{
			DownloadURL:    "https://provider.example.test/show-01.torrent",
			LocatorDisplay: "https://provider.example.test/show-01.torrent",
			InfoHash:       strings.Repeat("a", 40),
			PublishedAt:    published,
			SizeBytes:      &size,
			StableSeedKey:  "seed-01",
			// A subtitle row whose own name names no language takes the
			// release title's (sub.lang falls back to rt.sub_lang), so the seed
			// title names the language the release ships.
			Title:        "[RealGroup] Preview Show - 01 [CHS] [1080p]",
			TorrentState: animecatalogdomain.TorrentAcquisitionAvailable,
			Manifest: &animecatalogdomain.TorrentManifest{
				PayloadDigest: payloadDigest, ParserVersion: "torrent-parser-v1",
				TorrentVersion: "v1", TorrentName: torrentName,
				InfoHashV1: strings.Repeat("a", 40), TotalSizeBytes: total,
				ProjectionDigest: projectionDigest, Files: files,
			},
		}},
	}
}

// singleFileManifest is the exact manifest of a single-file torrent: the one
// first-level file is the torrent name itself. A preview only renders targets
// from a manifest, so tests that need a rendered target provide one.
func singleFileManifest(t *testing.T, name string) *animecatalogdomain.TorrentManifest {
	t.Helper()
	snapshot := manifestPreviewSnapshotWithFiles(t, "12", name, []animecatalogdomain.TorrentFile{
		{SourcePath: name, Filename: name, SizeBytes: 1234, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
	})
	return snapshot.Seeds[0].Manifest
}

// titleOnlyManifest marks a seed expanded without adding a file to parse. An
// expanded seed parses its release title and each video filename, so a test
// that counts parser calls for the title alone needs a manifest whose file list
// is empty: expansion is the manifest's presence, not its contents.
func titleOnlyManifest(t *testing.T, torrentName string) *animecatalogdomain.TorrentManifest {
	t.Helper()
	return manifestPreviewSnapshotWithFiles(t, "12", torrentName, nil).Seeds[0].Manifest
}

// previewEffectiveWithDefaultNaming builds the resolved selection a saved
// revision carries with a complete default naming ruleset, so the render path
// reaches the naming engine instead of failing template compilation.
func previewEffectiveWithDefaultNaming(t *testing.T, origin animedomain.ProviderKind, groups ...string) animedomain.EffectiveConfig {
	t.Helper()
	effective := previewTestEffective(origin, groups...)
	raw, err := animedomain.DefaultSectionValue(animedomain.SectionNamingRules)
	if err != nil {
		t.Fatal(err)
	}
	naming, ok := raw.(animedomain.NamingRules)
	if !ok {
		t.Fatalf("default naming section has unexpected type %T", raw)
	}
	effective.Rules.Naming = naming
	parserRaw, err := animedomain.DefaultSectionValue(animedomain.SectionParser)
	if err != nil {
		t.Fatal(err)
	}
	if parser, ok := parserRaw.(animedomain.ParserRules); ok {
		effective.Rules.Parser = parser
	}
	return effective
}

func TestPreviewRendersEveryFileOfTheExactManifest(t *testing.T) {
	draft := previewBoundDraft()
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: manifestPreviewSnapshot(t, "12")}}
	seedPreviewBusiness(t, service, &draft)
	setDraftOwnerYear(&draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("groups/items = %+v", view.Groups)
	}
	item := view.Groups[0].Items[0]
	if item.Evaluation.State != RSSListEvaluationEvaluated {
		t.Fatalf("evaluation = %+v", item.Evaluation)
	}
	if len(item.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(item.Files))
	}
	if item.Torrent.Manifest == nil {
		t.Fatal("manifest projection missing")
	}
	wantPaths := []string{"Preview Show S1/Preview Show - 01.mkv", "Preview Show S1/Preview Show - 01.zh-Hans.ass"}
	for index, want := range wantPaths {
		if item.Files[index].SourcePath != want {
			t.Fatalf("file %d source path = %q, want %q", index, item.Files[index].SourcePath, want)
		}
	}
	// The video and its bound subtitle each render a target that keeps their own
	// extension. The cover is a kind the product does not handle, so it is not a
	// candidate and takes no row at all.
	for _, index := range []int{0, 1} {
		if !item.Files[index].Included || item.Files[index].RenderedTarget == "" {
			t.Fatalf("file %d did not render a target: %+v", index, item.Files[index])
		}
	}
	if item.Files[0].RenderedTarget == item.Files[1].RenderedTarget {
		t.Fatalf("file targets collapsed: %q", item.Files[0].RenderedTarget)
	}
}

func TestPreviewKeepsFilteredFileVisibleWithItsReason(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewEffectiveWithDefaultNaming(t, animedomain.ProviderKindAnibt, "12")
	// File rules match the filename of a first-level video only: the rule
	// keeps episode 01, so episode 02 must stay visible as excluded evidence
	// instead of disappearing. The subtitle is not subject to rules, the
	// nested file and the cover are ignored by the product before any rule.
	effective.Rules.FileFilter = animedomain.FileFilterRules{MustContain: `- 01\.mkv$`}
	snapshot := manifestPreviewSnapshotWithFiles(t, "12", "Preview Show S1", []animecatalogdomain.TorrentFile{
		{SourcePath: "Preview Show S1/Preview Show - 01.mkv", Filename: "Preview Show - 01.mkv", SizeBytes: 1234, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Preview Show S1/Preview Show - 02.mkv", Filename: "Preview Show - 02.mkv", SizeBytes: 1234, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{SourcePath: "Preview Show S1/Preview Show - 01.zh-Hans.ass", Filename: "Preview Show - 01.zh-Hans.ass", SizeBytes: 20, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle, Language: "zh-Hans"},
		{SourcePath: "Preview Show S1/Subs/Preview Show - 01.en.ass", Filename: "Preview Show - 01.en.ass", SizeBytes: 20, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle, Language: "en"},
		{SourcePath: "Preview Show S1/cover.jpg", Filename: "cover.jpg", SizeBytes: 7, MediaKind: animecatalogdomain.TorrentFileMediaOther},
	})
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: snapshot}}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("groups/items = %+v", view.Groups)
	}
	item := view.Groups[0].Items[0]
	// The rule-excluded video and the subtitle that binds it stay visible (a
	// filter conclusion is evidence); the nested subtitle and the cover failed
	// the hard gates, so they are not candidates and take no row.
	if len(item.Files) != 3 {
		t.Fatalf("files = %d, want 3 (a rule-excluded file must stay visible)", len(item.Files))
	}
	if !item.Files[0].Included {
		t.Fatalf("matching video must stay included: %+v", item.Files[0])
	}
	// Rows are in natural path order: 01.mkv, 01.zh-Hans.ass, 02.mkv.
	if !item.Files[1].Included {
		t.Fatalf("a first-level subtitle is not subject to file rules: %+v", item.Files[1])
	}
	excluded := item.Files[2]
	if excluded.Included || excluded.RenderedTarget != "" || excluded.ReasonCode != "file_must_contain_not_matched" {
		t.Fatalf("file 2 = %+v, want the rule exclusion of the video that is not - 01.mkv", excluded)
	}
	for _, file := range item.Files {
		if file.ReasonCode == "ignored_nested" || file.ReasonCode == "ignored_kind" {
			t.Fatalf("a file that failed a hard gate must not be listed: %+v", file)
		}
	}
}

// A torrent whose payload was never acquired has no file to describe: the
// structure is the only source of a file row, so the release keeps its place
// with the acquisition reason and no evaluation. The feed item's own name is not
// a video and not a subtitle, so nothing is rendered in its place either.
func TestPreviewListsNoFileWithoutManifest(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
	snapshot := manifestPreviewSnapshot(t, "12")
	// An unacquirable torrent keeps its place with a reason and no manifest.
	snapshot.Seeds[0].Manifest = nil
	snapshot.Seeds[0].TorrentState = animecatalogdomain.TorrentAcquisitionUnavailable
	snapshot.Seeds[0].TorrentErrorCode = "torrent_metadata_unavailable"
	snapshot.Seeds[0].TorrentErrorReason = "the torrent payload could not be parsed"
	service := &Service{seedPreviews: &readmodelPreviewSource{snapshot: snapshot}}
	seedPreviewBusiness(t, service, &draft)

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, previewTestSelection(), []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("groups/items = %+v", view.Groups)
	}
	item := view.Groups[0].Items[0]
	// A seed without a manifest was never expanded, so nothing parsed its title:
	// the row is pending rather than a verdict the server never computed.
	if item.Evaluation.State != RSSListEvaluationPending {
		t.Fatalf("evaluation = %+v", item.Evaluation)
	}
	if len(item.Files) != 0 {
		t.Fatalf("files = %d, want no row without an acquired structure: %+v", len(item.Files), item.Files)
	}
	if item.Torrent.Manifest != nil || item.Torrent.State != string(animecatalogdomain.TorrentAcquisitionUnavailable) {
		t.Fatalf("torrent = %+v", item.Torrent)
	}
}
