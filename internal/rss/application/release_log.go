package application

import (
	"context"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

type releaseLogEffect struct {
	verdict, reason, correlation string
}

// record is called by the owner writer, not by the preflight evaluation. The
// runtime invokes the log hook only if the transaction containing that write commits.
func (e *releaseLogEffect) record(verdict, reason, correlation string) {
	if e != nil {
		*e = releaseLogEffect{verdict: verdict, reason: reason, correlation: correlation}
	}
}

func (s *Service) releaseEvaluationWithLog(claim workitems.Claim, snapshot releaseSnapshot, effect *releaseLogEffect, outcome worker.Outcome) worker.Outcome {
	if s.logger == nil {
		return outcome
	}
	return worker.AppendAfterCommit(outcome, func(ctx context.Context) {
		if ctx.Err() != nil {
			return
		}
		verdict, reason, correlation := effect.verdict, effect.reason, effect.correlation
		classification, message := releaseEvaluationMessage(verdict, reason)
		if verdict == "" && outcome.Kind == worker.OutcomeFinish {
			reason = outcome.OwnerResult.ReasonCode
			classification, message = releaseEvaluationNoWriteMessage(reason)
		}
		if message == "" {
			return
		}
		subject := "Release evaluation"
		if claim.ID == "" && snapshot.manualRetry {
			subject = "Manual release retry"
		}
		event := observabilityport.RuntimeEvent{
			Component: "rss", Operation: "release_evaluate", Outcome: classification, Stage: "evaluate_release",
			OwnerType: workitems.OwnerRelease, OwnerID: snapshot.releaseID,
			WorkItemID: claim.ID, Attempt: claim.Attempt,
			ReasonCode: reason, StateAfter: verdict, CorrelationID: correlation,
			Message: rssLogSubject(subject, snapshot.animeTitle, snapshot.groupName) + ": " + message,
		}
		if claim.ID != "" {
			event.Kind = claim.Kind
		}
		if classification == "failure" {
			s.logger.Warn(ctx, event)
		} else {
			s.logger.Info(ctx, event)
		}
	})
}

func releaseEvaluationNoWriteMessage(reason string) (string, string) {
	switch reason {
	case "release_unavailable":
		return "failure", "could not find the release and its subscription"
	case "release_terminal":
		return "skipped", "the release already has a final result"
	case "release_already_submitted":
		return "skipped", "a download task already exists for this release"
	case "subscription_pending":
		return "skipped", "the subscription has not started"
	case "provider_group_not_selected":
		return "skipped", "the provider group is not selected"
	case "parser_policy_required":
		return "failure", "the subscription has no usable parser settings"
	case "start_baseline_missing":
		return "failure", "could not tell whether the release predates the subscription"
	default:
		return "", ""
	}
}

func releaseEvaluationMessage(verdict, reason string) (string, string) {
	switch verdict {
	case "accepted_pending_download":
		return "success", "created a download task; the downloader has not yet confirmed submission"
	case "adopted_existing":
		return "success", "used existing files instead of downloading this release"
	case "waiting_metadata":
		return "skipped", "required metadata is missing; no download was started"
	case "awaiting_competition":
		return "skipped", "the release is waiting to be compared with other candidates"
	case "subscription_stopped":
		return "skipped", "the subscription is stopped; no download was started"
	case "source_unavailable":
		return "skipped", "the RSS source is no longer available"
	case "start_baseline":
		return "skipped", "no download was started because the release did not qualify as new"
	case "resume_baseline":
		return "skipped", "the release was already in the feed when the subscription resumed"
	case "filtered":
		switch reason {
		case "filtered_by_rules":
			return "skipped", "the release did not pass the subscription's filters"
		case "no_placeable_files":
			return "skipped", "the release has no files eligible for import"
		default:
			return "skipped", "the release was not selected for download"
		}
	case "parse_failed":
		switch reason {
		case "torrent_payload_missing", "torrent_metadata_unavailable", "torrent_manifest_missing":
			return "failure", "usable torrent information was not available"
		case "parser_selection_unavailable":
			return "failure", "the configured release parser was unavailable"
		case "feed_item_unavailable":
			return "failure", "could not read the release information saved from the feed"
		default:
			return "failure", "could not interpret the release information"
		}
	case "acquisition_failed":
		return "failure", "the release could not be accepted for download"
	case "discovered":
		return "success", "recorded the release without creating a download task"
	default:
		return "", ""
	}
}
