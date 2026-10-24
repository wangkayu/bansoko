package httpapi

import (
	"errors"
	"net/http"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

// ListAnimeFiles returns one anime's managed files with the latest scan
// result and the actions available per row.
func (s *Server) ListAnimeFiles(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID) {
	view, err := s.deps.FileMaintenance.ListAnimeFilesApplication(r.Context(), string(animeID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapAnimeFilesView(view))
}

// AlignAnimeFiles admits a one-off alignment of the anime's misaligned files.
func (s *Server) AlignAnimeFiles(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID) {
	var input generated.AnimeFilesAlignInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	if input.Generation < 1 {
		writeError(w, r, badRequest("VALIDATION_FAILED", "generation must be at least 1"))
		return
	}
	var placementIDs []string
	if input.PlacementIds != nil {
		for _, id := range *input.PlacementIds {
			placementIDs = append(placementIDs, string(id))
		}
	}
	var result filemaintenance.AnimeFilesAlignResult
	var err error
	// The 对齐（刷新元数据）variant re-pulls the bound works bypassing the
	// positive caches before the alignment is admitted; the plain action
	// stays cache-first.
	if input.RefreshMetadata != nil && *input.RefreshMetadata {
		result, err = s.deps.FileMaintenance.AlignAnimeFilesWithRefreshApplication(r.Context(), string(animeID), input.Generation, placementIDs)
	} else {
		result, err = s.deps.FileMaintenance.AlignAnimeFilesApplication(r.Context(), string(animeID), input.Generation, placementIDs)
	}
	if err != nil {
		writeError(w, r, mapAnimeFilesAlignError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusAccepted, generated.AnimeFilesAlignResult{
		Generation: result.Generation,
		Admitted:   result.Admitted,
	})
}

// animeFilesAlignDenials explains each ANIME_FILES_ALIGN_UNAVAILABLE reason;
// the reason itself travels as the detail code.
var animeFilesAlignDenials = map[string]string{
	filemaintenance.AnimeFilesReasonOwnerRetired: "The anime no longer admits changes.",
	filemaintenance.AnimeFilesReasonNotStarted:   "The subscription has not been started.",
	filemaintenance.AnimeFilesReasonScanRunning:  "A file scan is still running.",
	filemaintenance.AnimeFilesReasonScanRequired: "Scan the files first.",
	filemaintenance.AnimeFilesReasonScanChanged:  "A newer scan exists; read the files again.",
}

func mapAnimeFilesAlignError(err error) error {
	var denial *filemaintenance.AnimeFilesAlignError
	if !errors.As(err, &denial) {
		return err
	}
	message, ok := animeFilesAlignDenials[denial.Reason]
	if !ok {
		message = "The files cannot be aligned now."
	}
	apiErr := conflict("ANIME_FILES_ALIGN_UNAVAILABLE", "The files cannot be aligned now.", nil)
	apiErr.Details = []generated.ErrorDetail{{Code: denial.Reason, Message: message}}
	return apiErr
}

func mapAnimeFileAction(action filemaintenance.AnimeFileAction) generated.AnimeFileAction {
	mapped := generated.AnimeFileAction{Available: action.Available}
	if action.ReasonCode != "" {
		reason := action.ReasonCode
		mapped.ReasonCode = &reason
	}
	return mapped
}

func mapAnimeFileAlignment(view *filemaintenance.AnimeFileAlignmentView) *generated.AnimeFileAlignment {
	if view == nil {
		return nil
	}
	mapped := &generated.AnimeFileAlignment{State: generated.AnimeFileAlignmentState(view.State)}
	if view.ReasonCode != "" {
		reason := view.ReasonCode
		mapped.ReasonCode = &reason
	}
	return mapped
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func mapAnimeFilesView(view filemaintenance.AnimeFilesView) generated.AnimeFilesView {
	result := generated.AnimeFilesView{
		ManagedAnimeId: generated.ID(view.ManagedAnimeID),
		Root:           view.Root,
		Aligning:       view.Aligning,
		ScanAction:     mapAnimeFileAction(view.ScanAction),
		AlignAllAction: mapAnimeFileAction(view.AlignAllAction),
		Scan: generated.AnimeFilesScan{
			State:          generated.AnimeFilesScanState(view.Scan.State),
			Generation:     view.Scan.Generation,
			ScannedAt:      view.Scan.ScannedAt,
			FailureCode:    view.Scan.FailureCode,
			RescanRequired: view.Scan.RescanRequired,
		},
		Files: make([]generated.AnimeFile, 0, len(view.Files)),
	}
	for _, file := range view.Files {
		row := generated.AnimeFile{
			PlacementId:        generated.ID(file.PlacementID),
			MediaKind:          generated.AnimeFileMediaKind(file.MediaKind),
			Season:             file.Season,
			Episodes:           append([]string{}, file.Episodes...),
			Path:               file.Path,
			RelativePath:       file.RelativePath,
			SizeBytes:          file.SizeBytes,
			Status:             generated.AnimeFileStatus(file.Status),
			StatusReason:       optionalString(file.StatusReason),
			GroupName:          optionalString(file.GroupName),
			ReleaseTitle:       optionalString(file.ReleaseTitle),
			TargetPath:         file.TargetPath,
			TargetRelativePath: file.TargetRelativePath,
			Alignment:          mapAnimeFileAlignment(file.Alignment),
			Actions: generated.AnimeFileActions{
				Align:   mapAnimeFileAction(file.Actions.Align),
				Rerun:   mapAnimeFileAction(file.Actions.Rerun),
				Release: mapAnimeFileAction(file.Actions.Release),
			},
			Companions: make([]generated.AnimeFileCompanion, 0, len(file.Companions)),
		}
		if file.ReleaseID != "" {
			id := generated.ID(file.ReleaseID)
			row.ReleaseId = &id
		}
		for _, companion := range file.Companions {
			row.Companions = append(row.Companions, generated.AnimeFileCompanion{
				Id:                 generated.ID(companion.ID),
				MediaKind:          generated.AnimeFileCompanionMediaKind(companion.MediaKind),
				Path:               companion.Path,
				RelativePath:       companion.RelativePath,
				SizeBytes:          companion.SizeBytes,
				Status:             generated.AnimeFileCompanionStatus(companion.Status),
				TargetPath:         companion.TargetPath,
				TargetRelativePath: companion.TargetRelativePath,
				Alignment:          mapAnimeFileAlignment(companion.Alignment),
			})
		}
		result.Files = append(result.Files, row)
	}
	return result
}
