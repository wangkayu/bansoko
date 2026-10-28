package batchplan

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
)

var (
	episodeName  = regexp.MustCompile(` - (\d+)[ .]`)
	episodeRange = regexp.MustCompile(` - (\d+-\d+)[ .]`)
)

// identityOf resolves "Show - NN.mkv" to episode NN of season 1, "Show -
// NN-MM.mkv" to the range NN..MM, and every other name to a missing identity,
// like the filename parser does.
func identityOf(sourcePath string) (episodeidentity.EpisodeIdentity, error) {
	input := episodeidentity.EpisodeIdentityInput{MediaKind: "tv"}
	if match := episodeRange.FindStringSubmatch(sourcePath); match != nil {
		input.FileEvidence = episodeidentity.NewUnsupportedEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", episodeidentity.ShapeRange, match[1], "")
	} else if match := episodeName.FindStringSubmatch(sourcePath); match != nil {
		input.FileEvidence = episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", strings.TrimLeft(match[1], "0"))
	} else {
		input.FileEvidence = episodeidentity.NewMissingEvidence(episodeidentity.EpisodeSourceFileParser, "file_name")
	}
	return episodeidentity.NewResolver().Resolve(input), nil
}

func classify(t *testing.T, route string, paths ...string) Plan {
	t.Helper()
	files := make([]InputFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, InputFile{SourcePath: path})
	}
	plan, err := Classify(Input{
		FileKindsVersion: filekinds.Version, UnclassifiedRoute: route, Files: files, Identity: identityOf,
		RuleExclusion: func(sourcePath string) string {
			if strings.Contains(sourcePath, "NCOP") {
				return "file_not_in"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func outcomes(plan Plan) map[string]string {
	out := map[string]string{}
	for _, file := range plan.Files {
		out[file.SourcePath] = string(file.Outcome) + "/" + file.ReasonCode
	}
	return out
}

func TestClassifyBatchPartialCoverage(t *testing.T) {
	plan := classify(t, RouteSkip,
		"Show/Show - 01.mkv", "Show/Show - 02.mkv", "Show/Show - 02.chs.ass",
		"Show/NCOP.mkv", "Show/Show SP.mkv", "Show/SPs/Show - 03.mkv", "Show/cover.jpg", "Show/notes.ass",
	)
	want := map[string]string{
		"Show/Show - 01.mkv":     "planned/",
		"Show/Show - 02.mkv":     "planned/",
		"Show/Show - 02.chs.ass": "subtitle_follows/",
		"Show/NCOP.mkv":          "excluded_by_rule/file_not_in",
		"Show/Show SP.mkv":       "episode_unrecognized/" + episodeidentity.FailEpisodeMissing,
		"Show/SPs/Show - 03.mkv": "ignored_nested/ignored_nested",
		"Show/cover.jpg":         "ignored_kind/ignored_kind",
		"Show/notes.ass":         "subtitle_unbound/subtitle_binding_missing",
	}
	if got := outcomes(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %v\nwant %v", got, want)
	}
	if !reflect.DeepEqual(plan.CoverageKeys, []string{"tv:e1", "tv:e2"}) {
		t.Fatalf("coverage keys = %v", plan.CoverageKeys)
	}
	if !plan.Accepted() {
		t.Fatal("a batch with two keyed videos is accepted")
	}
}

// The unknown-file route decides what happens to a video whose own filename
// carries no episode. A subtitle is never an unknown file: it either follows
// its bound video or binds none and is excluded as unbound. This guard pins the
// gates that keep the route away from subtitles (the kind check runs before the
// unknown pass and inside UnknownVideos).
func TestClassifyRoutesOnlyVideosAsUnknownFiles(t *testing.T) {
	plan := classify(t, RouteRoute,
		"Show/Show SP.mkv", "Show/Show SP.chs.ass", "Show/notes.ass", "Show/cover.jpg",
	)
	for _, file := range plan.Files {
		if file.Outcome == OutcomeEpisodeUnrecognized && file.Kind != filekinds.KindVideo {
			t.Fatalf("only a video can be an unknown file: %+v", file)
		}
	}
	if got := outcomes(plan)["Show/Show SP.chs.ass"]; got != "subtitle_follows/" {
		t.Fatalf("a subtitle of a routed unknown video follows it: %s", got)
	}
	if got := outcomes(plan)["Show/notes.ass"]; got != "subtitle_unbound/subtitle_binding_missing" {
		t.Fatalf("a subtitle that matches no video is unbound: %s", got)
	}
	if got := plan.UnknownVideos(); got != 1 {
		t.Fatalf("unknown videos = %d, want the release's one video", got)
	}
	if !plan.Accepted() {
		t.Fatal("the route makes the unknown video placeable")
	}
}

func TestAcceptanceMatrix(t *testing.T) {
	// All unknown, skip: nothing placeable.
	if plan := classify(t, RouteSkip, "Show/Show SP.mkv", "Show/Show SP.ass"); plan.Accepted() {
		t.Fatal("all unknown under skip must not be accepted")
	}
	// All unknown, route: accepted, the subtitle follows its unknown video.
	plan := classify(t, RouteRoute, "Show/Show SP.mkv", "Show/Show SP.ass")
	if !plan.Accepted() || len(plan.CoverageKeys) != 0 {
		t.Fatalf("all unknown under route is accepted without coverage: %+v", plan)
	}
	if got := outcomes(plan)["Show/Show SP.ass"]; got != "subtitle_follows/" {
		t.Fatalf("subtitle of a routed unknown video = %s", got)
	}
	if !plan.Required("Show/Show SP.mkv") || !plan.Required("Show/Show SP.ass") {
		t.Fatal("routed unknown video and its subtitle are required")
	}
	// A single-episode release whose one video has no episode in its name.
	if plan := classify(t, RouteSkip, "[Group] Show - 05 [1080p].mkv"); !plan.Accepted() {
		t.Fatal("an episode-bearing single file is accepted")
	}
	if plan := classify(t, RouteSkip, "[Group] Show [1080p].mkv"); plan.Accepted() {
		t.Fatal("a single file without an episode in its own name is not accepted under skip")
	}
	// Rule-excluded only.
	if plan := classify(t, RouteRoute, "Show/NCOP.mkv"); plan.Accepted() {
		t.Fatal("a rule-excluded video is never an unknown file")
	}
}

func TestDedupKeepReplaceInFlight(t *testing.T) {
	plan := classify(t, RouteSkip, "Show/Show - 01.mkv", "Show/Show - 02.mkv", "Show/Show - 03.mkv", "Show/Show - 04.mkv", "Show/Show - 02.ass")
	plan.ApplyDedup(map[string]Verdict{
		"tv:e1": VerdictExisting,
		"tv:e2": VerdictReplace,
		"tv:e3": VerdictInFlight,
		"tv:e4": VerdictOpen,
	})
	got := outcomes(plan)
	for path, want := range map[string]string{
		"Show/Show - 01.mkv": "covered_existing/covered_existing",
		"Show/Show - 02.mkv": "planned/",
		"Show/Show - 03.mkv": "covered_in_flight/covered_in_flight",
		"Show/Show - 04.mkv": "planned/",
		"Show/Show - 02.ass": "subtitle_follows/",
	} {
		if got[path] != want {
			t.Errorf("%s = %s, want %s", path, got[path], want)
		}
	}
	// A replaced file of a batch stays planned: it replaces only its own key.
	if !reflect.DeepEqual(plan.CoverageKeys, []string{"tv:e2", "tv:e4"}) {
		t.Fatalf("coverage = %v", plan.CoverageKeys)
	}
	all := classify(t, RouteSkip, "Show/Show - 01.mkv", "Show/Show - 02.mkv")
	all.ApplyDedup(map[string]Verdict{"tv:e1": VerdictExisting, "tv:e2": VerdictExisting})
	if all.Accepted() {
		t.Fatal("a batch whose every key exists is not accepted (no_placeable_files)")
	}
}

func TestSingleKeyedVideoKeepsReplacement(t *testing.T) {
	plan := classify(t, RouteSkip, "Show - 05.mkv")
	plan.ApplyDedup(map[string]Verdict{"tv:e5": VerdictReplace})
	if got := outcomes(plan)["Show - 05.mkv"]; got != "planned/" {
		t.Fatalf("single-episode replace stays planned, got %s", got)
	}
	plan.ApplyDedup(map[string]Verdict{"tv:e5": VerdictInFlight})
	if plan.Accepted() {
		t.Fatal("an in-flight single episode is not accepted")
	}
}

func TestMarkCollisionDropsOnlyThatFile(t *testing.T) {
	plan := classify(t, RouteSkip, "Show/Show - 01.mkv", "Show/Show - 02.mkv", "Show/Show - 01.ass")
	plan.MarkCollision("Show/Show - 01.mkv", "target_collision_blocked")
	got := outcomes(plan)
	if got["Show/Show - 01.mkv"] != "target_collision/target_collision_blocked" || got["Show/Show - 02.mkv"] != "planned/" {
		t.Fatalf("collision outcomes = %v", got)
	}
	if got["Show/Show - 01.ass"] != "subtitle_unbound/subtitle_video_excluded" {
		t.Fatalf("subtitle of a collided video = %s", got["Show/Show - 01.ass"])
	}
	if !plan.Accepted() {
		t.Fatal("the remaining planned file keeps the release accepted")
	}
}

func TestMovieMainAndUnknown(t *testing.T) {
	big, small := int64(900), int64(10)
	key := "movie:0"
	plan, err := Classify(Input{
		FileKindsVersion: filekinds.Version, UnclassifiedRoute: RouteSkip, Movie: true,
		MovieIdentity: episodeidentity.EpisodeIdentity{State: episodeidentity.StateResolved, CoverageKey: &key},
		Files:         []InputFile{{SourcePath: "Movie/trailer.mkv", SizeBytes: &small}, {SourcePath: "Movie/Movie.mkv", SizeBytes: &big}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(plan)
	if got["Movie/Movie.mkv"] != "planned/" || got["Movie/trailer.mkv"] != "episode_unrecognized/unknown" {
		t.Fatalf("movie outcomes = %v", got)
	}
}

func TestEpisodesAndLabel(t *testing.T) {
	plan := classify(t, RouteSkip, "Show/Show - 01.mkv", "Show/Show - 03.mkv", "Show/Show - 07.mkv")
	episodes, ok := plan.Episodes()
	if !ok || !reflect.DeepEqual(episodes, []int{1, 3, 7}) {
		t.Fatalf("episodes = %v %v", episodes, ok)
	}
	// A frozen plan keeps the v1 keys it was accepted with; both shapes read
	// back as their episodes.
	frozen := Plan{CoverageKeys: []string{"tv:s2:e5", "tv:e3", "movie:0"}}
	if episodes, ok := frozen.Episodes(); !ok || !reflect.DeepEqual(episodes, []int{3, 5}) {
		t.Fatalf("frozen episodes = %v %v", episodes, ok)
	}
	for in, want := range map[string]string{
		"1,3,7":     "01, 03, 07",
		"1,2,3,4":   "01-04",
		"1,2,3,7,8": "01-03, 07-08",
		"12":        "12",
		"":          "",
	} {
		values := []int{}
		for _, part := range strings.Split(in, ",") {
			if part == "" {
				continue
			}
			var n int
			for _, r := range part {
				n = n*10 + int(r-'0')
			}
			values = append(values, n)
		}
		if got := EpisodesLabel(values); got != want {
			t.Errorf("EpisodesLabel(%s) = %q, want %q", in, got, want)
		}
	}
}

// A frozen plan reads its download-completion action back; a plan without one,
// or with an unknown one, reads as seed (take no action).
func TestPlanCompletionAction(t *testing.T) {
	for stored, want := range map[string]string{
		"":                       CompletionSeed,
		CompletionSeed:           CompletionSeed,
		CompletionPause:          CompletionPause,
		CompletionRemove:         CompletionRemove,
		CompletionRemoveWithData: CompletionRemoveWithData,
		"wipe":                   CompletionSeed,
		"REMOVE_WITH_DATA":       CompletionSeed,
	} {
		if got := (Plan{DownloadCompletion: stored}).CompletionAction(); got != want {
			t.Fatalf("stored %q reads %q, want %q", stored, got, want)
		}
	}
	plan, err := Classify(Input{FileKindsVersion: filekinds.Version, UnclassifiedRoute: RouteSkip, DownloadCompletion: CompletionRemove, Identity: identityOf,
		Files: []InputFile{{SourcePath: "Show/Show - 01.mkv"}}})
	if err != nil || plan.DownloadCompletion != CompletionRemove {
		t.Fatalf("classify keeps the action: %q %v", plan.DownloadCompletion, err)
	}
}

func TestSeedLimitsValidateAndRead(t *testing.T) {
	minutes, ratio, zero, tooLong := int64(60), 1.5, 0.0, int64(MaxSeedingTimeMinutes+1)
	if err := (SeedLimits{SeedingTimeMinutes: &minutes, AtLimit: SeedLimitStop}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (SeedLimits{Ratio: &ratio, AtLimit: SeedLimitRemoveWithData}).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]SeedLimits{
		"no limit":       {AtLimit: SeedLimitStop},
		"zero ratio":     {Ratio: &zero, AtLimit: SeedLimitStop},
		"too long":       {SeedingTimeMinutes: &tooLong, AtLimit: SeedLimitStop},
		"no action":      {Ratio: &ratio},
		"unknown action": {Ratio: &ratio, AtLimit: "pause"},
	} {
		if bad.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	limits := &SeedLimits{Ratio: &ratio, AtLimit: SeedLimitRemove}
	if got := (Plan{DownloadCompletion: CompletionSeed, DownloadCompletionSeedLimits: limits}).CompletionSeedLimits(); !got.Equal(limits) || got == limits {
		t.Fatalf("seed limits read = %+v", got)
	}
	if got := (Plan{DownloadCompletion: CompletionPause, DownloadCompletionSeedLimits: limits}).CompletionSeedLimits(); got != nil {
		t.Fatalf("limits on pause read = %+v", got)
	}
	if got := (Plan{}).CompletionSeedLimits(); got != nil {
		t.Fatalf("a plan without limits read = %+v", got)
	}
	plan, err := Classify(Input{FileKindsVersion: filekinds.Version, UnclassifiedRoute: RouteSkip, DownloadCompletion: CompletionSeed, DownloadCompletionSeedLimits: limits, Identity: identityOf,
		Files: []InputFile{{SourcePath: "Show/Show - 01.mkv"}}})
	if err != nil || !plan.DownloadCompletionSeedLimits.Equal(limits) {
		t.Fatalf("classify keeps the limits: %+v %v", plan.DownloadCompletionSeedLimits, err)
	}
}

// A multi-episode file occupies every member key; it is replaced only when no
// key is existing, a partial replacement keeps the present file, and its unit
// is all of its keys.
func TestMultiEpisodeFileKeysAndDedup(t *testing.T) {
	plan := classify(t, RouteSkip, "Show/Show - 01-02.mkv", "Show/Show - 03.mkv", "Show/Show - 01-02.ass")
	file, _ := plan.File("Show/Show - 01-02.mkv")
	if file.Outcome != OutcomePlanned || file.CoverageKey != "tv:e1" || !reflect.DeepEqual(file.Keys(), []string{"tv:e1", "tv:e2"}) {
		t.Fatalf("double file = %+v keys=%v", file, file.Keys())
	}
	if !reflect.DeepEqual(plan.CoverageKeys, []string{"tv:e1", "tv:e2", "tv:e3"}) || !reflect.DeepEqual(plan.CandidateKeys(), plan.CoverageKeys) {
		t.Fatalf("coverage = %v candidates=%v", plan.CoverageKeys, plan.CandidateKeys())
	}
	units := plan.CandidateUnits()
	if !reflect.DeepEqual(units["tv:e2"], []string{"tv:e1", "tv:e2"}) || !reflect.DeepEqual(units["tv:e3"], []string{"tv:e3"}) {
		t.Fatalf("units = %v", units)
	}
	for _, tc := range []struct {
		name     string
		verdicts map[string]Verdict
		want     string
	}{
		{"both replace", map[string]Verdict{"tv:e1": VerdictReplace, "tv:e2": VerdictReplace}, "planned/"},
		{"replace and fill", map[string]Verdict{"tv:e1": VerdictReplace, "tv:e2": VerdictOpen}, "planned/"},
		{"one key kept", map[string]Verdict{"tv:e1": VerdictReplace, "tv:e2": VerdictExisting}, "covered_existing/covered_existing"},
		{"partial", map[string]Verdict{"tv:e1": VerdictReplacePartial, "tv:e2": VerdictReplace}, "covered_existing/replacement_partial_coverage"},
		{"one key in flight", map[string]Verdict{"tv:e2": VerdictInFlight}, "covered_in_flight/covered_in_flight"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := classify(t, RouteSkip, "Show/Show - 01-02.mkv", "Show/Show - 03.mkv")
			plan.ApplyDedup(tc.verdicts)
			if got := outcomes(plan)["Show/Show - 01-02.mkv"]; got != tc.want {
				t.Fatalf("double file = %s, want %s", got, tc.want)
			}
			if got := outcomes(plan)["Show/Show - 03.mkv"]; got != "planned/" {
				t.Fatalf("single file = %s, want planned", got)
			}
		})
	}
	// A single file's frozen JSON carries no coverage_keys.
	single, _ := plan.File("Show/Show - 03.mkv")
	if len(single.CoverageKeys) != 0 {
		t.Fatalf("single file coverage_keys = %v", single.CoverageKeys)
	}
}

// A bound subtitle follows its video, not the file rules: the rule that
// would match the subtitle's own name does not
// exclude it while its video is planned, and an excluded video takes its
// subtitle with it.
func TestClassifyBoundSubtitleFollowsTheVideoNotTheFileRules(t *testing.T) {
	plan := classify(t, RouteSkip, "Show/Show - 01.mkv", "Show/Show - 01.NCOP.ass", "Show/NCOP.mkv", "Show/NCOP.chs.ass")
	got := outcomes(plan)
	if got["Show/Show - 01.NCOP.ass"] != "subtitle_follows/" {
		t.Fatalf("a bound subtitle the rule matches = %q, want subtitle_follows", got["Show/Show - 01.NCOP.ass"])
	}
	if got["Show/NCOP.mkv"] != "excluded_by_rule/file_not_in" || got["Show/NCOP.chs.ass"] != "subtitle_unbound/subtitle_video_excluded" {
		t.Fatalf("the excluded video and its subtitle = %q / %q", got["Show/NCOP.mkv"], got["Show/NCOP.chs.ass"])
	}
}
