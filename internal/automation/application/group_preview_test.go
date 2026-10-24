package application

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
)

// The preview ranks the persisted rows with the unsaved per-group modes laid
// over them, exactly as a save syncs them, and the same pure ranking the
// recompute uses decides.
func TestOverlayDiscoveryRowsRanksTheUnsavedModes(t *testing.T) {
	persisted := map[string]discoveryObservation{
		"alpha": {name: "Alpha", available: true, count: true},
		"beta":  {name: "Beta", available: true, count: true},
	}
	never := false
	overlay := map[string]ProviderGroupOverlay{
		"anibt:alpha": {Manual: &never},
		"mikan:beta":  {Manual: &never}, // another provider's group never applies
	}
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "gamma")
	result := providerdomain.GroupReadResult{Provider: providerdomain.ProviderAniBT, Groups: []providerdomain.GroupSnapshotEntry{{Ref: ref, DisplayName: "Gamma"}}}
	policy := discoveryPolicy{MaxRSS: 1, ProviderDefaults: map[providerdomain.Kind]selectiondomain.ProviderDefaultPolicy{providerdomain.ProviderAniBT: {Mode: selectiondomain.Ranked, Score: 50}}}

	decisions, err := rankDiscoveryScope("anibt", overlayDiscoveryRows("anibt", persisted, overlay), result, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	outcome := map[string]selectiondomain.GroupSelectionDecision{}
	for _, decision := range decisions {
		outcome[decision.Group.Ref.ExternalID] = decision
	}
	if outcome["alpha"].Reason != selectiondomain.ReasonPolicyNever {
		t.Fatalf("alpha is overlaid never: %+v", outcome["alpha"])
	}
	if outcome["beta"].Outcome != selectiondomain.GroupSelected || outcome["gamma"].Outcome == selectiondomain.GroupSelected {
		t.Fatalf("beta wins the one slot by key order over gamma: %+v", outcome)
	}
	if persisted["alpha"].manual != nil {
		t.Fatal("the overlay must not mutate the persisted rows")
	}
}
