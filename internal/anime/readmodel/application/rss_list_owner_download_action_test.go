package application

import (
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
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
		reason      string // empty: retry allowed
	}{
		{name: "never downloaded", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "filtered"},
		{name: "stopped subscription", lifecycle: "active", state: subscriptiondomain.SubscriptionStopped, verdict: "subscription_stopped"},
		{name: "failed download", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "failed", acquisition: "failed"},
		{name: "downloading", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "downloading", acquisition: "running", reason: "download_in_progress"},
		{name: "placed", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "succeeded", reason: "already_placed"},
		{name: "placed, file missing", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "succeeded", missing: true},
		{name: "downloaded, processing failed", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "failed"},
		{name: "downloaded, processing", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "accepted_pending_download", task: "completed", acquisition: "running", reason: "processing_in_progress"},
		{name: "pending subscription", lifecycle: "active", state: subscriptiondomain.SubscriptionPending, verdict: "filtered", reason: "not_started"},
		{name: "already present", lifecycle: "active", state: subscriptiondomain.SubscriptionActive, verdict: "adopted_existing", reason: "already_present"},
		{name: "retired owner keeps state active", lifecycle: "retired", state: subscriptiondomain.SubscriptionActive, verdict: "filtered", reason: "owner_retired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := AnimeWorkspace{Subscription: Subscription{State: tc.state, LifecycleState: tc.lifecycle}}
			got := rssListOwnerDownloadAction(workspace, Release{Verdict: tc.verdict, currentTaskState: tc.task, currentAcquisitionState: tc.acquisition, placementMissing: tc.missing})
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

// File rows agree with the frozen plan: a file the plan does not place is
// never shown included and carries the plan's reason or outcome; a placed file
// keeps its classification row.
func TestApplyFrozenPlanToFiles(t *testing.T) {
	plan := &batchplan.Plan{Version: batchplan.Version, UnclassifiedRoute: batchplan.RouteSkip, Files: []batchplan.File{
		{SourcePath: "Show/Show - 01.mkv", Kind: "video", Outcome: batchplan.OutcomeCoveredExisting, ReasonCode: batchplan.ReasonCoveredExisting},
		{SourcePath: "Show/Show - 02.mkv", Kind: "video", Outcome: batchplan.OutcomePlanned, CoverageKey: "tv:e2"},
		{SourcePath: "Show/Show - 02.chs.ass", Kind: "subtitle", BoundVideo: "Show/Show - 02.mkv", Outcome: batchplan.OutcomeSubtitleFollows},
		{SourcePath: "Show/cover.jpg", Kind: "other", Outcome: batchplan.OutcomeIgnoredKind},
	}}
	files := []RSSListFile{
		{SourcePath: "Show/Show - 01.mkv", Included: true},
		{SourcePath: "Show/Show - 02.mkv", Included: true, RenderedTarget: "/lib/Show/S01E02.mkv"},
		{SourcePath: "Show/Show - 02.chs.ass", Included: true},
		{SourcePath: "Show/cover.jpg", Included: true},
		{SourcePath: "Show/extra.txt", Included: false, ReasonCode: "ignored_kind"},
	}
	applyFrozenPlanToFiles(files, plan)
	if files[0].Included || files[0].ReasonCode != batchplan.ReasonCoveredExisting {
		t.Fatalf("covered file = %+v", files[0])
	}
	if !files[1].Included || files[1].ReasonCode != "" || files[1].RenderedTarget == "" {
		t.Fatalf("planned file = %+v", files[1])
	}
	// The plan's own binding travels with the subtitle row, so a reader can list
	// its output under that video instead of binding it again.
	if files[2].BoundVideoSourcePath != "Show/Show - 02.mkv" || !files[2].Included {
		t.Fatalf("a bound subtitle row = %+v, want the plan's binding", files[2])
	}
	if files[3].Included || files[3].ReasonCode != string(batchplan.OutcomeIgnoredKind) {
		t.Fatalf("ignored file = %+v", files[3])
	}
	if files[4].ReasonCode != "ignored_kind" || files[4].BoundVideoSourcePath != "" {
		t.Fatalf("a file the plan does not name keeps its row: %+v", files[4])
	}
	applyFrozenPlanToFiles(files, nil)
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

// The owner file row reports the release's own parsed facts, matched by exact
// source path: the live defect showed a parsed .mkv as "0 B / other" while the
// target row of the same release showed 713.6 MB / mkv. A file the release
// never parsed stays unknown (nil, withheld from the wire) rather than zero,
// and the frozen target is taken only in the persisted owner mode.
func TestApplyParsedReleaseFiles(t *testing.T) {
	size := int64(748_486_656) // 713.6 MiB
	target := "/library/FX Senshi Kurumi-chan/Season 01/FX Senshi Kurumi-chan - 03.mkv"
	releaseFiles := []ReleaseFile{
		{Path: "FX/[示例字幕组] FX Senshi Kurumi-chan - 03.mkv", Filename: "FX Senshi Kurumi-chan - 03.mkv", MediaKind: "video", SizeBytes: &size, TargetPath: &target},
		{Path: "FX/readme.txt", Filename: "readme.txt"},
	}
	files := []RSSListFile{
		{SourcePath: "FX/[示例字幕组] FX Senshi Kurumi-chan - 03.mkv", Included: true},
		{SourcePath: "FX/sub.ass", Included: true},
		{SourcePath: "FX/readme.txt", Included: false, ReasonCode: "ignored_kind"},
	}
	applyParsedReleaseFiles(files, releaseFiles, true)
	if files[0].SizeBytes == nil || *files[0].SizeBytes != size {
		t.Fatalf("a parsed file must carry its real size: %+v", files[0].SizeBytes)
	}
	if files[0].MediaKind == nil || *files[0].MediaKind != "video" {
		t.Fatalf("a parsed file must carry its real kind: %+v", files[0].MediaKind)
	}
	if files[0].RenderedTarget != target {
		t.Fatalf("the owner row takes the frozen target: %+v", files[0].RenderedTarget)
	}
	if files[1].SizeBytes != nil || files[1].MediaKind != nil {
		t.Fatalf("a file the release never parsed stays unknown: %+v", files[1])
	}
	if files[2].SizeBytes != nil || files[2].MediaKind != nil {
		t.Fatalf("a parsed file without a size or kind stays unknown: %+v", files[2])
	}
	// The unsaved-rules owner mode shows its own rendered target, but the size
	// and kind are measured facts and stay with the file.
	draft := []RSSListFile{{SourcePath: "FX/[示例字幕组] FX Senshi Kurumi-chan - 03.mkv", Included: true, RenderedTarget: "/draft/target.mkv"}}
	applyParsedReleaseFiles(draft, releaseFiles, false)
	if draft[0].MediaKind == nil || *draft[0].MediaKind != "video" || draft[0].SizeBytes == nil {
		t.Fatalf("the draft row keeps the parsed file facts: %+v", draft[0])
	}
	if draft[0].RenderedTarget != "/draft/target.mkv" {
		t.Fatalf("the draft row keeps its own rendered target: %+v", draft[0].RenderedTarget)
	}
}
