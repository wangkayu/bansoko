package httpapi

import (
	"context"
	"net/http"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/transport/httpapi/generated"
)

// DownloadCancelService cancels the download of one running execution record
// before import; the download worker carries the cancel out.
type DownloadCancelService interface {
	CancelDownload(ctx context.Context, acquisitionID string, expectedVersion int64, idempotencyKey string) (animeapplication.DownloadCancelResult, error)
}

// SetDownloadCancel wires the download cancel command. It is set once at
// composition, before the server starts serving.
func (s *Server) SetDownloadCancel(service DownloadCancelService) {
	if s != nil {
		s.deps.DownloadCancel = service
	}
}

// CancelAcquisitionDownload cancels the download of one execution record.
// If-Match carries the record version the caller saw; the Idempotency-Key
// replays an accepted request.
func (s *Server) CancelAcquisitionDownload(w http.ResponseWriter, r *http.Request, acquisitionId generated.AcquisitionID, params generated.CancelAcquisitionDownloadParams) {
	if s.deps.DownloadCancel == nil {
		writeError(w, r, serviceNotConfigured("download cancel"))
		return
	}
	result, err := s.deps.DownloadCancel.CancelDownload(r.Context(), string(acquisitionId), int64(params.IfMatch), string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusAccepted, generated.AcquisitionDownloadCancelResult{
		AcquisitionId: generated.ID(result.AcquisitionID),
		Version:       result.Version,
	})
}
