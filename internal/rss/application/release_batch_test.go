package application

import (
	"encoding/json"
	"testing"
)

// TestEvaluationSnapshotBatchKeyRoundTrip pins the link between the enqueue
// snapshot and the batch stamping of the evaluated rows: only a snapshot that
// carries a batch key belongs to a batch; anything else is standalone.
func TestEvaluationSnapshotBatchKeyRoundTrip(t *testing.T) {
	if got := releaseEvaluationBatchKey(evaluationSnapshot("rss_refresh", "")); got != "" {
		t.Fatalf("standalone snapshot carries a batch key: %q", got)
	}
	const key = "5f0f3b0e-8a63-4d4c-9a5e-4a2c1f5b7c11"
	if got := releaseEvaluationBatchKey(evaluationSnapshot("rss_refresh", key)); got != key {
		t.Fatalf("batched snapshot lost its key: %q", got)
	}
	for name, snapshot := range map[string]json.RawMessage{
		"nil":       nil,
		"empty":     {},
		"other":     json.RawMessage(`{"trigger":"group_selected"}`),
		"not json":  json.RawMessage(`not-json`),
		"blank key": json.RawMessage(`{"batch_key":"   "}`),
	} {
		if got := releaseEvaluationBatchKey(snapshot); got != "" {
			t.Fatalf("%s snapshot must be standalone, got %q", name, got)
		}
	}
}
