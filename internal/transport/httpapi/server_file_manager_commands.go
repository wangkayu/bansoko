package httpapi

import (
	"context"
	"errors"
	"net/http"

	"bansoko/internal/filemanager"
	"bansoko/internal/transport/httpapi/generated"
)

// File manager commands. Every write names the revision of the entry
// it was confirmed on; the service re-derives the entry, refuses a changed
// one with 409 FILE_MANAGER_ENTRY_CHANGED and never overwrites a file.

const fileManagerActor = "web"

func mapFileManagerTarget(target *generated.FileManagerTarget) *filemanager.Target {
	if target == nil {
		return nil
	}
	return &filemanager.Target{Path: target.Path, Conflict: filemanager.ConflictChoice(target.Conflict)}
}

// SubmitFileManagerOperation admits a move, rename, copy or delete of a file.
func (s *Server) SubmitFileManagerOperation(w http.ResponseWriter, r *http.Request, params generated.SubmitFileManagerOperationParams) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	var input generated.FileManagerOperationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	switch input.Operation {
	case generated.FileManagerOperationRequestKindMove, generated.FileManagerOperationRequestKindRename,
		generated.FileManagerOperationRequestKindCopy, generated.FileManagerOperationRequestKindDelete:
	default:
		writeError(w, r, badRequest("REQUEST_INVALID", "The operation is not one of move, rename, copy or delete."))
		return
	}
	op, err := s.deps.FileManager.SubmitOperation(r.Context(), filemanager.OperationRequest{
		IdempotencyKey: string(params.IdempotencyKey), Actor: fileManagerActor,
		Operation: filemanager.UserOperationKind(input.Operation), Path: input.Path,
		ExpectedRevision: input.ExpectedRevision, Target: mapFileManagerTarget(input.Target),
	})
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusAccepted, mapFileManagerOperation(op))
}

// GetFileManagerOperation reads one user file operation.
func (s *Server) GetFileManagerOperation(w http.ResponseWriter, r *http.Request, operationID generated.ID) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	op, err := s.deps.FileManager.Operation(r.Context(), operationID)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, mapFileManagerOperation(op))
}

// ReleaseFileManagerEntry releases one managed video from management.
func (s *Server) ReleaseFileManagerEntry(w http.ResponseWriter, r *http.Request, params generated.ReleaseFileManagerEntryParams) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	var input generated.FileManagerReleaseRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	outcome, err := s.deps.FileManager.Release(r.Context(), filemanager.ReleaseRequest{
		IdempotencyKey: string(params.IdempotencyKey), Actor: fileManagerActor, Path: input.Path,
		ExpectedRevision: input.ExpectedRevision, MoveTo: mapFileManagerTarget(input.MoveTo),
	})
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	result := generated.FileManagerReleaseResult{ManagedAnimeId: outcome.ManagedAnimeID, ReleasedPlacements: outcome.Released}
	if outcome.Operation != nil {
		view := mapFileManagerOperation(*outcome.Operation)
		result.Operation = &view
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, result)
}

// AlignFileManagerEntry aligns one managed video through the alignment chain.
func (s *Server) AlignFileManagerEntry(w http.ResponseWriter, r *http.Request, params generated.AlignFileManagerEntryParams) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	var input generated.FileManagerAlignRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	outcome, err := s.deps.FileManager.Align(r.Context(), filemanager.AlignRequest{
		IdempotencyKey: string(params.IdempotencyKey), Actor: fileManagerActor, Path: input.Path,
		ExpectedRevision: input.ExpectedRevision,
	})
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusAccepted, generated.FileManagerAlignResult{
		ManagedAnimeId: outcome.ManagedAnimeID,
	})
}

// PreviewFileManagerMatch shows what matching a detached video would do.
func (s *Server) PreviewFileManagerMatch(w http.ResponseWriter, r *http.Request) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	var input generated.FileManagerMatchPreviewRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	preview, err := s.previewFileManagerMatch(r.Context(), input)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, preview)
}

func (s *Server) previewFileManagerMatch(ctx context.Context, input generated.FileManagerMatchPreviewRequest) (generated.FileManagerMatchPreview, error) {
	request := filemanager.MatchRequest{Path: input.Path, Season: input.Season, Episode: input.Episode}
	if input.ManagedAnimeId != nil {
		request.ManagedAnimeID = *input.ManagedAnimeId
	}
	if input.MediaKind != nil {
		request.MediaKind = filemanager.MatchMediaKind(*input.MediaKind)
	}
	preview, err := s.deps.FileManager.PreviewMatch(ctx, request)
	if err != nil {
		return generated.FileManagerMatchPreview{}, err
	}
	return mapFileManagerMatchPreview(preview), nil
}

// MatchFileManagerEntry admits one confirmed match of a detached video.
func (s *Server) MatchFileManagerEntry(w http.ResponseWriter, r *http.Request, params generated.MatchFileManagerEntryParams) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	var input generated.FileManagerMatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.matchFileManagerEntry(r.Context(), string(params.IdempotencyKey), input)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusAccepted, view)
}

func (s *Server) matchFileManagerEntry(ctx context.Context, idempotencyKey string, input generated.FileManagerMatchRequest) (generated.FileManagerOperationView, error) {
	request := filemanager.SubmitMatchRequest{
		IdempotencyKey: idempotencyKey, Actor: fileManagerActor, Path: input.Path,
		ExpectedRevision: input.ExpectedRevision, ManagedAnimeID: input.ManagedAnimeId, Season: input.Season, Episode: input.Episode,
		ExpectedTarget: input.ExpectedTarget, Conflict: filemanager.ConflictChoice(input.Conflict),
	}
	if input.MediaKind != nil {
		request.MediaKind = filemanager.MatchMediaKind(*input.MediaKind)
	}
	op, err := s.deps.FileManager.SubmitMatch(ctx, request)
	if err != nil {
		return generated.FileManagerOperationView{}, err
	}
	return mapFileManagerOperation(op), nil
}

func mapFileManagerMatchPreview(preview filemanager.MatchPreview) generated.FileManagerMatchPreview {
	anime := generated.FileManagerAnimeRef{ManagedAnimeId: preview.Anime.ID, Title: preview.Anime.Title}
	if preview.Anime.SubscriptionState != "" {
		state := generated.SubscriptionState(preview.Anime.SubscriptionState)
		anime.SubscriptionState = &state
	}
	mapped := generated.FileManagerMatchPreview{
		Revision: preview.Revision, Anime: anime, MediaKind: generated.FileManagerMatchMediaKind(preview.MediaKind),
		Season: preview.Season, Episode: preview.Episode, FromLineage: preview.FromLineage,
		DirectoriesToCreate: append([]string{}, preview.DirectoriesToCreate...),
		SubtitlesLeftBehind: append([]string{}, preview.SubtitlesLeftBehind...),
		Blockers:            make([]generated.FileManagerMatchBlocker, 0, len(preview.Blockers)),
	}
	if preview.TargetPath != "" {
		target := preview.TargetPath
		mapped.TargetPath = &target
	}
	if preview.Conflict != nil {
		conflict := generated.FileManagerMatchConflict{OccupiedBy: generated.FileManagerMatchConflictOccupiedBy(preview.Conflict.OccupiedBy)}
		if preview.Conflict.VersionPath != "" {
			version := preview.Conflict.VersionPath
			conflict.VersionPath = &version
		}
		mapped.Conflict = &conflict
	}
	for _, blocker := range preview.Blockers {
		item := generated.FileManagerMatchBlocker{Code: generated.FileManagerMatchBlockerCode(blocker.Code)}
		if len(blocker.Facts) > 0 {
			facts := append([]string{}, blocker.Facts...)
			item.Facts = &facts
		}
		mapped.Blockers = append(mapped.Blockers, item)
	}
	return mapped
}

// ListAnimeTMDBSeasons lists the seasons of the TMDB show an anime is bound to.
func (s *Server) ListAnimeTMDBSeasons(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	result, err := s.listAnimeTMDBSeasons(r.Context(), managedAnimeID)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, result)
}

func (s *Server) listAnimeTMDBSeasons(ctx context.Context, managedAnimeID string) (generated.TMDBSeasonList, error) {
	seasons, err := s.deps.FileManager.TMDBSeasons(ctx, managedAnimeID)
	if err != nil {
		return generated.TMDBSeasonList{}, err
	}
	result := generated.TMDBSeasonList{Seasons: make([]generated.TMDBSeason, 0, len(seasons))}
	for _, season := range seasons {
		mapped := generated.TMDBSeason{Season: season.Number, EpisodeCount: season.EpisodeCount}
		if season.Name != "" {
			name := season.Name
			mapped.Name = &name
		}
		if season.AirDate != "" {
			date := season.AirDate
			mapped.AirDate = &date
		}
		result.Seasons = append(result.Seasons, mapped)
	}
	return result, nil
}

// ListAnimeTMDBSeasonEpisodes lists the episodes of one season of that show.
func (s *Server) ListAnimeTMDBSeasonEpisodes(w http.ResponseWriter, r *http.Request, managedAnimeID generated.ManagedAnimeID, season int) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	result, err := s.listAnimeTMDBSeasonEpisodes(r.Context(), managedAnimeID, season)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, result)
}

func (s *Server) listAnimeTMDBSeasonEpisodes(ctx context.Context, managedAnimeID string, season int) (generated.TMDBSeasonEpisodeList, error) {
	episodes, err := s.deps.FileManager.TMDBSeasonEpisodes(ctx, managedAnimeID, season)
	if err != nil {
		return generated.TMDBSeasonEpisodeList{}, err
	}
	result := generated.TMDBSeasonEpisodeList{Season: season, Episodes: make([]generated.TMDBEpisode, 0, len(episodes))}
	for _, episode := range episodes {
		mapped := generated.TMDBEpisode{Episode: episode.Number}
		if episode.Title != "" {
			title := episode.Title
			mapped.Title = &title
		}
		if episode.AirDate != "" {
			date := episode.AirDate
			mapped.AirDate = &date
		}
		result.Episodes = append(result.Episodes, mapped)
	}
	return result, nil
}

// fileManagerFailures are the typed failures the view exposes; any other
// stored code reads as a failed effect.
var fileManagerFailures = map[string]bool{
	filemanager.FailureTargetOccupied: true, filemanager.FailureSourceChanged: true, filemanager.FailureSourceMissing: true,
	filemanager.FailureSourceManaged: true, filemanager.FailureExecuteFailed: true,
	filemanager.FailureAnimeUnavailable: true,
}

// mapFileManagerOperation maps one stored operation view. Every path it
// carries is the absolute path the operation was frozen on.
func mapFileManagerOperation(op filemanager.UserOperation) generated.FileManagerOperationView {
	view := generated.FileManagerOperationView{
		Id: op.ID, Operation: generated.FileManagerUserOperation(op.Operation), State: generated.FileManagerOperationState(op.State),
		Path: op.SourcePath, CreatedAt: op.CreatedAt, CompletedAt: op.CompletedAt,
	}
	if op.TargetPath != "" {
		target := op.TargetPath
		view.TargetPath = &target
	}
	if op.ResultPath != "" {
		result := op.ResultPath
		view.ResultPath = &result
	}
	if op.Match != nil {
		view.Match = &generated.FileManagerMatchChoice{ManagedAnimeId: op.Match.ManagedAnimeID, Season: op.Match.Season, Episode: op.Match.Episode}
		if len(op.CreatedDirs) > 0 {
			view.CreatedDirectories = &op.CreatedDirs
		}
	}
	if op.State == filemanager.UserOperationBlocked || op.State == filemanager.UserOperationFailed {
		code := op.Failure
		if !fileManagerFailures[code] {
			code = filemanager.FailureExecuteFailed
		}
		failure := generated.FileManagerOperationFailure(code)
		view.Failure = &failure
	}
	return view
}
