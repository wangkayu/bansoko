package httpapi

import (
	"context"
	"net/http"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/transport/httpapi/generated"
)

// ImportRetryService retries the import (file execution) stage of one settled
// acquisition: every failed file effect runs again and every failed placement
// handoff is reopened, rebuilt from durable owner state.
type ImportRetryService interface {
	RetryImport(ctx context.Context, acquisitionID string, expectedVersion int64, idempotencyKey string) (animeapplication.ImportRetryResult, error)
}

// SetImportRetry wires the import-stage retry command. It is set once at
// composition, before the server starts serving.
func (s *Server) SetImportRetry(service ImportRetryService) {
	if s != nil {
		s.deps.ImportRetry = service
	}
}

// RetryAcquisitionImport retries the import stage of one execution record.
// If-Match carries the record version the caller saw; the Idempotency-Key
// replays an accepted request.
func (s *Server) RetryAcquisitionImport(w http.ResponseWriter, r *http.Request, acquisitionId generated.AcquisitionID, params generated.RetryAcquisitionImportParams) {
	if s.deps.ImportRetry == nil {
		writeError(w, r, serviceNotConfigured("import retry"))
		return
	}
	result, err := s.deps.ImportRetry.RetryImport(r.Context(), string(acquisitionId), int64(params.IfMatch), string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusAccepted, generated.AcquisitionImportRetryResult{
		AcquisitionId:      generated.ID(result.AcquisitionID),
		Attempt:            result.Attempt,
		RetriedFiles:       result.RetriedFiles,
		ReopenedPlacements: result.ReopenedPlacements,
		Version:            result.Version,
	})
}
