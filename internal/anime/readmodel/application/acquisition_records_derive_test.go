package application

import (
	"strings"
	"testing"
	"time"
)

func stageStates(stages []AcquisitionStage) string {
	parts := make([]string, len(stages))
	for i, stage := range stages {
		parts[i] = stateOf(stage)
	}
	return strings.Join(parts, ",")
}

func TestDeriveAcquisitionStages(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	owner := func(state string) *acquisitionStageOwner {
		return &acquisitionStageOwner{State: state, CreatedAt: now, UpdatedAt: now}
	}
	waited := func(state string) *acquisitionStageOwner {
		o := owner(state)
		o.WaitStartedAt = ptr(now)
		return o
	}
	cases := []struct {
		name  string
		facts acquisitionGraphFacts
		want  string
	}{
		{"accepted only", acquisitionGraphFacts{Branch: "download", RootState: "running"},
			"<not_reached>,<not_reached>,<not_reached>,<not_reached>,<not_reached>,pending,<not_reached>"},
		{"queued submit", acquisitionGraphFacts{Branch: "download", RootState: "running", Task: owner("queued")},
			"pending,<not_reached>,<not_reached>,<not_reached>,<not_reached>,pending,<not_reached>"},
		{"submit retrying", acquisitionGraphFacts{Branch: "download", RootState: "running", Task: owner("waiting_retry")},
			"running,<not_reached>,<not_reached>,<not_reached>,<not_reached>,pending,<not_reached>"},
		{"downloading", acquisitionGraphFacts{Branch: "download", RootState: "running", Task: owner("downloading")},
			"succeeded,running,<not_reached>,<not_reached>,<not_reached>,pending,<not_reached>"},
		{"wait retrying after submit", acquisitionGraphFacts{Branch: "download", RootState: "running", Task: waited("waiting_retry")},
			"succeeded,running,<not_reached>,<not_reached>,<not_reached>,pending,<not_reached>"},
		{"plan queued", acquisitionGraphFacts{Branch: "download", RootState: "running", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("queued")},
			"succeeded,succeeded,succeeded,pending,<not_reached>,pending,<not_reached>"},
		{"files partly delivered", acquisitionGraphFacts{Branch: "download", RootState: "running", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
			Requirements: &acquisitionRequirementFacts{Items: 2, Required: 2}, Operations: 2, LiveOperations: 1, DeliveredOK: 1},
			"succeeded,succeeded,succeeded,succeeded,running,pending,<not_reached>"},
		{"settlement blocked", acquisitionGraphFacts{Branch: "download", RootState: "running", BlockedCode: ptr("EFFECT_UNKNOWN"), Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
			Requirements: &acquisitionRequirementFacts{Items: 1, Required: 1}, Operations: 1, DeliveredOK: 1},
			"succeeded,succeeded,succeeded,succeeded,succeeded,blocked,<not_reached>"},
		{"delivery failed", acquisitionGraphFacts{Branch: "download", RootState: "failed", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
			Requirements: &acquisitionRequirementFacts{Items: 2, Required: 2}, Operations: 2, DeliveredOK: 1, DeliveredFailed: 1, FirstFailedReason: ptr("TARGET_EXISTS")},
			"succeeded,succeeded,succeeded,succeeded,failed,failed,<not_applicable>"},
		{"cancelled stops unfinished stages", acquisitionGraphFacts{Branch: "download", RootState: "cancelled", Task: owner("downloading")},
			"succeeded,cancelled,<not_reached>,<not_reached>,<not_reached>,cancelled,<not_applicable>"},
		{"submit failed", acquisitionGraphFacts{Branch: "download", RootState: "failed", Task: &acquisitionStageOwner{State: "failed", FailureCode: ptr("SUBMIT_REJECTED"), AttemptsStarted: 3, CreatedAt: now, UpdatedAt: now}},
			"failed,<not_reached>,<not_reached>,<not_reached>,<not_reached>,failed,<not_applicable>"},
		{"deleted in the downloader while downloading", acquisitionGraphFacts{Branch: "download", RootState: "cancelled", Task: &acquisitionStageOwner{State: "failed", FailureCode: ptr("DOWNLOADER_TASK_REMOVED"), CreatedAt: now, UpdatedAt: now, WaitStartedAt: ptr(now)}},
			"succeeded,cancelled,<not_reached>,<not_reached>,<not_reached>,cancelled,<not_applicable>"},
		{"deleted in the downloader before the first observation", acquisitionGraphFacts{Branch: "download", RootState: "cancelled", Task: &acquisitionStageOwner{State: "failed", FailureCode: ptr("DOWNLOADER_TASK_REMOVED"), CreatedAt: now, UpdatedAt: now}},
			"cancelled,<not_reached>,<not_reached>,<not_reached>,<not_reached>,cancelled,<not_applicable>"},
		{"adopted", acquisitionGraphFacts{Branch: "adopted", RootState: "succeeded"},
			"<not_applicable>,<not_applicable>,<not_applicable>,<not_applicable>,<not_applicable>,succeeded,<not_applicable>"},
		// Download completion: the completion stage of a succeeded download root.
		{"succeeded before the completion pass", acquisitionGraphFacts{Branch: "download", RootState: "succeeded", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
			Requirements: &acquisitionRequirementFacts{Items: 1, Required: 1}, Operations: 1, DeliveredOK: 1},
			"succeeded,succeeded,succeeded,succeeded,succeeded,succeeded,<not_reached>"},
		{"completion queued", acquisitionGraphFacts{Branch: "download", RootState: "succeeded", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
			Requirements: &acquisitionRequirementFacts{Items: 1, Required: 1}, Operations: 1, DeliveredOK: 1,
			Completion: &acquisitionCompletionFacts{State: "pending", CreatedAt: now, UpdatedAt: now}},
			"succeeded,succeeded,succeeded,succeeded,succeeded,succeeded,running"},
		{"completion done", acquisitionGraphFacts{Branch: "download", RootState: "succeeded", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
			Requirements: &acquisitionRequirementFacts{Items: 1, Required: 1}, Operations: 1, DeliveredOK: 1,
			Completion: &acquisitionCompletionFacts{State: "succeeded", ResultCode: ptr("removed_with_data"), CreatedAt: now, UpdatedAt: now}},
			"succeeded,succeeded,succeeded,succeeded,succeeded,succeeded,succeeded"},
		{"completion refused", acquisitionGraphFacts{Branch: "download", RootState: "succeeded", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
			Requirements: &acquisitionRequirementFacts{Items: 1, Required: 1}, Operations: 1, DeliveredOK: 1,
			Completion: &acquisitionCompletionFacts{State: "failed", ResultCode: ptr("placement_inside_content"), CreatedAt: now, UpdatedAt: now}},
			"succeeded,succeeded,succeeded,succeeded,succeeded,succeeded,failed"},
		{"completion row of a failed root is not shown", acquisitionGraphFacts{Branch: "download", RootState: "failed", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
			Requirements: &acquisitionRequirementFacts{Items: 1, Required: 1}, Operations: 1, DeliveredFailed: 1, FirstFailedReason: ptr("X"),
			Completion: &acquisitionCompletionFacts{State: "succeeded", ResultCode: ptr("seed_kept"), CreatedAt: now, UpdatedAt: now}},
			"succeeded,succeeded,succeeded,succeeded,failed,failed,<not_applicable>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stages := deriveAcquisitionStages(tc.facts)
			if got := stageStates(stages); got != tc.want {
				t.Fatalf("stages = %s, want %s", got, tc.want)
			}
			for i, stage := range stages {
				if stage.Stage != acquisitionStageOrder[i] {
					t.Fatalf("stage %d = %s, want the fixed order %v", i, stage.Stage, acquisitionStageOrder)
				}
			}
		})
	}

	failed := deriveAcquisitionStages(acquisitionGraphFacts{Branch: "download", RootState: "failed", Task: &acquisitionStageOwner{State: "failed", FailureCode: ptr("SUBMIT_REJECTED"), AttemptsStarted: 3, CreatedAt: now, UpdatedAt: now}})
	if failed[0].ReasonCode == nil || *failed[0].ReasonCode != "SUBMIT_REJECTED" || failed[0].AttemptsStarted == nil || *failed[0].AttemptsStarted != 3 {
		t.Fatalf("a failed submit carries its reason and attempts: %+v", failed[0])
	}
	removed := deriveAcquisitionStages(acquisitionGraphFacts{Branch: "download", RootState: "cancelled", Task: &acquisitionStageOwner{State: "failed", FailureCode: ptr("DOWNLOADER_TASK_REMOVED"), CreatedAt: now, UpdatedAt: now, WaitStartedAt: ptr(now)}})[1]
	if removed.ReasonCode == nil || *removed.ReasonCode != "DOWNLOADER_TASK_REMOVED" {
		t.Fatalf("a deleted download carries its reason: %+v", removed)
	}
	done := deriveAcquisitionStages(acquisitionGraphFacts{Branch: "download", RootState: "succeeded", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
		Completion: &acquisitionCompletionFacts{State: "succeeded", ResultCode: ptr("paused"), CreatedAt: now, UpdatedAt: now}})[6]
	if done.Stage != AcquisitionStageCompletion || done.ReasonCode == nil || *done.ReasonCode != "paused" {
		t.Fatalf("a settled completion carries its result as the reason code: %+v", done)
	}
	files := deriveAcquisitionStages(acquisitionGraphFacts{Branch: "download", RootState: "running", Task: owner("completed"), Artifact: owner("verified"), Plan: owner("succeeded"),
		Requirements: &acquisitionRequirementFacts{Items: 3, Required: 2}, Operations: 2, LiveOperations: 1, DeliveredOK: 1})[4]
	if files.Done == nil || *files.Done != 1 || files.Total == nil || *files.Total != 2 {
		t.Fatalf("file execution counts delivered of required: %+v", files)
	}
}

func TestSummaryPosition(t *testing.T) {
	running := deriveAcquisitionStages(acquisitionGraphFacts{Branch: "download", RootState: "running", Task: &acquisitionStageOwner{State: "waiting_retry", FailureCode: ptr("QB_UNAVAILABLE")}})
	stage, state, reason := summaryPosition("running", running, nil)
	if *stage != AcquisitionStageSubmit || *state != AcquisitionStageStateRunning || reason == nil || *reason != "QB_UNAVAILABLE" {
		t.Fatalf("a running root reports its first unfinished stage: %v %v %v", *stage, *state, reason)
	}
	adopted := deriveAcquisitionStages(acquisitionGraphFacts{Branch: "adopted", RootState: "running"})
	if stage, state, _ := summaryPosition("running", adopted, nil); *stage != AcquisitionStageSettlement || *state != AcquisitionStageStatePending {
		t.Fatalf("an adopted running root waits at settlement: %v %v", *stage, *state)
	}
	outcome := &AcquisitionOutcome{Outcome: "failed", PrimaryFailure: &AcquisitionFailure{Stage: AcquisitionStageInspect, ReasonCode: "INVENTORY_MISMATCH"}}
	if stage, state, reason := summaryPosition("failed", nil, outcome); *stage != AcquisitionStageInspect || *state != "failed" || *reason != "INVENTORY_MISMATCH" {
		t.Fatalf("a failed root reports its primary failure: %v %v %v", *stage, *state, *reason)
	}
	if stage, state, reason := summaryPosition("succeeded", nil, &AcquisitionOutcome{Outcome: "succeeded"}); stage != nil || state != nil || reason != nil {
		t.Fatal("a success reports no stage")
	}
}

func TestAcquisitionCursors(t *testing.T) {
	at := time.Date(2026, 10, 11, 1, 2, 3, 456789000, time.UTC)
	digest := recordFilterDigest(AcquisitionRecordQuery{})
	text := encodeCursor(recordCursor{Version: 1, At: at, ID: "root-1", Filter: digest})
	cursor, ok := decodeRecordCursor(text)
	if !ok || !cursor.At.Equal(at) || cursor.ID != "root-1" || cursor.Filter != digest {
		t.Fatalf("record cursor round trip: %+v %v", cursor, ok)
	}
	for _, bad := range []string{"", "%%%", text + "=", encodeCursor(map[string]any{"v": 1, "at": at, "id": "x", "f": digest, "extra": 1}), encodeCursor(recordCursor{Version: 2, At: at, ID: "x", Filter: digest}), encodeCursor(recordCursor{Version: 1, ID: "x", Filter: digest}), encodeCursor(recordCursor{Version: 1, At: at, ID: "x"}), strings.Repeat("a", 600)} {
		if _, ok := decodeRecordCursor(bad); ok {
			t.Fatalf("cursor %q must be rejected", bad)
		}
	}
	// The filter digest covers every filter but not the limit, and is stable
	// across time zones of the same instant.
	from := at.In(time.FixedZone("CST", 8*3600))
	seen := map[string]AcquisitionRecordQuery{}
	for _, query := range []AcquisitionRecordQuery{{}, {ManagedAnimeID: "anime-1"}, {State: "failed"}, {EntryKind: "one_shot"}, {Origin: "one_shot"}, {Origin: "subscription"}, {AcceptedFrom: &at}, {AcceptedTo: &at}} {
		d := recordFilterDigest(query)
		if prior, dup := seen[d]; dup {
			t.Fatalf("filters %+v and %+v share a digest", prior, query)
		}
		seen[d] = query
	}
	if recordFilterDigest(AcquisitionRecordQuery{AcceptedFrom: &at}) != recordFilterDigest(AcquisitionRecordQuery{AcceptedFrom: &from, Limit: 5}) {
		t.Fatal("the digest must ignore the limit and the zone of the same instant")
	}
	fileText := encodeCursor(fileCursor{Version: 1, Root: "root-1", After: 4})
	if cursor, ok := decodeFileCursor(fileText, "root-1"); !ok || cursor.After != 4 {
		t.Fatalf("file cursor round trip: %+v %v", cursor, ok)
	}
	if _, ok := decodeFileCursor(fileText, "root-2"); ok {
		t.Fatal("a file cursor is bound to its root")
	}
}

func TestDisplayValueRedaction(t *testing.T) {
	if got := displayValue(nil); got.State != AcquisitionDisplayUnavailable || got.Value != nil {
		t.Fatalf("nil text is unavailable: %+v", got)
	}
	if got := displayValue(ptr("[Group] Show - 01")); got.State != AcquisitionDisplayVisible || *got.Value != "[Group] Show - 01" {
		t.Fatalf("a plain title is visible: %+v", got)
	}
	for _, sensitive := range []string{"https://tracker.example.test/announce?passkey=abc", "/downloads/private/Show - 01.mkv"} {
		if got := displayValue(ptr(sensitive)); got.State != AcquisitionDisplayRedacted || got.Value != nil {
			t.Fatalf("%q must be redacted whole: %+v", sensitive, got)
		}
	}
	long := strings.Repeat("番", 900)
	if got := displayValue(&long); got.State != AcquisitionDisplayVisible || len(*got.Value) > acquisitionDisplayMaxBytes {
		t.Fatalf("a long title is bounded: %d bytes", len(*got.Value))
	}
}
