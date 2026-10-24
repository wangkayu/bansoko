package domain

import (
	"strings"
	"testing"

	acquisition "bansoko/internal/acquisition/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

// The pre-download target renders the value namespaces from the item's frozen
// tag sets, and an item that carries none renders them as missing instead of
// re-parsing its title.
func TestPreviewRendersFrozenNamespacesWithoutReparsing(t *testing.T) {
	effective := EffectiveConfig{}
	effective.Rules.Naming = NamingRulesFromDomain(naming.DefaultNamingRules())
	// The directory template renders an absolute target — a relative one stays
	// fail-closed — so the expectation below carries the deployment root the
	// code default names.
	effective.Rules.Naming.Tv.DirectoryTemplate = "/library/${rt.title}"
	effective.Rules.Naming.Tv.VideoFilenameTemplate = "[${rt.subgroup}] ${anibt.language ? `[${anibt.language}]` : ''}E${ep.display}.${file.ext}"
	release := PreviewReleaseResult{
		FrozenVars:    &namingvars.FrozenEvaluation{Status: "ok", Input: namingvars.Input{ReleaseTitle: "t", Files: []namingvars.File{}}, Output: namingvars.Vars{}},
		FrozenEpisode: &naming.FrozenEpisodeNamingFacts{Raw: "3", Number: "3", Display: "3"},
	}
	title := "[SampleGroup] Sample Title - 03 [1080p][CHS].mkv"
	file := ManifestFileInput{SourcePath: "03.mkv", MediaKind: "video", SizeBytes: 17}

	frozen := acquisition.FeedItem{
		Title:     title,
		TitleTags: &acquisition.TitleTagSet{Schema: acquisition.TitleTagSchema, Tags: []string{"resolution:1080p"}, ValueTags: []string{"title:Frozen Title", "group:Frozen Group"}},
		AniBTTags: &acquisition.AniBTTagSet{Tags: []string{"language:CHT", "language:CHS"}},
	}
	result := PreviewExactFile(frozen, release, file, effective, "Anime", SourceEvaluationInput{}, testRules())
	if result.RenderedTarget == nil || *result.RenderedTarget != "/library/Frozen Title/[Frozen Group] [CHS+CHT]E3.mkv" {
		t.Fatalf("frozen render = %+v", result)
	}

	// Non-AniBT release: the conditional AniBT segment drops out.
	frozen.AniBTTags = nil
	result = PreviewExactFile(frozen, release, file, effective, "Anime", SourceEvaluationInput{}, testRules())
	if result.RenderedTarget == nil || *result.RenderedTarget != "/library/Frozen Title/[Frozen Group] E3.mkv" {
		t.Fatalf("non-AniBT render = %+v", result)
	}

	// No frozen title tags: Rt stays missing, the title is not parsed again.
	result = PreviewExactFile(acquisition.FeedItem{Title: title}, release, file, effective, "Anime", SourceEvaluationInput{}, testRules())
	if result.RenderedTarget != nil || result.ReasonCode == nil || !strings.HasPrefix(*result.ReasonCode, "naming_fact_missing_rt_") {
		t.Fatalf("unfrozen render = %+v", result)
	}
}
