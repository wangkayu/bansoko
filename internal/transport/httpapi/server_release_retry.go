package httpapi

import (
	"errors"
	"net/http"

	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/transport/httpapi/generated"
)

// RetryRelease runs one Release through normal acceptance again. Filtered,
// held, present and failed are results; only a Release that may not be
// retried now is an error.
func (s *Server) RetryRelease(w http.ResponseWriter, r *http.Request, releaseID generated.ReleaseID, params generated.RetryReleaseParams) {
	result, err := s.deps.ReleaseRetry.RetryRelease(r.Context(), string(releaseID), string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, mapReleaseRetryError(err))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapReleaseRetryResult(result))
}

func mapReleaseRetryResult(result rssapplication.ReleaseRetryResult) generated.ReleaseRetryResult {
	mapped := generated.ReleaseRetryResult{
		ReleaseId: generated.ID(result.ReleaseID),
		Verdict:   result.Verdict,
		Outcome:   generated.ReleaseRetryResultOutcome(result.Outcome),
	}
	if result.ReasonCode != "" {
		reason := result.ReasonCode
		mapped.ReasonCode = &reason
	}
	if result.DownloadTaskID != "" {
		task := generated.ID(result.DownloadTaskID)
		mapped.DownloadTaskId = &task
	}
	return mapped
}

// releaseRetryDenials explains each RELEASE_NOT_RETRYABLE reason; the reason
// itself travels as the detail code.
var releaseRetryDenials = map[string]string{
	rssapplication.RetryReasonOwnerRetired:         "The anime is no longer subscribed.",
	rssapplication.RetryReasonAlreadyPresent:       "The files are already in the library.",
	rssapplication.RetryReasonDownloadInProgress:   "The download is still in progress.",
	rssapplication.RetryReasonProcessingInProgress: "The downloaded files are still being processed.",
	rssapplication.RetryReasonAlreadyPlaced:        "The files are already in the library.",
	rssapplication.RetryReasonPresentAtResume:      "The item was already in the feed when the subscription resumed.",
	rssapplication.RetryReasonNotStarted:           "The subscription has not been started.",
}

func mapReleaseRetryError(err error) error {
	var notRetryable *rssapplication.ReleaseNotRetryableError
	if !errors.As(err, &notRetryable) {
		return err
	}
	message, ok := releaseRetryDenials[notRetryable.Reason]
	if !ok {
		message = "The release cannot be retried now."
	}
	apiErr := conflict("RELEASE_NOT_RETRYABLE", "The release cannot be retried now.", nil)
	apiErr.Details = []generated.ErrorDetail{{Code: notRetryable.Reason, Message: message}}
	return apiErr
}
