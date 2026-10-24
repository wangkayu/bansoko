package application

import "testing"

func TestGroupScopeRequiresExactProviderSelection(t *testing.T) {
	for _, tc := range []struct {
		kind                   string
		exists, selected, want bool
	}{
		{"custom_rss", false, false, true}, {"provider_group", false, false, false}, {"provider_group", true, false, false}, {"provider_group", true, true, true},
	} {
		if got := groupScopeAllowed(tc.kind, tc.exists, tc.selected); got != tc.want {
			t.Fatalf("%+v: got %t", tc, got)
		}
	}
}
