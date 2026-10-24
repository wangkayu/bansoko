package httpapi

import (
	"net/http"

	"bansoko/internal/transport/httpapi/generated"
)

// ListPlacements returns verified placements (side-effect free).
func (s *Server) ListPlacements(w http.ResponseWriter, r *http.Request, params generated.ListPlacementsParams) {
	animeID := ""
	if params.ManagedAnimeId != nil {
		animeID = *params.ManagedAnimeId
	}
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	offset := 0
	if params.Offset != nil {
		offset = *params.Offset
	}
	views, err := s.deps.FileMaintenance.ListPlacementViews(r.Context(), animeID, limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapPlacementViews(views))
}

// GetPlacementCoverage returns the full coverage set of one placement.
func (s *Server) GetPlacementCoverage(w http.ResponseWriter, r *http.Request, placementId generated.ID) {
	views, err := s.deps.FileMaintenance.GetPlacementCoverageViews(r.Context(), placementId)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapPlacementCoverageViews(views))
}
