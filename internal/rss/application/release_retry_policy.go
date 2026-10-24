package application

// Release retry ("run again"): the RSS list action
// and the retry command share this one decision of whether a Release may be
// re-enqueued for the whole chain — evaluation under the current rules (so it
// can end filtered), then download and post-processing. A Release may run
// again when nothing of it is in progress and its chain did not end healthy:
// it has no download task, its download failed, its download finished but the
// post-processing did not succeed, or it succeeded but the latest library
// scan found a placed file missing or holding other content. A Release whose
// files are in the library, an adopted or baseline Release, a retired owner
// and a pending subscription may not.

// Typed non-retryable reasons. They are stable codes; the interface maps them
// to copy and never shows them raw.
const (
	RetryReasonOwnerRetired         = "owner_retired"
	RetryReasonAlreadyPresent       = "already_present"
	RetryReasonDownloadInProgress   = "download_in_progress"
	RetryReasonProcessingInProgress = "processing_in_progress"
	RetryReasonAlreadyPlaced        = "already_placed"
	RetryReasonPresentAtResume      = "present_at_resume"
	RetryReasonNotStarted           = "not_started"
)

// ReleaseRetryFacts are the read facts the decision needs: the Release verdict,
// the state of its current download task and of that task's acquisition, and
// the owner lifecycle. Empty strings mean absent.
type ReleaseRetryFacts struct {
	Verdict           string
	DownloadTaskState string
	AcquisitionState  string
	LifecycleState    string
	// SubscriptionState is the owner's Subscription state; empty means absent.
	SubscriptionState string
	// PlacementMissing says an active placement of this Release was found
	// missing or holding other content by the latest library scan
	// (ReleasePlacementIssueSQL).
	PlacementMissing bool
}

// ReleaseRetryDecision is the shared answer. ReasonCode is set only when the
// Release may not be retried.
type ReleaseRetryDecision struct {
	Allowed    bool
	ReasonCode string
}

// ReleaseRetryability decides whether the Release may run again. A stopped
// subscription does not block an explicit request.
func ReleaseRetryability(facts ReleaseRetryFacts) ReleaseRetryDecision {
	deny := func(reason string) ReleaseRetryDecision { return ReleaseRetryDecision{ReasonCode: reason} }
	allow := ReleaseRetryDecision{Allowed: true}
	if facts.LifecycleState != "" && facts.LifecycleState != "active" {
		return deny(RetryReasonOwnerRetired)
	}
	// A pending (never started) subscription does no work, manual included.
	if facts.SubscriptionState == "pending" {
		return deny(RetryReasonNotStarted)
	}
	acquisitionRunning := facts.AcquisitionState == "running"
	switch facts.DownloadTaskState {
	case "":
		if facts.Verdict == "adopted_existing" {
			return deny(RetryReasonAlreadyPresent)
		}
		if facts.Verdict == ReleaseVerdictResumeBaseline {
			return deny(RetryReasonPresentAtResume)
		}
		return allow
	case "failed":
		if acquisitionRunning {
			// The failed download is still being settled.
			return deny(RetryReasonDownloadInProgress)
		}
		return allow
	case "completed":
		switch facts.AcquisitionState {
		case "running":
			return deny(RetryReasonProcessingInProgress)
		case "", "succeeded":
			if facts.PlacementMissing {
				return allow
			}
			return deny(RetryReasonAlreadyPlaced)
		default:
			// Failed or cancelled post-processing runs again.
			return allow
		}
	default:
		// queued, submitting, downloading, waiting_retry
		return deny(RetryReasonDownloadInProgress)
	}
}

// ReleasePlacementIssueSQL is true for a releases row aliased r that has an
// active, verified placement whose latest library scan observation is missing
// or fingerprint_conflict. managedAnimeExpr names the Release's anime id (it
// keeps the read on the per-anime placement index). The RSS list and the
// retry command both read the PlacementMissing fact through it.
func ReleasePlacementIssueSQL(managedAnimeExpr string) string {
	return `EXISTS (SELECT 1 FROM placements p
        WHERE p.managed_anime_id = ` + managedAnimeExpr + ` AND p.quality_facts_ref = r.id AND ` + placementIssueCondition + `)`
}

// placementIssueCondition selects a placement aliased p whose latest scan
// observation found its file missing or holding other content (the same rule
// as the placement issue list).
const placementIssueCondition = `p.state = 'active' AND p.verification_state = 'verified'
          AND EXISTS (SELECT 1 FROM reconciliation_files rf
                      WHERE rf.managed_anime_id = p.managed_anime_id AND rf.facts->>'placement_id' = p.id
                        AND rf.disposition IN ('missing', 'fingerprint_conflict')
                        AND rf.generation = (SELECT MAX(generation) FROM reconciliation_files
                                             WHERE managed_anime_id = p.managed_anime_id AND disposition IS NOT NULL))`
