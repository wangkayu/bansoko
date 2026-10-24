package domain

import (
	"strings"
	"testing"
)

func TestFileReceiptStrictStepMatrix(t *testing.T) {
	const at = `"2026-10-08T00:00:00.000001Z"`
	const digest = `"sha256:0000000000000000000000000000000000000000000000000000000000000000"`
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"unstarted", `{"state":"not_started","proof":null,"observed_at":null,"reason_code":null}`, true},
		{"not_required", `{"state":"not_required","proof":null,"observed_at":null,"reason_code":null}`, true},
		{"started", `{"state":"started","proof":null,"observed_at":` + at + `,"reason_code":null}`, true},
		{"unknown", `{"state":"unknown","proof":null,"observed_at":` + at + `,"reason_code":"PROOF_UNAVAILABLE"}`, true},
		{"unknown_missing_reason", `{"state":"unknown","proof":null,"observed_at":` + at + `,"reason_code":null}`, false},
		{"confirmed_without_proof", `{"state":"confirmed","proof":null,"observed_at":` + at + `,"reason_code":null}`, false},
		{"started_without_time", `{"state":"started","proof":null,"observed_at":null,"reason_code":null}`, false},
		{"unstarted_with_time", `{"state":"not_started","proof":null,"observed_at":` + at + `,"reason_code":null}`, false},
		{"no_effect", `{"state":"no_effect","proof":{"kind":"no_effect","step":"publish_target","condition_digest":` + digest + `,"reason_code":"PROOF_UNAVAILABLE","closure_digest":` + digest + `},"observed_at":` + at + `,"reason_code":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate("StepObservationV1", parse(t, tc.raw))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestFileReceiptCheckpointExactSlotsAndEmitter(t *testing.T) {
	empty := `{"state":"not_started","proof":null,"observed_at":null,"reason_code":null}`
	slots := make([]string, 0, 5)
	for _, step := range fileStepOrder {
		slots = append(slots, `"`+step+`":`+empty)
	}
	checkpoint := `{` + strings.Join(slots, ",") + `}`
	if err := Validate("FileCheckpointV1", parse(t, checkpoint)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{` + strings.Join(slots[:4], ",") + `}`, strings.Replace(checkpoint, `"temp_cleanup"`, `"extra"`, 1)} {
		if err := Validate("FileCheckpointV1", parse(t, bad)); err == nil {
			t.Fatal("invalid slots accepted")
		}
	}
	envelope := `{"identity":{"schema_version":1,"permit_id":"permit","root_id":"root","original_work_id":"work","fencing_token":1,"executor_session":"session","spec_digest":"sha256:` + strings.Repeat("0", 64) + `"},"emitter":{"mode":"dispatch_session","session":"session"},"observed_at":"2026-10-08T00:00:00.000001Z","payload":{"checkpoint":` + checkpoint + `,"closure":{"state":"unavailable","value":null,"reason_code":"PROOF_UNAVAILABLE"}}}`
	if err := Validate("FileReceiptEnvelopeV1", parse(t, envelope)); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(envelope, `"mode":"dispatch_session","session":"session"`, `"mode":"dispatch_session","session":"other"`, 1)
	if err := Validate("FileReceiptEnvelopeV1", parse(t, bad)); err == nil {
		t.Fatal("wrong emitter accepted")
	}
}
