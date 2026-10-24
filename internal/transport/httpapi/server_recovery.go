package httpapi

import (
	"net/http"

	"bansoko/internal/transport/httpapi/generated"
)

// manualRetry runs one owner-specific recovery action. The HTTP idempotency
// key deduplicates the request only; it never becomes the WorkItem effect key.
func (s *Server) manualRetry(w http.ResponseWriter, r *http.Request, ownerType, ownerID, idempotencyKey string) {
	var input generated.ManualRetryInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.deps.ManualRetry.ManualRetry(r.Context(), ownerType, ownerID, input.FailedWorkItemId, input.Reason, idempotencyKey); err != nil {
		writeError(w, r, err)
		return
	}
	accepted := generated.ManualRetryResultAccepted(true)
	writeJSON(w, r.Context(), http.StatusAccepted, generated.ManualRetryResult{
		OwnerType: generated.ManualRetryResultOwnerType(ownerType),
		OwnerId:   ownerID,
		Accepted:  accepted,
	})
}

func (s *Server) RetryRSSSourceRuntime(w http.ResponseWriter, r *http.Request, rssSourceID generated.RSSSourceID, params generated.RetryRSSSourceRuntimeParams) {
	s.manualRetry(w, r, "rss_source_runtime", rssSourceID, string(params.IdempotencyKey))
}

func (s *Server) RetryDownloadTask(w http.ResponseWriter, r *http.Request, downloadTaskID generated.DownloadTaskID, params generated.RetryDownloadTaskParams) {
	s.manualRetry(w, r, "download_task", downloadTaskID, string(params.IdempotencyKey))
}

func (s *Server) RetryArtifact(w http.ResponseWriter, r *http.Request, artifactID generated.ArtifactID, params generated.RetryArtifactParams) {
	s.manualRetry(w, r, "artifact", artifactID, string(params.IdempotencyKey))
}
