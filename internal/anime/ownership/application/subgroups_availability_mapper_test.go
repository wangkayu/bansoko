package application

import (
	"bansoko/internal/transport/httpapi/generated"
	"encoding/json"
	"testing"
)

func TestSubgroupsAvailabilityGeneratedBothDirections(t *testing.T) {
	for _, tc := range []struct {
		available  bool
		generation int64
	}{{true, 17}, {false, 23}} {
		reason := "max_rss_exhausted"
		wire := generated.SubgroupsOwnerResult{EffectiveSelection: []generated.ProviderGroupSelection{{Available: &tc.available, AvailabilityGeneration: &tc.generation, ReasonCode: &reason}}}
		domain := subgroupsOwnerFromGenerated(wire)
		if domain.EffectiveSelection[0].Available != tc.available || domain.EffectiveSelection[0].AvailabilityGeneration != tc.generation {
			t.Fatal("generated input lost availability")
		}
		// The patch response is an owner read: it must carry the decision reason
		// in both directions, or a write silently erases the explanation.
		if domain.EffectiveSelection[0].ReasonCode == nil || *domain.EffectiveSelection[0].ReasonCode != reason {
			t.Fatal("generated input lost the selection reason")
		}
		output := subgroupsOwnerToGenerated(domain)
		row := output.EffectiveSelection[0]
		if row.Available == nil || *row.Available != tc.available || row.AvailabilityGeneration == nil || *row.AvailabilityGeneration != tc.generation {
			t.Fatal("generated output lost availability")
		}
		if row.ReasonCode == nil || *row.ReasonCode != reason {
			t.Fatal("generated output lost the selection reason")
		}
		raw, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Selection []struct {
				Available  *bool   `json:"available"`
				Generation *int64  `json:"availability_generation"`
				Reason     *string `json:"reason_code"`
			} `json:"effective_selection"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Selection) != 1 || response.Selection[0].Available == nil || *response.Selection[0].Available != tc.available || response.Selection[0].Generation == nil || *response.Selection[0].Generation != tc.generation {
			t.Fatalf("wire response=%s", raw)
		}
		if response.Selection[0].Reason == nil || *response.Selection[0].Reason != reason {
			t.Fatalf("wire response lost the selection reason=%s", raw)
		}
	}
}
