package domain

import "testing"

func TestCatalogStatusesRemainDistinct(t *testing.T) {
	if StatusOutage == StatusStale || StatusZeroResult == StatusOutage {
		t.Fatal("catalog outage, stale and zero-result states must remain distinct")
	}
}

func TestItemPreferenceValidation(t *testing.T) {
	for _, preference := range []ItemPreference{PreferenceNeutral, PreferenceWatchlisted, PreferenceIgnored} {
		if err := preference.Validate(); err != nil {
			t.Fatalf("valid preference %q rejected: %v", preference, err)
		}
	}
	if err := ItemPreference("favorite").Validate(); err == nil {
		t.Fatal("invalid catalog preference was accepted")
	}
}
