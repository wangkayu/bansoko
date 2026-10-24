package httpapi

import (
	"errors"
	"net/http"

	filemaintenance "bansoko/internal/filemaintenance"
	filemainttransport "bansoko/internal/filemaintenance/transport"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/transport/httpapi/generated"
)

// RedownloadPlacement requests a new download of a missing or mismatched
// placement's episode. Filtered, held, present and failed are results; a
// placement that does not admit the exit now is a 409.
func (s *Server) RedownloadPlacement(w http.ResponseWriter, r *http.Request, placementID generated.ID, params generated.RedownloadPlacementParams) {
	result, err := s.deps.ReleaseRetry.RedownloadPlacement(r.Context(), string(placementID), string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, mapPlacementExitError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapReleaseRetryResult(result))
}

// ReleasePlacement ends the management of a missing or mismatched placement.
func (s *Server) ReleasePlacement(w http.ResponseWriter, r *http.Request, placementID generated.ID, params generated.ReleasePlacementParams) {
	result, err := s.deps.FileMaintenance.ReleasePlacementApplication(r.Context(), string(placementID), string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, mapPlacementExitError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, generated.PlacementReleaseResult{
		PlacementId:    generated.ID(result.PlacementID),
		ManagedAnimeId: generated.ID(result.ManagedAnimeID),
		Released:       result.Released,
	})
}

// placementExitDenials explains each PLACEMENT_EXIT_UNAVAILABLE reason; the
// reason itself travels as the detail code.
var placementExitDenials = map[string]string{
	rssapplication.PlacementExitNotActive:      "The file is no longer managed.",
	rssapplication.PlacementExitFileNotMissing: "The latest scan found the file in place.",
	rssapplication.PlacementExitNoSource:       "The release that produced this file is no longer available.",
}

func mapPlacementExitError(err error) error {
	reason := ""
	var rssExit *rssapplication.PlacementExitError
	var maintenanceExit *filemaintenance.PlacementExitError
	switch {
	case errors.As(err, &rssExit):
		reason = rssExit.Reason
	case errors.As(err, &maintenanceExit):
		reason = maintenanceExit.Reason
	default:
		return err
	}
	message, ok := placementExitDenials[reason]
	if !ok {
		message, ok = releaseRetryDenials[reason]
	}
	if !ok {
		message = "The file cannot be handled this way now."
	}
	apiErr := conflict("PLACEMENT_EXIT_UNAVAILABLE", "The file cannot be handled this way now.", nil)
	apiErr.Details = []generated.ErrorDetail{{Code: reason, Message: message}}
	return apiErr
}

// ListPlacementIssues lists one anime's missing or mismatched placements.
func (s *Server) ListPlacementIssues(w http.ResponseWriter, r *http.Request, params generated.ListPlacementIssuesParams) {
	issues, err := s.deps.FileMaintenance.ListPlacementIssuesApplication(r.Context(), string(params.ManagedAnimeId))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.PlacementIssueViews(issues))
}
