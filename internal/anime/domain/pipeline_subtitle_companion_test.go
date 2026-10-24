package domain

import (
	"path"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/transport/httpapi/generated"
)

// companionPreviewFixture is the manifest preview fixture with a title of the
// caller's choosing. The release's subtitle language (rt.sub_lang) is what a
// subtitle whose own name names none renders (sub.lang falls back to it).
func companionPreviewFixture(t *testing.T, title string) (EffectiveConfig, PreviewReleaseResult, acquisitiondomain.FeedItem) {
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
		GUID:            "companion-preview",
		Title:           title,
		Enclosure:       "https://provider.example.test/show-01.torrent",
		TorrentFilename: "Preview Show S1",
		// The frozen title-parse set names the release's subtitle language;
		// sub.lang falls back to it for a subtitle whose own tag names none.
		TitleTags: &acquisitiondomain.TitleTagSet{Tags: []string{"sub_lang:SC"}},
	}
	source := SourceEvaluationInput{GroupName: "RealGroup"}
	release := EvaluateFeedItemWithIdentity(item, source, effective, domainValue[MetadataHold](generated.MetadataHold{}), testRules(), tvEpisodeIdentity(t, "1"))
	release.FrozenVars = explicitTestFrozenVars()
	return effective, release, item
}

// The preview's target for a bound subtitle and the placement stage's frozen
// target are composed by one rule (naming.CompanionSidecarFilename): the
// video's basename, the suffix the naming rules render for the subtitle, and
// the subtitle's own extension. These cases pin the preview half of that guard
// for the three shapes the rule has to get right: a suffix the rules actually
// render (never a constant), an empty suffix template, and a subtitle whose
// extension is the video's own.
func TestPreviewBoundSubtitleTargetMatchesThePlacementCompanionRule(t *testing.T) {
	const title = "[RealGroup] Preview Show - 01 [CHS] [1080p]"
	for _, tc := range []struct {
		name      string
		subtitle  string
		extension string
		// suffix is what the default rules render (sub.lang_tag): neither
		// "zh-Hans" nor "zh" is in the reltitle vocabulary, so these subtitles
		// fall back to the release's SC, whose tag is zh-Hans. suffixTemplate,
		// when set, replaces the template instead.
		suffix         string
		suffixTemplate *string
	}{
		{name: "rendered suffix", subtitle: "ep1.zh-Hans.ass", extension: "ass", suffix: "zh-Hans"},
		{name: "suffix template empty", subtitle: "ep1.zh-Hans.ass", extension: "ass", suffix: "", suffixTemplate: new(string)},
		{name: "subtitle extension is the video's", subtitle: "ep1.zh.mkv", extension: "mkv", suffix: "zh-Hans"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			effective, release, item := companionPreviewFixture(t, title)
			if tc.suffixTemplate != nil {
				effective.Rules.Naming.SubtitleSuffixTemplate = *tc.suffixTemplate
			}
			files := []ManifestFileInput{
				{FileRef: "video", Ordinal: 0, SourcePath: "Preview Show S1/ep1.mkv", Filename: "ep1.mkv", SizeBytes: 1234, MediaKind: "video"},
				{FileRef: "subtitle", Ordinal: 1, SourcePath: "Preview Show S1/" + tc.subtitle, Filename: tc.subtitle, SizeBytes: 20, MediaKind: "subtitle", PerFileEpisode: true, FrozenEpisode: release.FrozenEpisode, BoundVideo: "Preview Show S1/ep1.mkv"},
			}
			results := PreviewFilesForManifest(item, release, files, effective, "Preview Show", SourceEvaluationInput{GroupName: "RealGroup"}, testRules(), UnknownRouteSkip)
			if !results[0].Included || results[0].RenderedTarget == nil {
				t.Fatalf("the video did not render a target: %+v", results[0])
			}
			if results[1].RenderedTarget == nil {
				t.Fatalf("the bound subtitle did not render a target: %+v", results[1])
			}
			// The same helper the placement stage calls, fed with the video's
			// target segments the preview itself derived.
			directory, videoBasename := path.Split(strings.TrimSuffix(*results[0].RenderedTarget, path.Ext(*results[0].RenderedTarget)))
			want := directory + naming.CompanionSidecarFilename(videoBasename, tc.suffix, tc.extension)
			if got := *results[1].RenderedTarget; got != want {
				t.Fatalf("bound subtitle target = %q, want the placement rule's name %q", got, want)
			}
			// The suffix is really rendered, and the video's own extension is
			// replaced instead of doubled.
			if !strings.HasSuffix(*results[1].RenderedTarget, "."+tc.extension) {
				t.Fatalf("bound subtitle target %q lost the subtitle extension %q", *results[1].RenderedTarget, tc.extension)
			}
			if tc.suffix == "" {
				if strings.Contains(*results[1].RenderedTarget, ".und.") {
					t.Fatalf("an empty suffix template still rendered a suffix: %q", *results[1].RenderedTarget)
				}
			} else if !strings.HasSuffix(*results[1].RenderedTarget, "."+tc.suffix+"."+tc.extension) {
				t.Fatalf("bound subtitle target %q lost the rendered suffix %q", *results[1].RenderedTarget, tc.suffix)
			}
		})
	}
}

// precureManifest is one video of the owner's FLsnow release plus subtitles
// named stem + each given tail, all bound to that video.
func precureManifest(release PreviewReleaseResult, tails ...string) []ManifestFileInput {
	const stem = "[FLsnow][Star-Detective_Precure][36][TVer_1080p]"
	videoPath := "Precure/" + stem + ".mp4"
	files := []ManifestFileInput{{FileRef: "video", Ordinal: 0, SourcePath: videoPath, Filename: stem + ".mp4", SizeBytes: 1234, MediaKind: "video", PerFileEpisode: true, FrozenEpisode: release.FrozenEpisode}}
	for index, tail := range tails {
		name := stem + tail
		files = append(files, ManifestFileInput{FileRef: "subtitle" + tail, Ordinal: index + 1, SourcePath: "Precure/" + name, Filename: name, SizeBytes: 20, MediaKind: "subtitle", PerFileEpisode: true, FrozenEpisode: release.FrozenEpisode, BoundVideo: videoPath})
	}
	return files
}

// The default subtitle suffix is the subtitle's one BCP 47 tag
// (sub.lang_tag), resolved like the episode: the subtitle file's own tag
// first, normalized with the reltitle sub_lang vocabulary, else the release
// title's rt.sub_lang. The owner's FLsnow release (the CHS&CHT&JPN combo,
// rt.sub_lang
// JPSC+JPTC) ships `.chs&jpn.ass` and `.cht&jpn.ass`: the release-level
// default rendered both to `<video>.JPSC+JPTC.ass` and dropped both as
// target_duplicate.
func TestPreviewDefaultSubtitleSuffixIsTheFileFirstLanguage(t *testing.T) {
	const title = "[FLsnow][Star-Detective_Precure][36][TVer_1080p][CHS&CHT&JPN]"
	for _, tc := range []struct {
		name     string
		release  []string // the release title's frozen sub_lang tags
		tails    []string
		suffixes []string // the rendered suffix of each subtitle; "" = none
		facts    []string // each subtitle's exposed sub.lang|sub.lang_source
	}{
		{"each subtitle reads its own tag", []string{"sub_lang:JPSC", "sub_lang:JPTC"}, []string{".chs&jpn.ass", ".cht&jpn.ass"}, []string{"zh-Hans", "zh-Hant"}, []string{"JPN+SC|file", "JPN+TC|file"}},
		{"a lone untagged subtitle reads the release", []string{"sub_lang:JPSC", "sub_lang:JPTC"}, []string{".ass"}, []string{"zh"}, []string{"JPSC+JPTC|release"}},
		{"an unrecognizable tag reads the release", []string{"sub_lang:TC"}, []string{".forced.ass"}, []string{"zh-Hant"}, []string{"TC|release"}},
		{"no language anywhere drops the suffix", nil, []string{".ass"}, []string{""}, []string{"|"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			effective, release, item := companionPreviewFixture(t, title)
			item.TitleTags = &acquisitiondomain.TitleTagSet{Tags: tc.release}
			results := PreviewFilesForManifest(item, release, precureManifest(release, tc.tails...), effective, "Preview Show", SourceEvaluationInput{GroupName: "FLsnow"}, testRules(), UnknownRouteSkip)
			if !results[0].Included || results[0].RenderedTarget == nil {
				t.Fatalf("the video did not render a target: %+v", results[0])
			}
			videoTarget := strings.TrimSuffix(*results[0].RenderedTarget, path.Ext(*results[0].RenderedTarget))
			for index, suffix := range tc.suffixes {
				result := results[index+1]
				want := videoTarget + ".ass"
				if suffix != "" {
					want = videoTarget + "." + suffix + ".ass"
				}
				if !result.Included || result.ReasonCode != nil || result.RenderedTarget == nil || *result.RenderedTarget != want {
					t.Fatalf("subtitle %s = %+v (target %v), want included at %q", tc.tails[index], result, result.RenderedTarget, want)
				}
				if got := result.Subtitle.Lang + "|" + result.Subtitle.LangSource; got != tc.facts[index] || result.Subtitle.LangTag != suffix {
					t.Fatalf("subtitle %s exposed %+v, want %s with tag %q", tc.tails[index], result.Subtitle, tc.facts[index], suffix)
				}
			}
		})
	}
}

// Two subtitles of one video that render one target are settled by order,
// exactly as the placement stage does: the first in path order (not manifest
// order) keeps the target, the other is target_duplicate. The video is
// unaffected. `.chs.ass` and `.sc.ass` both read SC (zh-Hans).
func TestPreviewSubtitlesOfOneVideoOnOneTargetKeepTheFirstByPath(t *testing.T) {
	effective, release, item := companionPreviewFixture(t, "[FLsnow][Star-Detective_Precure][36][TVer_1080p]")
	results := PreviewFilesForManifest(item, release, precureManifest(release, ".sc.ass", ".chs.ass"), effective, "Preview Show", SourceEvaluationInput{GroupName: "FLsnow"}, testRules(), UnknownRouteSkip)
	if !results[0].Included || results[0].RenderedTarget == nil {
		t.Fatalf("the video must stay included: %+v", results[0])
	}
	want := strings.TrimSuffix(*results[0].RenderedTarget, path.Ext(*results[0].RenderedTarget)) + ".zh-Hans.ass"
	kept, dropped := results[2], results[1]
	if !kept.Included || kept.RenderedTarget == nil || *kept.RenderedTarget != want {
		t.Fatalf("the first subtitle by path = %+v, want included at %q", kept, want)
	}
	if dropped.Included || dropped.ReasonCode == nil || *dropped.ReasonCode != "target_duplicate" {
		t.Fatalf("the later subtitle = %+v, want target_duplicate", dropped)
	}
}

// Bound subtitles follow their video and are not subject to the file rules:
// a not_in "jpn" rule never drops
// `x.chs&jpn.ass` bound to a kept video, and a video the rules exclude takes
// its subtitles with it as subtitle_video_excluded.
func TestPreviewBoundSubtitlesFollowTheVideoNotTheFileRules(t *testing.T) {
	effective, release, item := companionPreviewFixture(t, "[FLsnow][Star-Detective_Precure][36][TVer_1080p]")
	effective.Rules.FileFilter = FileFilterRules{NotIn: "jpn"}
	results := PreviewFilesForManifest(item, release, precureManifest(release, ".chs&jpn.ass"), effective, "Preview Show", SourceEvaluationInput{GroupName: "FLsnow"}, testRules(), UnknownRouteSkip)
	if !results[0].Included || results[0].RenderedTarget == nil {
		t.Fatalf("the video must stay included: %+v", results[0])
	}
	want := strings.TrimSuffix(*results[0].RenderedTarget, path.Ext(*results[0].RenderedTarget)) + ".zh-Hans.ass"
	if got := results[1]; !got.Included || got.ReasonCode != nil || got.RenderedTarget == nil || *got.RenderedTarget != want {
		t.Fatalf("a bound subtitle the rule would match = %+v, want included at %q", got, want)
	}

	effective.Rules.FileFilter = FileFilterRules{NotIn: "TVer"}
	results = PreviewFilesForManifest(item, release, precureManifest(release, ".chs&jpn.ass"), effective, "Preview Show", SourceEvaluationInput{GroupName: "FLsnow"}, testRules(), UnknownRouteSkip)
	if results[0].Included || results[0].ReasonCode == nil || *results[0].ReasonCode != "file_not_in_matched" {
		t.Fatalf("the rule must exclude the video: %+v", results[0])
	}
	if got := results[1]; got.Included || got.ReasonCode == nil || *got.ReasonCode != "subtitle_video_excluded" {
		t.Fatalf("the subtitle of an excluded video = %+v, want subtitle_video_excluded", got)
	}
}
