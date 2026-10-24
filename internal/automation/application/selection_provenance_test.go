package application

import (
	selectiondomain "bansoko/internal/selection/domain"
	"testing"
)

func TestDerivedPolicyNeverBecomesManualIntent(t *testing.T) {
	for _, mode := range []selectiondomain.SelectionMode{selectiondomain.Never, selectiondomain.Always, selectiondomain.Ranked} {
		stored, manual := providerSelectionStorage(selectiondomain.GroupSelectionDecision{Group: selectiondomain.EffectiveProviderGroup{Mode: mode}})
		if stored != "auto" || manual != nil {
			t.Fatalf("derived %s manufactured manual intent: %s %v", mode, stored, manual)
		}
	}
}
