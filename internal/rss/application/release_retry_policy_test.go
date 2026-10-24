package application

import "testing"

func TestReleaseRetryability(t *testing.T) {
	for _, tc := range []struct {
		name   string
		facts  ReleaseRetryFacts
		reason string // empty: allowed
	}{
		{"never downloaded", ReleaseRetryFacts{Verdict: "discovered"}, ""},
		{"filtered", ReleaseRetryFacts{Verdict: "filtered"}, ""},
		{"held", ReleaseRetryFacts{Verdict: "waiting_metadata"}, ""},
		{"admission failed", ReleaseRetryFacts{Verdict: "acquisition_failed"}, ""},
		{"stopped subscription", ReleaseRetryFacts{Verdict: "subscription_stopped", LifecycleState: "active"}, ""},
		{"targets already present", ReleaseRetryFacts{Verdict: "adopted_existing"}, RetryReasonAlreadyPresent},
		{"queued", ReleaseRetryFacts{Verdict: "accepted_pending_download", DownloadTaskState: "queued", AcquisitionState: "running"}, RetryReasonDownloadInProgress},
		{"submitting", ReleaseRetryFacts{DownloadTaskState: "submitting", AcquisitionState: "running"}, RetryReasonDownloadInProgress},
		{"downloading", ReleaseRetryFacts{DownloadTaskState: "downloading", AcquisitionState: "running"}, RetryReasonDownloadInProgress},
		{"waiting retry", ReleaseRetryFacts{DownloadTaskState: "waiting_retry", AcquisitionState: "running"}, RetryReasonDownloadInProgress},
		{"failed, still settling", ReleaseRetryFacts{DownloadTaskState: "failed", AcquisitionState: "running"}, RetryReasonDownloadInProgress},
		{"failed, settled", ReleaseRetryFacts{DownloadTaskState: "failed", AcquisitionState: "failed"}, ""},
		{"failed, cancelled", ReleaseRetryFacts{DownloadTaskState: "failed", AcquisitionState: "cancelled"}, ""},
		{"failed, no acquisition", ReleaseRetryFacts{DownloadTaskState: "failed"}, ""},
		{"downloaded, processing", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "running"}, RetryReasonProcessingInProgress},
		{"placed", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "succeeded"}, RetryReasonAlreadyPlaced},
		{"placed, file missing", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "succeeded", PlacementMissing: true}, ""},
		{"processing, file missing", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "running", PlacementMissing: true}, RetryReasonProcessingInProgress},
		{"downloaded, later step failed", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "failed"}, ""},
		{"downloaded, later step cancelled", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "cancelled"}, ""},
		{"resume baseline", ReleaseRetryFacts{Verdict: ReleaseVerdictResumeBaseline}, RetryReasonPresentAtResume},
		{"pending subscription", ReleaseRetryFacts{Verdict: "filtered", SubscriptionState: "pending"}, RetryReasonNotStarted},
		{"pending subscription, failed download", ReleaseRetryFacts{DownloadTaskState: "failed", AcquisitionState: "failed", SubscriptionState: "pending"}, RetryReasonNotStarted},
		{"retired owner, file missing", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "succeeded", PlacementMissing: true, LifecycleState: "retired"}, RetryReasonOwnerRetired},
		{"retired owner", ReleaseRetryFacts{Verdict: "filtered", LifecycleState: "retired"}, RetryReasonOwnerRetired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ReleaseRetryability(tc.facts)
			if got.Allowed != (tc.reason == "") || got.ReasonCode != tc.reason {
				t.Fatalf("ReleaseRetryability(%+v) = %+v, want reason %q", tc.facts, got, tc.reason)
			}
		})
	}
}

func TestReleaseRetryResultOutcome(t *testing.T) {
	for _, tc := range []struct {
		verdict, reason, task, outcome string
	}{
		{"accepted_pending_download", "accepted", "task-2", RetryOutcomeAccepted},
		{"filtered", "filtered_by_rules", "", RetryOutcomeFiltered},
		{"filtered", "no_placeable_files", "", RetryOutcomeFiltered},
		{"waiting_metadata", "metadata_incomplete", "", RetryOutcomeHeld},
		{"adopted_existing", "targets_present", "", RetryOutcomePresent},
		{"parse_failed", "torrent_payload_missing", "", RetryOutcomeFailed},
		{"acquisition_failed", "target_collision", "", RetryOutcomeFailed},
	} {
		got := releaseRetryResult("rel-1", tc.verdict, tc.reason, tc.task)
		if got.Outcome != tc.outcome || got.DownloadTaskID != tc.task {
			t.Fatalf("%s/%s = %+v, want %s", tc.verdict, tc.reason, got, tc.outcome)
		}
		if tc.outcome == RetryOutcomeAccepted && got.ReasonCode != "" {
			t.Fatalf("accepted result carries reason %q", got.ReasonCode)
		}
		if tc.outcome != RetryOutcomeAccepted && got.ReasonCode != tc.reason {
			t.Fatalf("%s result reason = %q, want %q", tc.outcome, got.ReasonCode, tc.reason)
		}
	}
}

// A placement redownload may also run a Release whose files are in the
// library; anything in progress or retired still blocks it.
func TestReleaseRedownloadability(t *testing.T) {
	for _, tc := range []struct {
		name   string
		facts  ReleaseRetryFacts
		reason string
	}{
		{"placed", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "succeeded"}, ""},
		{"adopted", ReleaseRetryFacts{Verdict: "adopted_existing"}, ""},
		{"downloading", ReleaseRetryFacts{DownloadTaskState: "downloading", AcquisitionState: "running"}, RetryReasonDownloadInProgress},
		{"retired", ReleaseRetryFacts{DownloadTaskState: "completed", AcquisitionState: "succeeded", LifecycleState: "retired"}, RetryReasonOwnerRetired},
	} {
		got := ReleaseRedownloadability(tc.facts)
		if got.Allowed != (tc.reason == "") || got.ReasonCode != tc.reason {
			t.Fatalf("%s: ReleaseRedownloadability = %+v, want reason %q", tc.name, got, tc.reason)
		}
	}
}
