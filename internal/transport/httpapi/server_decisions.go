package httpapi

import (
	"net/http"

	filemainttransport "bansoko/internal/filemaintenance/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// ListReplacementDecisions returns persisted decisions (side-effect free).
func (s *Server) ListReplacementDecisions(w http.ResponseWriter, r *http.Request, params generated.ListReplacementDecisionsParams) {
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
	views, err := s.deps.FileMaintenance.ListDecisionViews(r.Context(), animeID, limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapReplacementDecisionViews(views))
}

// EvaluateReplacement persists the idempotent per-key replacement decisions.
func (s *Server) EvaluateReplacement(w http.ResponseWriter, r *http.Request) {
	var input generated.ReplacementEvaluateInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	views, err := s.deps.FileMaintenance.EvaluateDecisionApplication(r.Context(), filemainttransport.ReplacementEvaluateInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.ReplacementDecisionViews(views))
}
