package target

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
	"bansoko/internal/filekinds"
)

func batchScenario(t *testing.T, names ...string) parityScenario {
	t.Helper()
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	scenario.item.Title = "[RealGroup] Preview Show [E01-E03] [1080p]"
	scenario.item.ReleaseFiles = nil
	for _, name := range names {
		size := int64(1024)
		scenario.item.ReleaseFiles = append(scenario.item.ReleaseFiles, acquisitiondomain.ReleaseFile{SourcePath: "Pack/" + name, Filename: name, SizeBytes: &size, MediaKind: "video"})
	}
	return scenario
}

func planOutcome(result PlanResult, sourcePath string) string {
	file, ok := result.BatchPlan.File(sourcePath)
	if !ok {
		return "missing"
	}
	return string(file.Outcome) + "/" + file.ReasonCode
}

// A batch title is evidence only: every video is planned from its own name, and
// a block collision drops only the conflicting file.
func TestBatchPreflightDropsOnlyTheCollidingFile(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01].mkv", "Preview Show [E02].mkv", "Preview Show [E03].mkv")
	first := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if first.Admission.Admission != AdmissionDownload || len(first.Set.Targets) != 3 {
		t.Fatalf("batch admission = %+v with %d targets, want three downloads", first.Admission, len(first.Set.Targets))
	}
	var conflicting string
	for _, spec := range first.Set.Targets {
		if spec.SourcePath == "Pack/Preview Show [E02].mkv" {
			conflicting = spec.TargetPath
		}
	}
	if err := os.MkdirAll(filepath.Dir(conflicting), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflicting, make([]byte, 7), 0o644); err != nil {
		t.Fatal(err)
	}
	result := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if result.Admission.Admission != AdmissionDownload || len(result.Set.Targets) != 2 {
		t.Fatalf("admission = %+v with %d targets, want the two free episodes", result.Admission, len(result.Set.Targets))
	}
	if got := planOutcome(result, "Pack/Preview Show [E02].mkv"); got != "target_collision/target_collision_blocked" {
		t.Fatalf("colliding file = %s", got)
	}
	if got := result.BatchPlan.CoverageKeys; len(got) != 2 {
		t.Fatalf("coverage = %v, want the two placed episodes", got)
	}
}

// Two planned videos of one episode that render one target (a v2): the first
// by source path keeps it, the other is dropped as target_duplicate.
func TestBatchPreflightDuplicateTargetKeepsTheFirstSourcePath(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01].mkv", "Preview Show [E01] v2.mkv")
	result := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if result.Admission.Admission != AdmissionDownload || len(result.Set.Targets) != 1 {
		t.Fatalf("admission = %+v with %d targets", result.Admission, len(result.Set.Targets))
	}
	if result.Set.Targets[0].SourcePath != "Pack/Preview Show [E01] v2.mkv" {
		t.Fatalf("kept %s, want the first by source path", result.Set.Targets[0].SourcePath)
	}
	if got := planOutcome(result, "Pack/Preview Show [E01].mkv"); got != "target_collision/target_duplicate" {
		t.Fatalf("duplicate = %s", got)
	}
}

// Two selected videos of one episode on one target under a manual one-shot
// selection follow the global collision policy in source path order: skip
// keeps the first, versioned keeps both (the later one versioned), overwrite
// keeps only the last (the earlier one is replaced inside the release and never
// written). Without a manual selection the first keeps it under every policy.
func TestBatchPreflightManualSelectionSameEpisodeFollowsCollisionPolicy(t *testing.T) {
	first, second := "Pack/Preview Show [E01] v2.mkv", "Pack/Preview Show [E01].mkv"
	cases := []struct {
		policy  animedomain.CollisionPolicy
		manual  bool
		kept    []string
		dropped map[string]string
	}{
		{animedomain.CollisionPolicyFail, true, []string{first}, map[string]string{second: "target_collision/target_duplicate"}},
		{animedomain.CollisionPolicyVersioned, true, []string{first, second}, nil},
		{animedomain.CollisionPolicyOverwrite, true, []string{second}, map[string]string{first: "target_collision/target_replaced_in_release"}},
		{animedomain.CollisionPolicyVersioned, false, []string{first}, map[string]string{second: "target_collision/target_duplicate"}},
		{animedomain.CollisionPolicyOverwrite, false, []string{first}, map[string]string{second: "target_collision/target_duplicate"}},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/manual=%t", c.policy, c.manual), func(t *testing.T) {
			scenario := batchScenario(t, "Preview Show [E01].mkv", "Preview Show [E01] v2.mkv")
			scenario.effective.Rules.FileEffect.CollisionPolicy = c.policy
			request := scenario.oneShot()
			if c.manual {
				request.FileSelection = filekinds.NewSelection([]string{first, second})
			}
			result := PreflightOneShot(context.Background(), request, FilesystemProbe{})
			if result.Admission.Admission != AdmissionDownload || len(result.Set.Targets) != len(c.kept) {
				t.Fatalf("admission = %+v with %d targets, want %d", result.Admission, len(result.Set.Targets), len(c.kept))
			}
			paths := map[string]string{}
			for _, spec := range result.Set.Targets {
				paths[spec.SourcePath] = spec.TargetPath
			}
			for _, source := range c.kept {
				if _, ok := paths[source]; !ok {
					t.Fatalf("targets %v miss %s", paths, source)
				}
			}
			if len(c.kept) == 2 {
				if want := batchplan.VersionedTargetPath(paths[first], 2); paths[second] != want {
					t.Fatalf("versioned target = %q, want %q", paths[second], want)
				}
				// The frozen plan carries the number for the file operation re-render.
				if entry, _ := result.BatchPlan.File(second); entry.TargetVersion != 2 {
					t.Fatalf("plan target_version = %d, want 2", entry.TargetVersion)
				}
				if entry, _ := result.BatchPlan.File(first); entry.TargetVersion != 0 {
					t.Fatalf("first copy target_version = %d, want none", entry.TargetVersion)
				}
			}
			for source, want := range c.dropped {
				if got := planOutcome(result, source); got != want {
					t.Fatalf("%s = %s, want %s", source, got, want)
				}
			}
		})
	}
}

// Different episodes that render one target (a name that lost what tells them
// apart, typically a value that rendered empty) all fail, under every
// collision policy: neither overwrites nor versions its sibling. The other
// episodes of the release keep going.
func TestBatchPreflightDifferentEpisodesOnOneTargetFailEveryMember(t *testing.T) {
	for _, policy := range []animedomain.CollisionPolicy{animedomain.CollisionPolicyFail, animedomain.CollisionPolicyVersioned, animedomain.CollisionPolicyOverwrite} {
		t.Run(string(policy), func(t *testing.T) {
			scenario := batchScenario(t, "Preview Show [E01].mkv", "Preview Show [E02].mkv")
			scenario.effective.Rules.FileEffect.CollisionPolicy = policy
			// The name reads only a value that is defined and empty (no title
			// override was set), so both episodes render one name.
			scenario.effective.Rules.Naming.Tv.VideoFilenameTemplate = "${anime.title} ${anime.title_override}.${file.ext}"
			result := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
			if result.Admission.Admission != AdmissionFailed || result.Admission.Reason != batchplan.ReasonTargetDuplicate || len(result.Set.Targets) != 0 {
				t.Fatalf("admission = %+v with %d targets, want a target_duplicate failure", result.Admission, len(result.Set.Targets))
			}
			for _, name := range []string{"Pack/Preview Show [E01].mkv", "Pack/Preview Show [E02].mkv"} {
				if got := planOutcome(result, name); got != "target_collision/target_duplicate" {
					t.Fatalf("%s = %s", name, got)
				}
			}
			third := batchScenario(t, "Preview Show [E01].mkv", "Preview Show [E02].mkv", "Preview Show [E03].mkv")
			third.effective = scenario.effective
			third.effective.Rules.Naming.Tv.VideoFilenameTemplate = "${anime.title} ${ep.number === '3' ? 'E03' : ''}${anime.title_override}.${file.ext}"
			kept := PreflightOrdinary(context.Background(), third.ordinary(IntentEnsurePresent), FilesystemProbe{})
			if kept.Admission.Admission != AdmissionDownload || len(kept.Set.Targets) != 1 || kept.Set.Targets[0].SourcePath != "Pack/Preview Show [E03].mkv" {
				t.Fatalf("mixed batch = %+v %+v, want only the distinct episode", kept.Admission, kept.Set.Targets)
			}
		})
	}
}

// Unknown videos only: accepted with no pre-download target under route,
// skipped (no_placeable_files) under skip.
func TestBatchPreflightUnknownOnlyFollowsTheRoute(t *testing.T) {
	scenario := batchScenario(t, "Preview Show OVA.mkv")
	routed := scenario.ordinary(IntentEnsurePresent)
	routed.UnclassifiedRoute = batchplan.RouteRoute
	result := PreflightOrdinary(context.Background(), routed, FilesystemProbe{})
	if result.Admission.Admission != AdmissionDownload || len(result.Set.Targets) != 0 {
		t.Fatalf("routed unknown = %+v with %d targets", result.Admission, len(result.Set.Targets))
	}
	if result.BatchPlan.UnclassifiedRoute != batchplan.RouteRoute {
		t.Fatalf("plan route = %q", result.BatchPlan.UnclassifiedRoute)
	}
	skipped := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if skipped.Admission.Admission != AdmissionSkipped || skipped.Admission.Reason != batchplan.ReasonNoPlaceableFiles {
		t.Fatalf("skipped unknown = %+v", skipped.Admission)
	}
}

// A deduplicated plan supplied by the caller decides what is targeted.
func TestBatchPreflightTargetsOnlyThePlannedVideos(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01].mkv", "Preview Show [E02].mkv")
	request := scenario.ordinary(IntentEnsurePresent)
	evidence, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{Kind: "regex", EpisodePatterns: scenario.effective.Rules.Parser.EpisodePatterns}, scenario.item.Title)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildBatchPlan(context.Background(), BatchPlanInput{Evidence: evidence, Files: scenario.item.ReleaseFiles, FileFilter: scenario.effective.Rules.FileFilter, UnclassifiedRoute: batchplan.RouteSkip})
	if err != nil {
		t.Fatal(err)
	}
	plan.ApplyDedup(map[string]batchplan.Verdict{"tv:e1": batchplan.VerdictExisting})
	request.Evidence, request.BatchPlan = &evidence, &plan
	result := PreflightOrdinary(context.Background(), request, FilesystemProbe{})
	if len(result.Set.Targets) != 1 || result.Set.Targets[0].SourcePath != "Pack/Preview Show [E02].mkv" {
		t.Fatalf("targets = %+v, want only the uncovered episode", result.Set.Targets)
	}
	if got := planOutcome(result, "Pack/Preview Show [E01].mkv"); got != "covered_existing/covered_existing" {
		t.Fatalf("covered file = %s", got)
	}
	plan.ApplyDedup(map[string]batchplan.Verdict{"tv:e2": batchplan.VerdictInFlight})
	request.BatchPlan = &plan
	if skipped := PreflightOrdinary(context.Background(), request, FilesystemProbe{}); skipped.Admission.Admission != AdmissionSkipped {
		t.Fatalf("a fully covered batch is skipped, got %+v", skipped.Admission)
	}
}

// The download-completion action resolved at acceptance is frozen into the
// plan: an unset action is seed, the four closed actions pass through, and
// anything else fails the plan instead of freezing an unknown action.
func TestBatchPlanFreezesTheResolvedDownloadCompletion(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01].mkv")
	for action, want := range map[animedomain.DownloadCompletionAction]string{
		"":                                   batchplan.CompletionSeed,
		animedomain.DownloadCompletionSeed:   batchplan.CompletionSeed,
		animedomain.DownloadCompletionPause:  batchplan.CompletionPause,
		animedomain.DownloadCompletionRemove: batchplan.CompletionRemove,
		animedomain.DownloadCompletionRemoveWithData: batchplan.CompletionRemoveWithData,
	} {
		scenario.effective.DownloadCompletion = animedomain.ResolvedDownloadCompletion{Rules: animedomain.DownloadCompletionRules{Action: action}}
		result := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
		if result.Admission.Admission != AdmissionDownload || result.BatchPlan.DownloadCompletion != want {
			t.Fatalf("action %q: admission %+v, frozen %q, want %q", action, result.Admission, result.BatchPlan.DownloadCompletion, want)
		}
	}
	scenario.effective.DownloadCompletion = animedomain.ResolvedDownloadCompletion{Rules: animedomain.DownloadCompletionRules{Action: "wipe"}}
	result := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if result.Admission.Admission == AdmissionDownload {
		t.Fatalf("an unknown action was frozen: %+v", result.BatchPlan)
	}
}

// Seed limits freeze with the seed action; limits next to another action, or
// limits that are not valid, fail the plan.
func TestBatchPlanFreezesTheSeedLimits(t *testing.T) {
	scenario := batchScenario(t, "Preview Show [E01].mkv")
	ratio := 2.0
	limits := &batchplan.SeedLimits{Ratio: &ratio, AtLimit: batchplan.SeedLimitRemove}
	scenario.effective.DownloadCompletion = animedomain.ResolvedDownloadCompletion{Rules: animedomain.DownloadCompletionRules{Action: animedomain.DownloadCompletionSeed, SeedLimits: limits}}
	result := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if result.Admission.Admission != AdmissionDownload || !result.BatchPlan.DownloadCompletionSeedLimits.Equal(limits) {
		t.Fatalf("seed limits: admission %+v, frozen %+v", result.Admission, result.BatchPlan.DownloadCompletionSeedLimits)
	}
	for name, rules := range map[string]animedomain.DownloadCompletionRules{
		"limits on pause": {Action: animedomain.DownloadCompletionPause, SeedLimits: limits},
		"invalid limits":  {Action: animedomain.DownloadCompletionSeed, SeedLimits: &batchplan.SeedLimits{AtLimit: batchplan.SeedLimitStop}},
	} {
		scenario.effective.DownloadCompletion = animedomain.ResolvedDownloadCompletion{Rules: rules}
		if result := PreflightOrdinary(context.Background(), scenario.ordinary(IntentEnsurePresent), FilesystemProbe{}); result.Admission.Admission == AdmissionDownload {
			t.Fatalf("%s was frozen: %+v", name, result.BatchPlan)
		}
	}
}
