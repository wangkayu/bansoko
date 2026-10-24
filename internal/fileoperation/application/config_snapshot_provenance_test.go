package application

import (
	"encoding/json"
	"testing"

	"bansoko/internal/parser"
)

// The FileOperation frozen config snapshot must name the parser frozen on the
// Release media fact snapshot, inside its existing JSON contract, so the plan
// can be explained and replayed without reading current instance parser policy.
func TestMarshalConfigSnapshotFreezesParserProvenance(t *testing.T) {
	provenance := parser.Provenance{
		Kind:                  "regex",
		Config:                json.RawMessage(`{"episode_patterns":["E([0-9]+)"]}`),
		ConfigFingerprint:     "sha256:selected-profile",
		ConfigVersion:         "regex-config-v1",
		ImplementationVersion: "regex-v1",
		OutputContractVersion: "episode-evidence-v1",
	}
	raw, err := marshalConfigSnapshot(effectiveConfig{versions: map[string]int64{}}, nil, &provenance)
	if err != nil {
		t.Fatalf("marshal config snapshot: %v", err)
	}
	var snapshot struct {
		Parser *parser.Provenance `json:"parser"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode config snapshot %s: %v", raw, err)
	}
	if snapshot.Parser == nil {
		t.Fatalf("config snapshot %s carries no parser provenance", raw)
	}
	if snapshot.Parser.Kind != provenance.Kind ||
		snapshot.Parser.ConfigVersion != provenance.ConfigVersion ||
		snapshot.Parser.ImplementationVersion != provenance.ImplementationVersion ||
		snapshot.Parser.OutputContractVersion != provenance.OutputContractVersion ||
		snapshot.Parser.ConfigFingerprint != provenance.ConfigFingerprint ||
		string(snapshot.Parser.Config) != string(provenance.Config) {
		t.Fatalf("frozen parser provenance = %+v, want %+v", *snapshot.Parser, provenance)
	}
}

// A Release without a media fact snapshot has no frozen parser evidence, so the
// key is absent rather than filled from current policy.
func TestMarshalConfigSnapshotOmitsMissingParserProvenance(t *testing.T) {
	raw, err := marshalConfigSnapshot(effectiveConfig{versions: map[string]int64{}}, nil, nil)
	if err != nil {
		t.Fatalf("marshal config snapshot: %v", err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode config snapshot %s: %v", raw, err)
	}
	if _, present := snapshot["parser"]; present {
		t.Fatalf("config snapshot %s invented parser provenance", raw)
	}
}

// The frozen validation (blocked placeholder) path carries the same provenance.
func TestMarshalConfigSnapshotKeepsProvenanceWithValidation(t *testing.T) {
	provenance := parser.Provenance{Kind: "regex", ConfigFingerprint: "sha256:frozen"}
	raw, err := marshalConfigSnapshot(effectiveConfig{versions: map[string]int64{}},
		map[string]any{"code": "file_operation_blocked", "reason": "blocked"}, &provenance)
	if err != nil {
		t.Fatalf("marshal config snapshot: %v", err)
	}
	var snapshot struct {
		Parser     *parser.Provenance `json:"parser"`
		Validation map[string]any     `json:"validation"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode config snapshot %s: %v", raw, err)
	}
	if snapshot.Parser == nil || snapshot.Parser.ConfigFingerprint != "sha256:frozen" {
		t.Fatalf("validation snapshot lost the parser provenance: %s", raw)
	}
	if snapshot.Validation["code"] != "file_operation_blocked" {
		t.Fatalf("validation snapshot lost its validation block: %s", raw)
	}
}
