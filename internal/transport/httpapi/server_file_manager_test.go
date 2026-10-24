package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/filemanager"
	observabilityport "bansoko/internal/observability/port"
)

// fakeFileManager records the arguments it was called with and answers a
// fixed result or a typed error. Every path it carries is absolute, the way
// the real service reads and writes.
type fakeFileManager struct {
	err            error
	path, cursor   string
	limit          int
	detail         filemanager.Detail
	located        string
	operation      filemanager.OperationRequest
	release        filemanager.ReleaseRequest
	align          filemanager.AlignRequest
	matchPreview   filemanager.MatchRequest
	match          filemanager.SubmitMatchRequest
	seasonsFor     string
	episodesFor    string
	episodesSeason int
}

func (f *fakeFileManager) PreviewMatch(_ context.Context, request filemanager.MatchRequest) (filemanager.MatchPreview, error) {
	f.matchPreview = request
	if f.err != nil {
		return filemanager.MatchPreview{}, f.err
	}
	return filemanager.MatchPreview{
		Revision: "rev-2", Anime: filemanager.AnimeRef{ID: "anime-1", Title: "Show", SubscriptionState: "active"},
		MediaKind: filemanager.MatchMediaTV, Season: request.Season, Episode: request.Episode,
		TargetPath: "/srv/library/Show/Season 01/Show - E03.mkv", DirectoriesToCreate: []string{"/srv/library/Show/Season 01"},
		Conflict:            &filemanager.MatchConflict{OccupiedBy: filemanager.OccupantFile, VersionPath: "/srv/library/Show/Season 01/Show - E03 (2).mkv"},
		Blockers:            []filemanager.MatchBlocker{{Code: filemanager.MatchBlockedFactsMissing, Facts: []string{"media.resolution"}}},
		SubtitlesLeftBehind: []string{"/srv/library/Show/Extras/ep.ass"}, FromLineage: true,
	}, nil
}

func (f *fakeFileManager) SubmitMatch(_ context.Context, request filemanager.SubmitMatchRequest) (filemanager.UserOperation, error) {
	f.match = request
	if f.err != nil {
		return filemanager.UserOperation{}, f.err
	}
	return filemanager.UserOperation{
		ID: "op-m", Operation: filemanager.UserMatch, State: filemanager.UserOperationCompleted,
		SourcePath: "/srv/library/Show/extras/ep.mkv",
		TargetPath: "/srv/library/Show/Season 01/Show - E03.mkv", ResultPath: "/srv/library/Show/Season 01/Show - E03.mkv",
		Match:       &filemanager.MatchInfo{ManagedAnimeID: request.ManagedAnimeID, Season: request.Season, Episode: request.Episode},
		CreatedDirs: []string{"/srv/library/Show/Season 01", "/elsewhere/kept-as-is"},
	}, nil
}

func (f *fakeFileManager) TMDBSeasons(_ context.Context, id string) ([]filemanager.TMDBSeason, error) {
	f.seasonsFor = id
	if f.err != nil {
		return nil, f.err
	}
	return []filemanager.TMDBSeason{{Number: 0, Name: "Specials", EpisodeCount: 2}, {Number: 1, EpisodeCount: 12, AirDate: "2024-01-01"}}, nil
}

func (f *fakeFileManager) TMDBSeasonEpisodes(_ context.Context, id string, season int) ([]filemanager.TMDBEpisode, error) {
	f.episodesFor, f.episodesSeason = id, season
	if f.err != nil {
		return nil, f.err
	}
	return []filemanager.TMDBEpisode{{Number: 1, Title: "One", AirDate: "2024-01-01"}, {Number: 2}}, nil
}

func (f *fakeFileManager) SubmitOperation(_ context.Context, request filemanager.OperationRequest) (filemanager.UserOperation, error) {
	f.operation = request
	if f.err != nil {
		return filemanager.UserOperation{}, f.err
	}
	op := filemanager.UserOperation{
		ID: "op-1", Operation: request.Operation, State: filemanager.UserOperationReady, SourcePath: request.Path,
	}
	if request.Target != nil {
		op.TargetPath = request.Target.Path
	}
	return op, nil
}

func (f *fakeFileManager) Operation(_ context.Context, id string) (filemanager.UserOperation, error) {
	if f.err != nil {
		return filemanager.UserOperation{}, f.err
	}
	return filemanager.UserOperation{ID: id, Operation: filemanager.UserDelete, State: filemanager.UserOperationFailed,
		SourcePath: "/srv/library/a/ep01.mkv", Failure: "WORKER_EXECUTION_STOPPED"}, nil
}

func (f *fakeFileManager) Release(_ context.Context, request filemanager.ReleaseRequest) (filemanager.ReleaseOutcome, error) {
	f.release = request
	if f.err != nil {
		return filemanager.ReleaseOutcome{}, f.err
	}
	return filemanager.ReleaseOutcome{ManagedAnimeID: "anime-1", Released: 2}, nil
}

func (f *fakeFileManager) Align(_ context.Context, request filemanager.AlignRequest) (filemanager.AlignOutcome, error) {
	f.align = request
	if f.err != nil {
		return filemanager.AlignOutcome{}, f.err
	}
	return filemanager.AlignOutcome{ManagedAnimeID: "anime-1"}, nil
}

func (f *fakeFileManager) Mounts(context.Context) ([]filemanager.Mount, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []filemanager.Mount{
		{MountConfig: filemanager.MountConfig{Key: filemanager.RootMountKey, Kind: filemanager.MountRoot, Path: filemanager.RootMountPath}, Available: true},
		{MountConfig: filemanager.MountConfig{Key: "download:qb-main", Kind: filemanager.MountDownload, ConnectionID: "qb-main", Label: "Main", Path: "/srv/downloads"},
			Unavailable: filemanager.MountNotConfigured},
	}, nil
}

func (f *fakeFileManager) List(_ context.Context, path, cursor string, limit int) (filemanager.Listing, error) {
	f.path, f.cursor, f.limit = path, cursor, limit
	if f.err != nil {
		return filemanager.Listing{}, f.err
	}
	size := int64(4)
	return filemanager.Listing{
		Path: path, NextCursor: "next",
		Entries: []filemanager.Entry{{Name: "ep01.zh.ass", Path: "/srv/library/Show/ep01.zh.ass", Kind: filemanager.KindFile, SizeBytes: &size,
			State: filemanager.StateManaged, Locked: true, ManagedAnimeID: "anime-1",
			Role: filemanager.RoleSubtitle, CompanionOf: "/srv/library/Show/ep01.mkv", NotAligned: true}},
	}, nil
}

func (f *fakeFileManager) Detail(_ context.Context, path string) (filemanager.Detail, error) {
	f.path = path
	if f.err != nil {
		return filemanager.Detail{}, f.err
	}
	return f.detail, nil
}

func (f *fakeFileManager) Locate(_ context.Context, id string) (filemanager.Location, error) {
	f.located = id
	if f.err != nil {
		return filemanager.Location{}, f.err
	}
	return filemanager.Location{Path: "/srv/library/Show"}, nil
}

func serveFileManager(t *testing.T, service *fakeFileManager, target string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	server := &Server{deps: Dependencies{FileManager: service, SkipAuth: true, RuntimeLogger: observabilityport.NopRuntimeLogger{}}}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: body is not JSON: %s", target, recorder.Body.String())
	}
	return recorder, body
}

func TestFileManagerHandlersShapeResponses(t *testing.T) {
	service := &fakeFileManager{detail: filemanager.Detail{
		Entry: filemanager.Entry{Name: "ep01.mkv", Path: "/srv/library/Show/ep01.mkv", Kind: filemanager.KindFile,
			State: filemanager.StateManaged, Locked: true, ManagedAnimeID: "anime-1", Role: filemanager.RoleVideo},
		Anime:          &filemanager.AnimeRef{ID: "anime-1", Title: "Show", SubscriptionState: "active"},
		AcquisitionID:  "acq-1",
		DetachedReason: filemanager.DetachedNoRecord, // dropped: the state is not detached
		Operations: []filemanager.OperationRule{
			{Operation: filemanager.OperationAlign, Allowed: true},
			{Operation: filemanager.OperationMove, Block: filemanager.BlockManaged},
		},
	}}

	recorder, body := serveFileManager(t, service, "/api/v1/file-manager/mounts")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("mounts: %d %v", recorder.Code, recorder.Header())
	}
	mounts := body["mounts"].([]any)
	root := mounts[0].(map[string]any)
	download := mounts[1].(map[string]any)
	if len(mounts) != 2 || root["key"] != "root" || root["kind"] != "root" || root["path"] != "/" ||
		root["available"] != true || root["unavailable_reason"] != nil ||
		download["key"] != "download:qb-main" || download["kind"] != "download" || download["path"] != "/srv/downloads" ||
		download["connection_id"] != "qb-main" || download["label"] != "Main" ||
		download["available"] != false || download["unavailable_reason"] != "not_configured" {
		t.Fatalf("mounts body = %v", body)
	}

	recorder, body = serveFileManager(t, service, "/api/v1/file-manager/entries?path=%2Fsrv%2Flibrary%2FShow&cursor=abc&limit=50")
	if recorder.Code != http.StatusOK || service.path != "/srv/library/Show" || service.cursor != "abc" || service.limit != 50 {
		t.Fatalf("entries: %d, called with %+v", recorder.Code, service)
	}
	if body["path"] != "/srv/library/Show" {
		t.Fatalf("the listing names the absolute directory it read: %v", body)
	}
	entry := body["entries"].([]any)[0].(map[string]any)
	if body["next_cursor"] != "next" || entry["path"] != "/srv/library/Show/ep01.zh.ass" || entry["state"] != "managed" ||
		entry["locked"] != true || entry["managed_anime_id"] != "anime-1" || entry["role"] != "subtitle" ||
		entry["companion_of"] != "/srv/library/Show/ep01.mkv" || entry["not_aligned"] != true || entry["size_bytes"] != float64(4) {
		t.Fatalf("entries body = %v", body)
	}

	recorder, body = serveFileManager(t, service, "/api/v1/file-manager/entry?path=%2Fsrv%2Flibrary%2FShow%2Fep01.mkv")
	if recorder.Code != http.StatusOK || service.path != "/srv/library/Show/ep01.mkv" {
		t.Fatalf("entry: %d %v", recorder.Code, body)
	}
	detail := body["entry"].(map[string]any)
	operations := body["operations"].([]any)
	move := operations[1].(map[string]any)
	if body["detached_reason"] != nil || body["acquisition_id"] != "acq-1" || body["anime"].(map[string]any)["managed_anime_id"] != "anime-1" ||
		detail["locked"] != true || detail["path"] != "/srv/library/Show/ep01.mkv" ||
		move["operation"] != "move" || move["allowed"] != false || move["blocked_reason"] != "managed" {
		t.Fatalf("entry body = %v", body)
	}

	recorder, body = serveFileManager(t, service, "/api/v1/file-manager/anime/anime-1/location")
	if recorder.Code != http.StatusOK || service.located != "anime-1" || body["path"] != "/srv/library/Show" {
		t.Fatalf("location: %d %v", recorder.Code, body)
	}
}

func TestFileManagerHandlersMapTypedErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&filemanager.Error{Kind: filemanager.ErrorInvalid, Code: filemanager.CodePathInvalid, Message: "bad"}, http.StatusBadRequest, filemanager.CodePathInvalid},
		{&filemanager.Error{Kind: filemanager.ErrorNotFound, Code: filemanager.CodePathNotFound, Message: "none"}, http.StatusNotFound, filemanager.CodePathNotFound},
		{&filemanager.Error{Kind: filemanager.ErrorConflict, Code: filemanager.CodeEntryLocked, Message: "locked"}, http.StatusConflict, filemanager.CodeEntryLocked},
		{&filemanager.Error{Kind: filemanager.ErrorUnavailable, Code: filemanager.CodePathUnreadable, Message: "gone"}, http.StatusServiceUnavailable, filemanager.CodePathUnreadable},
	} {
		recorder, body := serveFileManager(t, &fakeFileManager{err: tc.err}, "/api/v1/file-manager/entries?path=%2Fsrv")
		if recorder.Code != tc.status || body["code"] != tc.code {
			t.Fatalf("%s: %d %v", tc.code, recorder.Code, body)
		}
	}
	// A refusal that names the managed files inside a refused directory
	// carries them as the error detail's paths.
	locked := &filemanager.Error{Kind: filemanager.ErrorConflict, Code: filemanager.CodeEntryLocked, Message: "locked",
		Paths: []string{"/srv/library/Show/ep01.mkv"}}
	recorder, body := serveFileManager(t, &fakeFileManager{err: locked}, "/api/v1/file-manager/entries?path=%2Fsrv%2Flibrary%2FShow")
	details, _ := body["details"].([]any)
	if recorder.Code != http.StatusConflict || len(details) != 1 ||
		details[0].(map[string]any)["paths"].([]any)[0] != "/srv/library/Show/ep01.mkv" {
		t.Fatalf("locked refusal detail: %d %v", recorder.Code, body)
	}

	recorder, body = serveFileManager(t, &fakeFileManager{}, "/api/v1/file-manager/entry")
	if recorder.Code != http.StatusBadRequest || body["code"] != "REQUEST_INVALID" {
		t.Fatalf("a missing required path is a request error: %d %v", recorder.Code, body)
	}
}

func postFileManager(t *testing.T, service *fakeFileManager, target, key, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	server := &Server{deps: Dependencies{FileManager: service, SkipAuth: true, RuntimeLogger: observabilityport.NopRuntimeLogger{}}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	server.Handler().ServeHTTP(recorder, request)
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("%s: body is not JSON: %s", target, recorder.Body.String())
	}
	return recorder, decoded
}

func TestFileManagerCommandHandlers(t *testing.T) {
	service := &fakeFileManager{}
	recorder, body := postFileManager(t, service, "/api/v1/file-manager/operations", "key-1",
		`{"operation":"move","path":"/srv/library/a/ep01.mkv","expected_revision":"rev","target":{"path":"/srv/library/b/ep01.mkv","conflict":"version"}}`)
	if recorder.Code != http.StatusAccepted || service.operation.IdempotencyKey != "key-1" || service.operation.Actor != "web" ||
		service.operation.Target == nil || service.operation.Target.Conflict != filemanager.ConflictVersion || service.operation.ExpectedRevision != "rev" {
		t.Fatalf("operation: %d %v %+v", recorder.Code, body, service.operation)
	}
	if body["path"] != "/srv/library/a/ep01.mkv" || body["target_path"] != "/srv/library/b/ep01.mkv" ||
		body["result_path"] != nil || body["state"] != "ready" {
		t.Fatalf("operation view carries the absolute paths verbatim: %v", body)
	}

	recorder, body = serveFileManager(t, service, "/api/v1/file-manager/operations/op-9")
	if recorder.Code != http.StatusOK || body["id"] != "op-9" || body["failure"] != "execute_failed" ||
		body["path"] != "/srv/library/a/ep01.mkv" {
		t.Fatalf("an untyped stored failure reads as a failed effect: %d %v", recorder.Code, body)
	}

	recorder, body = postFileManager(t, service, "/api/v1/file-manager/entry/release", "key-2",
		`{"path":"/srv/library/a/ep01.mkv","expected_revision":"rev"}`)
	if recorder.Code != http.StatusOK || body["released_placements"] != float64(2) || service.release.Actor != "web" || service.release.MoveTo != nil {
		t.Fatalf("release: %d %v", recorder.Code, body)
	}

	recorder, body = postFileManager(t, service, "/api/v1/file-manager/entry/align", "key-3",
		`{"path":"/srv/library/a/ep01.mkv","expected_revision":"rev"}`)
	if recorder.Code != http.StatusAccepted || service.align.IdempotencyKey != "key-3" {
		t.Fatalf("align: %d %v", recorder.Code, body)
	}

	conflict := &fakeFileManager{err: &filemanager.Error{Kind: filemanager.ErrorConflict, Code: filemanager.CodeEntryChanged, Message: "changed"}}
	recorder, body = postFileManager(t, conflict, "/api/v1/file-manager/entry/release", "key-4",
		`{"path":"/srv/library/a/ep01.mkv","expected_revision":"old"}`)
	if recorder.Code != http.StatusConflict || body["code"] != filemanager.CodeEntryChanged {
		t.Fatalf("a changed entry is a typed conflict: %d %v", recorder.Code, body)
	}
}

func TestFileManagerMatchHandlers(t *testing.T) {
	service := &fakeFileManager{}
	recorder, body := postFileManager(t, service, "/api/v1/file-manager/entry/match/preview", "",
		`{"path":"/srv/library/Show/extras/ep.mkv","managed_anime_id":"anime-1","season":1,"episode":3}`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("preview: %d %v", recorder.Code, body)
	}
	if service.matchPreview.ManagedAnimeID != "anime-1" || service.matchPreview.Season != 1 || service.matchPreview.Episode != 3 {
		t.Fatalf("preview request: %+v", service.matchPreview)
	}
	conflict := body["conflict"].(map[string]any)
	blocker := body["blockers"].([]any)[0].(map[string]any)
	if body["target_path"] != "/srv/library/Show/Season 01/Show - E03.mkv" || body["revision"] != "rev-2" || body["from_lineage"] != true ||
		body["directories_to_create"].([]any)[0] != "/srv/library/Show/Season 01" || conflict["occupied_by"] != "file" ||
		conflict["version_path"] != "/srv/library/Show/Season 01/Show - E03 (2).mkv" || blocker["code"] != "facts_missing" ||
		blocker["facts"].([]any)[0] != "media.resolution" || body["subtitles_left_behind"].([]any)[0] != "/srv/library/Show/Extras/ep.ass" {
		t.Fatalf("preview body: %v", body)
	}

	recorder, body = postFileManager(t, service, "/api/v1/file-manager/entry/match", "key-m",
		`{"path":"/srv/library/Show/extras/ep.mkv","expected_revision":"rev-2","managed_anime_id":"anime-1","season":1,"episode":3,"expected_target":"/srv/library/Show/Season 01/Show - E03.mkv","conflict":"version"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("match: %d %v", recorder.Code, body)
	}
	if service.match.IdempotencyKey != "key-m" || service.match.Actor != "web" || service.match.ExpectedRevision != "rev-2" ||
		service.match.ExpectedTarget != "/srv/library/Show/Season 01/Show - E03.mkv" || service.match.Conflict != filemanager.ConflictVersion {
		t.Fatalf("match request: %+v", service.match)
	}
	choice := body["match"].(map[string]any)
	created := body["created_directories"].([]any)
	if body["operation"] != "match" || choice["managed_anime_id"] != "anime-1" || choice["season"] != float64(1) || choice["episode"] != float64(3) ||
		len(created) != 2 || created[0] != "/srv/library/Show/Season 01" || created[1] != "/elsewhere/kept-as-is" ||
		body["path"] != "/srv/library/Show/extras/ep.mkv" || body["result_path"] != "/srv/library/Show/Season 01/Show - E03.mkv" {
		t.Fatalf("match view carries the absolute directories it created: %v", body)
	}

	recorder, body = postFileManager(t, service, "/api/v1/file-manager/operations", "key-x",
		`{"operation":"match","path":"/srv/library/a.mkv","expected_revision":"r"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("a match is not submitted as a plain operation: %d %v", recorder.Code, body)
	}

	recorder, body = serveFileManager(t, service, "/api/v1/anime/anime-1/tmdb/seasons")
	seasons := body["seasons"].([]any)
	if recorder.Code != http.StatusOK || service.seasonsFor != "anime-1" || len(seasons) != 2 ||
		seasons[0].(map[string]any)["season"] != float64(0) || seasons[1].(map[string]any)["air_date"] != "2024-01-01" {
		t.Fatalf("seasons: %d %v", recorder.Code, body)
	}
	recorder, body = serveFileManager(t, service, "/api/v1/anime/anime-1/tmdb/seasons/1/episodes")
	episodes := body["episodes"].([]any)
	if recorder.Code != http.StatusOK || service.episodesSeason != 1 || len(episodes) != 2 || episodes[0].(map[string]any)["title"] != "One" ||
		episodes[1].(map[string]any)["title"] != nil {
		t.Fatalf("episodes: %d %v", recorder.Code, body)
	}

	for _, tc := range []struct {
		err    *filemanager.Error
		status int
	}{
		{&filemanager.Error{Kind: filemanager.ErrorConflict, Code: filemanager.CodeTMDBBindingRequired, Message: "bind"}, http.StatusConflict},
		{&filemanager.Error{Kind: filemanager.ErrorUnavailable, Code: filemanager.CodeMetadataUnavailable, Message: "down"}, http.StatusServiceUnavailable},
	} {
		recorder, body = serveFileManager(t, &fakeFileManager{err: tc.err}, "/api/v1/anime/anime-1/tmdb/seasons")
		if recorder.Code != tc.status || body["code"] != tc.err.Code {
			t.Fatalf("%s: %d %v", tc.err.Code, recorder.Code, body)
		}
	}
}

// The match preview renders and reads only, so the maintenance gate lets it
// through; the match itself is a write.
func TestMatchPreviewStaysAvailableDuringMaintenance(t *testing.T) {
	if !isReadOnlyPath(http.MethodPost, "/api/v1/file-manager/entry/match/preview") {
		t.Fatal("the match preview is a read")
	}
	for _, path := range []string{"/api/v1/file-manager/entry/match", "/api/v1/file-manager/operations", "/api/v1/file-manager/entry/align"} {
		if isReadOnlyPath(http.MethodPost, path) {
			t.Fatalf("%s is a write", path)
		}
	}
}
