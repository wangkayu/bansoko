package application

import (
	"context"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Call only for a stage result applied by the download owner. An already
// terminal task or a superseded attempt is not a new download result.
func (s *Service) withDownloadResultLog(c workitems.Claim, out worker.Outcome, operation, result, message string) worker.Outcome {
	if s.logger == nil || c.ID == "" {
		return out
	}
	event := observabilityport.RuntimeEvent{
		Message: message, Component: "download", Operation: operation, Outcome: result,
		ReasonCode: out.OwnerResult.ReasonCode, StateAfter: out.OwnerResult.State,
		OwnerType: c.Owner.Type, OwnerID: c.Owner.ID, WorkItemID: c.ID, Kind: c.Kind, Attempt: c.Attempt,
	}
	return worker.AppendAfterCommit(out, func(ctx context.Context) {
		if result == "failure" {
			s.logger.Warn(ctx, event)
		} else {
			s.logger.Info(ctx, event)
		}
	})
}

func (s *Service) withDownloadTerminalLog(c workitems.Claim, out worker.Outcome, operation string) worker.Outcome {
	switch out.OwnerResult.State {
	case "completed":
		return s.withDownloadResultLog(c, out, operation, "success", "Download completed in qBittorrent.")
	case "failed":
		result, message := "failure", "qBittorrent download failed."
		switch out.OwnerResult.ReasonCode {
		case FailureDownloaderTaskRemoved:
			result, message = "cancelled", "Download was cancelled because the task was removed from qBittorrent."
		case FailureUserCancelled:
			result, message = "cancelled", "Download was cancelled by the user."
		case FailureProviderGroupNotAllowed:
			result, message = "cancelled", "Download was cancelled because its source group is no longer allowed."
		case "DOWNLOAD_STALLED":
			message = "Download failed because no progress was made before the timeout."
		case "DOWNLOAD_PROGRESS_UNVERIFIABLE", "DOWNLOAD_OBSERVATION_UNAVAILABLE":
			message = "Download failed because progress could not be verified before the timeout."
		case "SYNC_BUDGET_EXHAUSTED":
			message = "Download did not finish within the allowed time or check limit."
		}
		return s.withDownloadResultLog(c, out, operation, result, message)
	default:
		return out
	}
}
