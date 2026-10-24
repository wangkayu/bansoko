package application

import (
	"testing"

	"bansoko/internal/workitems"
)

// Three closed sets have to name the same owners for a manual retry to work at
// all: the store's admission (which decides whether the request key may be
// written), the schema column that key lands in, and this package's version
// reader (which decides whether the owner row can be locked). Before this test
// each was a separate literal switch or map, and the store's own conformance
// test caught postprocessing_plan admitted with no column and no reader.
//
// The schema half is proven on the database side by
// workitems.TestManualRetryOwnerTypesAreInsertable; this side proves the two Go
// halves cannot drift from each other without a gate failing.
func TestOwnerVersionReaderCoversTheManualRetryAllowlist(t *testing.T) {
	admitted := make(map[string]bool)
	for _, ownerType := range workitems.ManualRetryOwnerTypes() {
		admitted[ownerType] = true
	}
	for ownerType := range ownerVersionReaders {
		if !admitted[ownerType] {
			t.Errorf("ownerVersionReaders resolves %q, which manual retry does not admit", ownerType)
		}
	}
	for ownerType := range admitted {
		if _, ok := ownerVersionReaders[ownerType]; !ok {
			t.Errorf("manual retry admits %q but ownerVersionReaders has no version reader: every retry of it would answer OWNER_TYPE_INVALID", ownerType)
		}
	}
}
