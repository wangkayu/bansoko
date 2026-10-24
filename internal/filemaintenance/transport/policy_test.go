package transport

import (
	"encoding/json"
	"testing"

	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func TestPolicyInputMapsPointersAndSafety(t *testing.T) {
	interval := 3600
	alignment := true
	safety := map[string]interface{}{"max_operations": 7, "custom": "value"}
	input, err := PolicyInput(generated.FileMaintenancePolicyInput{
		Enabled:             true,
		ScanIntervalSeconds: &interval,
		AlignmentEnabled:    &alignment,
		ExpectedVersion:     4,
		Safety:              &safety,
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.ExpectedVersion != 4 || !input.Enabled || input.ScanIntervalSeconds == nil || *input.ScanIntervalSeconds != interval || input.AlignmentEnabled == nil || !*input.AlignmentEnabled {
		t.Fatalf("mapped input = %+v", input)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(input.Safety, &got); err != nil {
		t.Fatal(err)
	}
	if got["custom"] != "value" || got["max_operations"] != float64(7) {
		t.Fatalf("safety = %#v", got)
	}
	safety["custom"] = "changed"
	if string(input.Safety) == string(mustJSON(t, safety)) {
		t.Fatal("mapped safety aliases input map")
	}
}

func TestPolicyInputUsesDefaultSafety(t *testing.T) {
	input, err := PolicyInput(generated.FileMaintenancePolicyInput{})
	if err != nil {
		t.Fatal(err)
	}
	if string(input.Safety) != `{"max_operations":100}` {
		t.Fatalf("default safety = %s", input.Safety)
	}
}

func TestPolicyViewMapsRawSafetyAndPointers(t *testing.T) {
	interval := 1800
	alignment := false
	value := filemaintenance.PolicyView{
		ScopeKey:            "anime:one",
		Enabled:             true,
		ScanIntervalSeconds: &interval,
		AlignmentEnabled:    &alignment,
		Safety:              json.RawMessage(`{"max_operations":9,"custom":true}`),
		Version:             8,
	}
	view := PolicyView(value)
	if view.ScopeKey != value.ScopeKey || !view.Enabled || view.Version != 8 || view.ScanIntervalSeconds == nil || *view.ScanIntervalSeconds != interval || view.AlignmentEnabled == nil || *view.AlignmentEnabled || view.Safety["custom"] != true {
		t.Fatalf("mapped view = %+v", view)
	}
	view.Safety["custom"] = false
	if string(value.Safety) != `{"max_operations":9,"custom":true}` {
		t.Fatal("mapped safety aliases application raw JSON")
	}
	*view.ScanIntervalSeconds = 1
	if *value.ScanIntervalSeconds != interval {
		t.Fatal("mapped interval aliases application pointer")
	}
}

func mustJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
