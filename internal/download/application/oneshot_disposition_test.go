package application

import (
	"testing"
	"time"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

func finishFailed(code string) worker.Outcome {
	return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: code}, nil)
}

func TestDownloadDisposition(t *testing.T) {
	cases := []struct {
		name string
		out  worker.Outcome
		want workitems.EffectDisposition
	}{
		{"submit succeeded", worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "downloading", ReasonCode: "qb_submit_succeeded"}, nil), workitems.DispositionConfirmed},
		{"lookup reconciled", worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "downloading", ReasonCode: "qb_lookup_reconciled"}, nil), workitems.DispositionConfirmed},
		{"sync observed completion", worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "qb_download_completed"}, nil), workitems.DispositionConfirmed},
		{"stall backstop terminal", finishFailed("download_stalled"), workitems.DispositionConfirmed},
		{"provider task failed terminally", finishFailed("qb_provider_failed"), workitems.DispositionConfirmed},
		{"owner already changed", finishFailed("download_task_already_changed"), workitems.DispositionNoExternalEffect},
		{"already recorded", finishFailed("provider_task_already_recorded"), workitems.DispositionNoExternalEffect},
		{"frozen input invalid", finishFailed("frozen_input_invalid"), workitems.DispositionNoExternalEffect},
		{"payload missing", finishFailed("torrent_payload_missing"), workitems.DispositionNoExternalEffect},
		{"correlation invalid", finishFailed("provider_correlation_invalid"), workitems.DispositionNoExternalEffect},
		{"correlation missing", finishFailed("provider_correlation_missing"), workitems.DispositionNoExternalEffect},
		{"retryable timeout", worker.Retry("qb_timeout", "timed out", time.Second, nil), workitems.DispositionAmbiguous},
	}
	for _, tc := range cases {
		if got := downloadDisposition(tc.out); got != tc.want {
			t.Fatalf("%s: disposition = %q, want %q", tc.name, got, tc.want)
		}
	}
}
