package target

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"bansoko/internal/batchplan"
)

// A batch plan is refused for its size only when it would not fit the media
// fact snapshot's stored bound next to the release's own facts; a box set of
// several hundred deep files fits.
func TestBatchPlanTooLargeMeasuresThePlanAgainstTheSnapshotBound(t *testing.T) {
	plan := func(files int) batchplan.Plan {
		result := batchplan.Plan{Version: "v", UnclassifiedRoute: batchplan.RouteSkip}
		dir := strings.Repeat("Detective Conan Season 01 [Ma10p_1080p]/", 4)
		for index := 0; index < files; index++ {
			result.Files = append(result.Files, batchplan.File{SourcePath: fmt.Sprintf("%sDetective Conan Booklet Page %05d.png", dir, index), Level: "nested", Kind: "other", Outcome: batchplan.OutcomeIgnoredKind})
		}
		return result
	}
	boxSet := plan(619)
	if BatchPlanTooLarge(boxSet) {
		t.Fatal("a 619-file plan was refused")
	}
	huge := plan(9000)
	raw, err := json.Marshal(huge)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= batchPlanRawLimit {
		t.Fatalf("the fixture plan is %d bytes, not over the %d byte bound", len(raw), batchPlanRawLimit)
	}
	if !BatchPlanTooLarge(huge) {
		t.Fatalf("a %d byte plan was not refused", len(raw))
	}
	if batchPlanRawLimit >= releaseMediaFactsRawLimit || releaseMediaFactsRawLimit >= MaxReleaseMediaFactsBytes {
		t.Fatal("the raw bounds leave no headroom below the stored bound")
	}
}
