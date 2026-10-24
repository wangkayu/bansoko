package application

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acquisition "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

// The pre-download preview (RSS list, draft bench, one-shot, acquisition all
// render through anime/domain) and the typed planner must treat the owner
// season the same. The named season is the owner-edited season frozen in the
// Vars input (anime.season); the TMDB binding's season never stands in. An
// unset season has no implicit default: the shipped default TV directory keeps
// anime.season_folder as a folder level of its own, so both paths refuse the
// render and name that fact (fail-closed; the repair is setting the season).
// An input frozen before the owner season existed names the season its TMDB
// binding named (naming-render-3), on both paths.
func TestOwnerSeasonPreviewAndPlannerRenderTheSameTarget(t *testing.T) {
	rules := naming.DefaultNamingRules()
	attrRules := attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, GroupBrackets: []string{"[", "]"}}
	effective := animedomain.EffectiveConfig{}
	effective.Rules.Naming = animedomain.NamingRulesFromDomain(rules)
	episode := &naming.FrozenEpisodeNamingFacts{Raw: "3", Number: "3", Display: "3"}
	const title = "[Group] Show - 03 [1080p]"
	year := int64(2025)
	owner := func(value int64) *int64 { return &value }

	for _, tc := range []struct {
		name        string
		owner       *int64
		tmdb        bool
		season      int
		seasonBound bool
		legacyInput bool
		wantSeason  string
	}{
		{name: "unset, no TMDB binding"},
		{name: "unset despite a bound TMDB season", tmdb: true, season: 2},
		{name: "owner specials season", owner: owner(0), wantSeason: "/Season 00/"},
		{name: "owner season wins over the TMDB season", owner: owner(2), tmdb: true, season: 3, wantSeason: "/Season 02/"},
		{name: "legacy input names its TMDB season", tmdb: true, season: 2, legacyInput: true, wantSeason: "/Season 02/"},
		{name: "legacy input, preserved season 0", tmdb: true, season: 0, seasonBound: true, legacyInput: true, wantSeason: "/Season 00/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := namingvars.ReleaseEvidence{AnimeEvidence: namingvars.AnimeEvidence{AnimeYear: &year, AnimeSeason: tc.owner}, ReleaseTitle: title}
			if tc.tmdb {
				season := int64(tc.season)
				evidence.TmdbID, evidence.TmdbKind, evidence.TmdbSeason = "tmdb-1", "tv", &season
			}
			facts := namingvars.BuildInput(evidence).Facts
			if tc.legacyInput {
				delete(facts["anime"].(map[string]any), "season")
			}
			frozen := namingvars.FrozenEvaluation{Input: namingvars.Input{ReleaseTitle: title, Facts: facts}, Output: namingvars.Vars{}, Status: "ok"}

			// Preview path.
			release := animedomain.PreviewReleaseResult{FrozenVars: &frozen, FrozenEpisode: episode}
			preview := animedomain.PreviewExactFile(acquisition.FeedItem{Title: title}, release, animedomain.ManifestFileInput{SourcePath: "Show - 03.mkv", MediaKind: "video", SizeBytes: 17}, effective, "Show", animedomain.SourceEvaluationInput{}, attrRules)

			// Planner path: the artifact's season column comes from the same
			// frozen binding through the prepare projection.
			rawFacts, _ := json.Marshal(map[string]string{"title": title})
			artifact := artifactContext{animeTitle: "Show", rawFacts: rawFacts, acceptedFacts: true, acceptedResolution: "1080p"}
			if tc.tmdb {
				artifact.tmdbWorkID.String, artifact.tmdbWorkID.Valid = "tmdb-1", true
				artifact.tmdbMediaKind.String, artifact.tmdbMediaKind.Valid = "tv", true
				artifact.tmdbSeason = frozenSeasonColumn(tc.season, tc.seasonBound)
			}
			planned, planErr := planSeasonTarget(artifact, rules, episode, &frozen)
			if tc.wantSeason == "" {
				// An unset owner season refuses on both paths, naming the
				// empty season folder of the default directory.
				if preview.RenderedTarget != nil || strings.Join(preview.EmptyFacts, ",") != "anime.season_folder" {
					t.Fatalf("unset season must refuse the preview, got %+v", preview)
				}
				var namingErr *naming.Error
				if planErr == nil || !errors.As(planErr, &namingErr) || strings.Join(namingErr.Missing, ",") != "anime.season_folder" {
					t.Fatalf("unset season must refuse the planner render, got %q %v", planned, planErr)
				}
				return
			}
			if tc.seasonBound {
				// A preserved season 0 is frozen only by explicit continuous-SP
				// recognition; the preview's automatic binding never carries
				// it, so the preview refuses while the planner still names
				// the preserved season its input carried.
				if preview.RenderedTarget != nil || strings.Join(preview.EmptyFacts, ",") != "anime.season_folder" {
					t.Fatalf("a preserved season 0 must refuse the preview, got %+v", preview)
				}
				if planErr != nil || !strings.Contains(planned, tc.wantSeason) {
					t.Fatalf("planner target %q %v, want %s", planned, planErr, tc.wantSeason)
				}
				return
			}
			if preview.RenderedTarget == nil {
				t.Fatalf("preview render failed: %+v", preview)
			}
			if planErr != nil {
				t.Fatalf("planner render failed: %q %v", planned, planErr)
			}
			if planned != *preview.RenderedTarget {
				t.Fatalf("planner target %q != preview target %q", planned, *preview.RenderedTarget)
			}
			if !strings.Contains(planned, tc.wantSeason) {
				t.Fatalf("target %q, want %s", planned, tc.wantSeason)
			}
		})
	}
}

func planSeasonTarget(artifact artifactContext, rules naming.NamingRules, episode *naming.FrozenEpisodeNamingFacts, frozen *namingvars.FrozenEvaluation) (string, error) {
	resolved, err := buildNamingContext(artifact, artifactFileContext{relativePath: "Show - 03.mkv", sourcePath: "Show - 03.mkv", mediaKind: "video", sizeBytes: 17}, episode, frozen)
	if err != nil {
		return "", err
	}
	engine := naming.NewEngine()
	compiled, err := engine.CompileRulesWithVars(rules, frozen.Output)
	if err != nil {
		return "", err
	}
	directory, filename, err := compiled.RenderVideo(engine, resolved.Context)
	if err != nil {
		return "", err
	}
	return renderedTarget(directory, filename)
}

// A library root under a dot directory (CI's GOTMPDIR is
// $GITHUB_WORKSPACE/.gotmp) is the template's own literal text: the planner
// target keeps it verbatim, and so does the subtitle beside the locked video.
func TestPlannerKeepsADotDirectoryInTheLibraryRoot(t *testing.T) {
	const root = "/work/.gotmp/TestRoot/001"
	rules := naming.DefaultNamingRules()
	rules.TV.DirectoryTemplate = strings.Replace(rules.TV.DirectoryTemplate, "/media", root, 1)
	episode := &naming.FrozenEpisodeNamingFacts{Raw: "1", Number: "1", Display: "1"}
	const title = "[Group] Show - 01 [1080p]"
	year := int64(2025)
	season := int64(1)
	evidence := namingvars.ReleaseEvidence{AnimeEvidence: namingvars.AnimeEvidence{AnimeYear: &year, AnimeSeason: &season}, ReleaseTitle: title}
	evidence.TmdbID, evidence.TmdbKind, evidence.TmdbSeason = "tmdb-1", "tv", &season
	frozen := namingvars.FrozenEvaluation{Input: namingvars.Input{ReleaseTitle: title, Facts: namingvars.BuildInput(evidence).Facts}, Output: namingvars.Vars{}, Status: "ok"}
	rawFacts, _ := json.Marshal(map[string]string{"title": title})
	artifact := artifactContext{animeTitle: "Show", rawFacts: rawFacts, acceptedFacts: true, acceptedResolution: "1080p"}
	artifact.tmdbWorkID.String, artifact.tmdbWorkID.Valid = "tmdb-1", true
	artifact.tmdbMediaKind.String, artifact.tmdbMediaKind.Valid = "tv", true
	artifact.tmdbSeason = frozenSeasonColumn(1, false)

	video, err := planSeasonTarget(artifact, rules, episode, &frozen)
	if err != nil {
		t.Fatal(err)
	}
	if want := root + "/TV/Show (2025)/Season 01/Show S01E01.mkv"; video != want {
		t.Fatalf("video target %q, want %q", video, want)
	}
	directory := video[:strings.LastIndex(video, "/")]
	var subtitle string
	subtitle, err = renderedTarget(directory, naming.CompanionSidecarFilename("Show S01E01", "zh-CN", "ass"))
	if err != nil || subtitle != root+"/TV/Show (2025)/Season 01/Show S01E01.zh-CN.ass" {
		t.Fatalf("subtitle target %q, %v", subtitle, err)
	}
}
