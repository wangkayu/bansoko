package workitems

import "testing"

func TestKindHasExternalEffect(t *testing.T) {
	effectBearing := map[string]bool{
		KindQBSubmit:               true,
		KindFileExecute:            true,
		KindPluginDeliver:          true,
		KindAlignmentExecute:       true,
		KindReplacementDelete:      true,
		KindOffsetMigrationExecute: true,
		KindFileManagerExecute:     true,
	}
	for _, shape := range ownerKinds {
		for _, kind := range shape.kinds {
			if got := KindHasExternalEffect(kind); got != effectBearing[kind] {
				t.Fatalf("KindHasExternalEffect(%q) = %t, want %t", kind, got, effectBearing[kind])
			}
		}
	}
}
