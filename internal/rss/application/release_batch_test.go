package application

import (
	"encoding/json"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

// TestReleaseBatchHeadRecordPinsTheWriterInvariants pins the envelope the
// activity writer validates fail-closed for every main-line entry: a missing
// identity column only surfaces when the real writer runs, and the local suite
// skips the database lanes. Building the head here keeps that class of
// omission visible without a VM round trip.
func TestReleaseBatchHeadRecordPinsTheWriterInvariants(t *testing.T) {
	snapshot := sourceSnapshot{
		sourceID: "src-1", subscriptionID: "sub-1", managedAnimeID: "anime-1", groupName: "Group A",
	}
	const key = "5f0f3b0e-8a63-4d4c-9a5e-4a2c1f5b7c11"
	for name, tc := range map[string]struct {
		created int
		total   int
		message observabilityport.MessageCode
	}{
		"first evaluation": {created: 4, total: 4, message: observabilityport.MessageReleaseBatchFirst},
		"reevaluation":     {created: 1, total: 4, message: observabilityport.MessageReleaseBatchReevaluated},
	} {
		t.Run(name, func(t *testing.T) {
			record, err := releaseBatchHeadRecord(snapshot, key, "rss_refresh", tc.created, tc.total)
			if err != nil {
				t.Fatalf("build head: %v", err)
			}
			if record.Kind != string(observabilityport.ActivityKindReleaseBatchProcessed) || !observabilityport.IsActivityKind(record.Kind) {
				t.Fatalf("kind = %q, want the registered batch kind", record.Kind)
			}
			spec, ok := observabilityport.LookupMessage(record.MessageCode)
			if !ok {
				t.Fatalf("message code %q is not registered", record.MessageCode)
			}
			if spec.Kind != observabilityport.ActivityKind(record.Kind) {
				t.Fatalf("message %q belongs to kind %q, not %q", record.MessageCode, spec.Kind, record.Kind)
			}
			if record.MessageCode != tc.message {
				t.Fatalf("message = %q, want %q", record.MessageCode, tc.message)
			}
			for label, value := range map[string]string{
				"reason code": record.ReasonCode, "subject type": record.SubjectType,
				"subject ID": record.SubjectID, "dedupe key": record.DedupeKey,
			} {
				if strings.TrimSpace(value) == "" {
					t.Fatalf("%s is empty; the writer rejects the head", label)
				}
			}
			if record.ReasonCode != record.Kind {
				t.Fatalf("reason code = %q, want the kind", record.ReasonCode)
			}
			if record.SubjectType != "release_batch" || record.SubjectID != key || record.SubjectVersion < 1 {
				t.Fatalf("subject = %s/%s v%d, want release_batch/%s v1", record.SubjectType, record.SubjectID, record.SubjectVersion, key)
			}
			// encodeMessageParams rejects a param the code does not declare, so
			// the count must be the declared one.
			declared := false
			for _, param := range spec.Params {
				declared = declared || param.Name == observabilityport.ParamCount
			}
			if !declared {
				t.Fatalf("message %q declares no %q param", record.MessageCode, observabilityport.ParamCount)
			}
			if got := record.MessageParams[observabilityport.ParamCount]; got != tc.total {
				t.Fatalf("count param = %v, want %d", got, tc.total)
			}
			var details map[string]any
			if err := json.Unmarshal(record.Details, &details); err != nil {
				t.Fatalf("decode details: %v", err)
			}
			if details["trigger"] != "rss_refresh" || details["created"] != float64(tc.created) || details["changed"] != float64(tc.total-tc.created) {
				t.Fatalf("details = %v, want trigger/created/changed rss_refresh/%d/%d", details, tc.created, tc.total-tc.created)
			}
		})
	}
}

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
