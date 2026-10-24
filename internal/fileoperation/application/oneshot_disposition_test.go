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

func TestFileOperationDisposition(t *testing.T) {
	cases := []struct {
		name string
		out  worker.Outcome
		want workitems.EffectDisposition
	}{
		{"completed and reread", worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "file_observed"}, nil), workitems.DispositionConfirmed},
		{"converged after source missing", worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "target_converged_after_source_missing"}, nil), workitems.DispositionConfirmed},
		{"effect uncertain blocks cleanup", finishFailed("EFFECT_UNCERTAIN"), workitems.DispositionAmbiguous},
		{"source missing placed nothing", finishFailed("SOURCE_MISSING"), workitems.DispositionNoExternalEffect},
		{"source cleanup failed is known", finishFailed("SOURCE_CLEANUP_FAILED"), workitems.DispositionConfirmed},
		{"unsafe target placed nothing", finishFailed("file_operation_input_invalid"), workitems.DispositionConfirmed},
		{"transient retry stays ambiguous", worker.Retry("file_executor_transient", "sharing violation", time.Second, nil), workitems.DispositionAmbiguous},
	}
	for _, tc := range cases {
		if got := fileOperationDisposition(tc.out); got != tc.want {
			t.Fatalf("%s: disposition = %q, want %q", tc.name, got, tc.want)
		}
	}
}
