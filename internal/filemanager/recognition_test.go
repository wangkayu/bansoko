package filemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/download/domain"
	"bansoko/internal/postprocessing/naming"
)

type fakeRecognitionPolicy struct{ route string }

func (p fakeRecognitionPolicy) UnclassifiedRoute(context.Context) (string, error) {
	return p.route, nil
}

type fakeRecognitionAnimeFacts struct {
	loads []string
	fills int
}

func (f *fakeRecognitionAnimeFacts) LoadAnimeFacts(_ context.Context, id string) (naming.AnimeNamingFacts, error) {
	f.loads = append(f.loads, id)
	return naming.AnimeNamingFacts{ID: id, Title: "Show"}, nil
}

func (f *fakeRecognitionAnimeFacts) FillWorkTitles(context.Context, *naming.AnimeNamingFacts) error {
	f.fills++
	return nil
}

type fakeRecognitionMediaProber struct {
	mu    sync.Mutex
	facts map[string]domain.MediaFacts
	err   error
	calls []string
}

func (f *fakeRecognitionMediaProber) Probe(_ context.Context, path string) (domain.MediaFacts, error) {
	f.mu.Lock()
	f.calls = append(f.calls, path)
	err := f.err
	facts := f.facts[path]
	f.mu.Unlock()
	if err != nil {
		return domain.MediaFacts{}, err
	}
	return facts, nil
}

// recognitionRules names files with the given absolute directory: a video keeps
// its episode number as the name and a subtitle carries the "sub" suffix. The
// unknown variant parks an unclassified file in the directory's Extras folder.
func recognitionRules(directory string) naming.NamingRules {
	variant := naming.NamingVariant{
		DirectoryTemplate:     directory,
		VideoFilenameTemplate: "${ep.number}.${file.ext}",
	}
	return naming.NamingRules{
		TV: variant, Movie: variant,
		Unknown: naming.NamingVariant{
			DirectoryTemplate:     filepath.Join(directory, "Extras"),
			VideoFilenameTemplate: "${fn}",
		},
		SubtitleSuffixTemplate: "sub",
		Language:               "js-naming-1",
	}
}

func newRecognitionFixture(t *testing.T, policy RecognitionPolicy) commandFixture {
	t.Helper()
	f := newCommandFixture(t)
	f.service.SetRecognition(nil, nil, policy)
	return f
}

// recognitionRequest renders into the folder the selected files live in.
func recognitionRequest(paths ...string) RecognitionRequest {
	return RecognitionRequest{
		Paths:  paths,
		Parser: RecognitionParser{ID: "regex"},
		Naming: recognitionRules(filepath.Dir(paths[0])),
	}
}

func recognitionRow(t *testing.T, preview RecognitionPreview, name string) RecognitionRow {
	t.Helper()
	for _, row := range preview.Rows {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no row for %s in %+v", name, preview.Rows)
	return RecognitionRow{}
}

func TestRecognitionPreviewGroupsVideoAndBoundSubtitles(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	writeFile(t, f.abs("Loose/Show - 01.zh.ass"), 4)

	preview, err := f.service.PreviewRecognition(ctx, recognitionRequest(f.abs("Loose/Show - 01.mkv"), f.abs("Loose/Show - 01.zh.ass")))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Directory != f.abs("Loose") || preview.PlanDigest == "" {
		t.Fatalf("preview = %+v", preview)
	}
	video := recognitionRow(t, preview, "Show - 01.mkv")
	if video.Disposition != RecognitionRename || video.TargetName != "1.mkv" || video.TargetPath != f.abs("Loose/1.mkv") || video.Episode == nil || video.Episode.Number != "1" {
		t.Fatalf("video row = %+v", video)
	}
	if video.Group != "Show - 01" {
		t.Fatalf("video group = %q", video.Group)
	}
	subtitle := recognitionRow(t, preview, "Show - 01.zh.ass")
	if subtitle.Disposition != RecognitionRename || subtitle.TargetName != "1.sub.ass" || subtitle.TargetPath != f.abs("Loose/1.sub.ass") || subtitle.Kind != "subtitle" {
		t.Fatalf("subtitle row = %+v", subtitle)
	}
	if len(preview.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", preview.Conflicts)
	}
}

func TestRecognitionUnclassifiedFollowsTheRoutePolicy(t *testing.T) {
	ctx := context.Background()
	// Skip (the default) leaves an episode-less video and its subtitle alone.
	skip := newRecognitionFixture(t, fakeRecognitionPolicy{route: "skip"})
	writeFile(t, skip.abs("Loose/mystery.mkv"), 4)
	writeFile(t, skip.abs("Loose/mystery.zh.ass"), 4)
	preview, err := skip.service.PreviewRecognition(ctx, recognitionRequest(skip.abs("Loose/mystery.mkv"), skip.abs("Loose/mystery.zh.ass")))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, row := range preview.Rows {
		if row.Disposition != RecognitionSkip || row.TargetPath != "" {
			t.Fatalf("skip row = %+v", row)
		}
	}
	if _, err := skip.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "mystery", ExpectedPlanDigest: preview.PlanDigest, RecognitionRequest: recognitionRequest(skip.abs("Loose/mystery.mkv"), skip.abs("Loose/mystery.zh.ass"))}); !hasCode(err, CodeRecognitionNothingToDo) {
		t.Fatalf("skip submit = %v", err)
	}

	// Route organizes them with the rules' unknown variant; the subtitle
	// follows its video.
	route := newRecognitionFixture(t, fakeRecognitionPolicy{route: "route"})
	writeFile(t, route.abs("Loose/mystery.mkv"), 4)
	writeFile(t, route.abs("Loose/mystery.zh.ass"), 4)
	preview, err = route.service.PreviewRecognition(ctx, recognitionRequest(route.abs("Loose/mystery.mkv"), route.abs("Loose/mystery.zh.ass")))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	video := recognitionRow(t, preview, "mystery.mkv")
	if video.Disposition != RecognitionMove || video.TargetPath != route.abs("Loose/Extras/mystery.mkv") {
		t.Fatalf("route video row = %+v", video)
	}
	subtitle := recognitionRow(t, preview, "mystery.zh.ass")
	if subtitle.Disposition != RecognitionMove || subtitle.TargetPath != route.abs("Loose/Extras/mystery.sub.ass") {
		t.Fatalf("route subtitle row = %+v", subtitle)
	}
	outcome, err := route.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "routed", ExpectedPlanDigest: preview.PlanDigest, RecognitionRequest: recognitionRequest(route.abs("Loose/mystery.mkv"), route.abs("Loose/mystery.zh.ass"))})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if outcome.Moved != 2 || outcome.Renamed != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if info, err := os.Stat(route.abs("Loose/Extras")); err != nil || !info.IsDir() {
		t.Fatalf("Extras was not created: %v", err)
	}
}

func TestRecognitionRefusesUntouchedSelections(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Show/loose.mkv"), 4)

	// A managed file is read-only; recognition leaves managing alone.
	_, err := f.service.PreviewRecognition(ctx, recognitionRequest(f.abs("Show/managed.mkv")))
	mustCode(t, err, CodeRecognitionRequiresDetached)

	// A directory is not a file.
	_, err = f.service.PreviewRecognition(ctx, recognitionRequest(f.abs("Show")))
	mustCode(t, err, CodeRecognitionInvalid)

	// One directory per operation.
	_, err = f.service.PreviewRecognition(ctx, recognitionRequest(f.abs("Loose/a.mkv"), f.abs("Show/loose.mkv")))
	mustCode(t, err, CodeRecognitionMixedDirectory)

	// The same file twice is ambiguous.
	_, err = f.service.PreviewRecognition(ctx, recognitionRequest(f.abs("Loose/a.mkv"), f.abs("Loose/a.mkv")))
	mustCode(t, err, CodeRecognitionInvalid)

	// A subtitle without its video cannot be named.
	writeFile(t, f.abs("Loose/orphan.zh.ass"), 4)
	preview, err := f.service.PreviewRecognition(ctx, recognitionRequest(f.abs("Loose/orphan.zh.ass")))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if row := recognitionRow(t, preview, "orphan.zh.ass"); row.Disposition != RecognitionError || len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerVideoMissing {
		t.Fatalf("orphan row = %+v", row)
	}
}

// The rules render an absolute target directory away from the selection; the
// subtitle follows its video into that directory.
func TestRecognitionRendersTheRulesDirectory(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	writeFile(t, f.abs("Loose/Show - 01.zh.ass"), 4)
	request := recognitionRequest(f.abs("Loose/Show - 01.mkv"), f.abs("Loose/Show - 01.zh.ass"))
	request.Naming.TV.DirectoryTemplate = f.abs("Library/Show")
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	video := recognitionRow(t, preview, "Show - 01.mkv")
	if video.Disposition != RecognitionMove || video.TargetName != "1.mkv" || filepath.Dir(video.TargetPath) != f.abs("Library/Show") {
		t.Fatalf("video row = %+v", video)
	}
	subtitle := recognitionRow(t, preview, "Show - 01.zh.ass")
	if subtitle.Disposition != RecognitionMove || subtitle.TargetPath != f.abs("Library/Show/1.sub.ass") {
		t.Fatalf("subtitle row = %+v", subtitle)
	}
	outcome, err := f.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "subdir", ExpectedPlanDigest: preview.PlanDigest, RecognitionRequest: request})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if outcome.Moved != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	// The rendered directory levels exist once the submit is admitted; the
	// moves themselves are the executor's, like every recognition operation.
	if info, err := os.Stat(f.abs("Library/Show")); err != nil || !info.IsDir() {
		t.Fatalf("rendered directory was not created: %v", err)
	}
}

func TestRecognitionRefusesAnIllegalRenderedTarget(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	for _, rules := range []func(naming.NamingRules) naming.NamingRules{
		func(rules naming.NamingRules) naming.NamingRules {
			rules.TV.DirectoryTemplate = "relative/folder"
			return rules
		},
		func(rules naming.NamingRules) naming.NamingRules {
			rules.TV.VideoFilenameTemplate = "${ep.number}/Show - 01.${file.ext}"
			return rules
		},
		func(rules naming.NamingRules) naming.NamingRules {
			rules.TV.VideoFilenameTemplate = "${ep.number}/../Show - 01.${file.ext}"
			return rules
		},
	} {
		request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
		request.Naming = rules(request.Naming)
		preview, err := f.service.PreviewRecognition(ctx, request)
		if err != nil {
			t.Fatalf("preview %+v: %v", request.Naming.TV, err)
		}
		row := recognitionRow(t, preview, "Show - 01.mkv")
		if row.Disposition != RecognitionError || len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerTargetInvalid {
			t.Fatalf("rules %+v row = %+v", request.Naming.TV, row)
		}
		_, err = f.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "illegal", ExpectedPlanDigest: preview.PlanDigest, RecognitionRequest: request})
		mustCode(t, err, CodeRecognitionNotRenderable)
	}
}

// A target outside every authorized root is the row's own blocker.
func TestRecognitionRefusesATargetOutsideTheRoots(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	request.Naming.TV.DirectoryTemplate = "/media/library"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	row := recognitionRow(t, preview, "Show - 01.mkv")
	if row.Disposition != RecognitionError || len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerOutsideRoots {
		t.Fatalf("row = %+v", row)
	}
}

func TestRecognitionMissingAnimeFactsIsABlocker(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	request.Naming.TV.VideoFilenameTemplate = "${anime.title}.${file.ext}"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	row := recognitionRow(t, preview, "Show - 01.mkv")
	if row.Disposition != RecognitionError || len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerFactsMissing {
		t.Fatalf("row = %+v", row)
	}
	if len(row.Blockers[0].Facts) == 0 || !strings.HasPrefix(row.Blockers[0].Facts[0], "anime.") {
		t.Fatalf("facts = %v", row.Blockers[0].Facts)
	}
}

func TestRecognitionConflictChoices(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	writeFile(t, f.abs("Loose/Show - 01.zh.ass"), 4)
	writeFile(t, f.abs("Loose/Show - 02.mkv"), 4)
	writeFile(t, f.abs("Loose/1.mkv"), 4)
	paths := []string{f.abs("Loose/Show - 01.mkv"), f.abs("Loose/Show - 01.zh.ass"), f.abs("Loose/Show - 02.mkv")}
	request := recognitionRequest(paths...)

	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	conflicted := recognitionRow(t, preview, "Show - 01.mkv")
	if conflicted.Conflict == nil || conflicted.Conflict.OccupiedBy != OccupantFile || conflicted.Conflict.VersionPath != f.abs("Loose/1 (2).mkv") {
		t.Fatalf("conflicted row = %+v", conflicted)
	}
	if len(preview.Conflicts) != 1 {
		t.Fatalf("conflicts = %v", preview.Conflicts)
	}
	if row := recognitionRow(t, preview, "Show - 02.mkv"); row.Conflict != nil {
		t.Fatalf("free row = %+v", row)
	}

	// Block admits nothing.
	_, err = f.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "block", ExpectedPlanDigest: preview.PlanDigest, Conflict: RecognitionConflictBlock, RecognitionRequest: request})
	mustCode(t, err, CodeTargetExists)
	if len(f.commands.admitted) != 0 {
		t.Fatalf("block admitted %+v", f.commands.admitted)
	}

	// Skip leaves the conflicting group (video and subtitle) alone and admits
	// the rest.
	skipped, err := f.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "skip", ExpectedPlanDigest: preview.PlanDigest, Conflict: RecognitionConflictSkip, RecognitionRequest: request})
	if err != nil {
		t.Fatalf("skip submit: %v", err)
	}
	if len(skipped.Operations) != 1 || skipped.Operations[0].SourcePath != f.abs("Loose/Show - 02.mkv") ||
		skipped.Operations[0].TargetPath != f.abs("Loose/2.mkv") {
		t.Fatalf("skip outcome = %+v", skipped)
	}

	// Version takes the first free sibling, and the subtitles follow the video.
	versioned := newRecognitionFixture(t, fakeRecognitionPolicy{})
	writeFile(t, versioned.abs("Loose/Show - 01.mkv"), 4)
	writeFile(t, versioned.abs("Loose/Show - 01.zh.ass"), 4)
	writeFile(t, versioned.abs("Loose/1.mkv"), 4)
	versionRequest := recognitionRequest(versioned.abs("Loose/Show - 01.mkv"), versioned.abs("Loose/Show - 01.zh.ass"))
	versionPreview, err := versioned.service.PreviewRecognition(ctx, versionRequest)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	outcome, err := versioned.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "version", ExpectedPlanDigest: versionPreview.PlanDigest, Conflict: RecognitionConflictVersion, RecognitionRequest: versionRequest})
	if err != nil {
		t.Fatalf("version submit: %v", err)
	}
	targets := map[string]string{}
	for _, operation := range outcome.Operations {
		targets[operation.SourcePath] = operation.TargetPath
	}
	for _, admission := range versioned.commands.admitted {
		if admission.Collision != ConflictVersion {
			t.Fatalf("version admission collision = %q", admission.Collision)
		}
	}
	if targets[versioned.abs("Loose/Show - 01.mkv")] != versioned.abs("Loose/1 (2).mkv") {
		t.Fatalf("video target = %q", targets[versioned.abs("Loose/Show - 01.mkv")])
	}
	if targets[versioned.abs("Loose/Show - 01.zh.ass")] != versioned.abs("Loose/1 (2).sub.ass") {
		t.Fatalf("subtitle target = %q", targets[versioned.abs("Loose/Show - 01.zh.ass")])
	}
}

func TestRecognitionSubmitIsIdempotentAndGuardsThePlan(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	submit := SubmitRecognitionRequest{IdempotencyKey: "batch-1", ExpectedPlanDigest: preview.PlanDigest, RecognitionRequest: request}
	outcome, err := f.service.SubmitRecognition(ctx, submit)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(outcome.Operations) != 1 || outcome.Renamed != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	admission := f.commands.admitted[0]
	if admission.Operation != UserRename || admission.SourcePath != f.abs("Loose/Show - 01.mkv") ||
		admission.TargetPath != f.abs("Loose/1.mkv") || !admission.RequireDetachedSource || admission.Collision != ConflictBlock {
		t.Fatalf("admission = %+v", admission)
	}
	if !strings.HasPrefix(admission.IdempotencyKey, "batch-1:") || admission.Digest == "" {
		t.Fatalf("admission key = %q", admission.IdempotencyKey)
	}

	// The same key replays the admitted batch instead of admitting again.
	replayed, err := f.service.SubmitRecognition(ctx, submit)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed.Operations) != 1 || len(f.commands.admitted) != 1 {
		t.Fatalf("replay = %+v, admitted = %d", replayed, len(f.commands.admitted))
	}

	// The same key with another request is a conflict.
	other := submit
	other.Naming.TV.VideoFilenameTemplate = "${file.stem}.${file.ext}"
	_, err = f.service.SubmitRecognition(ctx, other)
	mustCode(t, err, CodeIdempotencyConflict)

	// A plan that changed since the preview is refused.
	writeFile(t, f.abs("Loose/Show - 03.mkv"), 4)
	fresh := newRecognitionFixture(t, fakeRecognitionPolicy{})
	writeFile(t, fresh.abs("Loose/Show - 01.mkv"), 4)
	freshPreview, err := fresh.service.PreviewRecognition(ctx, recognitionRequest(fresh.abs("Loose/Show - 01.mkv")))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	_, err = fresh.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "batch-2", ExpectedPlanDigest: "stale", RecognitionRequest: recognitionRequest(fresh.abs("Loose/Show - 01.mkv"))})
	mustCode(t, err, CodeRecognitionPlanChanged)
	if freshPreview.PlanDigest == "" {
		t.Fatal("empty digest")
	}
}

func TestRecognitionReplaysLegacySubmissionAfterSourceDisappears(t *testing.T) {
	ctx := context.Background()
	f := newRecognitionFixture(t, nil)
	submit := SubmitRecognitionRequest{
		RecognitionRequest: RecognitionRequest{
			Paths: []string{"/old/ep.mkv"},
			Match: &RecognitionMatch{ManagedAnimeID: " anime-1 ", Episode: intPointer(3)},
		},
		IdempotencyKey: "legacy", ExpectedPlanDigest: "legacy-plan", Conflict: RecognitionConflictBlock,
		LegacyLoadRuntimeFacts: true,
	}
	// This shape records the digest as written before r2 and episode ranges.
	stored := requestDigest(struct {
		Paths            []string
		ParserID         string
		ParserConfig     string
		Naming           naming.NamingRules
		LoadRuntimeFacts bool
		Match            struct {
			Anime   string
			Season  *int
			Episode *int
			Offset  int
			Draft   recognitionAnimeDigest
		}
		Overrides []RecognitionEpisodeOverride
		Conflict  RecognitionConflictChoice
	}{
		Paths: submit.Paths, LoadRuntimeFacts: true, Conflict: RecognitionConflictBlock,
		Match: struct {
			Anime   string
			Season  *int
			Episode *int
			Offset  int
			Draft   recognitionAnimeDigest
		}{Anime: " anime-1 ", Episode: intPointer(3)},
	})
	f.commands.byKey = map[string]UserOperation{"legacy:old": {ID: "old", Operation: UserRename}}
	f.commands.digests = map[string]string{"legacy:old": stored}
	result, err := f.service.SubmitRecognition(ctx, submit)
	if err != nil || len(result.Operations) != 1 || result.Operations[0].ID != "old" || len(f.commands.admitted) != 0 {
		t.Fatalf("legacy replay=%+v, admissions=%v, error=%v", result, f.commands.admitted, err)
	}
	submit.LegacyLoadRuntimeFacts = false
	_, err = f.service.SubmitRecognition(ctx, submit)
	mustCode(t, err, CodeIdempotencyConflict)
	submit.IdempotencyKey = "unsubmitted"
	_, err = f.service.SubmitRecognition(ctx, submit)
	mustCode(t, err, CodeRecognitionPlanChanged)
}

func TestRecognitionEpisodeEndChangesBothDigests(t *testing.T) {
	ctx := context.Background()
	f := newRecognitionFixture(t, nil)
	video := f.abs("Loose/Show - 01.mkv")
	writeFile(t, video, 4)
	request := recognitionRequest(video)
	request.Naming.TV.VideoFilenameTemplate = "constant.mkv"
	request.Match = &RecognitionMatch{Episode: intPointer(3), EpisodeEnd: intPointer(4)}
	first, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	before := recognitionRequestDigest(SubmitRecognitionRequest{RecognitionRequest: request})
	request.Match.EpisodeEnd = intPointer(5)
	second, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Rows[0].TargetName != second.Rows[0].TargetName || first.PlanDigest == second.PlanDigest ||
		before == recognitionRequestDigest(SubmitRecognitionRequest{RecognitionRequest: request}) {
		t.Fatalf("range change did not produce distinct digests: %s, %s", first.PlanDigest, second.PlanDigest)
	}
	_, err = f.service.SubmitRecognition(ctx, SubmitRecognitionRequest{
		RecognitionRequest: request, ExpectedPlanDigest: first.PlanDigest, IdempotencyKey: "range-change",
	})
	mustCode(t, err, CodeRecognitionPlanChanged)
	if len(f.commands.admitted) != 0 {
		t.Fatalf("admissions = %v", f.commands.admitted)
	}
}

func TestRecognitionManualEpisodeReachesTheRender(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	anime := &fakeRecognitionAnimeFacts{}
	f.service.SetRecognition(anime, nil, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/mystery.mkv"), 4)
	request := RecognitionRequest{
		Paths: []string{f.abs("Loose/mystery.mkv")}, Parser: RecognitionParser{ID: "regex"},
		Naming: recognitionRules(f.abs("Loose")),
		Match:  &RecognitionMatch{ManagedAnimeID: "anime-1", Season: intPointer(3), Episode: intPointer(7), Offset: 1},
	}
	request.Naming.TV.VideoFilenameTemplate = "${anime.title}-${anime.season}-${ep.number}-${ep.source}.${file.ext}"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	row := recognitionRow(t, preview, "mystery.mkv")
	if row.Disposition != RecognitionRename || row.Episode == nil || row.Episode.Number != "8" || row.Episode.Source != "manual" {
		t.Fatalf("row = %+v, episode = %+v", row, row.Episode)
	}
	if row.TargetName != "Show-3-8-manual.mkv" || len(anime.loads) != 1 || anime.loads[0] != "anime-1" {
		t.Fatalf("target = %q, anime loads = %v", row.TargetName, anime.loads)
	}
}

func TestRecognitionEpisodeOverridesAreFinalAndInheritedBySubtitles(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	first := f.abs("Loose/Show - 01.mkv")
	second := f.abs("Loose/Show - 02.mkv")
	third := f.abs("Loose/Show - 03.mkv")
	subtitle := f.abs("Loose/Show - 01.zh.ass")
	writeFile(t, first, 4)
	writeFile(t, second, 4)
	writeFile(t, third, 4)
	writeFile(t, subtitle, 4)
	request := recognitionRequest(first, second, third, subtitle)
	request.Naming.TV.VideoFilenameTemplate = "${anime.season}-${ep.display}-${fn}"
	request.Naming.SubtitleSuffixTemplate = "sub-${anime.season}-${ep.number}"
	request.Match = &RecognitionMatch{Season: intPointer(2), Offset: 10}
	request.Overrides = []RecognitionEpisodeOverride{
		{Path: "  " + first + "  ", Season: intPointer(0), Episode: 7},
		{Path: third, Episode: 5, EpisodeEnd: 9},
	}

	preview, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	firstRow := recognitionRow(t, preview, "Show - 01.mkv")
	if firstRow.Episode == nil || firstRow.Episode.Raw != "1" || firstRow.Episode.Number != "7" ||
		firstRow.Episode.Offset != 0 || firstRow.Episode.Source != "manual" || firstRow.TargetName != "0-7-Show - 01.mkv" {
		t.Fatalf("overridden video = %+v", firstRow)
	}
	secondRow := recognitionRow(t, preview, "Show - 02.mkv")
	if secondRow.Episode == nil || secondRow.Episode.Number != "12" || secondRow.Episode.Offset != 10 ||
		secondRow.Episode.Source != "file_parser" || secondRow.TargetName != "2-12-Show - 02.mkv" {
		t.Fatalf("unoverridden video = %+v", secondRow)
	}
	subtitleRow := recognitionRow(t, preview, "Show - 01.zh.ass")
	if subtitleRow.Episode == nil || subtitleRow.Episode.Number != "7" || subtitleRow.Episode.Offset != 0 ||
		subtitleRow.TargetName != "0-7-Show - 01.sub-0-7.ass" {
		t.Fatalf("bound subtitle = %+v", subtitleRow)
	}
	thirdRow := recognitionRow(t, preview, "Show - 03.mkv")
	if thirdRow.Episode == nil || thirdRow.Episode.Number != "5" || thirdRow.Episode.NumberEnd != "9" ||
		thirdRow.Episode.Source != "manual" || thirdRow.Episode.Offset != 0 || thirdRow.TargetName != "2-5-9-Show - 03.mkv" {
		t.Fatalf("manual range must keep its source, end and inherited season: %+v", thirdRow)
	}
}

func TestRecognitionEpisodeOverridesAreValidated(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	video := f.abs("Loose/Show - 01.mkv")
	subtitle := f.abs("Loose/Show - 01.zh.ass")
	stale := f.abs("Loose/Show - 02.mkv")
	writeFile(t, video, 4)
	writeFile(t, subtitle, 4)
	writeFile(t, stale, 4)
	for _, tc := range []struct {
		name      string
		overrides []RecognitionEpisodeOverride
	}{
		{name: "stale", overrides: []RecognitionEpisodeOverride{{Path: stale, Episode: 2}}},
		{name: "subtitle", overrides: []RecognitionEpisodeOverride{{Path: subtitle, Episode: 2}}},
		{name: "duplicate", overrides: []RecognitionEpisodeOverride{{Path: video, Episode: 2}, {Path: " " + video + " ", Episode: 3}}},
		{name: "empty path", overrides: []RecognitionEpisodeOverride{{Path: " ", Episode: 2}}},
		{name: "relative path", overrides: []RecognitionEpisodeOverride{{Path: "Show - 01.mkv", Episode: 2}}},
		{name: "negative season", overrides: []RecognitionEpisodeOverride{{Path: video, Season: intPointer(-1), Episode: 2}}},
		{name: "zero episode", overrides: []RecognitionEpisodeOverride{{Path: video, Episode: 0}}},
		{name: "episode too large", overrides: []RecognitionEpisodeOverride{{Path: video, Episode: maxMatchEpisode + 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := recognitionRequest(video, subtitle)
			request.Overrides = tc.overrides
			_, err := f.service.PreviewRecognition(context.Background(), request)
			mustCode(t, err, CodeRecognitionInvalid)
		})
	}
}

func TestRecognitionEpisodeOverridesJoinCanonicalDigests(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	first := f.abs("Loose/Show - 01.mkv")
	second := f.abs("Loose/Show - 02.mkv")
	writeFile(t, first, 4)
	writeFile(t, second, 4)
	plain, err := f.service.PreviewRecognition(context.Background(), recognitionRequest(first, second))
	if err != nil {
		t.Fatalf("plain preview: %v", err)
	}
	request := recognitionRequest(first, second)
	request.Overrides = []RecognitionEpisodeOverride{
		{Path: second, Episode: 2},
		{Path: first, Episode: 1},
	}
	preview, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil {
		t.Fatalf("override preview: %v", err)
	}
	if preview.PlanDigest == plain.PlanDigest {
		t.Fatal("overrides must join the plan digest even when the rendered rows do not change")
	}
	reordered := recognitionRequest(first, second)
	reordered.Overrides = []RecognitionEpisodeOverride{
		{Path: " " + first + " ", Episode: 1},
		{Path: second, Episode: 2},
	}
	canonical, err := f.service.PreviewRecognition(context.Background(), reordered)
	if err != nil {
		t.Fatalf("canonical preview: %v", err)
	}
	if canonical.PlanDigest != preview.PlanDigest {
		t.Fatal("override order and surrounding whitespace must not change the plan digest")
	}
	if _, err := f.service.SubmitRecognition(context.Background(), SubmitRecognitionRequest{
		IdempotencyKey: "override-canonical", ExpectedPlanDigest: preview.PlanDigest, RecognitionRequest: reordered,
	}); err != nil {
		t.Fatalf("canonical submit: %v", err)
	}

	plainSubmit := SubmitRecognitionRequest{RecognitionRequest: recognitionRequest(first, second)}
	overrideSubmit := SubmitRecognitionRequest{RecognitionRequest: request}
	reorderedSubmit := SubmitRecognitionRequest{RecognitionRequest: reordered}
	if recognitionRequestDigest(overrideSubmit) == recognitionRequestDigest(plainSubmit) {
		t.Fatal("overrides must join the idempotency request digest")
	}
	if recognitionRequestDigest(overrideSubmit) != recognitionRequestDigest(reorderedSubmit) {
		t.Fatal("the idempotency request digest must use canonical overrides")
	}
}

func TestRecognitionRuntimeFactsAreDemandDrivenAndCached(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{route: "route"})
	ctx := context.Background()
	first := f.abs("Loose/Show - 01.mkv")
	second := f.abs("Loose/Show - 02.mkv")
	subtitle := f.abs("Loose/Show - 01.zh.ass")
	for _, path := range []string{first, second, subtitle} {
		writeFile(t, path, 4)
	}
	prober := &fakeRecognitionMediaProber{facts: map[string]domain.MediaFacts{
		first:  {Resolution: "1080p", VideoCodec: "h264"},
		second: {Resolution: "2160p"},
	}}
	f.service.SetRecognitionMediaProber(prober)
	request := recognitionRequest(first, second, subtitle)
	request.Naming.Movie.VideoFilenameTemplate = "${media.resolution}-${fn}"
	request.Naming.Unknown.VideoFilenameTemplate = "${media.resolution}-${fn}"
	if _, err := f.service.PreviewRecognition(ctx, request); err != nil || len(prober.calls) != 0 {
		t.Fatalf("unselected templates: probes=%v, err=%v", prober.calls, err)
	}
	unknown := f.abs("Loose/mystery.mkv")
	writeFile(t, unknown, 4)
	request.Paths = append(request.Paths, unknown)
	request.Naming.Unknown.VideoFilenameTemplate = "${fn}"
	request.Naming.TV.VideoFilenameTemplate = "${media.resolution}-${fn}"
	request.Naming.SubtitleSuffixTemplate = "${media.video_codec}"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil || len(prober.calls) != 2 || len(preview.RuntimeFacts.MediaProbe) != 2 {
		t.Fatalf("preview=%+v, probes=%v, err=%v", preview, prober.calls, err)
	}
	if row := recognitionRow(t, preview, "Show - 01.zh.ass"); row.TargetName != "1080p-Show - 01.h264.ass" || row.SizeBytes != 4 {
		t.Fatalf("subtitle did not inherit its video's media facts: %+v", row)
	}
	request.DisableRuntimeFacts = true
	cached, err := f.service.PreviewRecognition(ctx, request)
	if err != nil || cached.PlanDigest != preview.PlanDigest || len(prober.calls) != 2 {
		t.Fatalf("cache-only preview=%+v, probes=%v, err=%v", cached, prober.calls, err)
	}
	request.DisableRuntimeFacts = false
	request.RefreshMedia = true
	refreshed, err := f.service.PreviewRecognition(ctx, request)
	wantCalls := []string{first, first, second, second}
	slices.Sort(wantCalls)
	gotCalls := append([]string(nil), prober.calls...)
	slices.Sort(gotCalls)
	if err != nil || refreshed.PlanDigest != preview.PlanDigest || !slices.Equal(gotCalls, wantCalls) ||
		len(refreshed.RuntimeFacts.MediaProbe) != 2 ||
		refreshed.RuntimeFacts.MediaProbe[0].Path != first ||
		refreshed.RuntimeFacts.MediaProbe[1].Path != second {
		t.Fatalf("refreshed preview=%+v, probes=%v, err=%v", refreshed, prober.calls, err)
	}
	request.RefreshMedia = false
	_, err = f.service.SubmitRecognition(ctx, SubmitRecognitionRequest{RecognitionRequest: request, ExpectedPlanDigest: preview.PlanDigest, IdempotencyKey: "runtime-cache", Conflict: RecognitionConflictBlock})
	if err != nil || len(prober.calls) != 4 {
		t.Fatalf("submit probes=%v, err=%v", prober.calls, err)
	}
}

func TestRecognitionRuntimeProbeFailureBlocksTheRowAndSubmit(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	prober := &fakeRecognitionMediaProber{err: errors.New("probe failed")}
	f.service.SetRecognitionMediaProber(prober)
	path := f.abs("Loose/Show - 01.mkv")
	writeFile(t, path, 4)
	request := recognitionRequest(path)
	directory := f.abs("Loose/New")
	request.Naming.TV.DirectoryTemplate = directory
	request.Naming.TV.VideoFilenameTemplate = "${media.resolution ? 'result' : 'empty'}.${file.ext}"
	preview, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	row := recognitionRow(t, preview, "Show - 01.mkv")
	if row.Disposition != RecognitionError || row.TargetName != "result.mkv" || row.HasPlaceholder || len(preview.Conflicts) != 0 ||
		len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerRuntimeFacts || strings.Join(row.Blockers[0].DiagnosticIDs, ",") != "media-1" {
		t.Fatalf("row = %+v", row)
	}
	for _, strategy := range []RecognitionConflictChoice{RecognitionConflictBlock, RecognitionConflictSkip, RecognitionConflictVersion, RecognitionConflictOverwrite} {
		_, err = f.service.SubmitRecognition(context.Background(), SubmitRecognitionRequest{
			IdempotencyKey: "runtime-failure", ExpectedPlanDigest: preview.PlanDigest,
			RecognitionRequest: request, Conflict: strategy,
		})
		mustCode(t, err, CodeRecognitionNotRenderable)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) || len(prober.calls) != 1 {
		t.Fatalf("blocked submit touched directory or reread facts: stat=%v, probes=%v", err, prober.calls)
	}
	if len(f.commands.admitted) != 0 {
		t.Fatalf("blocked preview admitted operations: %v", f.commands.admitted)
	}

	// An earlier ready token must also be refused when the rebuilt plan has
	// blockers, even when the final rendered path is identical.
	f.service.SetRecognitionMediaProber(&fakeRecognitionMediaProber{facts: map[string]domain.MediaFacts{path: {Resolution: "1080p"}}})
	ready, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil || !strings.HasPrefix(ready.PlanDigest, "r2.ready.") || ready.Rows[0].TargetPath != row.TargetPath {
		t.Fatalf("ready preview = %+v, error = %v", ready, err)
	}
	f.service.SetRecognitionMediaProber(prober)
	for _, strategy := range []RecognitionConflictChoice{RecognitionConflictBlock, RecognitionConflictSkip, RecognitionConflictVersion, RecognitionConflictOverwrite} {
		_, err = f.service.SubmitRecognition(context.Background(), SubmitRecognitionRequest{
			IdempotencyKey: "runtime-rebuild-failure", ExpectedPlanDigest: ready.PlanDigest,
			RecognitionRequest: request, Conflict: strategy,
		})
		mustCode(t, err, CodeRecognitionNotRenderable)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) || len(f.commands.admitted) != 0 || len(prober.calls) != 5 {
		t.Fatalf("blocked rebuild: stat=%v, admissions=%v, probes=%v", err, f.commands.admitted, prober.calls)
	}
}

func hasCode(err error, code string) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code
}

func intPointer(value int) *int { return &value }

func TestRecognitionAnimeDraftReachesTheRender(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	anime := &fakeRecognitionAnimeFacts{}
	f.service.SetRecognition(anime, nil, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	year := 2024
	request := RecognitionRequest{
		Paths: []string{f.abs("Loose/Show - 01.mkv")}, Parser: RecognitionParser{ID: "regex"},
		Naming: recognitionRules(f.abs("Loose")),
		Match: &RecognitionMatch{
			ManagedAnimeID: "anime-1", Season: intPointer(2),
			Anime: &RecognitionAnimeDraft{
				Title: "Edited", MediaKind: "movie", Year: &year,
				BGM:  &RecognitionAnimeBinding{WorkID: "bgm-9"},
				TMDB: &RecognitionTMDBBinding{WorkID: "42", MediaKind: "movie", Season: 0},
			},
		},
	}
	request.Naming.Movie.VideoFilenameTemplate = "${anime.title}-${anime.kind}-${anime.year}.${file.ext}"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if row := preview.Rows[0]; row.TargetName != "Edited-movie-2024.mkv" || len(anime.loads) != 1 || anime.fills != 1 {
		t.Fatalf("row=%+v, fact reads=%+v", row, anime)
	}
}

func TestRecognitionAnimeDraftIsValidated(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	invalid := []*RecognitionAnimeDraft{
		{Title: "  ", MediaKind: "tv"},
		{Title: "Show", MediaKind: "music"},
		{Title: "Show", MediaKind: "tv", Year: intPointer(1899)},
		{Title: "Show", MediaKind: "tv", BGM: &RecognitionAnimeBinding{}},
		{Title: "Show", MediaKind: "tv", TMDB: &RecognitionTMDBBinding{MediaKind: "tv"}},
		{Title: "Show", MediaKind: "tv", TMDB: &RecognitionTMDBBinding{WorkID: "1", Season: 1}},
		{Title: "Show", MediaKind: "tv", TMDB: &RecognitionTMDBBinding{WorkID: "1", MediaKind: "tv", Season: -1}},
	}
	for _, draft := range invalid {
		request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
		request.Match = &RecognitionMatch{ManagedAnimeID: "anime-1", Anime: draft}
		_, err := f.service.PreviewRecognition(context.Background(), request)
		mustCode(t, err, CodeRecognitionInvalid)
	}
}

func TestRecognitionMatchFormsAreValidated(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)

	// A match carrying nothing is meaningless.
	empty := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	empty.Match = &RecognitionMatch{}
	_, err := f.service.PreviewRecognition(context.Background(), empty)
	mustCode(t, err, CodeRecognitionInvalid)

	// A bare episode adjustment over a pure parse is legal.
	offsetOnly := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	offsetOnly.Match = &RecognitionMatch{Offset: -1}
	preview, err := f.service.PreviewRecognition(context.Background(), offsetOnly)
	if err != nil {
		t.Fatalf("offset-only match: %v", err)
	}
	if row := recognitionRow(t, preview, "Show - 01.mkv"); row.Episode == nil || row.Episode.Offset != -1 {
		t.Fatalf("offset-only row = %+v", row)
	}
	episodeOnly := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	episodeOnly.Match = &RecognitionMatch{Episode: intPointer(5)}
	if _, err := f.service.PreviewRecognition(context.Background(), episodeOnly); err != nil {
		t.Fatalf("episode-only match: %v", err)
	}
}

func TestRecognitionDraftOnlyMatchReadsNoAnime(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	request.Match = &RecognitionMatch{Anime: &RecognitionAnimeDraft{Title: "Edited", MediaKind: "tv"}}
	request.Naming.TV.VideoFilenameTemplate = "${anime.title}-${anime.kind}.${file.ext}"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil || preview.Rows[0].TargetName != "Edited-tv.mkv" {
		t.Fatalf("preview=%+v, err=%v", preview, err)
	}
}

func TestRecognitionTitleHintReadsTheFirstParsedTitle(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	writeFile(t, f.abs("Loose/[组A] 测试番剧 - 01 [1080p].mkv"), 4)
	preview, err := f.service.PreviewRecognition(context.Background(), recognitionRequest(f.abs("Loose/[组A] 测试番剧 - 01 [1080p].mkv")))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.TitleHint != "测试番剧" {
		t.Fatalf("title hint = %q", preview.TitleHint)
	}
}
