package application

import (
	"encoding/json"
	"testing"

	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

// The placement stage freezes a bound subtitle's target as the video's locked
// basename, the suffix the naming rules render for the subtitle, and the
// subtitle's own extension, composed by naming.CompanionSidecarFilename. This
// is the placement half of the preview/placement guard: it renders the suffix
// from the placement's own naming context builder (buildNamingContext), so the
// frozen name and the name the RSS preview derives are the same string for the
// same naming configuration.
func TestSubtitlePlacementComposesTheCompanionFilename(t *testing.T) {
	// The release's frozen source facts carry the title-parse set; the default
	// suffix template renders sub.lang_tag, which falls back to its
	// rt.sub_lang (SC) for a subtitle whose own tag names no language.
	artifact := artifactContext{
		releaseID:     "release-1",
		animeTitle:    "Show",
		rawFacts:      json.RawMessage(`{"title":"[RealGroup] Show - 01 [CHS] [1080p]"}`),
		sourceFacts:   json.RawMessage(`{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@x","tags":["resolution:1080p","sub_lang:SC"],"value_tags":["title:Show"]}}`),
		sourceKind:    "rss",
		groupName:     "RealGroup",
		acceptedFacts: true,
	}
	frozen := &namingvars.FrozenEvaluation{Status: "ok", Output: namingvars.Vars{"test": {"marker": "ok"}}}

	for _, tc := range []struct {
		name           string
		subtitlePath   string
		suffixTemplate *string
		want           string
	}{
		{name: "rendered suffix", subtitlePath: "Show - 01.zh-Hans.ass", want: "Show - E01.zh-Hans.ass"},
		{name: "suffix template empty", subtitlePath: "Show - 01.zh-Hans.ass", suffixTemplate: new(string), want: "Show - E01.ass"},
		{name: "subtitle extension is the video's", subtitlePath: "Show - 01.zh.mkv", want: "Show - E01.zh-Hans.mkv"},
		// The subtitle file's own tag comes first (sub.lang source=file).
		{name: "own tag chs&jpn", subtitlePath: "Show - 01.chs&jpn.ass", want: "Show - E01.zh-Hans.ass"},
		{name: "own tag cht&jpn", subtitlePath: "Show - 01.cht&jpn.ass", want: "Show - E01.zh-Hant.ass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := naming.DefaultNamingRules()
			if tc.suffixTemplate != nil {
				rules.SubtitleSuffixTemplate = *tc.suffixTemplate
			}
			engine := naming.NewEngine()
			compiled, err := engine.CompileRulesWithVars(rules, frozen.Output)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := buildNamingContext(artifact, artifactFileContext{sourcePath: tc.subtitlePath, mediaKind: "subtitle", sizeBytes: 20}, nil, frozen)
			if err != nil {
				t.Fatal(err)
			}
			suffix, err := compiled.RenderSubtitleSuffix(engine, resolved.Context)
			if err != nil {
				t.Fatal(err)
			}
			// The video target's own segments are the locked basename the
			// subtitle's plan carries (prepare.go's lockedBasename).
			got := naming.CompanionSidecarFilename("Show - E01", suffix, resolved.Context.File.Extension)
			if got != tc.want {
				t.Fatalf("frozen subtitle filename = %q, want %q", got, tc.want)
			}
		})
	}
}
