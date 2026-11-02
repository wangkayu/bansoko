package businesslog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

func releaseBatchFixture(created, changed int) ReleaseBatch {
	return ReleaseBatch{
		SubscriptionID: "sub-1", ManagedAnimeID: "anime-1", SourceID: "src-1", GroupName: "Group A",
		BatchKey: "5f0f3b0e-8a63-4d4c-9a5e-4a2c1f5b7c11", Trigger: "rss_refresh",
		Created: created, Changed: changed,
	}
}

func TestWriteReleaseBatchTxDelegatesTypedRecord(t *testing.T) {
	writer := &captureActivityWriter{}
	if err := WriteReleaseBatchTx(context.Background(), txscope.Tx{}, writer, releaseBatchFixture(4, 0)); err != nil {
		t.Fatalf("write release batch: %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.calls)
	}
	got := writer.record
	if got.Kind != "release_batch_processed" || got.Outcome != "info" || got.ReasonCode != "release_batch_processed" {
		t.Fatalf("unexpected head kind/outcome/reason: %+v", got)
	}
	if got.SubjectType != "release_batch" || got.SubjectID != "5f0f3b0e-8a63-4d4c-9a5e-4a2c1f5b7c11" || got.SubjectVersion != 1 {
		t.Fatalf("unexpected head subject: %+v", got)
	}
	if got.DedupeKey != "release_batch_processed:5f0f3b0e-8a63-4d4c-9a5e-4a2c1f5b7c11" {
		t.Fatalf("dedupe key = %q", got.DedupeKey)
	}
	if got.RSSSourceID != "src-1" || got.GroupRef != "Group A" {
		t.Fatalf("head lost its source or group: %+v", got)
	}
	if got.MessageCode != observabilityport.MessageReleaseBatchFirst {
		t.Fatalf("message = %q, want the first-evaluation code", got.MessageCode)
	}
	if got.MessageParams[observabilityport.ParamCount] != 4 {
		t.Fatalf("count param = %v, want 4", got.MessageParams[observabilityport.ParamCount])
	}
	if err := WriteReleaseBatchTx(context.Background(), txscope.Tx{}, nil, releaseBatchFixture(1, 0)); err == nil {
		t.Fatal("a nil writer must be refused")
	}
}

// TestReleaseBatchRecordPinsTheWriterInvariants pins the envelope the activity
// writer validates fail-closed for every main-line entry: a missing identity
// column only surfaces when the real writer runs, and the local suite skips
// the database lanes. Building the head here keeps that class of omission
// visible without a VM round trip.
func TestReleaseBatchRecordPinsTheWriterInvariants(t *testing.T) {
	for name, tc := range map[string]struct {
		created int
		changed int
		message observabilityport.MessageCode
	}{
		"first evaluation": {created: 4, message: observabilityport.MessageReleaseBatchFirst},
		"reevaluation":     {created: 1, changed: 3, message: observabilityport.MessageReleaseBatchReevaluated},
	} {
		t.Run(name, func(t *testing.T) {
			batch := releaseBatchFixture(tc.created, tc.changed)
			record, err := releaseBatchRecord(batch)
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
			if record.SubjectID != batch.BatchKey || record.SubjectVersion < 1 {
				t.Fatalf("subject = %s/%s v%d, want release_batch/%s v1", record.SubjectType, record.SubjectID, record.SubjectVersion, batch.BatchKey)
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
			if got := record.MessageParams[observabilityport.ParamCount]; got != tc.created+tc.changed {
				t.Fatalf("count param = %v, want %d", got, tc.created+tc.changed)
			}
			var details map[string]any
			if err := json.Unmarshal(record.Details, &details); err != nil {
				t.Fatalf("decode details: %v", err)
			}
			if details["trigger"] != batch.Trigger || details["created"] != float64(tc.created) || details["changed"] != float64(tc.changed) {
				t.Fatalf("details = %v, want trigger/created/changed %s/%d/%d", details, batch.Trigger, tc.created, tc.changed)
			}
		})
	}
}
