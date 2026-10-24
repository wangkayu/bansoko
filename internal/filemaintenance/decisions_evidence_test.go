package filemaintenance

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// TestLoadPlacementCleanupEvidenceNoopReturnsNonNilEmptyMap pins the no-op
// contract: nil db or no resolvable ids never queries and always returns a
// non-nil empty map.
func TestLoadPlacementCleanupEvidenceNoopReturnsNonNilEmptyMap(t *testing.T) {
	loaded, err := loadPlacementCleanupEvidence(context.Background(), nil, []string{"plc-1"})
	if err != nil {
		t.Fatalf("nil db: %v", err)
	}
	if loaded == nil || len(loaded) != 0 {
		t.Fatalf("nil db must return a non-nil empty map, got %#v", loaded)
	}
	loaded, err = loadPlacementCleanupEvidence(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("nil ids: %v", err)
	}
	if loaded == nil || len(loaded) != 0 {
		t.Fatalf("nil ids must return a non-nil empty map, got %#v", loaded)
	}
	// Empty/duplicate-only ids resolve to no statements at all.
	placements, coverage := placementCleanupEvidenceQueries(nil)
	if len(placements) != 0 || len(coverage) != 0 {
		t.Fatalf("no ids must build no statements, got %d/%d", len(placements), len(coverage))
	}
	placements, coverage = placementCleanupEvidenceQueries([]string{"", ""})
	if len(placements) != 0 || len(coverage) != 0 {
		t.Fatalf("empty ids must build no statements, got %d/%d", len(placements), len(coverage))
	}
}

// TestPlacementCleanupEvidenceQueriesBoundedParameterized pins the safe SQL
// seam: dedupe in first-seen order, generated numeric placeholders only (no
// id interpolated into the text), and chunks bounded by
// placementCleanupEvidenceChunk.
func TestPlacementCleanupEvidenceQueriesBoundedParameterized(t *testing.T) {
	ids := []string{"plc-b", "plc-a", "plc-b", "", "plc-c"}
	placements, coverage := placementCleanupEvidenceQueries(ids)
	if len(placements) != 1 || len(coverage) != 1 {
		t.Fatalf("one chunk expected, got %d placement / %d coverage statements", len(placements), len(coverage))
	}
	wantArgs := []any{"plc-b", "plc-a", "plc-c"}
	for name, query := range map[string]placementCleanupEvidenceQuery{"placements": placements[0], "coverage": coverage[0]} {
		if len(query.Args) != len(wantArgs) {
			t.Fatalf("%s args = %#v, want %#v", name, query.Args, wantArgs)
		}
		for index := range wantArgs {
			if query.Args[index] != wantArgs[index] {
				t.Fatalf("%s args = %#v, want %#v", name, query.Args, wantArgs)
			}
			if strings.Contains(query.Text, wantArgs[index].(string)) {
				t.Fatalf("%s query text interpolates an id: %s", name, query.Text)
			}
			placeholder := "$" + strconv.Itoa(index+1)
			if !strings.Contains(query.Text, placeholder) {
				t.Fatalf("%s query text misses placeholder %s: %s", name, placeholder, query.Text)
			}
		}
	}
	if !strings.Contains(coverage[0].Text, "ORDER BY placement_id, coverage_ordinal") {
		t.Fatalf("coverage query must preserve stored ordinal order: %s", coverage[0].Text)
	}

	// Chunking: chunk*2+1 unique ids build exactly three bounded statements
	// per kind, each within the placeholder bound.
	bulk := make([]string, 0, placementCleanupEvidenceChunk*2+1)
	for index := 0; index < placementCleanupEvidenceChunk*2+1; index++ {
		bulk = append(bulk, "plc-"+strconv.Itoa(index))
	}
	placements, coverage = placementCleanupEvidenceQueries(bulk)
	if len(placements) != 3 || len(coverage) != 3 {
		t.Fatalf("expected 3 chunks per kind, got %d/%d", len(placements), len(coverage))
	}
	for index, query := range placements {
		if len(query.Args) > placementCleanupEvidenceChunk {
			t.Fatalf("chunk %d exceeds the placeholder bound: %d args", index, len(query.Args))
		}
	}
	if got := len(placements[2].Args); got != 1 {
		t.Fatalf("last chunk must carry the single remainder id, got %d args", got)
	}
}

// TestPlacementPathsFromExactKeys pins the same-path suppression input: the
// derived path map keeps exact stored paths (no normalization), a missing
// winner stays conservative false, and a missing loser entry differs from an
// empty path.
func TestPlacementPathsFromExactKeys(t *testing.T) {
	evidence := map[string]placementCleanupEvidence{
		"plc-winner": {PhysicalPath: "/Library/Show/E01.mkv", Fingerprint: "fp-w"},
		"plc-loser":  {PhysicalPath: "/library/show/E01.mkv", Fingerprint: "fp-l"},
	}
	paths := placementPathsFrom(evidence)
	if len(paths) != 2 {
		t.Fatalf("paths = %#v", paths)
	}
	if paths["plc-winner"] != "/Library/Show/E01.mkv" || paths["plc-loser"] != "/library/show/E01.mkv" {
		t.Fatalf("paths must be the exact stored values: %#v", paths)
	}
	// Exact comparison, no normalization: differing case is not same-path.
	if samePlacementPathFrom(paths, &PlacementRef{PlacementID: "plc-winner"}, PlacementRef{PlacementID: "plc-loser"}) {
		t.Fatal("same-path comparison must be exact, not normalized")
	}
	// Missing winner evidence stays conservative false.
	loserOnly := placementPathsFrom(map[string]placementCleanupEvidence{"plc-loser": {PhysicalPath: "/x"}})
	if samePlacementPathFrom(loserOnly, &PlacementRef{PlacementID: "plc-winner"}, PlacementRef{PlacementID: "plc-loser"}) {
		t.Fatal("missing winner path must be conservative false")
	}
	// Identical exact paths still suppress cleanup for the loser.
	same := placementPathsFrom(map[string]placementCleanupEvidence{
		"plc-winner": {PhysicalPath: "/x"},
		"plc-loser":  {PhysicalPath: "/x"},
	})
	if !samePlacementPathFrom(same, &PlacementRef{PlacementID: "plc-winner"}, PlacementRef{PlacementID: "plc-loser"}) {
		t.Fatal("identical exact paths must suppress the loser cleanup")
	}
}
