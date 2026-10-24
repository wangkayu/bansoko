package application

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfidenceExactSourceProjection(t *testing.T) {
	for source, want := range map[string]string{"0": "0", "1": "1", "1.0": "1", "0.80": "0.8", "0.000": "0", "0.80000000000000001": "0.80000000000000001", "0." + strings.Repeat("1", 64): "0." + strings.Repeat("1", 64)} {
		got, err := projectConfidenceNumber(json.RawMessage(source))
		if err != nil || got != want {
			t.Fatalf("%s -> %s %v", source, got, err)
		}
	}
	for _, source := range []string{"-0", "-0.1", "1.1", "8e-1", "0.8e0", "01", ".8", "null", `"0.8"`, "NaN", "Inf", "0." + strings.Repeat("1", 65)} {
		if _, err := projectConfidenceNumber(json.RawMessage(source)); err == nil {
			t.Fatalf("accepted source %s", source)
		}
	}
}
func TestBindingSnapshotRetainsExactNumber(t *testing.T) {
	raw := []byte(`{"version":"subtitle-binding-1","method":"episode_match","confidence":0.80000000000000001,"diagnostics":[]}`)
	value, err := decodeBindingSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"confidence":0.80000000000000001`) {
		t.Fatalf("lost precision: %s", encoded)
	}
}
