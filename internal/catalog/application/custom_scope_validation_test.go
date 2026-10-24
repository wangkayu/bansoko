package application

import "testing"

func TestCustomSnapshotScopeDoesNotRelaxProviderIdentifiers(t *testing.T) {
	entry, group := CustomSnapshotScope("candidate#3", "https://example.test/feed", "Only Group")
	if err := validateFeedSnapshotScope("custom", entry, group); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ provider, entry, group string }{{"custom", "123", "456"}, {"custom", entry, "12"}, {"mikan", entry, group}, {"anibt", entry, group}} {
		if err := validateFeedSnapshotScope(tc.provider, tc.entry, tc.group); err == nil {
			t.Fatalf("invalid scope accepted: %+v", tc)
		}
	}
	if err := validateFeedSnapshotScope("mikan", "123", "456"); err != nil {
		t.Fatal(err)
	}
}
