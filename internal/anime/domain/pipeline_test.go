package domain

import (
	"path"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/transport/httpapi/generated"
)

func testRules() attrrules.Rules {
	return attrrules.Rules{Version: 1, Resolution: []string{"2160p", "1440p", "1080p", "720p", "480p"}, Quality: []string{"remux", "web-dl", "webrip", "bluray", "bdrip", "hdtv"}, Language: []string{"chs", "cht", "sc", "tc", "jpn", "eng"}, SpecialMarkers: []string{"special", "ncop", "nced"}, GroupBrackets: []string{"[", "]"}}
}

func TestRenderPreviewFileForNameUsesReleaseResolutionWhenFilenameOmitsIt(t *testing.T) {
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	fp, err := namingvars.Fingerprint(vars)
	if err != nil {
		t.Fatal(err)
	}
	if err := namingvars.SetActive(vars, fp); err != nil {
		t.Fatal(err)
	}
	defaultNaming, err := DefaultSectionValue(SectionNamingRules)
	if err != nil {
		t.Fatal(err)
	}
	namingRules := domainValue[NamingRules](defaultNaming)
	effective := EffectiveConfig{Rules: EffectiveRules{
		Parser: ParserRules{EpisodePatterns: acquisitiondomain.DefaultEpisodePatterns},
		Naming: namingRules,
	}}
	item := acquisitiondomain.FeedItem{
		GUID:            "preview-resolution",
		Title:           "[RealGroup] Preview Show - 01 [1080p]",
		TorrentFilename: "Preview Show - 01.mkv",
	}
	release := EvaluateFeedItemWithIdentity(item, SourceEvaluationInput{GroupName: "RealGroup"}, effective, domainValue[MetadataHold](domainValue[MetadataHold](generated.MetadataHold{})), testRules(), tvEpisodeIdentity(t, "1"))
	release.FrozenVars = explicitTestFrozenVars()
	// One render core serves every manifest file: a filename that omits the
	// resolution takes it from the release. The default layout no longer names
	// the resolution, so the template under test is one that does.
	effective.Rules.Naming.Tv.VideoFilenameTemplate = "${anime.title} [${media.resolution}] ${s00e00}.${file.ext}"
	outcome := renderPreviewFileForName(item, release, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), "Preview Show - 01.mkv", "video", 0, release.FrozenEpisode, false, false)
	if !outcome.Included || outcome.RenderedTarget == nil {
		t.Fatalf("preview did not render: %+v", outcome)
	}
	if !strings.Contains(*outcome.RenderedTarget, "[1080p]") {
		t.Fatalf("release resolution was not carried into target preview: %q", *outcome.RenderedTarget)
	}

	// The read-model preview (RSS list, draft) shows the media facts as
	// placeholders: their real value exists only once the file is inspected.
	// The exact render above is the frozen pre-download target and has none.
	effective.Rules.Naming.Tv.VideoFilenameTemplate = "${anime.title} [${media.resolution} ${media.video_codec}] ${s00e00}.${file.ext}"
	preview := renderPreviewFileForName(item, release, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), "Preview Show - 01.mkv", "video", 0, release.FrozenEpisode, false, true)
	if !preview.Included || preview.RenderedTarget == nil || !strings.HasSuffix(*preview.RenderedTarget, "/Preview Show [⟨media.resolution⟩ ⟨media.video_codec⟩] S01E01.mkv") {
		t.Fatalf("preview placeholders = %+v %v", preview, preview.RenderedTarget)
	}
	for _, fact := range preview.EmptyFacts {
		if strings.HasPrefix(fact, "file.") {
			t.Fatalf("a placeholder is never an empty fact: %v", preview.EmptyFacts)
		}
	}
}

// --- manifest-driven per-file rendering --------------------------------------

// tvEpisodeIdentity states the release identity these projection tests feed the
// evaluator. A production path always resolves it from the selected parser
// evidence; what these tests exercise is the naming/file projection, not identity
// resolution, so they state it explicitly instead of re-parsing the title.
func tvEpisodeIdentity(t *testing.T, episode string) episodeidentity.EpisodeIdentity {
	t.Helper()
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    "tv",
		FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceReleaseParser, "release_title", episode),
	})
	if identity.State != episodeidentity.StateResolved {
		t.Fatalf("test identity state = %q, want resolved", identity.State)
	}
	return identity
}

func manifestPreviewFixture(t *testing.T) (EffectiveConfig, PreviewReleaseResult, acquisitiondomain.FeedItem) {
	t.Helper()
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	fp, err := namingvars.Fingerprint(vars)
	if err != nil {
		t.Fatal(err)
	}
	if err := namingvars.SetActive(vars, fp); err != nil {
		t.Fatal(err)
	}
	defaultNaming, err := DefaultSectionValue(SectionNamingRules)
	if err != nil {
		t.Fatal(err)
	}
	effective := EffectiveConfig{Rules: EffectiveRules{
		Parser: ParserRules{EpisodePatterns: acquisitiondomain.DefaultEpisodePatterns},
		Naming: domainValue[NamingRules](defaultNaming),
	}}
	item := acquisitiondomain.FeedItem{
		GUID:            "manifest-preview",
		Title:           "[RealGroup] Preview Show - 01 [1080p]",
		Enclosure:       "https://provider.example.test/show-01.torrent",
		TorrentFilename: "Preview Show S1",
		// A persisted release reads its title tags back from the frozen
		// source facts; the fixture states the same set instead of re-parsing.
		TitleTags: &acquisitiondomain.TitleTagSet{Tags: []string{"resolution:1080p", "sub_lang:SC"}},
	}
	source := SourceEvaluationInput{GroupName: "RealGroup"}
	release := EvaluateFeedItemWithIdentity(item, source, effective, domainValue[MetadataHold](generated.MetadataHold{}), testRules(), tvEpisodeIdentity(t, "1"))
	release.FrozenVars = explicitTestFrozenVars()
	return effective, release, item
}

func manifestFilesFixture() []ManifestFileInput {
	return []ManifestFileInput{
		{
			FileRef: "sha256:" + strings.Repeat("1", 64), Ordinal: 0,
			SourcePath: "Preview Show S1/ep1.mkv", Filename: "ep1.mkv", SizeBytes: 1234, MediaKind: "video",
		},
		{
			FileRef: "sha256:" + strings.Repeat("2", 64), Ordinal: 1,
			SourcePath: "Preview Show S1/ep1.zh-Hans.ass", Filename: "ep1.zh-Hans.ass",
			SizeBytes: 20, MediaKind: "subtitle", Language: "zh-Hans",
		},
		{
			FileRef: "sha256:" + strings.Repeat("3", 64), Ordinal: 2,
			SourcePath: "Preview Show S1/cover.jpg", Filename: "cover.jpg", SizeBytes: 7, MediaKind: "other",
		},
		{
			FileRef: "sha256:" + strings.Repeat("4", 64), Ordinal: 3,
			SourcePath: "Preview Show S1/SPs/ncop.mkv", Filename: "ncop.mkv", SizeBytes: 9, MediaKind: "video",
		},
	}
}

func TestPreviewFilesForManifestProjectsEligibleFilesWithTheirOwnTarget(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	results := PreviewFilesForManifest(item, release, manifestFilesFixture(), effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)

	if len(results) != 4 {
		t.Fatalf("projected %d files, want 4", len(results))
	}
	for index, result := range results {
		if result.Ordinal != index {
			t.Fatalf("result %d has ordinal %d", index, result.Ordinal)
		}
	}
	// First-level video and subtitle render, each with its own extension.
	targets := map[string]struct{}{}
	for _, index := range []int{0, 1} {
		result := results[index]
		if !result.Included || result.RenderedTarget == nil {
			t.Fatalf("result %d did not render a target: %+v", index, result)
		}
		sourceExt := strings.ToLower(path.Ext(result.Filename))
		if !strings.HasSuffix(strings.ToLower(*result.RenderedTarget), sourceExt) {
			t.Fatalf("result %d target %q lost the file extension %q", index, *result.RenderedTarget, sourceExt)
		}
		targets[*result.RenderedTarget] = struct{}{}
	}
	if len(targets) != 2 {
		t.Fatalf("targets collapsed: %v", targets)
	}
	// An unsupported kind and a nested file stay visible, read-only, with a
	// typed reason and no render.
	for index, reason := range map[int]string{2: "ignored_kind", 3: "ignored_nested"} {
		result := results[index]
		if result.Included || result.RenderedTarget != nil || result.RenderError != nil || result.ReasonCode == nil || *result.ReasonCode != reason {
			t.Fatalf("result %d must be ignored with %s: %+v", index, reason, result)
		}
	}
	// The manifest facts survive into the result unchanged.
	if results[1].FileRef != "sha256:"+strings.Repeat("2", 64) || results[1].MediaKind != "subtitle" || results[1].Language != "zh-Hans" {
		t.Fatalf("projected file lost its manifest identity: %+v", results[1])
	}
	if results[1].SourcePath != "Preview Show S1/ep1.zh-Hans.ass" || results[1].SizeBytes != 20 {
		t.Fatalf("projected file lost its manifest path or size: %+v", results[1])
	}
}

func TestManifestSubtitleFactsReachNamingContext(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	files := manifestFilesFixture()
	results := PreviewFilesForManifest(item, release, files, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	if results[1].RenderError != nil {
		t.Fatalf("subtitle manifest entry failed rendering: %s", *results[1].RenderError)
	}
	if results[1].MediaKind != "subtitle" || results[1].SizeBytes != 20 {
		t.Fatalf("manifest facts lost: %+v", results[1])
	}
	ctx := renderPreviewFileForName(item, release, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), files[1].Filename, files[1].MediaKind, files[1].SizeBytes, release.FrozenEpisode, false, false)
	if ctx.RenderedTarget == nil {
		t.Fatalf("subtitle naming context did not render: %+v", ctx)
	}
	// The real manifest projection reaches the naming template with its exact file kind and size, and the resolver fills subtitle format/language.
	effective.Rules.Naming.Tv.VideoFilenameTemplate = "${file.kind}|${file.size}|${file.sub.format}|${file.sub.lang}|${file.ext}"
	ctx = renderPreviewFileForName(item, release, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), files[1].Filename, files[1].MediaKind, files[1].SizeBytes, release.FrozenEpisode, false, false)
	if ctx.RenderedTarget == nil || *ctx.RenderedTarget != "/media/TV/Preview Show (2025)/Season 01/subtitle|20|ass|und|ass" {
		t.Fatalf("manifest subtitle facts did not reach naming render: target=%v result=%+v", ctx.RenderedTarget, ctx)
	}
}

// File rules apply to first-level videos by filename only. A rejected video
// stays visible with its rule reason; a subtitle is not subject to the rules.
func TestPreviewFilesForManifestFileRulesMatchVideoFilenameOnly(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	effective.Rules.FileFilter = FileFilterRules{MustContain: `(?i)ep9`}

	results := PreviewFilesForManifest(item, release, manifestFilesFixture(), effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	if results[0].Included || results[0].ReasonCode == nil || *results[0].ReasonCode != "file_must_contain_not_matched" {
		t.Fatalf("the video must be excluded by must_contain with its reason: %+v", results[0])
	}
	if !results[1].Included || results[1].RenderedTarget == nil {
		t.Fatalf("a subtitle is not subject to file rules: %+v", results[1])
	}
}

// A not_in that matches the torrent name must not exclude the torrent: the
// rules see the filename only, never torrent.name or a directory.
func TestPreviewFilesForManifestNotInOnTorrentNameDoesNotExcludeTheTorrent(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	effective.Rules.FileFilter = FileFilterRules{NotIn: `(?i)PREVIEW`}
	results := PreviewFilesForManifest(item, release, manifestFilesFixture(), effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	if !results[0].Included || results[0].RenderedTarget == nil {
		t.Fatalf("not_in matching only the torrent name excluded the video: %+v", results[0])
	}

	// Upper case in the rule proves the match is case-insensitive; it does
	// exclude a video whose own filename matches.
	effective.Rules.FileFilter = FileFilterRules{NotIn: `(?i)EP1`}
	results = PreviewFilesForManifest(item, release, manifestFilesFixture(), effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	if results[0].Included || results[0].ReasonCode == nil || *results[0].ReasonCode != "file_not_in_matched" || results[0].RenderError != nil {
		t.Fatalf("the filename-matched video must be excluded with its reason: %+v", results[0])
	}

	// The one exclusion decision is what the manifest renderer consults for
	// every file, and it sees the filename only: never the torrent name, never
	// a directory.
	if reason := ManifestFileExclusion(filekinds.Version, effective.Rules.FileFilter, "ep1.mkv"); reason != "file_not_in_matched" {
		t.Fatalf("the exclusion decision disagreed with the manifest renderer: %q", reason)
	}
}

func TestPreviewFilesForManifestOrdersByOrdinal(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	shuffled := manifestFilesFixture()
	shuffled[0], shuffled[2] = shuffled[2], shuffled[0]

	results := PreviewFilesForManifest(item, release, shuffled, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	for index, result := range results {
		if result.Ordinal != index {
			t.Fatalf("result %d has ordinal %d; output must follow the manifest order", index, result.Ordinal)
		}
	}
	if results[0].MediaKind != "video" || results[2].MediaKind != "other" {
		t.Fatalf("ordinal ordering lost the manifest facts: %+v", results)
	}
}

// A TV video whose own filename has no episode is an unknown file in the
// preview: it never renders the release title's episode. Under the route it
// renders the unknown template with its prefix-bound subtitle; under skip both
// are left out.
func TestPreviewFilesForManifestUnknownVideoFollowsTheRoute(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	video := ManifestFileInput{
		FileRef: "sha256:" + strings.Repeat("6", 64), Ordinal: 0, SourcePath: "Preview Show S1/special.mkv", Filename: "special.mkv",
		SizeBytes: 10, MediaKind: "video", PerFileEpisode: true,
	}
	subtitle := ManifestFileInput{
		FileRef: "sha256:" + strings.Repeat("7", 64), Ordinal: 1, SourcePath: "Preview Show S1/special.zh.ass", Filename: "special.zh.ass",
		SizeBytes: 3, MediaKind: "subtitle", PerFileEpisode: true, BoundVideo: video.SourcePath,
	}
	files := []ManifestFileInput{video, subtitle}
	source := SourceEvaluationInput{GroupName: "RealGroup"}

	routed := PreviewFilesForManifest(item, release, files, effective, "Preview Show", source, testRules(), UnknownRouteRoute)
	if !routed[0].Included || routed[0].RenderedTarget == nil || *routed[0].RenderedTarget != "/media/TV/Preview Show (2025)/Extras/special.mkv" {
		t.Fatalf("a routed unknown video renders the unknown template: %+v", routed[0])
	}
	// The subtitle follows the video: it lands beside it under the video's
	// basename, with the subtitle suffix and its own extension (MF design 4a),
	// not under its own name. The default suffix is sub.lang_tag: "zh" is not
	// a reltitle tag, so the release's SC (zh-Hans) stands.
	if !routed[1].Included || routed[1].RenderedTarget == nil || *routed[1].RenderedTarget != "/media/TV/Preview Show (2025)/Extras/special.zh-Hans.ass" {
		t.Fatalf("its subtitle follows it as an unknown file: %+v", routed[1])
	}

	skipped := PreviewFilesForManifest(item, release, files, effective, "Preview Show", source, testRules(), UnknownRouteSkip)
	if skipped[0].Included || skipped[0].RenderedTarget != nil || skipped[0].ReasonCode == nil || *skipped[0].ReasonCode != "unknown_skipped" {
		t.Fatalf("a skipped unknown video is excluded as unknown_skipped: %+v", skipped[0])
	}
	if skipped[1].Included || skipped[1].ReasonCode == nil || *skipped[1].ReasonCode != "subtitle_video_excluded" {
		t.Fatalf("its subtitle is excluded with it: %+v", skipped[1])
	}

	// The pre-download plan (no route) never gives an unknown file a target.
	plan := PreviewExactFile(item, release, video, effective, "Preview Show", source, testRules())
	if plan.Included || plan.RenderedTarget != nil || plan.ReasonCode == nil || *plan.ReasonCode != "unknown" {
		t.Fatalf("the pre-download plan keeps an unknown file without a target: %+v", plan)
	}

	video.FrozenEpisode = &naming.FrozenEpisodeNamingFacts{Raw: "5", Number: "5", Display: "5"}
	results := PreviewFilesForManifest(item, release, []ManifestFileInput{video}, effective, "Preview Show", source, testRules(), UnknownRouteSkip)
	if results[0].RenderedTarget == nil || *results[0].RenderedTarget != "/media/TV/Preview Show (2025)/Season 01/Preview Show S01E05.mkv" {
		t.Fatalf("the file's own episode must render: %+v", results[0])
	}
}

// A movie plans one main video: the largest the file rules keep. Every other
// first-level video, and its prefix-bound subtitle, is an unknown file that
// follows the route.
func TestPreviewFilesForManifestMovieNonMainVideosAreUnknown(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	release.FrozenEpisode = &naming.FrozenEpisodeNamingFacts{Raw: "0", Number: "0", Display: "0"}
	release.FrozenMediaKind = string(TMDBMediaKindMovie)
	files := []ManifestFileInput{
		{FileRef: "sha256:" + strings.Repeat("1", 64), Ordinal: 0, SourcePath: "Movie/trailer.mkv", Filename: "trailer.mkv", SizeBytes: 10, MediaKind: "video"},
		{FileRef: "sha256:" + strings.Repeat("2", 64), Ordinal: 1, SourcePath: "Movie/feature.mkv", Filename: "feature.mkv", SizeBytes: 1000, MediaKind: "video"},
		{FileRef: "sha256:" + strings.Repeat("3", 64), Ordinal: 2, SourcePath: "Movie/trailer.ass", Filename: "trailer.ass", SizeBytes: 3, MediaKind: "subtitle", BoundVideo: "Movie/trailer.mkv"},
		{FileRef: "sha256:" + strings.Repeat("4", 64), Ordinal: 3, SourcePath: "Movie/feature.ass", Filename: "feature.ass", SizeBytes: 3, MediaKind: "subtitle", BoundVideo: "Movie/feature.mkv"},
		{FileRef: "sha256:" + strings.Repeat("5", 64), Ordinal: 4, SourcePath: "Movie/chs.ass", Filename: "chs.ass", SizeBytes: 3, MediaKind: "subtitle"},
	}
	source := SourceEvaluationInput{GroupName: "RealGroup"}

	routed := PreviewFilesForManifest(item, release, files, effective, "Preview Show", source, testRules(), UnknownRouteRoute)
	if !routed[0].Included || routed[0].RenderedTarget == nil || *routed[0].RenderedTarget != "/media/Movie/Preview Show (2025)/Extras/trailer.mkv" {
		t.Fatalf("a non-main movie video is an unknown file: %+v", routed[0])
	}
	if !routed[2].Included || routed[2].RenderedTarget == nil || *routed[2].RenderedTarget != "/media/Movie/Preview Show (2025)/Extras/trailer.zh-Hans.ass" {
		t.Fatalf("its subtitle follows it: %+v", routed[2])
	}
	if !routed[1].Included || routed[1].RenderedTarget == nil || *routed[1].RenderedTarget != "/media/Movie/Preview Show (2025)/feature.mkv" {
		t.Fatalf("the main video renders the movie template: %+v", routed[1])
	}
	if !routed[3].Included || routed[3].RenderedTarget == nil || *routed[3].RenderedTarget != "/media/Movie/Preview Show (2025)/feature.zh-Hans.ass" {
		t.Fatalf("the main video's subtitle renders with it: %+v", routed[3])
	}
	if routed[4].Included || routed[4].ReasonCode == nil || *routed[4].ReasonCode != "subtitle_binding_missing" {
		t.Fatalf("a movie subtitle binds only by prefix: %+v", routed[4])
	}

	skipped := PreviewFilesForManifest(item, release, files, effective, "Preview Show", source, testRules(), UnknownRouteSkip)
	if skipped[0].Included || skipped[0].ReasonCode == nil || *skipped[0].ReasonCode != "unknown_skipped" {
		t.Fatalf("a skipped non-main video is unknown_skipped: %+v", skipped[0])
	}
	if skipped[2].Included || skipped[2].ReasonCode == nil || *skipped[2].ReasonCode != "subtitle_video_excluded" {
		t.Fatalf("its subtitle is excluded with it: %+v", skipped[2])
	}
	if !skipped[1].Included {
		t.Fatalf("the main video is unaffected by the route: %+v", skipped[1])
	}

	// A file rule that drops the larger video makes the next one the main video.
	effective.Rules.FileFilter = FileFilterRules{NotIn: `(?i)feature`}
	filtered := PreviewFilesForManifest(item, release, files, effective, "Preview Show", source, testRules(), UnknownRouteSkip)
	if !filtered[0].Included || filtered[0].RenderedTarget == nil || *filtered[0].RenderedTarget != "/media/Movie/Preview Show (2025)/trailer.mkv" {
		t.Fatalf("the largest video the rules keep is the main video: %+v", filtered[0])
	}
}

// The pre-download target/naming plan must consume the frozen Release episode
// identity resolved from the one selected parser/profile instead of re-parsing
// the title with the naming fallback. The release title carries [E07]: the
// bundled profile renders 07, while the frozen identity from the selected
// parser renders 42.
func TestRenderPreviewFileForNameConsumesFrozenReleaseEpisode(t *testing.T) {
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	fp, err := namingvars.Fingerprint(vars)
	if err != nil {
		t.Fatal(err)
	}
	if err := namingvars.SetActive(vars, fp); err != nil {
		t.Fatal(err)
	}
	defaultNaming, err := DefaultSectionValue(SectionNamingRules)
	if err != nil {
		t.Fatal(err)
	}
	effective := EffectiveConfig{Rules: EffectiveRules{
		Parser: ParserRules{EpisodePatterns: []string{`\[E(?P<episode>\d{1,4})\]`}},
		Naming: domainValue[NamingRules](defaultNaming),
	}}
	item := acquisitiondomain.FeedItem{
		GUID:            "frozen-release-episode",
		Title:           "[RealGroup] Preview Show [E07] [1080p]",
		TorrentFilename: "Preview Show [E07].mkv",
	}
	source := SourceEvaluationInput{GroupName: "RealGroup"}

	release := EvaluateFeedItemWithIdentity(item, source, effective, domainValue[MetadataHold](generated.MetadataHold{}), testRules(), tvEpisodeIdentity(t, "1"))
	release.FrozenVars = explicitTestFrozenVars()
	release.FrozenEpisode = nil
	unresolved := renderPreviewFileForName(item, release, effective, "Preview Show", source, testRules(), "Preview Show [E07].mkv", "video", 0, release.FrozenEpisode, false, false)
	if unresolved.RenderedTarget != nil {
		t.Fatalf("without a frozen identity the render must not re-parse the title: %+v", unresolved)
	}

	release.FrozenEpisode = &naming.FrozenEpisodeNamingFacts{Raw: "42", Number: "42", Display: "42"}
	frozen := renderPreviewFileForName(item, release, effective, "Preview Show", source, testRules(), "Preview Show [E07].mkv", "video", 0, release.FrozenEpisode, false, false)
	if frozen.RenderedTarget == nil {
		t.Fatalf("frozen target did not render: %+v", frozen)
	}
	if !strings.Contains(*frozen.RenderedTarget, "E42") {
		t.Fatalf("frozen release episode was not consumed: %q", *frozen.RenderedTarget)
	}
	if strings.Contains(*frozen.RenderedTarget, "E07") {
		t.Fatalf("target re-parsed the title despite the frozen release episode: %q", *frozen.RenderedTarget)
	}
}

// A subtitle bound to a video of the same release lands beside that video: the
// target is the video's target with the subtitle's own extension in place of
// the video's (the placement stage's companion rule), never a render of the
// subtitle naming template.
func TestPreviewManifestBindsSubtitleBesideItsVideo(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	files := []ManifestFileInput{
		{FileRef: "sha256:" + strings.Repeat("1", 64), Ordinal: 0, SourcePath: "Preview Show S1/ep1.mkv", Filename: "ep1.mkv", SizeBytes: 1234, MediaKind: "video"},
		{FileRef: "sha256:" + strings.Repeat("2", 64), Ordinal: 1, SourcePath: "Preview Show S1/ep1.zh-Hans.ass", Filename: "ep1.zh-Hans.ass", SizeBytes: 20, MediaKind: "subtitle", Language: "zh-Hans", PerFileEpisode: true, FrozenEpisode: release.FrozenEpisode, BoundVideo: "Preview Show S1/ep1.mkv"},
	}
	results := PreviewFilesForManifest(item, release, files, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	if !results[0].Included || results[0].RenderedTarget == nil {
		t.Fatalf("the video did not render a target: %+v", results[0])
	}
	videoTarget := *results[0].RenderedTarget
	// The video's basename plus the subtitle suffix the default rules render
	// (sub.lang_tag: the release's SC, as "zh-Hans" is no reltitle tag) plus
	// the subtitle's own extension.
	want := strings.TrimSuffix(videoTarget, path.Ext(videoTarget)) + ".zh-Hans.ass"
	got := ""
	if results[1].RenderedTarget != nil {
		got = *results[1].RenderedTarget
	}
	if got != want {
		t.Fatalf("a bound subtitle target = %q, want the video's target with its own extension %q", got, want)
	}
	if !results[1].Included || results[1].Safety != PreviewFileResultSafety("valid") {
		t.Fatalf("a bound subtitle is placeable: %+v", results[1])
	}
	if results[1].BoundVideo != "Preview Show S1/ep1.mkv" {
		t.Fatalf("the video a subtitle follows must travel with the result: %+v", results[1])
	}

	// A subtitle that matches no video keeps its own row and says so.
	unbound := PreviewFilesForManifest(item, release, []ManifestFileInput{
		files[0],
		{FileRef: "sha256:" + strings.Repeat("3", 64), Ordinal: 1, SourcePath: "Preview Show S1/notes.ass", Filename: "notes.ass", SizeBytes: 20, MediaKind: "subtitle", PerFileEpisode: true},
	}, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	if unbound[1].Included || unbound[1].ReasonCode == nil || *unbound[1].ReasonCode != "subtitle_binding_missing" {
		t.Fatalf("an unbound subtitle is not placed: %+v", unbound[1])
	}
	if unbound[1].BoundVideo != "" {
		t.Fatalf("an unbound subtitle follows no video: %+v", unbound[1])
	}
}

// A subtitle follows its bound video: it is excluded with a video the rules
// reject, and one that matches no video is an unbound, unplaced subtitle.
func TestPreviewFilesForManifestSubtitleFollowsItsBoundVideo(t *testing.T) {
	effective, release, item := manifestPreviewFixture(t)
	files := manifestFilesFixture()[:2]
	files[0].PerFileEpisode, files[0].FrozenEpisode = true, release.FrozenEpisode
	files[1].PerFileEpisode, files[1].BoundVideo, files[1].FrozenEpisode = true, files[0].SourcePath, release.FrozenEpisode
	orphan := ManifestFileInput{FileRef: "sha256:" + strings.Repeat("5", 64), Ordinal: 2, SourcePath: "Preview Show S1/notes.ass", Filename: "notes.ass", SizeBytes: 3, MediaKind: "subtitle", PerFileEpisode: true}
	files = append(files, orphan)

	results := PreviewFilesForManifest(item, release, files, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	if !results[1].Included || results[1].RenderedTarget == nil {
		t.Fatalf("a subtitle of a rendered video renders: %+v", results[1])
	}
	if results[2].Included || results[2].ReasonCode == nil || *results[2].ReasonCode != "subtitle_binding_missing" {
		t.Fatalf("an unbound subtitle is not placed: %+v", results[2])
	}

	effective.Rules.FileFilter = FileFilterRules{MustContain: `(?i)ep9`}
	results = PreviewFilesForManifest(item, release, files, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
	if results[1].Included || results[1].RenderedTarget != nil || results[1].ReasonCode == nil || *results[1].ReasonCode != "subtitle_video_excluded" {
		t.Fatalf("a subtitle follows a rule-excluded video out: %+v", results[1])
	}
}
