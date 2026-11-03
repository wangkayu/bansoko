package filemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/filemaintenance"
)

type fakeRecognitionPolicy struct{ route string }

func (p fakeRecognitionPolicy) UnclassifiedRoute(context.Context) (string, error) {
	return p.route, nil
}

// fakeRecognitionRenderer stands in for the naming renderer when a test needs
// a matched render (the real one reads the anime from the database).
type fakeRecognitionRenderer struct {
	inputs []filemaintenance.RecognitionRenderInput
	render func(filemaintenance.RecognitionRenderInput) (string, error)
}

func (f *fakeRecognitionRenderer) RenderRecognition(_ context.Context, input filemaintenance.RecognitionRenderInput) (string, error) {
	f.inputs = append(f.inputs, input)
	return f.render(input)
}

const recognitionVideoTemplate = "${ep.number}.${file.ext}"

func newRecognitionFixture(t *testing.T, policy RecognitionPolicy) commandFixture {
	t.Helper()
	f := newCommandFixture(t)
	f.service.SetRecognition(filemaintenance.NewNamingTargetRenderer(nil, nil), nil, policy)
	return f
}

func recognitionRequest(paths ...string) RecognitionRequest {
	return RecognitionRequest{
		Paths:            paths,
		Parser:           RecognitionParser{ID: "regex"},
		VideoTemplate:    recognitionVideoTemplate,
		SubtitleTemplate: "sub",
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
	if video.Disposition != RecognitionRename || video.TargetName != "1.mkv" || video.Episode == nil || video.Episode.Number != "1" {
		t.Fatalf("video row = %+v", video)
	}
	if video.Group != "Show - 01" {
		t.Fatalf("video group = %q", video.Group)
	}
	subtitle := recognitionRow(t, preview, "Show - 01.zh.ass")
	if subtitle.Disposition != RecognitionRename || subtitle.TargetName != "1.sub.ass" || subtitle.Kind != "subtitle" {
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

	// Route moves them under their own names into Extras.
	route := newRecognitionFixture(t, fakeRecognitionPolicy{route: "route"})
	writeFile(t, route.abs("Loose/mystery.mkv"), 4)
	writeFile(t, route.abs("Loose/mystery.zh.ass"), 4)
	preview, err = route.service.PreviewRecognition(ctx, recognitionRequest(route.abs("Loose/mystery.mkv"), route.abs("Loose/mystery.zh.ass")))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, row := range preview.Rows {
		if row.Disposition != RecognitionMove || filepath.Dir(row.TargetPath) != route.abs("Loose/Extras") || row.TargetName != row.Name {
			t.Fatalf("route row = %+v", row)
		}
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
	preview, err := f.service.PreviewRecognition(ctx, RecognitionRequest{
		Paths: []string{f.abs("Loose/orphan.zh.ass")}, Parser: RecognitionParser{ID: "regex"},
		VideoTemplate: recognitionVideoTemplate, SubtitleTemplate: "sub",
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if row := recognitionRow(t, preview, "orphan.zh.ass"); row.Disposition != RecognitionError || len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerVideoMissing {
		t.Fatalf("orphan row = %+v", row)
	}
}

// A filename template that renders `/`-separated levels moves the file into
// those levels under the selection's folder; the subtitle follows its video.
func TestRecognitionRendersASubdirectoryTarget(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	writeFile(t, f.abs("Loose/Show - 01.zh.ass"), 4)
	request := recognitionRequest(f.abs("Loose/Show - 01.mkv"), f.abs("Loose/Show - 01.zh.ass"))
	request.VideoTemplate = "${ep.number}/Show - 01.${file.ext}"
	request.SubtitleTemplate = "zh"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	video := recognitionRow(t, preview, "Show - 01.mkv")
	if video.Disposition != RecognitionMove || video.TargetName != "1/Show - 01.mkv" || filepath.Dir(video.TargetPath) != f.abs("Loose/1") {
		t.Fatalf("video row = %+v", video)
	}
	subtitle := recognitionRow(t, preview, "Show - 01.zh.ass")
	if subtitle.Disposition != RecognitionMove || subtitle.TargetName != "1/Show - 01.zh.ass" || filepath.Dir(subtitle.TargetPath) != f.abs("Loose/1") {
		t.Fatalf("subtitle row = %+v", subtitle)
	}
	outcome, err := f.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "subdir", ExpectedPlanDigest: preview.PlanDigest, RecognitionRequest: request})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if outcome.Moved != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	for _, op := range outcome.Operations {
		if op.TargetPath != video.TargetPath && op.TargetPath != subtitle.TargetPath {
			t.Fatalf("operation target = %s", op.TargetPath)
		}
	}
	// The rendered directory levels exist once the submit is admitted; the
	// moves themselves are the executor's, like every recognition operation.
	if info, err := os.Stat(f.abs("Loose/1")); err != nil || !info.IsDir() {
		t.Fatalf("rendered directory was not created: %v", err)
	}
}

func TestRecognitionRefusesAnIllegalRenderedTarget(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	for _, template := range []string{
		"${ep.number}/../Show - 01.${file.ext}",                               // traversal
		"/${file.stem}.${file.ext}",                                           // escapes upward to an absolute
		"${ep.number}//Show - 01.${file.ext}",                                 // empty level
		strings.Repeat("a/", maxRecognitionDepth+1) + "Show - 01.${file.ext}", // too deep
	} {
		request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
		request.VideoTemplate = template
		preview, err := f.service.PreviewRecognition(ctx, request)
		if err != nil {
			t.Fatalf("preview %q: %v", template, err)
		}
		row := recognitionRow(t, preview, "Show - 01.mkv")
		if row.Disposition != RecognitionError || len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerTargetInvalid {
			t.Fatalf("template %q row = %+v", template, row)
		}
		_, err = f.service.SubmitRecognition(ctx, SubmitRecognitionRequest{IdempotencyKey: "illegal", ExpectedPlanDigest: preview.PlanDigest, RecognitionRequest: request})
		mustCode(t, err, CodeRecognitionNotRenderable)
	}
}

func TestRecognitionMissingAnimeFactsIsABlocker(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	request := recognitionRequest(f.abs("Loose/Show - 01.mkv"))
	request.VideoTemplate = "${anime.title}.${file.ext}"
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
	other.VideoTemplate = "${file.stem}.${file.ext}"
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
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (string, error) {
		if input.Kind != "video_filename" {
			return "sub", nil
		}
		return "ep-" + input.Episode + "." + input.EpisodeSource + "." + input.FileName, nil
	}}
	f.service.SetRecognition(renderer, fakeMatchAnimes{"anime-1": {ID: "anime-1", Title: "Show"}}, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/mystery.mkv"), 4)
	request := RecognitionRequest{
		Paths: []string{f.abs("Loose/mystery.mkv")}, Parser: RecognitionParser{ID: "regex"},
		VideoTemplate: "x", SubtitleTemplate: "sub",
		Match: &RecognitionMatch{ManagedAnimeID: "anime-1", Season: intPointer(3), Episode: intPointer(7), Offset: 1},
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

func hasCode(err error, code string) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code
}

func intPointer(value int) *int { return &value }

func TestRecognitionAnimeDraftReachesTheRender(t *testing.T) {
	f := newCommandFixture(t)
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (string, error) {
		if input.Kind != "video_filename" {
			return "sub", nil
		}
		return "renamed." + input.FileName, nil
	}}
	f.service.SetRecognition(renderer, fakeMatchAnimes{"anime-1": {ID: "anime-1", Title: "Show"}}, fakeRecognitionPolicy{})
	ctx := context.Background()
	writeFile(t, f.abs("Loose/Show - 01.mkv"), 4)
	year := 2024
	request := RecognitionRequest{
		Paths: []string{f.abs("Loose/Show - 01.mkv")}, Parser: RecognitionParser{ID: "regex"},
		VideoTemplate: "x", SubtitleTemplate: "sub",
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
	renderer := &fakeRecognitionRenderer{render: func(input filemaintenance.RecognitionRenderInput) (string, error) {
		if input.Kind != "video_filename" {
			return "sub", nil
		}
		return "renamed." + input.FileName, nil
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
