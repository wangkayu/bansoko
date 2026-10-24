package application

import (
	"bansoko/internal/namingvars"
	"encoding/json"
	"testing"
)

// These are internal engine projections, not new disabled wire fields.
func TestTypedPlanVarsRoundTripPreservesEmptyAndError(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		output       namingvars.Vars
	}{
		{"disabled", "ok", namingvars.Vars{}},
		{"frozen_empty", "ok", namingvars.Vars{}},
		{"frozen_error", "error", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := typedPlanPolicy(nil, nil, namingvars.FrozenEvaluation{Status: tc.status, Output: tc.output})
			if err != nil {
				t.Fatal(err)
			}
			var policy struct {
				Vars namingvars.FrozenEvaluation `json:"vars"`
			}
			if err := json.Unmarshal(raw, &policy); err != nil {
				t.Fatal(err)
			}
			if policy.Vars.Status != tc.status {
				t.Fatal("status changed")
			}
			snapshot, err := marshalConfigSnapshot(effectiveConfig{vars: &policy.Vars}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(snapshot, &policy); err != nil {
				t.Fatal(err)
			}
			if tc.status == "ok" {
				if err := policy.Vars.Output.Validate(); err != nil {
					t.Fatal(err)
				}
			} else if policy.Vars.Output != nil {
				t.Fatal("error output became a successful empty object")
			}
		})
	}
}
