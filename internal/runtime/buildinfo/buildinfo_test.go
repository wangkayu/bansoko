package buildinfo

import "testing"

func TestParseOrder(t *testing.T) {
	for _, tc := range []struct {
		name, series, lane, sequence string
		ordered, invalid             bool
	}{
		{"legacy", "", "", "", false, false},
		{"stable", CISeries, "stable", "42", true, false},
		{"test", CISeries, "test", "42", true, false},
		{"zero", CISeries, "stable", "0", false, true},
		{"negative", CISeries, "stable", "-1", false, true},
		{"overflow", CISeries, "stable", "9223372036854775808", false, true},
		{"unknown lane", CISeries, "nightly", "42", false, true},
		{"partial", "", "test", "42", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseOrder(tc.series, tc.lane, tc.sequence)
			if got.Ordered() != tc.ordered || (err != nil) != tc.invalid {
				t.Fatalf("ParseOrder = %+v, %v", got, err)
			}
		})
	}
}

func TestCurrentUsesLinkedBuildDirtyMarker(t *testing.T) {
	originalDirty, originalMarker := Dirty, BuildDirty
	t.Cleanup(func() {
		Dirty = originalDirty
		BuildDirty = originalMarker
	})

	Dirty = false
	BuildDirty = "false"
	if current := Current(); current.Dirty {
		t.Fatalf("clean linked build was reported dirty: %+v", current)
	}

	BuildDirty = "true"
	if current := Current(); !current.Dirty {
		t.Fatalf("dirty development build was reported clean: %+v", current)
	}
}
