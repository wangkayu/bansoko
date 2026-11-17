package target

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/postprocessing/naming"
)

func TestPendingTargetKeepsIdentityAndDownloads(t *testing.T) {
	for index, template := range []string{
		"${ep.number}${segment(media.resolution, ' [', ']')}.${file.ext}",
		"${ep.number}-${res}.${file.ext}",
		"${joinNonEmpty([ep.number, media.video_codec], '-')}.${file.ext}",
	} {
		t.Run([]string{"optional media", "shorthand media", "list media"}[index], func(t *testing.T) {
			s := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
			s.effective.Rules.Naming.Tv.VideoFilenameTemplate = template
			if _, err := naming.NewEngine().CompileRulesWithVars(animedomain.NamingRulesToDomain(s.effective.Rules.Naming), s.preview.FrozenVars.Output); err != nil {
				t.Fatal(err)
			}
			a, b := assertParity(t, s, "")
			for _, got := range []PlanResult{a, b} {
				if got.Admission.Admission != AdmissionDownload || len(got.Set.Targets) != 0 || got.BatchPlan.PendingTargets() != 1 {
					t.Fatalf("pending plan = %+v", got)
				}
				f := got.BatchPlan.Files[0]
				if f.Outcome != batchplan.OutcomePlanned || f.Identity == nil || f.Identity.State != episodeidentity.StateResolved || len(f.Keys()) != 1 {
					t.Fatalf("pending changed identity: %+v", f)
				}
				if got.ReleaseFiles[0].TargetPath != nil || len(got.Observations) != 0 {
					t.Fatal("pending target was rendered or probed")
				}
			}
		})
	}
}

func TestExecutionOnlyTargetsPreventWholeBatchAdoption(t *testing.T) {
	ready := batchplan.File{Kind: "video", Outcome: batchplan.OutcomePlanned, CoverageKey: "tv:e1"}
	pending := batchplan.File{Kind: "video", Outcome: batchplan.OutcomePlanned, CoverageKey: "tv:e2", TargetPendingFacts: []string{"media.resolution"}}
	unknown := batchplan.File{Kind: "video", Outcome: batchplan.OutcomeEpisodeUnrecognized}
	for _, tc := range []struct {
		name  string
		files []batchplan.File
		want  bool
	}{
		{"pending only", []batchplan.File{pending}, true},
		{"ready plus pending", []batchplan.File{ready, pending}, true},
		{"ready plus routed unknown", []batchplan.File{ready, unknown}, true},
		{"all ready", []batchplan.File{ready}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := batchplan.Plan{Files: tc.files, UnclassifiedRoute: batchplan.RouteRoute}
			if got := hasExecutionOnlyTargets(plan); got != tc.want {
				t.Fatalf("execution required = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPreparedFileTagsPermitExactTargetAdoption(t *testing.T) {
	s := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	s.item.ReleaseFiles[0].SourcePath = "Preview Show [E07] [720p].mkv"
	s.item.ReleaseFiles[0].Filename = s.item.ReleaseFiles[0].SourcePath
	s.effective.Rules.Naming.Tv.VideoFilenameTemplate = "${ep.number}-${frt.res}.${file.ext}"
	first := PreflightOrdinary(context.Background(), s.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if len(first.Set.Targets) != 1 || first.BatchPlan.PendingTargets() != 0 {
		t.Fatalf("prepared frt stayed pending: %+v", first)
	}
	target := first.Set.Targets[0].TargetPath
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, make([]byte, *s.item.ReleaseFiles[0].SizeBytes), 0644); err != nil {
		t.Fatal(err)
	}
	a, b := assertParity(t, s, target)
	if a.Admission.Admission != AdmissionAdopted || b.Admission.Admission != AdmissionAdopted {
		t.Fatal("fully ready exact batch did not adopt")
	}
}
