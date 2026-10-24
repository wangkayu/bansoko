package domain

import (
	"strings"
	"testing"

	acquisition "bansoko/internal/acquisition/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

func TestExactPreviewUsesRealFrozenVarsNotActiveSample(t *testing.T) {
	source := `vars.release = {title: input.release_title};`
	fp, _ := namingvars.SourceFingerprint(source)
	program, err := namingvars.NewProgram(namingvars.ProgramRef{Version: namingvars.ProfileVersion(fp), Digest: fp}, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := namingvars.SetActive(namingvars.Vars{"release": {"title": "WRONG SAMPLE"}}, "sample"); err != nil {
		t.Fatal(err)
	}
	rules := attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web"}, Language: []string{"jpn"}, SpecialMarkers: []string{"special"}, GroupBrackets: []string{"[", "]"}}
	effective := EffectiveConfig{}
	effective.Rules.Naming = NamingRulesFromDomain(naming.DefaultNamingRules())
	// The directory template must render an absolute target: a relative
	// directory stays fail-closed, so the fixture names the code default
	// deployment root and lets the frozen vars fill the rest.
	effective.Rules.Naming.Tv.DirectoryTemplate = "/library/${vars.release.title}"
	for _, title := range []string{"Release A", "Release B"} {
		// A literal admission record of `source`; fixtures never run the
		// time-limited evaluator.
		frozen := namingvars.FrozenEvaluation{Program: program.Ref(), Input: namingvars.Input{ReleaseTitle: title, Files: []namingvars.File{{Path: "01.mkv", Name: "01.mkv", Size: 17}}}, Output: namingvars.Vars{"release": {"title": title}}, Status: "ok"}
		release := PreviewReleaseResult{FrozenVars: &frozen, FrozenEpisode: &naming.FrozenEpisodeNamingFacts{Raw: "1", Number: "1", Display: "1"}}
		result := PreviewExactFile(acquisition.FeedItem{Title: title}, release, ManifestFileInput{SourcePath: "01.mkv", MediaKind: "video", SizeBytes: 17}, effective, "Anime", SourceEvaluationInput{}, rules)
		if result.RenderedTarget == nil {
			t.Fatalf("render failed: %+v", result)
		}
		if !strings.Contains(*result.RenderedTarget, title) || strings.Contains(*result.RenderedTarget, "WRONG SAMPLE") {
			t.Fatalf("target=%s", *result.RenderedTarget)
		}
	}
}
