package target

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
)

func TestMain(m *testing.M) {
	rules := attrrules.Rules{
		Version:        1,
		Resolution:     []string{"2160p", "1440p", "1080p", "720p", "480p"},
		Quality:        []string{"remux", "web-dl", "webrip", "bluray", "bdrip", "hdtv"},
		Language:       []string{"chs", "cht", "sc", "tc", "jpn", "eng"},
		SpecialMarkers: []string{"special", "ncop", "nced"},
		GroupBrackets:  []string{"[", "]"},
	}
	fingerprint, err := attrrules.Fingerprint(rules)
	if err != nil {
		panic(err)
	}
	if err := attrrules.SetActive(rules, fingerprint); err != nil {
		panic(err)
	}
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	varsFingerprint, err := namingvars.Fingerprint(vars)
	if err != nil {
		panic(err)
	}
	if err := namingvars.SetActive(vars, varsFingerprint); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// testEffectiveConfig resolves the code-owned defaults for the sections the
// pre-download plan consumes, so the scenario is the same configuration shape
// an instance owns.
func testEffectiveConfig(t *testing.T, collision animedomain.CollisionPolicy, operation animedomain.FileEffectRulesOperation) animedomain.EffectiveConfig {
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
	return animedomain.EffectiveConfig{Rules: animedomain.EffectiveRules{
		Naming:     namingRules,
		FileFilter: filterRules,
		FileEffect: animedomain.FileEffectRules{CollisionPolicy: collision, Operation: operation},
		Parser:     animedomain.ParserRules{EpisodePatterns: []string{`\[E(?P<episode>\d{1,4})\]`}},
	}}
}

func testScenarioItem() acquisitiondomain.FeedItem {
	size := int64(1024)
	return acquisitiondomain.FeedItem{
		GUID:  "guid-shared-admission",
		Title: "[RealGroup] Preview Show [E07] [1080p]",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{
			{
				SourcePath: "Preview Show [E07].mkv",
				Filename:   "Preview Show [E07].mkv",
				SizeBytes:  &size,
				MediaKind:  "video",
			},
		},
	}
}

func testScenarioPreview() animedomain.PreviewReleaseResult {
	return animedomain.PreviewReleaseResult{
		StableItemKey: "guid:guid-shared-admission",
		Verdict:       animedomain.ReleaseVerdict("accepted_pending_download"),
		// The target render consumes only the Release's frozen Vars evaluation;
		// there is no active-sample fallback.
		FrozenVars: &namingvars.FrozenEvaluation{
			Status: "ok",
			Output: namingvars.Vars{"test": {"marker": "ok"}},
			// The frozen acceptance input is the only place the render reads the
			// anime's title, its provider year and its bound season: the shipped
			// default templates name ${anime.title}, ${anime.year} and
			// ${anime.season_folder}, and the s00e00 shorthand needs the bound season
			// to render SxxExx rather than an empty season. A scenario that
			// expects a rendered target therefore carries the facts an admission
			// freezes for a metadata-bound anime; a scenario without them is an
			// unbound anime, which the default rules refuse by name.
			Input: namingvars.Input{
				ReleaseTitle: "[RealGroup] Preview Show [E07] [1080p]",
				Facts: map[string]any{
					"anime": map[string]any{"title_base": "Preview Show", "title": "Preview Show", "year": "2025", "year_source": "tmdb"},
					"tmdb":  map[string]any{"id": "preview-tmdb", "kind": "tv", "season": 1, "year": 2025},
				},
			},
		},
	}
}

type parityScenario struct {
	item      acquisitiondomain.FeedItem
	preview   animedomain.PreviewReleaseResult
	effective animedomain.EffectiveConfig
	// root is the absolute directory the scenario's naming templates render
	// into; the engine has no global anchor to fall back on.
	root string
}

func newParityScenario(t *testing.T, collision animedomain.CollisionPolicy, operation animedomain.FileEffectRulesOperation) parityScenario {
	t.Helper()
	effective := testEffectiveConfig(t, collision, operation)
	root := t.TempDir()
	directoryTemplate := filepath.ToSlash(root)
	effective.Rules.Naming.Tv.DirectoryTemplate = directoryTemplate
	effective.Rules.Naming.Unknown.DirectoryTemplate = directoryTemplate
	effective.Rules.Naming.Movie.DirectoryTemplate = directoryTemplate
	return parityScenario{
		item:      testScenarioItem(),
		preview:   testScenarioPreview(),
		effective: effective,
		root:      root,
	}
}

func (s parityScenario) ordinary(intent Intent) OrdinaryRequest {
	return OrdinaryRequest{
		ReleaseID:  "release-shared-admission",
		AnimeTitle: "Preview Show",
		GroupName:  "RealGroup",
		ParserKind: "regex",
		Effective:  s.effective,
		Intent:     intent,
		Item:       s.item,
		Preview:    s.preview,
		// The unknown-file route the plan freezes.
		UnclassifiedRoute: "skip",
	}
}

func (s parityScenario) oneShot() OneShotRequest {
	return OneShotRequest{
		ReleaseID:  "release-shared-admission",
		AnimeTitle: "Preview Show",
		GroupName:  "RealGroup",
		ParserKind: "regex",
		Effective:  s.effective,
		Item:       s.item,
		Preview:    s.preview,
		// The unknown-file route the plan freezes.
		UnclassifiedRoute: "skip",
	}
}

// assertParity runs both path entry points over the same scenario and proves
// they produce one identical plan: same frozen set digest, same canonical
// targets, same collision policy and intent, and the same admission
// classification. wantPath, when non-empty, pins the canonical target path.
func assertParity(t *testing.T, scenario parityScenario, wantPath string) (PlanResult, PlanResult) {
	t.Helper()
	ctx := context.Background()
	ordinary := PreflightOrdinary(ctx, scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	oneShot := PreflightOneShot(ctx, scenario.oneShot(), FilesystemProbe{})
	if ordinary.Set.Digest != oneShot.Set.Digest {
		t.Fatalf("target set digests differ: ordinary=%q one-shot=%q", ordinary.Set.Digest, oneShot.Set.Digest)
	}
	if ordinary.Admission != oneShot.Admission {
		t.Fatalf("admission differs: ordinary=%+v one-shot=%+v", ordinary.Admission, oneShot.Admission)
	}
	if ordinary.PlanReason != oneShot.PlanReason {
		t.Fatalf("plan reason differs: ordinary=%q one-shot=%q", ordinary.PlanReason, oneShot.PlanReason)
	}
	if len(ordinary.Set.Targets) != len(oneShot.Set.Targets) {
		t.Fatalf("target count differs: ordinary=%d one-shot=%d", len(ordinary.Set.Targets), len(oneShot.Set.Targets))
	}
	for index := range ordinary.Set.Targets {
		left, right := ordinary.Set.Targets[index], oneShot.Set.Targets[index]
		if left.TargetPath != right.TargetPath {
			t.Fatalf("target path differs at %d: ordinary=%q one-shot=%q", index, left.TargetPath, right.TargetPath)
		}
		if left.CollisionPolicy != right.CollisionPolicy || left.Intent != right.Intent || left.ExpectedSizeBytes != right.ExpectedSizeBytes {
			t.Fatalf("frozen target spec differs at %d: ordinary=%+v one-shot=%+v", index, left, right)
		}
		if wantPath != "" && left.TargetPath != wantPath {
			t.Fatalf("target path = %q, want %q", left.TargetPath, wantPath)
		}
	}
	if wantPath != "" {
		if len(ordinary.ReleaseFiles) != 1 || ordinary.ReleaseFiles[0].TargetPath == nil || *ordinary.ReleaseFiles[0].TargetPath != wantPath {
			t.Fatalf("ordinary release files did not freeze the planned target: %+v", ordinary.ReleaseFiles)
		}
		if len(oneShot.ReleaseFiles) != 1 || oneShot.ReleaseFiles[0].TargetPath == nil || *oneShot.ReleaseFiles[0].TargetPath != wantPath {
			t.Fatalf("one-shot release files did not freeze the planned target: %+v", oneShot.ReleaseFiles)
		}
	}
	return ordinary, oneShot
}

func TestSharedAdmissionMissingTargetDownloads(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	ordinary, oneShot := assertParity(t, scenario, "")
	if ordinary.Admission.Admission != AdmissionDownload {
		t.Fatalf("admission = %+v, want download_required", ordinary.Admission)
	}
	if oneShot.Admission.Admission != AdmissionDownload {
		t.Fatalf("one-shot admission = %+v, want download_required", oneShot.Admission)
	}
	if len(oneShot.ReleaseFiles) != 1 || oneShot.ReleaseFiles[0].TargetPath == nil || *oneShot.ReleaseFiles[0].TargetPath != oneShot.Set.Targets[0].TargetPath {
		t.Fatalf("one-shot release files did not freeze the planned target: %+v", oneShot.ReleaseFiles)
	}
}

func TestSharedAdmissionAdoptsExactExistingTarget(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	ctx := context.Background()
	probe := FilesystemProbe{}
	planned := PreflightOneShot(ctx, scenario.oneShot(), probe)
	if planned.Admission.Admission != AdmissionDownload {
		t.Fatalf("precondition admission = %+v, want download_required", planned.Admission)
	}
	targetPath := planned.Set.Targets[0].TargetPath
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, make([]byte, planned.Set.Targets[0].ExpectedSizeBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	ordinary, oneShot := assertParity(t, scenario, targetPath)
	if ordinary.Admission.Admission != AdmissionAdopted {
		t.Fatalf("ordinary admission = %+v, want adopted_existing", ordinary.Admission)
	}
	if oneShot.Admission.Admission != AdmissionAdopted {
		t.Fatalf("one-shot admission = %+v, want adopted_existing", oneShot.Admission)
	}
	if oneShot.Admission.Reason != "all_targets_exact_size" {
		t.Fatalf("one-shot adoption reason = %q, want all_targets_exact_size", oneShot.Admission.Reason)
	}
	if len(oneShot.Observations) != 1 || oneShot.Observations[0].State != ObservationPresentMatched {
		t.Fatalf("one-shot observations = %+v, want one present_matched", oneShot.Observations)
	}
}

func TestSharedAdmissionBlocksMismatchedTargetUnderBlockPolicy(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	if err := writeTargetOfSize(t, scenario, 512); err != nil {
		t.Fatal(err)
	}
	ordinary, oneShot := assertParity(t, scenario, "")
	if ordinary.Admission.Admission != AdmissionFailed || oneShot.Admission.Admission != AdmissionFailed {
		t.Fatalf("admission = ordinary %+v one-shot %+v, want acquisition_failed", ordinary.Admission, oneShot.Admission)
	}
	if ordinary.Admission.Reason != "target_collision_blocked" || oneShot.Admission.Reason != "target_collision_blocked" {
		t.Fatalf("failure reason = ordinary %q one-shot %q, want target_collision_blocked", ordinary.Admission.Reason, oneShot.Admission.Reason)
	}
}

func TestSharedAdmissionAuthorizesMismatchUnderOverwritePolicy(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyOverwrite, animedomain.FileEffectRulesOperationCopy)
	if err := writeTargetOfSize(t, scenario, 512); err != nil {
		t.Fatal(err)
	}
	ordinary, oneShot := assertParity(t, scenario, "")
	if ordinary.Admission.Admission != AdmissionDownload || oneShot.Admission.Admission != AdmissionDownload {
		t.Fatalf("admission = ordinary %+v one-shot %+v, want download_required", ordinary.Admission, oneShot.Admission)
	}
	if oneShot.Set.Targets[0].CollisionPolicy != "overwrite" {
		t.Fatalf("frozen collision policy = %q, want overwrite", oneShot.Set.Targets[0].CollisionPolicy)
	}
}

func TestSharedAdmissionFailsClosedOnUnreadableTarget(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyOverwrite, animedomain.FileEffectRulesOperationCopy)
	planned := PreflightOneShot(context.Background(), scenario.oneShot(), FilesystemProbe{})
	if planned.Admission.Admission != AdmissionDownload {
		t.Fatalf("precondition admission = %+v, want download_required", planned.Admission)
	}
	if err := os.MkdirAll(planned.Set.Targets[0].TargetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	ordinary, oneShot := assertParity(t, scenario, "")
	if ordinary.Admission.Reason != "target_unreadable" || oneShot.Admission.Reason != "target_unreadable" {
		t.Fatalf("failure reason = ordinary %q one-shot %q, want target_unreadable", ordinary.Admission.Reason, oneShot.Admission.Reason)
	}
}

func TestSharedAdmissionFailsClosedWithoutProbe(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	ctx := context.Background()
	ordinary := PreflightOrdinary(ctx, scenario.ordinary(IntentEnsurePresent), nil)
	oneShot := PreflightOneShot(ctx, scenario.oneShot(), nil)
	if ordinary.Admission.Reason != "target_probe_unavailable" || oneShot.Admission.Reason != "target_probe_unavailable" {
		t.Fatalf("failure reason = ordinary %q one-shot %q, want target_probe_unavailable", ordinary.Admission.Reason, oneShot.Admission.Reason)
	}
}

// Naming templates must render absolute targets: a relative directory is a
// typed render failure, no global root can rescue it, and the file records the
// naming diagnostic instead of a frozen target.
func TestSharedAdmissionFailsClosedForRelativeTarget(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	scenario.effective.Rules.Naming.Tv.DirectoryTemplate = "Preview Show"
	ctx := context.Background()
	ordinary := PreflightOrdinary(ctx, scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	oneShot := PreflightOneShot(ctx, scenario.oneShot(), FilesystemProbe{})
	if ordinary.Admission.Reason != "target_render_failed" || oneShot.Admission.Reason != "target_render_failed" {
		t.Fatalf("failure reason = ordinary %q one-shot %q, want target_render_failed", ordinary.Admission.Reason, oneShot.Admission.Reason)
	}
	if len(oneShot.ReleaseFiles) != 1 || oneShot.ReleaseFiles[0].TargetDiagnostic == nil ||
		oneShot.ReleaseFiles[0].TargetDiagnostic.Code != "NAMING_TARGET_NOT_ABSOLUTE" {
		t.Fatalf("one-shot diagnostic = %+v, want NAMING_TARGET_NOT_ABSOLUTE", oneShot.ReleaseFiles)
	}
}

func TestSharedAdmissionFailsClosedForUnavailableSelectedParser(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	request := scenario.oneShot()
	request.ParserKind = "unregistered-shared-admission-parser"
	ctx := context.Background()
	ordinaryRequest := scenario.ordinary(IntentEnsurePresent)
	ordinaryRequest.ParserKind = request.ParserKind
	ordinary := PreflightOrdinary(ctx, ordinaryRequest, FilesystemProbe{})
	oneShot := PreflightOneShot(ctx, request, FilesystemProbe{})
	if ordinary.Admission.Reason != "parser_selection_unavailable" || oneShot.Admission.Reason != "parser_selection_unavailable" {
		t.Fatalf("failure reason = ordinary %q one-shot %q, want parser_selection_unavailable", ordinary.Admission.Reason, oneShot.Admission.Reason)
	}
}

// An empty value renders empty and only an invalid result refuses admission;
// a variable that is not defined for the release refuses it whatever the
// path. In both cases no target is frozen and the diagnostic names the fact.
func TestSharedAdmissionRefusesOnlyAnInvalidRenderedName(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	ctx := context.Background()
	for template, want := range map[string]string{
		// No title override was set: defined and empty, and the stem is left
		// empty.
		"${anime.title_override}.${file.ext}": "naming_fact_missing_anime_title_override",
		// No Bangumi binding: bgm.id is undefined for this release.
		"Show ${bgm.id}.${file.ext}": "naming_fact_undefined_bgm_id",
	} {
		scenario.effective.Rules.Naming.Tv.VideoFilenameTemplate = template
		ordinary := PreflightOrdinary(ctx, scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
		oneShot := PreflightOneShot(ctx, scenario.oneShot(), FilesystemProbe{})
		for name, planned := range map[string]PlanResult{"ordinary": ordinary, "one-shot": oneShot} {
			if planned.Admission.Admission != AdmissionFailed || len(planned.Set.Targets) != 0 {
				t.Fatalf("%s %s admitted: %+v %+v", name, template, planned.Admission, planned.Set.Targets)
			}
			if len(planned.ReleaseFiles) != 1 || planned.ReleaseFiles[0].TargetPath != nil || planned.ReleaseFiles[0].TargetDiagnostic == nil ||
				planned.ReleaseFiles[0].TargetDiagnostic.Code != want {
				t.Fatalf("%s %s diagnostic = %+v, want %s", name, template, planned.ReleaseFiles, want)
			}
		}
	}
	scenario.effective.Rules.Naming.Tv.VideoFilenameTemplate = "${anime.title_override}-${tmdb.id}.${file.ext}"
	admitted := PreflightOrdinary(ctx, scenario.ordinary(IntentEnsurePresent), FilesystemProbe{})
	if admitted.Admission.Admission == AdmissionFailed || len(admitted.Set.Targets) != 1 || filepath.Base(admitted.Set.Targets[0].TargetPath) != "-preview-tmdb.mkv" {
		t.Fatalf("one empty value = %+v %+v", admitted.Admission, admitted.Set.Targets)
	}
}

// A replace intent is a distinct operation: an exact-size incumbent must never
// short-circuit it into adoption.
func TestOrdinaryReplaceIntentIsNeverAdopted(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyOverwrite, animedomain.FileEffectRulesOperationCopy)
	if err := writeTargetOfSize(t, scenario, 1024); err != nil {
		t.Fatal(err)
	}
	planned := PreflightOrdinary(context.Background(), scenario.ordinary(IntentReplace), FilesystemProbe{})
	if planned.Admission.Admission != AdmissionDownload {
		t.Fatalf("replacement admission = %+v, want download_required", planned.Admission)
	}
	if planned.Admission.Reason != "replacement_requires_new_artifact" {
		t.Fatalf("replacement reason = %q, want replacement_requires_new_artifact", planned.Admission.Reason)
	}
}

// writeTargetOfSize plans the scenario once to learn the canonical target path,
// then writes a regular file of the requested size there.
func writeTargetOfSize(t *testing.T, scenario parityScenario, size int64) error {
	t.Helper()
	planned := PreflightOneShot(context.Background(), scenario.oneShot(), FilesystemProbe{})
	if len(planned.Set.Targets) != 1 {
		t.Fatalf("scenario planned %d targets, want 1", len(planned.Set.Targets))
	}
	targetPath := planned.Set.Targets[0].TargetPath
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(targetPath, make([]byte, size), 0o644)
}
