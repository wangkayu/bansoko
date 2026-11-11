package filemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/download/domain"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/postprocessing/naming"
)

type fakeRecognitionPolicy struct{ route string }

func (p fakeRecognitionPolicy) UnclassifiedRoute(context.Context) (string, error) {
	return p.route, nil
}

// fakeRecognitionRenderer stands in for the naming renderer when a test needs
// a matched render (the real one reads the anime from the database).
type fakeRecognitionRenderer struct {
	inputs []filemaintenance.RecognitionRenderInput
	render func(filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error)
}

func (f *fakeRecognitionRenderer) RenderRecognition(_ context.Context, input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error) {
	f.inputs = append(f.inputs, input)
	return f.render(input)
}

func (f *fakeRecognitionRenderer) RenderRecognitionSuffix(_ context.Context, input filemaintenance.RecognitionRenderInput) (string, error) {
	f.inputs = append(f.inputs, input)
	return "sub", nil
}

type fakeRecognitionMediaProber struct {
	facts map[string]domain.MediaFacts
	err   error
	calls []string
}

func (f *fakeRecognitionMediaProber) Probe(_ context.Context, path string) (domain.MediaFacts, error) {
	f.calls = append(f.calls, path)
	if f.err != nil {
		return domain.MediaFacts{}, f.err
	}
	return f.facts[path], nil
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
	f.service.SetRecognition(filemaintenance.NewNamingTargetRenderer(nil, nil), nil, policy)
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

func TestRecognitionManualEpisodeReachesTheRender(t *testing.T) {
	f := newCommandFixture(t)
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error) {
		return filemaintenance.RecognitionRendered{
			Directory: input.Rules.TV.DirectoryTemplate,
			Filename:  "ep-" + input.Episode + "." + input.EpisodeSource + "." + input.FileName,
		}, nil
	}}
	f.service.SetRecognition(renderer, fakeMatchAnimes{"anime-1": {ID: "anime-1", Title: "Show"}}, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/mystery.mkv"), 4)
	request := RecognitionRequest{
		Paths: []string{f.abs("Loose/mystery.mkv")}, Parser: RecognitionParser{ID: "regex"},
		Naming: recognitionRules(f.abs("Loose")),
		Match:  &RecognitionMatch{ManagedAnimeID: "anime-1", Season: intPointer(3), Episode: intPointer(7), Offset: 1},
	}
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	row := recognitionRow(t, preview, "mystery.mkv")
	if row.Disposition != RecognitionRename || row.Episode == nil || row.Episode.Number != "8" || row.Episode.Source != "manual" {
		t.Fatalf("row = %+v, episode = %+v", row, row.Episode)
	}
	if len(renderer.inputs) == 0 || renderer.inputs[0].Episode != "8" || renderer.inputs[0].EpisodeSource != "manual" || renderer.inputs[0].ManagedAnimeID != "anime-1" {
		t.Fatalf("render input = %+v", renderer.inputs)
	}
	if renderer.inputs[0].Season == nil || *renderer.inputs[0].Season != 3 {
		t.Fatalf("season = %v", renderer.inputs[0].Season)
	}
}

func TestRecognitionEpisodeOverridesAreFinalAndInheritedBySubtitles(t *testing.T) {
	f := newCommandFixture(t)
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error) {
		return filemaintenance.RecognitionRendered{
			Directory: input.Rules.TV.DirectoryTemplate,
			Filename:  input.Episode + "-" + input.FileName,
		}, nil
	}}
	f.service.SetRecognition(renderer, nil, fakeRecognitionPolicy{})
	first := f.abs("Loose/Show - 01.mkv")
	second := f.abs("Loose/Show - 02.mkv")
	third := f.abs("Loose/Show - 03.mkv")
	subtitle := f.abs("Loose/Show - 01.zh.ass")
	writeFile(t, first, 4)
	writeFile(t, second, 4)
	writeFile(t, third, 4)
	writeFile(t, subtitle, 4)
	request := recognitionRequest(first, second, third, subtitle)
	request.Match = &RecognitionMatch{Season: intPointer(2), Offset: 10}
	request.Overrides = []RecognitionEpisodeOverride{
		{Path: "  " + first + "  ", Season: intPointer(0), Episode: 7},
		{Path: third, Episode: 5},
	}

	preview, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	firstRow := recognitionRow(t, preview, "Show - 01.mkv")
	if firstRow.Episode == nil || firstRow.Episode.Raw != "1" || firstRow.Episode.Number != "7" ||
		firstRow.Episode.Offset != 0 || firstRow.Episode.Source != "manual" || firstRow.TargetName != "7-Show - 01.mkv" {
		t.Fatalf("overridden video = %+v", firstRow)
	}
	secondRow := recognitionRow(t, preview, "Show - 02.mkv")
	if secondRow.Episode == nil || secondRow.Episode.Number != "12" || secondRow.Episode.Offset != 10 ||
		secondRow.Episode.Source != "file_parser" || secondRow.TargetName != "12-Show - 02.mkv" {
		t.Fatalf("unoverridden video = %+v", secondRow)
	}
	subtitleRow := recognitionRow(t, preview, "Show - 01.zh.ass")
	if subtitleRow.Episode == nil || subtitleRow.Episode.Number != "7" || subtitleRow.Episode.Offset != 0 ||
		subtitleRow.TargetName != "7-Show - 01.sub.ass" {
		t.Fatalf("bound subtitle = %+v", subtitleRow)
	}
	seasons := map[string]*int{}
	offsets := map[string]int{}
	for _, input := range renderer.inputs {
		seasons[input.FileName] = input.Season
		offsets[input.FileName] = input.EpisodeOffset
	}
	for _, name := range []string{"Show - 01.mkv", "Show - 01.zh.ass"} {
		if seasons[name] == nil || *seasons[name] != 0 || offsets[name] != 0 {
			t.Fatalf("%s did not inherit the explicit zero season and final episode: season=%v offset=%d", name, seasons[name], offsets[name])
		}
	}
	if seasons["Show - 02.mkv"] == nil || *seasons["Show - 02.mkv"] != 2 || offsets["Show - 02.mkv"] != 10 {
		t.Fatalf("unoverridden render lost the main settings: %+v", renderer.inputs)
	}
	thirdRow := recognitionRow(t, preview, "Show - 03.mkv")
	if thirdRow.Episode == nil || thirdRow.Episode.Number != "5" || thirdRow.Episode.Offset != 0 ||
		seasons["Show - 03.mkv"] == nil || *seasons["Show - 03.mkv"] != 2 {
		t.Fatalf("an omitted override season must inherit the main season: row=%+v season=%v", thirdRow, seasons["Show - 03.mkv"])
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

func TestRecognitionRuntimeFactsAreExplicitAndPerVideo(t *testing.T) {
	f := newCommandFixture(t)
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error) {
		return filemaintenance.RecognitionRendered{
			Directory: input.Rules.TV.DirectoryTemplate,
			Filename:  input.Media.Resolution + "-" + input.FileName,
		}, nil
	}}
	f.service.SetRecognition(renderer, nil, fakeRecognitionPolicy{})
	ctx := context.Background()
	first := f.abs("Loose/Show - 01.mkv")
	second := f.abs("Loose/Show - 02.mkv")
	subtitle := f.abs("Loose/Show - 01.zh.ass")
	writeFile(t, first, 4)
	writeFile(t, second, 4)
	writeFile(t, subtitle, 4)
	prober := &fakeRecognitionMediaProber{facts: map[string]domain.MediaFacts{
		first:  {Resolution: "1080p", VideoCodec: "h264", Container: "mkv"},
		second: {Resolution: "2160p", VideoCodec: "hevc", HDR: "hdr10"},
	}}
	f.service.SetRecognitionMediaProber(prober)
	request := recognitionRequest(first, second, subtitle)

	if _, err := f.service.PreviewRecognition(ctx, request); err != nil {
		t.Fatalf("default preview: %v", err)
	}
	if len(prober.calls) != 0 {
		t.Fatalf("default preview probed media: %v", prober.calls)
	}
	request.LoadRuntimeFacts = true
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("runtime preview: %v", err)
	}
	if len(prober.calls) != 2 {
		t.Fatalf("runtime probes = %v", prober.calls)
	}
	if got := recognitionRow(t, preview, "Show - 01.mkv").TargetName; got != "1080p-Show - 01.mkv" {
		t.Fatalf("first target = %q", got)
	}
	if got := recognitionRow(t, preview, "Show - 02.mkv").TargetName; got != "2160p-Show - 02.mkv" {
		t.Fatalf("second target = %q", got)
	}
	var subtitleInput *filemaintenance.RecognitionRenderInput
	for index := range renderer.inputs {
		input := &renderer.inputs[index]
		if input.FileName == "Show - 01.zh.ass" {
			subtitleInput = input
		}
	}
	if subtitleInput == nil || subtitleInput.Media.Resolution != "1080p" || subtitleInput.Media.VideoCodec != "h264" {
		t.Fatalf("subtitle did not inherit its video's media facts: %+v", subtitleInput)
	}
}

func TestRecognitionRuntimeFactsModeChangesThePlanDigest(t *testing.T) {
	f := newCommandFixture(t)
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error) {
		return filemaintenance.RecognitionRendered{Directory: input.Rules.TV.DirectoryTemplate, Filename: input.FileName}, nil
	}}
	f.service.SetRecognition(renderer, nil, fakeRecognitionPolicy{})
	f.service.SetRecognitionMediaProber(&fakeRecognitionMediaProber{})
	path := f.abs("Loose/Show - 01.mkv")
	writeFile(t, path, 4)
	request := recognitionRequest(path)
	plain, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil {
		t.Fatalf("plain preview: %v", err)
	}
	request.LoadRuntimeFacts = true
	loaded, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil {
		t.Fatalf("loaded preview: %v", err)
	}
	if plain.PlanDigest == loaded.PlanDigest {
		t.Fatal("plain and runtime plans must not share a digest")
	}
}

func TestRecognitionRuntimeProbeFailureBlocksTheRowAndSubmit(t *testing.T) {
	f := newCommandFixture(t)
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error) {
		return filemaintenance.RecognitionRendered{Directory: input.Rules.TV.DirectoryTemplate, Filename: input.FileName}, nil
	}}
	f.service.SetRecognition(renderer, nil, fakeRecognitionPolicy{})
	f.service.SetRecognitionMediaProber(&fakeRecognitionMediaProber{err: errors.New("probe failed")})
	path := f.abs("Loose/Show - 01.mkv")
	writeFile(t, path, 4)
	request := recognitionRequest(path)
	request.LoadRuntimeFacts = true
	preview, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	row := recognitionRow(t, preview, "Show - 01.mkv")
	if row.Disposition != RecognitionError || len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerRuntimeFacts {
		t.Fatalf("row = %+v", row)
	}
	_, err = f.service.SubmitRecognition(context.Background(), SubmitRecognitionRequest{
		IdempotencyKey: "runtime-failure", ExpectedPlanDigest: preview.PlanDigest,
		RecognitionRequest: request, Conflict: RecognitionConflictBlock,
	})
	mustCode(t, err, CodeRecognitionNotRenderable)
}

func hasCode(err error, code string) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code
}

func intPointer(value int) *int { return &value }

func TestRecognitionAnimeDraftReachesTheRender(t *testing.T) {
	f := newCommandFixture(t)
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error) {
		return filemaintenance.RecognitionRendered{
			Directory: input.Rules.TV.DirectoryTemplate,
			Filename:  "renamed." + input.FileName,
		}, nil
	}}
	f.service.SetRecognition(renderer, fakeMatchAnimes{"anime-1": {ID: "anime-1", Title: "Show"}}, fakeRecognitionPolicy{})
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
	if _, err := f.service.PreviewRecognition(ctx, request); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(renderer.inputs) == 0 || renderer.inputs[0].AnimeDraft == nil {
		t.Fatalf("render input = %+v", renderer.inputs)
	}
	draft := renderer.inputs[0].AnimeDraft
	if draft.Title != "Edited" || draft.MediaKind != "movie" || draft.Year == nil || *draft.Year != 2024 {
		t.Fatalf("draft = %+v", draft)
	}
	if !draft.BGM || draft.BGMWorkID != "bgm-9" || !draft.TMDB || draft.TMDBWorkID != "42" || draft.TMDBMediaKind != "movie" || draft.TMDBSeason != 0 {
		t.Fatalf("bindings = %+v", draft)
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
	f := newCommandFixture(t)
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (filemaintenance.RecognitionRendered, error) {
		return filemaintenance.RecognitionRendered{
			Directory: input.Rules.TV.DirectoryTemplate,
			Filename:  "renamed." + input.FileName,
		}, nil
	}}
	// No matchAnimes at all: a draft-only match must not read one.
	f.service.SetRecognition(renderer, nil, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	request.Match = &RecognitionMatch{Anime: &RecognitionAnimeDraft{Title: "Edited", MediaKind: "tv"}}
	if _, err := f.service.PreviewRecognition(ctx, request); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(renderer.inputs) == 0 || renderer.inputs[0].ManagedAnimeID != "" {
		t.Fatalf("render input = %+v", renderer.inputs)
	}
	draft := renderer.inputs[0].AnimeDraft
	if draft == nil || draft.Title != "Edited" || draft.MediaKind != "tv" {
		t.Fatalf("draft = %+v", draft)
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
