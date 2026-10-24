package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/namingvars"
)

// Database-free coverage for the finite one-shot target admission boundary.
// The shared planner itself (rendering, collision, adoption) is
// proven in acquisition/target; these tests prove the one-shot path feeds it the
// same facts the ordinary path does and maps a fail-closed admission to a typed
// refusal instead of continuing to qB.

func oneShotTargetDraft() animedomain.AnimeDraft {
	override := "One Shot Override"
	return animedomain.AnimeDraft{
		Origin:        animedomain.SubscriptionOriginOneshot,
		Title:         "One Shot Show",
		TitleOverride: &override,
	}
}

func oneShotTargetItem() acquisitiondomain.FeedItem {
	size := int64(1024)
	return acquisitiondomain.FeedItem{
		GUID:  "guid-one-shot-target",
		Title: "[RealGroup] One Shot Show - 01 [1080p]",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{
				SourcePath: "One Shot Show - 01.mkv",
				Filename:   "One Shot Show - 01.mkv",
				SizeBytes:  &size,
				MediaKind:  "video",
			},
		},
	}
}

func oneShotTargetPreview() animedomain.PreviewReleaseResult {
	source := `vars.actual = {title: input.release_title};`
	digest, _ := namingvars.SourceFingerprint(source)
	program, err := namingvars.NewProgram(namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, source)
	if err != nil {
		panic(err)
	}
	item := oneShotTargetItem()
	input := namingvars.Input{ReleaseTitle: item.Title}
	for _, file := range item.ReleaseFiles {
		input.Files = append(input.Files, namingvars.File{Name: file.Filename, Path: file.SourcePath, Size: *file.SizeBytes})
	}
	// The frozen binding facts the instance's own metadata rows supply (the
	// same ones the production freeze reads through ItemEvidence): the shipped
	// default templates name ${anime.year}, ${anime.season_folder} and the
	// s00e00 shorthand's season, so an admission that expects a rendered target
	// carries the bound season and the owner-edited year. The title
	// is deliberately left to the request's coalesced instance title.
	input.Facts = map[string]any{
		"anime": map[string]any{"year": "2025"},
		"tmdb":  map[string]any{"id": "oneshot-tmdb", "kind": "tv", "season": 1, "year": 2025},
	}
	// A literal admission record of `source`; fixtures never run the
	// time-limited evaluator.
	frozen := namingvars.FrozenEvaluation{Program: program.Ref(), Input: input, Output: namingvars.Vars{"actual": {"title": item.Title}}, Status: "ok"}
	return animedomain.PreviewReleaseResult{
		FrozenVars:    &frozen,
		StableItemKey: "guid:guid-one-shot-target",
		Verdict:       animedomain.ReleaseVerdict("accepted_pending_download"),
	}
}

func oneShotTargetEffective(t *testing.T) animedomain.EffectiveConfig {
	t.Helper()
	namingValue, err := animedomain.DefaultSectionValue(animedomain.SectionNamingRules)
	if err != nil {
		t.Fatalf("default naming section: %v", err)
	}
	namingRules, ok := namingValue.(animedomain.NamingRules)
	if !ok {
		t.Fatalf("default naming section has type %T", namingValue)
	}
	filterValue, err := animedomain.DefaultSectionValue(animedomain.SectionFileRules)
	if err != nil {
		t.Fatalf("default file rules section: %v", err)
	}
	filterRules, ok := filterValue.(animedomain.FileFilterRules)
	if !ok {
		t.Fatalf("default file rules section has type %T", filterValue)
	}
	parserValue, err := animedomain.DefaultSectionValue(animedomain.SectionParser)
	if err != nil {
		t.Fatalf("default parser section: %v", err)
	}
	parserRules, ok := parserValue.(animedomain.ParserRules)
	if !ok {
		t.Fatalf("default parser section has type %T", parserValue)
	}
	return animedomain.EffectiveConfig{Rules: animedomain.EffectiveRules{
		Naming:     namingRules,
		FileFilter: filterRules,
		Parser:     parserRules,
		FileEffect: animedomain.FileEffectRules{
			Operation:       animedomain.FileEffectRulesOperationCopy,
			CollisionPolicy: animedomain.CollisionPolicyFail,
		},
	}}
}

// oneShotTargetEffectiveAtRoot is oneShotTargetEffective with every directory
// template named under root: naming targets are absolute by construction, so a
// test that materializes the rendered target must own its directory instead of
// inheriting the deployment mount /media.
func oneShotTargetEffectiveAtRoot(t *testing.T, root string) animedomain.EffectiveConfig {
	t.Helper()
	effective := oneShotTargetEffective(t)
	prefix := filepath.ToSlash(strings.TrimRight(root, "/"))
	rooted := func(template string) string { return strings.Replace(template, "/media", prefix, 1) }
	effective.Rules.Naming.Tv.DirectoryTemplate = rooted(effective.Rules.Naming.Tv.DirectoryTemplate)
	effective.Rules.Naming.Movie.DirectoryTemplate = rooted(effective.Rules.Naming.Movie.DirectoryTemplate)
	effective.Rules.Naming.Unknown.DirectoryTemplate = rooted(effective.Rules.Naming.Unknown.DirectoryTemplate)
	return effective
}

func oneShotTargetPlan(t *testing.T, root string) acquisitiontarget.PlanResult {
	t.Helper()
	request := oneShotTargetRequest("release-one-shot-target", oneShotTargetDraft(), "RealGroup", 0, "regex", oneShotTargetEffectiveAtRoot(t, root), oneShotTargetItem(), oneShotTargetPreview(), nil, "skip")
	return planOneShotTargets(context.Background(), acquisitiontarget.FilesystemProbe{}, request)
}

// The one-shot request must name the instance exactly the way ordinary RSS
// evaluation does: the coalesced title, the selected seed's provider group, no
// input offset it is handed, and the shared ensure_present intent.
func TestOneShotTargetRequestCarriesTheOrdinaryPlanningFacts(t *testing.T) {
	if got := oneShotAnimeTitle(oneShotTargetDraft()); got != "One Shot Override" {
		t.Fatalf("one-shot anime title = %q, want the title override", got)
	}
	blank := "   "
	if got := oneShotAnimeTitle(animedomain.AnimeDraft{Title: "Fallback Show", TitleOverride: &blank}); got != "Fallback Show" {
		t.Fatalf("one-shot anime title = %q, want the plain title when the override is blank", got)
	}

	offsetRequest := oneShotTargetRequest("release-one-shot-target", oneShotTargetDraft(), "RealGroup", 3, "regex", oneShotTargetEffective(t), oneShotTargetItem(), oneShotTargetPreview(), nil, "skip")
	if offsetRequest.Offset != 3 {
		t.Fatalf("request offset = %d, want the request episode_offset 3", offsetRequest.Offset)
	}

	plan := oneShotTargetPlan(t, t.TempDir())
	if plan.Admission.Admission != acquisitiontarget.AdmissionDownload {
		t.Fatalf("admission = %+v, want download_required for a missing target", plan.Admission)
	}
	if len(plan.Set.Targets) != 1 {
		t.Fatalf("target set = %+v, want exactly one target", plan.Set)
	}
	target := plan.Set.Targets[0]
	if target.Intent != acquisitiontarget.IntentEnsurePresent {
		t.Fatalf("frozen intent = %q, want ensure_present", target.Intent)
	}
	if !strings.Contains(target.TargetPath, "One Shot Override") {
		t.Fatalf("target %q did not use the coalesced instance title", target.TargetPath)
	}
	if !filepath.IsAbs(target.TargetPath) {
		t.Fatalf("target %q is not an absolute canonical path", target.TargetPath)
	}
	if target.ExpectedSizeBytes != 1024 {
		t.Fatalf("frozen expected size = %d, want the selected entry size 1024", target.ExpectedSizeBytes)
	}
	if len(plan.ReleaseFiles) != 1 || plan.ReleaseFiles[0].TargetPath == nil || *plan.ReleaseFiles[0].TargetPath != target.TargetPath {
		t.Fatalf("admission did not project the rendered target onto the release files: %+v", plan.ReleaseFiles)
	}
	if plan.Set.Digest == "" {
		t.Fatal("frozen target set has no digest")
	}
	if plan.Evidence.Selection.Parser.ID == "" {
		t.Fatalf("admission resolved no parser selection evidence: %+v", plan.Evidence)
	}
}

// An already satisfied exact-size target set is adopted by the shared planner:
// the finite command records the frozen target facts and succeeds with no
// download chain. Adoption must never be routed through the fail-closed
// refusal mapper.
func TestOneShotAdoptedTargetIsNotARefusal(t *testing.T) {
	root := t.TempDir()
	planned := oneShotTargetPlan(t, root)
	if planned.Admission.Admission != acquisitiontarget.AdmissionDownload {
		t.Fatalf("precondition admission = %+v, want download_required", planned.Admission)
	}
	targetPath := planned.Set.Targets[0].TargetPath
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, make([]byte, planned.Set.Targets[0].ExpectedSizeBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	adopted := oneShotTargetPlan(t, root)
	if adopted.Admission.Admission != acquisitiontarget.AdmissionAdopted {
		t.Fatalf("admission = %+v, want adopted_existing", adopted.Admission)
	}
	if adopted.Admission.Reason != "all_targets_exact_size" {
		t.Fatalf("adoption reason = %q, want all_targets_exact_size", adopted.Admission.Reason)
	}
	// Adoption is a success: the refusal mapper is only reached for a real
	// fail-closed admission, so the adoption reason must not carry a typed
	// TARGET_ALREADY_SATISFIED refusal any more.
	var business *apperrors.Business
	if err := oneShotTargetFailure(adopted); errors.As(err, &business) && business.Code == "TARGET_ALREADY_SATISFIED" {
		t.Fatalf("adoption must not be a typed refusal: %v", err)
	}
}

// Every fail-closed classification keeps its own typed, transport-visible code
// so an unsafe, unusable or conflicting target can never look like a normal
// submission.
func TestOneShotTargetFailureKeepsTheTypedReason(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		status int
		code   string
	}{
		{name: "probe unavailable", reason: "target_probe_unavailable", status: 503, code: "TARGET_PROBE_UNAVAILABLE"},
		{name: "relative target", reason: "target_not_absolute", status: 422, code: "TARGET_ACQUISITION_FAILED"},
		{name: "collision blocked", reason: "target_collision_blocked", status: 409, code: "TARGET_COLLISION_BLOCKED"},
		{name: "render failed", reason: "target_render_failed", status: 422, code: "TARGET_ACQUISITION_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := oneShotTargetFailure(acquisitiontarget.PlanResult{
				Admission:  acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: test.reason},
				PlanReason: "detail for " + test.reason,
			})
			var business *apperrors.Business
			if !errors.As(err, &business) {
				t.Fatalf("failure = %v, want a typed apperrors.Business", err)
			}
			if business.Status != test.status || business.Code != test.code {
				t.Fatalf("failure = %d %s, want %d %s", business.Status, business.Code, test.status, test.code)
			}
			if !strings.Contains(business.Message, test.reason) {
				t.Fatalf("failure message %q does not carry the planner reason %q", business.Message, test.reason)
			}
		})
	}
}

// The wired production boundary fails closed when the probe is missing: the
// shared planner returns target_probe_unavailable and the command maps it to a
// typed 503 instead of continuing with a probe-less submission.
func TestOneShotTargetAdmissionFailsClosedWithoutProbe(t *testing.T) {
	request := oneShotTargetRequest("release-one-shot-target", oneShotTargetDraft(), "RealGroup", 0, "regex", oneShotTargetEffective(t), oneShotTargetItem(), oneShotTargetPreview(), nil, "skip")
	plan := planOneShotTargets(context.Background(), nil, request)
	if plan.Admission.Reason != "target_probe_unavailable" {
		t.Fatalf("failure reason = %q, want target_probe_unavailable", plan.Admission.Reason)
	}
	err := oneShotTargetFailure(plan)
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "TARGET_PROBE_UNAVAILABLE" {
		t.Fatalf("failure = %v, want TARGET_PROBE_UNAVAILABLE", err)
	}
}
