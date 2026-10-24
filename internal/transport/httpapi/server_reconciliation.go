package httpapi

import (
	"net/http"

	filemainttransport "bansoko/internal/filemaintenance/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// SubmitReconciliationScan starts (or idempotently replays) an asynchronous
// snapshot scan for one anime. The provider never produces filesystem
// effects; it only observes.
func (s *Server) SubmitReconciliationScan(w http.ResponseWriter, r *http.Request) {
	var input generated.ReconciliationScanSubmitInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.deps.Reconciliation.SubmitScan(r.Context(), mapReconciliationScanSubmitInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := mapReconciliationScanView(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}

// GetReconciliationScan returns the current scan owner view.
func (s *Server) GetReconciliationScan(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID) {
	result, err := s.deps.Reconciliation.GetScan(r.Context(), string(animeID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := mapReconciliationScanView(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}

// ListReconciliationObservations pages the immutable observations of one
// generation.
func (s *Server) ListReconciliationObservations(w http.ResponseWriter, r *http.Request, animeID generated.ManagedAnimeID, params generated.ListReconciliationObservationsParams) {
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	offset := 0
	if params.Offset != nil {
		offset = *params.Offset
	}
	result, err := s.deps.Reconciliation.ListObservations(r.Context(), string(animeID), int64(params.Generation), limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := mapReconciliationObservationPage(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, page)
}

// ListReconciliationPathDiffs returns the side-effect free path-difference
// preview of one calibration generation (0 = latest).
func (s *Server) ListReconciliationPathDiffs(w http.ResponseWriter, r *http.Request, managedAnimeId generated.ManagedAnimeID, params generated.ListReconciliationPathDiffsParams) {
	limit, offset := 100, 0
	generation := int64(0)
	if params.Generation != nil {
		generation = int64(*params.Generation)
	}
	if params.Limit != nil {
		limit = *params.Limit
	}
	if params.Offset != nil {
		offset = *params.Offset
	}
	views, err := s.deps.FileMaintenance.ListReconciliationPathDiffs(r.Context(), string(managedAnimeId), generation, limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, filemainttransport.PathDiffViews(views))
}
