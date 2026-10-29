package filemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/episodetitle"
	"bansoko/internal/filemaintenance"
)

type fakeMatchRenderer struct {
	inputs  []filemaintenance.MatchRenderInput
	render  filemaintenance.MatchRender
	err     error
	renders int
}

func (f *fakeMatchRenderer) RenderMatch(_ context.Context, input filemaintenance.MatchRenderInput) (filemaintenance.MatchRender, error) {
	f.inputs = append(f.inputs, input)
	f.renders++
	return f.render, f.err
}

type fakeMatchAnimes map[string]MatchAnimeFact

func (f fakeMatchAnimes) MatchAnime(_ context.Context, id string) (*MatchAnimeFact, error) {
	if fact, ok := f[id]; ok {
		return &fact, nil
	}
	return nil, nil
}

type fakeCatalog struct {
	seasons  []episodetitle.Season
	episodes episodetitle.List
	err      error
}

func (f fakeCatalog) Seasons(context.Context, episodetitle.Source, string) ([]episodetitle.Season, error) {
	return f.seasons, f.err
}

func (f fakeCatalog) Season(context.Context, episodetitle.Key, bool) (episodetitle.List, error) {
	return f.episodes, f.err
}

type matchFixture struct {
	commandFixture
	renderer *fakeMatchRenderer
	animes   fakeMatchAnimes
	download string
}

// matchTargetRel is the rendered target the fixture's renderer returns.
const matchTargetRel = "Show/Season 01/Show - E03.mkv"

func newMatchFixture(t *testing.T) matchFixture {
	t.Helper()
	f := newCommandFixture(t)
	for _, rel := range []string{"Show/extras/orig.mkv", "Show/extras/orig.zh.ass", "Show/extras/other.ass", "Show/extras/notes.txt", "Show/Season 02/taken.mkv"} {
		writeFile(t, filepath.Join(f.library, filepath.FromSlash(rel)), 4)
	}
	// The released placement the file came from.
	released := activePlacement("p-orig", filepath.Join(f.library, "Show", "extras", "orig.mkv"), "anime-1")
	released.State = "released"
	f.store.placements = append(f.store.placements, released)
	// A download connection's root is a second browsable tree: a match may
	// take its source from any path the container can read.
	download := canonicalTemp(t)
	writeFile(t, filepath.Join(download, "ep.mkv"), 4)
	f.store.mounts = append(f.store.mounts, MountConfig{Key: DownloadMountPrefix + "c1", Kind: MountDownload, ConnectionID: "c1", Path: download})
	renderer := &fakeMatchRenderer{render: filemaintenance.MatchRender{Path: filepath.Join(f.library, "Show", "Season 01", "Show - E03.mkv"), Lineage: true}}
	animes := fakeMatchAnimes{
		"anime-1": {ID: "anime-1", Title: "Show", SubscriptionState: "active", TMDBWorkID: "42", TMDBMediaKind: "tv"},
		"anime-2": {ID: "anime-2", Title: "Other", SubscriptionState: "stopped"},
		"retired": {ID: "retired", Title: "Gone", SubscriptionState: "stopped", Retired: true},
		"oneshot": {ID: "oneshot", Title: "Temp", SubscriptionState: "stopped", OneShot: true},
		"movie":   {ID: "movie", Title: "Film", SubscriptionState: "active", TMDBWorkID: "7", TMDBMediaKind: "movie"},
		// A movie owner kind next to a TV tuple: the owner kind decides.
		"owner-movie": {ID: "owner-movie", Title: "Pinned", SubscriptionState: "active", TMDBWorkID: "8", TMDBMediaKind: "tv", OwnerMediaKind: "movie"},
	}
	f.service.SetMatch(renderer, animes, fakeCatalog{
		seasons:  []episodetitle.Season{{Number: 0, Name: "Specials", EpisodeCount: 2}, {Number: 1, EpisodeCount: 12}},
		episodes: episodetitle.List{Episodes: []episodetitle.Episode{{Number: 1, Title: "One", AirDate: "2024-01-01"}}},
	})
	return matchFixture{commandFixture: f, renderer: renderer, animes: animes, download: download}
}

func (f matchFixture) request(rel string) MatchRequest {
	return MatchRequest{Path: f.abs(rel), Season: 1, Episode: 3}
}

func (f matchFixture) target(rel string) string {
	return f.abs(rel)
}

func TestMatchUnwiredIsRefused(t *testing.T) {
	f := newCommandFixture(t)
	_, err := f.service.PreviewMatch(context.Background(), MatchRequest{Path: f.abs("Loose/a.mkv"), Season: 1, Episode: 1})
	mustCode(t, err, CodeCommandsUnavailable)
	_, err = f.service.SubmitMatch(context.Background(), SubmitMatchRequest{IdempotencyKey: "k"})
	mustCode(t, err, CodeCommandsUnavailable)
	_, err = f.service.TMDBSeasons(context.Background(), "anime-1")
	mustCode(t, err, CodeCommandsUnavailable)
}

func TestMatchRuleOnlyForDetachedVideos(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	rule := func(rel string) *OperationRule {
		t.Helper()
		detail, err := f.service.Detail(ctx, f.abs(rel))
		if err != nil {
			t.Fatalf("detail %s: %v", rel, err)
		}
		for _, candidate := range detail.Operations {
			if candidate.Operation == OperationMatch {
				return &candidate
			}
		}
		return nil
	}
	if got := rule("Loose/a.mkv"); got == nil || !got.Allowed {
		t.Fatalf("a loose video is matchable: %+v", got)
	}
	if got := rule("Show/extras/orig.mkv"); got == nil || !got.Allowed {
		t.Fatalf("a released video is matchable: %+v", got)
	}
	if got := rule("Show/managed.mkv"); got != nil {
		t.Fatalf("a managed video is aligned, not matched: %+v", got)
	}
	if got := rule("Show/managed.zh.ass"); got != nil {
		t.Fatalf("a subtitle is not matched: %+v", got)
	}
	if got := rule("Show/extras/notes.txt"); got != nil {
		t.Fatalf("a non-video is not matched: %+v", got)
	}
	detail, err := f.service.Detail(ctx, f.abs("Show/extras/orig.mkv"))
	if err != nil || detail.MatchDefaultAnime == nil || detail.MatchDefaultAnime.ID != "anime-1" {
		t.Fatalf("the default anime is the placement's: %+v %v", detail.MatchDefaultAnime, err)
	}

	f.store.inProgress = append(f.store.inProgress, InProgressFact{Path: f.abs("Loose/b.mkv"), Kind: InProgressUserOperation})
	if got := rule("Loose/b.mkv"); got == nil || got.Allowed || got.Block != BlockInProgress {
		t.Fatalf("an in-progress video is blocked: %+v", got)
	}

	// A video outside any work tree is matchable too: the source may lie
	// anywhere the container can read, and the target is the work's own.
	downloadDetail, err := f.service.Detail(ctx, filepath.Join(f.download, "ep.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	var allowed *OperationRule
	for index := range downloadDetail.Operations {
		if downloadDetail.Operations[index].Operation == OperationMatch {
			allowed = &downloadDetail.Operations[index]
		}
	}
	if allowed == nil || !allowed.Allowed {
		t.Fatalf("a file outside the work tree is matchable: %+v", allowed)
	}
	if _, err := f.service.PreviewMatch(ctx, MatchRequest{Path: filepath.Join(f.download, "ep.mkv"), ManagedAnimeID: "anime-1", Season: 1, Episode: 1}); err != nil {
		t.Fatalf("preview of a match from another mount: %v", err)
	}
}

func TestPreviewMatchWritesNothingAndShowsTheTarget(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	preview, err := f.service.PreviewMatch(ctx, f.request("Show/extras/orig.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.TargetPath != f.target(matchTargetRel) || preview.Anime.ID != "anime-1" || preview.Season != 1 || preview.Episode != 3 || !preview.FromLineage {
		t.Fatalf("preview = %+v", preview)
	}
	if want := f.abs("Show/Season 01"); len(preview.DirectoriesToCreate) != 1 || preview.DirectoriesToCreate[0] != want {
		t.Fatalf("directories to create = %v", preview.DirectoriesToCreate)
	}
	if len(preview.SubtitlesLeftBehind) != 1 || preview.SubtitlesLeftBehind[0] != f.abs("Show/extras/orig.zh.ass") {
		t.Fatalf("only the subtitle that follows the video's name stays behind: %v", preview.SubtitlesLeftBehind)
	}
	if preview.Conflict != nil || len(preview.Blockers) != 0 || preview.Revision == "" {
		t.Fatalf("preview = %+v", preview)
	}
	input := f.renderer.inputs[0]
	if input.ManagedAnimeID != "anime-1" || input.LineagePlacementID != "p-orig" || input.FileName != "orig.mkv" || input.Season != 1 || input.Episode != 3 {
		t.Fatalf("render input = %+v", input)
	}
	if len(f.commands.admitted) != 0 {
		t.Fatal("a preview admits nothing")
	}
	if _, err := os.Lstat(f.abs("Show/Season 01")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a preview creates no directory: %v", err)
	}
}

func TestPreviewMatchResolvesTheAnime(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()

	request := f.request("Loose/a.mkv")
	_, err := f.service.PreviewMatch(ctx, request)
	mustCode(t, err, CodeMatchAnimeRequired)

	request.ManagedAnimeID = "anime-2"
	preview, err := f.service.PreviewMatch(ctx, request)
	if err != nil || preview.Anime.ID != "anime-2" || f.renderer.inputs[len(f.renderer.inputs)-1].LineagePlacementID != "" {
		t.Fatalf("a stopped anime is selectable and a file without lineage renders without it: %+v %v", preview, err)
	}

	for id, want := range map[string]string{"retired": CodeAnimeRetired, "oneshot": CodeMatchAnimeUnavailable, "missing": CodeMatchAnimeUnavailable} {
		request.ManagedAnimeID = id
		_, err := f.service.PreviewMatch(ctx, request)
		mustCode(t, err, want)
	}

	// An explicit anime overrides the lineage's.
	request = f.request("Show/extras/orig.mkv")
	request.ManagedAnimeID = "anime-2"
	if preview, err = f.service.PreviewMatch(ctx, request); err != nil || preview.Anime.ID != "anime-2" {
		t.Fatalf("%+v %v", preview, err)
	}
}

func TestPreviewMatchRefusesWhatItDoesNotApplyTo(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	for rel, want := range map[string]string{
		"Show/managed.mkv":          CodeMatchRequiresDetached,
		"Show/managed.zh.ass":       CodeMatchRequiresDetached,
		"Show/extras/notes.txt":     CodeOperationNotAllowed,
		"Show/extras/orig.zh.ass":   CodeOperationNotAllowed,
		"Loose":                     CodeOperationNotAllowed,
		".bansoko-cleanup-x/object": CodeOperationNotAllowed,
	} {
		request := f.request(rel)
		request.ManagedAnimeID = "anime-1"
		_, err := f.service.PreviewMatch(ctx, request)
		mustCode(t, err, want)
	}
	request := f.request("Show/extras/orig.mkv")
	for _, bad := range []MatchRequest{
		{Season: -1, Episode: 1}, {Season: 10000, Episode: 1}, {Season: 1, Episode: 0}, {Season: 1, Episode: 100000}, {Season: 1, Episode: 1, MediaKind: "movie"},
	} {
		bad.Path = request.Path
		_, err := f.service.PreviewMatch(ctx, bad)
		mustCode(t, err, CodeMatchInvalid)
	}
	if f.renderer.renders != 0 {
		t.Fatal("nothing renders for a request that does not apply")
	}
}

func TestPreviewMatchReportsBlockers(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	request := f.request("Show/extras/orig.mkv")

	f.renderer.err = &filemaintenance.MatchRenderError{Code: filemaintenance.MatchRenderFactsMissing, Facts: []string{"media.resolution", "anime.group"}}
	preview, err := f.service.PreviewMatch(ctx, request)
	if err != nil || preview.TargetPath != "" || len(preview.Blockers) != 1 || preview.Blockers[0].Code != MatchBlockedFactsMissing ||
		len(preview.Blockers[0].Facts) != 2 {
		t.Fatalf("missing facts are a blocker with their paths: %+v %v", preview, err)
	}
	f.renderer.err = &filemaintenance.MatchRenderError{Code: filemaintenance.MatchRenderTemplateInvalid}
	if preview, _ = f.service.PreviewMatch(ctx, request); len(preview.Blockers) != 1 || preview.Blockers[0].Code != MatchBlockedTemplateInvalid {
		t.Fatalf("%+v", preview)
	}
	f.renderer.err = errors.New("database is down")
	if _, err = f.service.PreviewMatch(ctx, request); err == nil {
		t.Fatal("a system fault is an error, not a blocker")
	}
	f.renderer.err = nil

	for rendered, want := range map[string]MatchBlockerCode{
		"../escape/ep.mkv":                 MatchBlockedTargetInvalid,
		"/":                                MatchBlockedTargetInvalid,
		f.abs(".bansoko-cleanup-x/ep.mkv"): MatchBlockedTargetInvalid,
		filepath.Join(f.appdata, "ep.mkv"): MatchBlockedTargetInvalid,
		"":                                 MatchBlockedTargetInvalid,
		f.abs("Show/" + strings.Repeat("x", 300)): MatchBlockedTargetInvalid,
		f.abs("Show/extras/orig.mkv"):             MatchBlockedTargetIsSource,
	} {
		f.renderer.render.Path = rendered
		preview, err := f.service.PreviewMatch(ctx, request)
		if err != nil || len(preview.Blockers) != 1 || preview.Blockers[0].Code != want || preview.TargetPath != "" {
			t.Fatalf("rendered %q: %+v %v", rendered, preview, err)
		}
	}
}

func TestPreviewMatchRefusesSymlinksAndFilesInTheTargetChain(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	request := f.request("Show/extras/orig.mkv")
	outside := canonicalTemp(t)
	if err := os.Symlink(outside, filepath.Join(f.library, "Linked")); err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{f.abs("Linked/Season 01/ep.mkv"), f.abs("Show/extras/orig.mkv/deeper/ep.mkv")} {
		f.renderer.render.Path = rendered
		preview, err := f.service.PreviewMatch(ctx, request)
		if err != nil || len(preview.Blockers) != 1 || preview.Blockers[0].Code != MatchBlockedTargetInvalid {
			t.Fatalf("%q must not be a target: %+v %v", rendered, preview, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(outside, "Season 01")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nothing was created through the symlink")
	}
}

func TestPreviewMatchReportsAnOccupiedTarget(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	request := f.request("Show/extras/orig.mkv")

	f.renderer.render.Path = f.abs("Show/Season 02/taken.mkv")
	preview, err := f.service.PreviewMatch(ctx, request)
	if err != nil || preview.Conflict == nil || preview.Conflict.OccupiedBy != OccupantFile ||
		preview.Conflict.VersionPath != f.abs("Show/Season 02/taken (2).mkv") || len(preview.DirectoriesToCreate) != 0 {
		t.Fatalf("an existing file is a conflict with a suffix candidate: %+v %v", preview, err)
	}

	// A managed file is on disk too, and still reads as managed, not as a file.
	f.renderer.render.Path = f.abs("Show/managed.mkv")
	if preview, _ = f.service.PreviewMatch(ctx, request); preview.Conflict == nil || preview.Conflict.OccupiedBy != OccupantManaged {
		t.Fatalf("a managed file on disk: %+v", preview.Conflict)
	}

	// A managed file named by an owner, even when missing from disk.
	f.store.placements = append(f.store.placements, activePlacement("p-t", f.abs("Show/Season 03/Show - E01.mkv"), "anime-1"))
	f.renderer.render.Path = f.abs("Show/Season 03/Show - E01.mkv")
	if preview, _ = f.service.PreviewMatch(ctx, request); preview.Conflict == nil || preview.Conflict.OccupiedBy != OccupantManaged {
		t.Fatalf("a managed path is a conflict: %+v", preview.Conflict)
	}
	f.store.placements = f.store.placements[:len(f.store.placements)-1]

	f.store.inProgress = append(f.store.inProgress, InProgressFact{Path: f.abs("Show/Season 03/Show - E01.mkv"), Kind: InProgressFileOperation})
	if preview, _ = f.service.PreviewMatch(ctx, request); preview.Conflict == nil || preview.Conflict.OccupiedBy != OccupantInProgress {
		t.Fatalf("an unfinished owner holds the target: %+v", preview.Conflict)
	}
}

func (f matchFixture) submit(t *testing.T, key string) SubmitMatchRequest {
	t.Helper()
	return SubmitMatchRequest{
		IdempotencyKey: key, Actor: "web", Path: f.abs("Show/extras/orig.mkv"),
		ExpectedRevision: f.revision(t, "Show/extras/orig.mkv"), ManagedAnimeID: "anime-1", Season: 1, Episode: 3,
		ExpectedTarget: f.target(matchTargetRel), Conflict: ConflictBlock,
	}
}

func TestSubmitMatchFreezesTheRequest(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	op, err := f.service.SubmitMatch(ctx, f.submit(t, "key-1"))
	if err != nil || op.Operation != UserMatch {
		t.Fatalf("%+v %v", op, err)
	}
	if len(f.commands.admitted) != 1 {
		t.Fatal("expected one admission")
	}
	admission := f.commands.admitted[0]
	if admission.Operation != UserMatch || !admission.RequireDetachedSource || admission.Collision != ConflictBlock ||
		admission.TargetPath != f.abs("Show/Season 01/Show - E03.mkv") ||
		admission.SourcePath != f.abs("Show/extras/orig.mkv") ||
		admission.ExpectedSize != 4 ||
		len(admission.SourceSpellings) == 0 || len(admission.TargetSpellings) == 0 {
		t.Fatalf("admission = %+v", admission)
	}
	match := admission.Match
	if match == nil || match.ManagedAnimeID != "anime-1" || match.Season != 1 || match.Episode != 3 {
		t.Fatalf("the choice is on the admission: %+v", match)
	}
	if _, err := os.Lstat(f.abs("Show/Season 01")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the service creates no directory; the worker does: %v", err)
	}
}

func TestSubmitMatchChecksRevisionTargetAndConflict(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()

	request := f.submit(t, "k-rev")
	request.ExpectedRevision = "stale"
	_, err := f.service.SubmitMatch(ctx, request)
	mustCode(t, err, CodeEntryChanged)

	request = f.submit(t, "k-target")
	request.ExpectedTarget = f.abs("Show/Season 01/Another.mkv")
	_, err = f.service.SubmitMatch(ctx, request)
	mustCode(t, err, CodeMatchTargetChanged)

	f.renderer.render.Path = f.abs("Show/Season 02/taken.mkv")
	request = f.submit(t, "k-block")
	request.ExpectedTarget = f.abs("Show/Season 02/taken.mkv")
	_, err = f.service.SubmitMatch(ctx, request)
	mustCode(t, err, CodeTargetExists)
	request.IdempotencyKey, request.Conflict = "k-version", ConflictVersion
	op, err := f.service.SubmitMatch(ctx, request)
	if err != nil || op.ID == "" || f.commands.admitted[len(f.commands.admitted)-1].Collision != ConflictVersion {
		t.Fatalf("the version choice admits the occupied target: %+v %v", op, err)
	}

	f.renderer.render.Path = f.target(matchTargetRel)
	f.renderer.err = &filemaintenance.MatchRenderError{Code: filemaintenance.MatchRenderFactsMissing, Facts: []string{"media.resolution"}}
	_, err = f.service.SubmitMatch(ctx, f.submit(t, "k-render"))
	mustCode(t, err, CodeMatchNotRenderable)
	f.renderer.err = nil

	for rel, want := range map[string]string{"Show/managed.mkv": CodeMatchRequiresDetached, "Loose": CodeOperationNotAllowed} {
		request := f.submit(t, "k-"+rel)
		request.Path = f.abs(rel)
		_, err := f.service.SubmitMatch(ctx, request)
		mustCode(t, err, want)
	}
	request = f.submit(t, "k-retired")
	request.ManagedAnimeID = "retired"
	_, err = f.service.SubmitMatch(ctx, request)
	mustCode(t, err, CodeAnimeRetired)

	request = f.submit(t, "k-conflict-choice")
	request.Conflict = "overwrite"
	_, err = f.service.SubmitMatch(ctx, request)
	mustCode(t, err, CodeTargetInvalid)
}

func TestSubmitMatchIsIdempotent(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	request := f.submit(t, "key-idem")
	first, err := f.service.SubmitMatch(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	renders := f.renderer.renders
	again, err := f.service.SubmitMatch(ctx, request)
	if err != nil || again.ID != first.ID || len(f.commands.admitted) != 1 || f.renderer.renders != renders {
		t.Fatalf("a replay returns the original without rendering or admitting again: %+v %v", again, err)
	}
	other := request
	other.Episode = 4
	_, err = f.service.SubmitMatch(ctx, other)
	mustCode(t, err, CodeIdempotencyConflict)
}

func TestPlainOperationsNeverMatch(t *testing.T) {
	f := newMatchFixture(t)
	_, err := f.service.SubmitOperation(context.Background(), OperationRequest{
		IdempotencyKey: "k", Operation: UserMatch, Path: f.abs("Loose/a.mkv"),
		ExpectedRevision: f.revision(t, "Loose/a.mkv"), Target: &Target{Path: f.abs("Loose/z.mkv"), Conflict: ConflictBlock},
	})
	mustCode(t, err, CodeTargetInvalid)
}

func TestTMDBSeasonLists(t *testing.T) {
	f := newMatchFixture(t)
	ctx := context.Background()
	seasons, err := f.service.TMDBSeasons(ctx, "anime-1")
	if err != nil || len(seasons) != 2 || seasons[0].Number != 0 || seasons[1].EpisodeCount != 12 {
		t.Fatalf("season 0 is listed: %+v %v", seasons, err)
	}
	episodes, err := f.service.TMDBSeasonEpisodes(ctx, "anime-1", 1)
	if err != nil || len(episodes) != 1 || episodes[0].Title != "One" || episodes[0].AirDate != "2024-01-01" {
		t.Fatalf("%+v %v", episodes, err)
	}
	for _, id := range []string{"anime-2", "movie", "owner-movie"} {
		_, err = f.service.TMDBSeasons(ctx, id)
		mustCode(t, err, CodeTMDBBindingRequired)
		_, err = f.service.TMDBSeasonEpisodes(ctx, id, 1)
		mustCode(t, err, CodeTMDBBindingRequired)
	}
	_, err = f.service.TMDBSeasons(ctx, "nobody")
	mustCode(t, err, CodeMatchAnimeUnavailable)
	_, err = f.service.TMDBSeasonEpisodes(ctx, "anime-1", -1)
	mustCode(t, err, CodeMatchInvalid)

	f.service.SetMatch(f.renderer, f.animes, fakeCatalog{err: errors.Join(episodetitle.ErrTransient, errors.New("503"))})
	_, err = f.service.TMDBSeasons(ctx, "anime-1")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != ErrorUnavailable || typed.Code != CodeMetadataUnavailable {
		t.Fatalf("a provider outage is unavailable, not a conflict: %v", err)
	}
}
