package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"bansoko/internal/filemanager"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/transport/httpapi/generated"
)

// File manager recognition: the preview renders the selected files' targets
// side-effect-free, and the submit confirms that plan. Both read the same
// naming rules and optional match; the submit additionally names the digest
// the preview returned and how it settles occupied targets.

// PreviewFileManagerRecognition renders what recognizing the selected files
// would name them.
func (s *Server) PreviewFileManagerRecognition(w http.ResponseWriter, r *http.Request) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	var input generated.FileManagerRecognitionRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	preview, err := s.previewFileManagerRecognition(r.Context(), input)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, preview)
}

func (s *Server) previewFileManagerRecognition(ctx context.Context, input generated.FileManagerRecognitionRequest) (generated.FileManagerRecognitionPreview, error) {
	request, err := mapFileManagerRecognitionRequest(input.Paths, input.Naming, input.Parser, input.Match)
	if err != nil {
		return generated.FileManagerRecognitionPreview{}, err
	}
	preview, err := s.deps.FileManager.PreviewRecognition(ctx, request)
	if err != nil {
		return generated.FileManagerRecognitionPreview{}, err
	}
	return mapFileManagerRecognitionPreview(preview), nil
}

// SubmitFileManagerRecognition admits the previewed recognition as ordinary
// rename and move operations.
func (s *Server) SubmitFileManagerRecognition(w http.ResponseWriter, r *http.Request, params generated.SubmitFileManagerRecognitionParams) {
	if s.deps.FileManager == nil {
		writeError(w, r, errors.New("file manager service is not configured"))
		return
	}
	var input generated.FileManagerRecognitionSubmitRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.submitFileManagerRecognition(r.Context(), string(params.IdempotencyKey), input)
	if err != nil {
		writeFileManagerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusAccepted, result)
}

func (s *Server) submitFileManagerRecognition(ctx context.Context, idempotencyKey string, input generated.FileManagerRecognitionSubmitRequest) (generated.FileManagerRecognitionResult, error) {
	request, err := mapFileManagerRecognitionRequest(input.Paths, input.Naming, input.Parser, input.Match)
	if err != nil {
		return generated.FileManagerRecognitionResult{}, err
	}
	outcome, err := s.deps.FileManager.SubmitRecognition(ctx, filemanager.SubmitRecognitionRequest{
		IdempotencyKey: idempotencyKey, Actor: fileManagerActor, RecognitionRequest: request,
		ExpectedPlanDigest: input.ExpectedPlanDigest, Conflict: filemanager.RecognitionConflictChoice(input.Conflict),
	})
	if err != nil {
		return generated.FileManagerRecognitionResult{}, err
	}
	mapped := generated.FileManagerRecognitionResult{
		Renamed: outcome.Renamed, Moved: outcome.Moved,
		Operations: make([]generated.FileManagerOperationView, 0, len(outcome.Operations)),
	}
	for _, operation := range outcome.Operations {
		mapped.Operations = append(mapped.Operations, mapFileManagerOperation(operation))
	}
	return mapped, nil
}

// mapFileManagerRecognitionRequest maps the shared request fields of the
// preview and the submit. The naming rules are the operation's own, exactly the
// value one-shot renders with. A parser config that is not an object is
// refused.
func mapFileManagerRecognitionRequest(paths []string, rules generated.NamingRules, parser *generated.FileManagerRecognitionParser, match *generated.FileManagerRecognitionMatch) (filemanager.RecognitionRequest, error) {
	request := filemanager.RecognitionRequest{
		Paths:  append([]string{}, paths...),
		Naming: mapFileManagerNamingRules(rules),
	}
	if parser != nil {
		if parser.Id != nil {
			request.Parser.ID = string(*parser.Id)
		}
		if parser.Config != nil {
			config, err := json.Marshal(*parser.Config)
			if err != nil {
				return filemanager.RecognitionRequest{}, badRequest("REQUEST_INVALID", "The parser config is not a JSON object.")
			}
			request.Parser.Config = config
		}
	}
	if match != nil {
		request.Match = &filemanager.RecognitionMatch{
			Season: match.Season, Episode: match.Episode,
			Anime: mapFileManagerRecognitionAnimeDraft(match.Anime),
		}
		if match.ManagedAnimeId != nil {
			request.Match.ManagedAnimeID = string(*match.ManagedAnimeId)
		}
		if match.Offset != nil {
			request.Match.Offset = *match.Offset
		}
	}
	return request, nil
}

// mapFileManagerNamingRules maps the API's naming rules to the naming core's
// own value.
func mapFileManagerNamingRules(rules generated.NamingRules) naming.NamingRules {
	mapped := naming.NamingRules{
		TV: naming.NamingVariant{
			DirectoryTemplate:     rules.Tv.DirectoryTemplate,
			VideoFilenameTemplate: rules.Tv.VideoFilenameTemplate,
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     rules.Movie.DirectoryTemplate,
			VideoFilenameTemplate: rules.Movie.VideoFilenameTemplate,
		},
		Unknown: naming.NamingVariant{
			DirectoryTemplate:     rules.Unknown.DirectoryTemplate,
			VideoFilenameTemplate: rules.Unknown.VideoFilenameTemplate,
		},
		SubtitleSuffixTemplate: rules.SubtitleSuffixTemplate,
		Language:               string(rules.Language),
	}
	if rules.Preset != nil {
		mapped.Preset = *rules.Preset
	}
	return mapped
}

// mapFileManagerRecognitionAnimeDraft maps the operation's temporary draft.
// null keeps the anime's stored rows.
func mapFileManagerRecognitionAnimeDraft(draft *generated.FileManagerRecognitionAnimeDraft) *filemanager.RecognitionAnimeDraft {
	if draft == nil {
		return nil
	}
	mapped := &filemanager.RecognitionAnimeDraft{
		Title: draft.Title, MediaKind: string(draft.MediaKind), Year: draft.Year,
	}
	if draft.Bgm != nil {
		mapped.BGM = &filemanager.RecognitionAnimeBinding{WorkID: draft.Bgm.WorkId}
	}
	if draft.Tmdb != nil {
		mapped.TMDB = &filemanager.RecognitionTMDBBinding{
			WorkID: draft.Tmdb.WorkId, MediaKind: string(draft.Tmdb.MediaKind), Season: draft.Tmdb.Season,
		}
	}
	return mapped
}

func mapFileManagerRecognitionPreview(preview filemanager.RecognitionPreview) generated.FileManagerRecognitionPreview {
	mapped := generated.FileManagerRecognitionPreview{
		Directory: preview.Directory, UnknownRoute: generated.FileManagerRecognitionUnknownRoute(preview.UnknownRoute),
		PlanDigest: preview.PlanDigest, Conflicts: append([]string{}, preview.Conflicts...),
		Rows: make([]generated.FileManagerRecognitionRow, 0, len(preview.Rows)),
	}
	if preview.TitleHint != "" {
		hint := preview.TitleHint
		mapped.TitleHint = &hint
	}
	for _, row := range preview.Rows {
		mapped.Rows = append(mapped.Rows, mapFileManagerRecognitionRow(row))
	}
	return mapped
}

func mapFileManagerRecognitionRow(row filemanager.RecognitionRow) generated.FileManagerRecognitionRow {
	mapped := generated.FileManagerRecognitionRow{
		Path: row.Path, Name: row.Name, Kind: generated.FileManagerRecognitionRowKind(row.Kind),
		Group: row.Group, Revision: row.Revision, Disposition: generated.FileManagerRecognitionDisposition(row.Disposition),
	}
	if row.Episode != nil {
		episode := generated.FileManagerRecognitionEpisode{Number: row.Episode.Number, Offset: row.Episode.Offset, Source: row.Episode.Source}
		if row.Episode.Raw != "" {
			raw := row.Episode.Raw
			episode.Raw = &raw
		}
		if row.Episode.NumberEnd != "" {
			end := row.Episode.NumberEnd
			episode.NumberEnd = &end
		}
		mapped.Episode = &episode
	}
	if row.TargetPath != "" {
		target := row.TargetPath
		mapped.TargetPath = &target
	}
	if row.TargetName != "" {
		name := row.TargetName
		mapped.TargetName = &name
	}
	if len(row.Blockers) > 0 {
		blockers := make([]generated.FileManagerRecognitionBlocker, 0, len(row.Blockers))
		for _, blocker := range row.Blockers {
			item := generated.FileManagerRecognitionBlocker{Code: generated.FileManagerRecognitionBlockerCode(blocker.Code)}
			if len(blocker.Facts) > 0 {
				facts := append([]string{}, blocker.Facts...)
				item.Facts = &facts
			}
			blockers = append(blockers, item)
		}
		mapped.Blockers = &blockers
	}
	if row.Conflict != nil {
		conflict := generated.FileManagerRecognitionConflict{OccupiedBy: generated.FileManagerRecognitionConflictOccupiedBy(row.Conflict.OccupiedBy)}
		if row.Conflict.VersionPath != "" {
			version := row.Conflict.VersionPath
			conflict.VersionPath = &version
		}
		mapped.Conflict = &conflict
	}
	return mapped
}
