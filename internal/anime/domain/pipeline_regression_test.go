package domain

import (
	"fmt"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/transport/httpapi/generated"
)

// A template that compiles but throws for a concrete file is a render failure,
// not a configuration failure: the file is marked unsafe, carries the thrown
// message, and produces no target. This is the non-Postgres form of the same
// boundary the preview service test pins end to end.
func TestCompiledTemplateRenderFailureIsCarriedPerFile(t *testing.T) {
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	fp, err := namingvars.Fingerprint(vars)
	if err != nil {
		t.Fatal(err)
	}
	if err := namingvars.SetActive(vars, fp); err != nil {
		t.Fatal(err)
	}
	throwing := "${(() => { if (anime.title_base) { throw new Error('缺少标题'); } return ''; })()}"
	rules := NamingRules{
		Language: NamingRulesLanguageJsNaming1,
		Tv:       NamingVariant{DirectoryTemplate: throwing, VideoFilenameTemplate: throwing},
		Movie:    NamingVariant{DirectoryTemplate: throwing, VideoFilenameTemplate: throwing},
	}
	if _, err := naming.NewEngine().CompileRules(NamingRulesToDomain(rules)); err != nil {
		t.Fatalf("a template that throws for a concrete file must still compile: %v", err)
	}

	effective := EffectiveConfig{Rules: EffectiveRules{
		Parser: ParserRules{EpisodePatterns: acquisitiondomain.DefaultEpisodePatterns},
		Naming: rules,
	}}
	item := acquisitiondomain.FeedItem{
		GUID:            "render-error-per-file",
		Title:           "[RealGroup] Preview Show - 03 [1080p]",
		TorrentFilename: "Preview Show - 03.mkv",
	}
	source := SourceEvaluationInput{GroupName: "RealGroup"}
	release := EvaluateFeedItemWithIdentity(item, source, effective, domainValue[MetadataHold](generated.MetadataHold{}), testRules(), tvEpisodeIdentity(t, "3"))
	release.FrozenVars = explicitTestFrozenVars()
	outcome := renderPreviewFileForName(item, release, effective, "Preview Show", source, testRules(), "Preview Show - 03.mkv", "video", 0, release.FrozenEpisode, false, false)
	if outcome.RenderedTarget != nil {
		t.Fatalf("a throwing template must not produce a target: %+v", outcome)
	}
	if outcome.RenderError == nil || !strings.Contains(*outcome.RenderError, "缺少标题") {
		t.Fatalf("the thrown message must be carried on the file result: %+v", outcome)
	}
	if outcome.Safety != PreviewFileResultSafety("invalid") {
		t.Fatalf("a render failure must mark the file invalid: %+v", outcome)
	}
}

// A value with no value renders empty; the preview refuses only an invalid
// result ("-.mkv"), naming the missing fact, and otherwise shows the target
// with the facts that rendered empty.
func TestPreviewRefusesOnlyAnInvalidRenderedName(t *testing.T) {
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	fp, err := namingvars.Fingerprint(vars)
	if err != nil {
		t.Fatal(err)
	}
	if err := namingvars.SetActive(vars, fp); err != nil {
		t.Fatal(err)
	}
	rules := NamingRulesFromDomain(naming.DefaultNamingRules())
	rules.Tv.VideoFilenameTemplate = "${bgm.id}-${anibt.language}.${file.ext}"
	effective := EffectiveConfig{Rules: EffectiveRules{
		Parser: ParserRules{EpisodePatterns: acquisitiondomain.DefaultEpisodePatterns},
		Naming: rules,
	}}
	item := acquisitiondomain.FeedItem{
		GUID:            "preview-missing-work-ids",
		Title:           "[RealGroup] Preview Show - 03 [1080p]",
		TorrentFilename: "Preview Show - 03.mkv",
	}
	source := SourceEvaluationInput{GroupName: "RealGroup"}
	release := EvaluateFeedItemWithIdentity(item, source, effective, domainValue[MetadataHold](generated.MetadataHold{}), testRules(), tvEpisodeIdentity(t, "3"))
	release.FrozenVars = explicitTestFrozenVars()
	outcome := renderPreviewFileForName(item, release, effective, "Preview Show", source, testRules(), "Preview Show - 03.mkv", "video", 0, release.FrozenEpisode, false, false)
	if outcome.RenderedTarget != nil {
		t.Fatalf("a template needing absent work ids must not produce a target: %q", *outcome.RenderedTarget)
	}
	if outcome.Safety != PreviewFileResultSafety("invalid") {
		t.Fatalf("the refusal marks the file invalid: %+v", outcome)
	}
	// No Bangumi binding and not an AniBT release: both are undefined
	// variables, never empty values.
	if outcome.ReasonCode == nil || *outcome.ReasonCode != "naming_fact_undefined_anibt_language" {
		t.Fatalf("the refusal names the undefined variable: %+v", outcome)
	}
	if strings.Join(outcome.EmptyFacts, ",") != "anibt.language,bgm.id" {
		t.Fatalf("the refusal lists every undefined variable: %+v", outcome.EmptyFacts)
	}
	effective.Rules.Naming.Tv.VideoFilenameTemplate = "${anime.title_override}-${tmdb.id}.${file.ext}"
	outcome = renderPreviewFileForName(item, release, effective, "Preview Show", source, testRules(), "Preview Show - 03.mkv", "video", 0, release.FrozenEpisode, false, false)
	if outcome.RenderedTarget == nil || *outcome.RenderedTarget != "/media/TV/Preview Show (2025)/Season 01/-1.mkv" || strings.Join(outcome.EmptyFacts, ",") != "anime.title_override" {
		t.Fatalf("one empty value = %+v", outcome)
	}
}

// The manifest preview applies the target plan's duplicate rule: one episode
// rendered twice keeps the first by source path, different episodes or
// subtitles on one target are all dropped.
func TestMarkDuplicateTargets(t *testing.T) {
	target := func(value string) *string { return &value }
	episode := func(number string) *naming.FrozenEpisodeNamingFacts {
		return &naming.FrozenEpisodeNamingFacts{Number: number, Display: number, Raw: number}
	}
	files := []ManifestFileInput{
		{SourcePath: "a.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: episode("1")},
		{SourcePath: "b.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: episode("1")},
		{SourcePath: "c.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: episode("2")},
		{SourcePath: "d.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: episode("3")},
		{SourcePath: "e.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: episode("4")},
	}
	results := []ManifestFileResult{
		{SourcePath: "a.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show S01E01.mkv")},
		{SourcePath: "b.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show S01E01.mkv")},
		{SourcePath: "c.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show.mkv")},
		{SourcePath: "d.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show.mkv")},
		{SourcePath: "e.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show S01E04.mkv")},
	}
	markDuplicateTargets(results, files, false, "fail", false)
	got := []string{}
	for _, result := range results {
		reason := ""
		if result.ReasonCode != nil {
			reason = *result.ReasonCode
		}
		got = append(got, fmt.Sprintf("%s:%t:%s", result.SourcePath, result.Included, reason))
	}
	want := "a.mkv:true:,b.mkv:false:target_duplicate,c.mkv:false:target_duplicate,d.mkv:false:target_duplicate,e.mkv:true:"
	if strings.Join(got, ",") != want {
		t.Fatalf("marked = %s", strings.Join(got, ","))
	}
	subtitles := []ManifestFileResult{
		{SourcePath: "a.chs.ass", MediaKind: "subtitle", Included: true, RenderedTarget: target("/l/Show S01E01.ass")},
		{SourcePath: "a.cht.ass", MediaKind: "subtitle", Included: true, RenderedTarget: target("/l/Show S01E01.ass")},
	}
	markDuplicateTargets(subtitles, []ManifestFileInput{{SourcePath: "a.chs.ass"}, {SourcePath: "a.cht.ass"}}, true, "fail", false)
	if subtitles[0].Included || subtitles[1].Included {
		t.Fatalf("subtitles on one target must both be dropped: %+v", subtitles)
	}
}

// Under a manual one-shot selection a same-episode group on one target follows
// the global collision policy; different episodes on one target still drop.
func TestMarkDuplicateTargetsManualSelection(t *testing.T) {
	target := func(value string) *string { return &value }
	episode := &naming.FrozenEpisodeNamingFacts{Number: "1", Display: "1", Raw: "1"}
	other := &naming.FrozenEpisodeNamingFacts{Number: "2", Display: "2", Raw: "2"}
	files := []ManifestFileInput{
		{SourcePath: "Pack/S2/Show - 01.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: episode},
		{SourcePath: "Pack/Show - 01.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: episode},
		{SourcePath: "c.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: episode},
		{SourcePath: "d.mkv", MediaKind: "video", PerFileEpisode: true, FrozenEpisode: other},
	}
	run := func(policy string) string {
		results := []ManifestFileResult{
			{SourcePath: "Pack/S2/Show - 01.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show S01E01.mkv")},
			{SourcePath: "Pack/Show - 01.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show S01E01.mkv")},
			{SourcePath: "c.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show.mkv")},
			{SourcePath: "d.mkv", MediaKind: "video", Included: true, RenderedTarget: target("/l/Show.mkv")},
		}
		markDuplicateTargets(results, files, false, policy, true)
		got := []string{}
		for _, result := range results {
			reason := ""
			if result.ReasonCode != nil {
				reason = *result.ReasonCode
			}
			got = append(got, fmt.Sprintf("%s:%t:%s:%s", result.SourcePath, result.Included, reason, *result.RenderedTarget))
		}
		return strings.Join(got, ",")
	}
	tail := ",c.mkv:false:target_duplicate:/l/Show.mkv,d.mkv:false:target_duplicate:/l/Show.mkv"
	cases := map[string]string{
		"fail":      "Pack/S2/Show - 01.mkv:true::/l/Show S01E01.mkv,Pack/Show - 01.mkv:false:target_duplicate:/l/Show S01E01.mkv" + tail,
		"versioned": "Pack/S2/Show - 01.mkv:true::/l/Show S01E01.mkv,Pack/Show - 01.mkv:true::/l/Show S01E01 (2).mkv" + tail,
		"overwrite": "Pack/S2/Show - 01.mkv:false:target_replaced_in_release:/l/Show S01E01.mkv,Pack/Show - 01.mkv:true::/l/Show S01E01.mkv" + tail,
	}
	for policy, want := range cases {
		if got := run(policy); got != want {
			t.Fatalf("%s:\n got  %s\n want %s", policy, got, want)
		}
	}
}
