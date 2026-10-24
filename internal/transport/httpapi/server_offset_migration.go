package httpapi

import (
	"net/http"

	"bansoko/internal/transport/httpapi/generated"
)

// ListOffsetMigrations returns the frozen offset-correction migration plans
// of one anime (side-effect free).
func (s *Server) ListOffsetMigrations(w http.ResponseWriter, r *http.Request, params generated.ListOffsetMigrationsParams) {
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
	var policyVersion *int64
	if params.PolicyVersion != nil {
		value := int64(*params.PolicyVersion)
		policyVersion = &value
	}
	views, err := s.deps.FileMaintenance.ListOffsetMigrationViews(r.Context(), animeID, policyVersion, limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapOffsetMigrationPlanViews(views))
}

// ListOffsetMigrationGenerations returns the durable offset migration
// generations of one anime (side-effect free). A generation is the owner
// truth of one effective offset change: its state and aggregate counts expose
// pending/planning/ready/completed/superseded/failed progress.
func (s *Server) ListOffsetMigrationGenerations(w http.ResponseWriter, r *http.Request, params generated.ListOffsetMigrationGenerationsParams) {
	animeID := ""
	if params.ManagedAnimeId != nil {
		animeID = *params.ManagedAnimeId
	}
	limit, offset := 100, 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	if params.Offset != nil {
		offset = *params.Offset
	}
	views, err := s.deps.FileMaintenance.ListOffsetMigrationGenerationViews(r.Context(), animeID, limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapOffsetMigrationGenerationViews(views))
}
