package application

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapturedVarsEvidenceProtocolClassification(t *testing.T) {
	for _, tc := range []struct {
		name, input, capture, want string
		invalid                    bool
	}{
		{"historical", `{"config":{"vars":{"mode":"frozen","status":"ok"}}}`, `{"vars_protocol":{"version":0}}`, "VARS_EVALUATION_RECORD_UNAVAILABLE", false},
		{"disabled", `{"config":{"vars":{"mode":"disabled"}}}`, `{"vars_protocol":{"version":1}}`, "absent", false},
		{"required_missing", `{"config":{"vars":{"mode":"frozen","status":"ok"}}}`, `{"vars_protocol":{"version":1}}`, "", true},
		{"protocol_missing", `{"config":{"vars":{"mode":"frozen","status":"ok"}}}`, `{}`, "", true},
		{"protocol_version_missing", `{"config":{"vars":{"mode":"frozen","status":"ok"}}}`, `{"vars_protocol":{}}`, "", true},
		{"historical_backfill", `{"config":{"vars":{"mode":"frozen","status":"ok"}}}`, `{"vars_protocol":{"version":0},"vars_evaluation":{"canonical_utf8":"{}"}}`, "", true},
		{"disabled_digest", `{"config":{"vars":{"mode":"disabled"}}}`, `{"vars_protocol":{"version":1,"digest":"unexpected"}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &artifactPreparedBatch{input: json.RawMessage(tc.input)}
			b.capture.Facts = json.RawMessage(tc.capture)
			v, err := capturedVarsResult(b)
			if tc.invalid {
				if err == nil || err.Error() != "VARS_EVALUATION_EVIDENCE_INCONSISTENT" {
					t.Fatalf("%v %v", v, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(v)
			if !strings.Contains(string(raw), tc.want) {
				t.Fatalf("%s", raw)
			}
		})
	}
}
