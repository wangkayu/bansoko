package main

import (
	"testing"

	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/downloadcompletion"
)

// The cancel keeps a qB task the remove_with_data ownership rule would not
// delete, and retries only a transient failure.
func TestCancelEffectFollowsTheRemoveWithDataResult(t *testing.T) {
	cases := []struct {
		name    string
		outcome downloadcompletion.Outcome
		want    string
	}{
		{"deleted", downloadcompletion.Outcome{Success: true, Code: downloadcompletion.ResultRemovedData}, downloadapplication.CancelEffectDeletedWithData},
		{"absent", downloadcompletion.Outcome{Success: true, Code: downloadcompletion.ResultTaskAbsent}, downloadapplication.CancelEffectAbsent},
		{"shared", downloadcompletion.Outcome{Success: true, Code: downloadcompletion.ResultTorrentShared}, downloadapplication.CancelEffectDetached},
		{"transient", downloadcompletion.Outcome{Retryable: true, Code: "verify_failed"}, downloadapplication.CancelEffectRetry},
		{"safety refusal", downloadcompletion.Outcome{Code: "unsafe_data_delete"}, downloadapplication.CancelEffectDetached},
		{"mismatch", downloadcompletion.Outcome{Code: "qb_task_mismatch"}, downloadapplication.CancelEffectDetached},
		{"other success", downloadcompletion.Outcome{Success: true, Code: downloadcompletion.ResultRemoved}, downloadapplication.CancelEffectDetached},
	}
	for _, tc := range cases {
		if got := cancelEffect(tc.outcome); got != tc.want {
			t.Fatalf("%s: cancelEffect(%+v) = %s, want %s", tc.name, tc.outcome, got, tc.want)
		}
	}
}
