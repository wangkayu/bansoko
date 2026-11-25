package target

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
)

// The carrier form of an unplanned admission names no set and no targets.
func TestUnplannedFrozenFactsEncodeWithoutTargets(t *testing.T) {
	raw, err := json.Marshal(UnplannedFrozenFacts())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"admission":"download_required","target_count":0}` {
		t.Fatalf("unplanned frozen facts = %s", raw)
	}
}

// A torrent over the filter's inventory budget is refused by its size, not as
// an invalid path.
func TestPlanRefusesAnInventoryOverTheBudgetAsTooLarge(t *testing.T) {
	scenario := newParityScenario(t, animedomain.CollisionPolicyFail, animedomain.FileEffectRulesOperationCopy)
	size := int64(1)
	for index := 0; index < animedomain.MaxFilterInventoryEntries; index++ {
		name := fmt.Sprintf("Frame %05d.png", index)
		scenario.item.ReleaseFiles = append(scenario.item.ReleaseFiles, acquisitiondomain.ReleaseFile{SourcePath: name, Filename: name, SizeBytes: &size, MediaKind: "other"})
	}
	plan := PreflightOneShot(context.Background(), scenario.oneShot(), FilesystemProbe{})
	if plan.Admission.Reason != "torrent_too_large" {
		t.Fatalf("admission = %+v, want torrent_too_large", plan.Admission)
	}
}
