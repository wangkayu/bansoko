package httpapi

import (
	"net/http"

	candidatefactstransport "bansoko/internal/release/facts/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// GetReleaseCandidateFacts returns the versioned facts for one release
// (latest by default). Read-only; never re-parses on read.
func (s *Server) GetReleaseCandidateFacts(w http.ResponseWriter, r *http.Request, releaseID generated.ReleaseID, params generated.GetReleaseCandidateFactsParams) {
	var version *int
	if params.Version != nil {
		v := *params.Version
		version = &v
	}
	result, err := s.deps.CandidateFacts.GetFacts(r.Context(), string(releaseID), version)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, candidatefactstransport.View(result))
}
