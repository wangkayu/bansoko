package application

import (
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/filekinds"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// The owner row offers "run again" exactly when the retry command accepts it:
// a stopped subscription, failed post-processing and a placed file the scan
// found missing do not block it; a running download or processing, a release
// whose files are in the library and a retired owner do, each with its reason.
func TestRSSListOwnerDownloadActionFollowsTheRetryDecision(t *testing.T) {
	for _, tc := range []struct {
		name        string
		lifecycle   string
		state       subscriptiondomain.SubscriptionState
		verdict     string
		task        string
		acquisition string
		missing     bool
		released    bool
		reason      string // empty: retry allowed
	}{
		{name: "never downloaded", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "filtered"},
		{name: "stopped subscription", lifecycle: "active", state: subscriptiondomain.SubscriptionStopped, verdict: "subscription_stopped"},
		{name: "failed download", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "failed", acquisition: "failed"},
		{name: "downloading", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "downloading", acquisition: "running", reason: "download_in_progress"},
		{name: "placed", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "succeeded", reason: "already_placed"},
		{name: "placed, file missing", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "succeeded", missing: true},
		{name: "released from management", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "succeeded", released: true, reason: "placement_released"},
		{name: "downloaded, processing failed", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "failed"},
		{name: "downloaded, processing", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "running", reason: "processing_in_progress"},
		{name: "pending subscription", lifecycle: "active", state: subscriptiondomain.SubscriptionPending, verdict: "filtered", reason: "not_started"},
		{name: "already present", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "adopted_existing", reason: "already_present"},
		{name: "retired owner keeps state active", lifecycle: "retired", state: subscriptiondomain.SubscriptionActive, verdict: "filtered", reason: "owner_retired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := AnimeWorkspace{Subscription: Subscription{State: tc.state, LifecycleState: tc.lifecycle}}
			got := rssListOwnerDownloadAction(workspace, Release{Verdict: tc.verdict, currentTaskState: tc.task, currentAcquisitionState: tc.acquisition, placementMissing: tc.missing, placementReleased: tc.released})
			if tc.reason == "" {
				if !got.Allowed || got.Kind != RSSListDownloadRetry || got.ReasonCode != "" {
					t.Fatalf("download action = %+v, want retry allowed", got)
				}
				return
			}
			if got.Allowed || got.Kind != RSSListDownloadNone || got.ReasonCode != tc.reason || got.Reason == "" {
				t.Fatalf("download action = %+v, want blocked by %q with copy", got, tc.reason)
			}
		})
	}
}

// An owner row waits on a re-evaluation only when the schedulers would pick
// its Release up: a submitted, adopted, failed-acquisition or baseline Release
// keeps its persisted verdict whatever the rules version.
func TestRSSListReleaseReevaluatingFollowsTheSchedulerScope(t *testing.T) {
	task := "task-1"
	for _, tc := range []struct {
		name    string
		release Release
		want    bool
	}{
		{name: "unsubmitted, stale", release: Release{Verdict: "filtered", EvaluatedRulesVersion: 1}, want: true},
		{name: "unsubmitted, current", release: Release{Verdict: "filtered", EvaluatedRulesVersion: 2}},
		{name: "submitted, stale", release: Release{Verdict: "accepted_pending_download", EvaluatedRulesVersion: 1, DownloadTaskID: &task}},
		{name: "adopted, stale", release: Release{Verdict: "adopted_existing", EvaluatedRulesVersion: 1}},
		{name: "acquisition failed, stale", release: Release{Verdict: "acquisition_failed", EvaluatedRulesVersion: 1}},
		{name: "resume baseline, stale", release: Release{Verdict: "resume_baseline", EvaluatedRulesVersion: 1}},
		{name: "start baseline, stale", release: Release{Verdict: "start_baseline", EvaluatedRulesVersion: 1}},
	} {
		if got := rssListReleaseReevaluating(tc.release, 2); got != tc.want {
			t.Fatalf("%s: reevaluating = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The row list starts after the two hard gates: a file below the torrent's
// first level or of a kind the product does not handle was never a candidate
// and takes no row, while a file that passed them keeps its row whatever the
// filter, the plan or the binding concluded about it.
func TestRSSListFileRowDropsHardGateFiles(t *testing.T) {
	for _, tc := range []struct {
		reason string
		row    bool
	}{
		{reason: filekinds.ReasonIgnoredNested},
		{reason: filekinds.ReasonIgnoredKind},
		{reason: "", row: true},
		{reason: "excluded_by_rule", row: true},
		{reason: "file_not_in_matched", row: true},
		{reason: "episode_unrecognized", row: true},
		{reason: "unknown", row: true},
		{reason: "unknown_skipped", row: true},
		{reason: "subtitle_binding_missing", row: true},
		{reason: "subtitle_video_excluded", row: true},
	} {
		file := animedomain.PreviewFileResult{SourcePath: "Show/Show - 01.mkv"}
		if tc.reason != "" {
			reason := tc.reason
			file.ReasonCode = &reason
		}
		row, listed := rssListFileRow(file, nil)
		if listed != tc.row {
			t.Fatalf("reason %q: listed = %v, want %v", tc.reason, listed, tc.row)
		}
		if listed && row.SourcePath != "Show/Show - 01.mkv" {
			t.Fatalf("a listed row lost its source path: %+v", row)
		}
	}
}

// The owner file row reports the release's own parsed size and kind, matched
// by exact source path. A file the release never parsed stays unknown (nil,
// withheld from the wire) rather than zero. The current rendered target is not
// an intrinsic file fact and is never overwritten by the frozen target.
func TestApplyParsedReleaseFiles(t *testing.T) {
	size := int64(748_486_656) // 713.6 MiB
	target := "/library/Example Show/Season 01/Example Show - 03.mkv"
	releaseFiles := []ReleaseFile{
		{Path: "Example/[Example Group] Example Show - 03.mkv", Filename: "Example Show - 03.mkv", MediaKind: "video", SizeBytes: &size, TargetPath: &target},
		{Path: "Example/readme.txt", Filename: "readme.txt"},
	}
	files := []RSSListFile{
		{SourcePath: "Example/[Example Group] Example Show - 03.mkv", Included: true, RenderedTarget: "/current/target.mkv"},
		{SourcePath: "Example/sub.ass", Included: true},
		{SourcePath: "Example/readme.txt", Included: false, ReasonCode: "ignored_kind"},
	}
	applyParsedReleaseFiles(files, releaseFiles)
	if files[0].SizeBytes == nil || *files[0].SizeBytes != size {
		t.Fatalf("a parsed file must carry its real size: %+v", files[0].SizeBytes)
	}
	if files[0].MediaKind == nil || *files[0].MediaKind != "video" {
		t.Fatalf("a parsed file must carry its real kind: %+v", files[0].MediaKind)
	}
	if files[0].RenderedTarget != "/current/target.mkv" {
		t.Fatalf("the current target must survive parsed facts: %+v", files[0].RenderedTarget)
	}
	if files[1].SizeBytes != nil || files[1].MediaKind != nil {
		t.Fatalf("a file the release never parsed stays unknown: %+v", files[1])
	}
	if files[2].SizeBytes != nil || files[2].MediaKind != nil {
		t.Fatalf("a parsed file without a size or kind stays unknown: %+v", files[2])
	}
}
