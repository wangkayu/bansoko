package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
	"context"
	"database/sql"
	"errors"
	"testing"
)

func typedErrorCode(err error) string {
	var business *apperrors.Business
	if errors.As(err, &business) {
		return business.Code
	}
	var validation *apperrors.Validation
	if errors.As(err, &validation) {
		return validation.Code
	}
	return ""
}
func assertNoOneShotRows(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"one_shot_acquisitions", "download_tasks", "work_items", "acquisition_targets", "releases"} {
		if n := animetest.CountRows(t, db, table); n != 0 {
			t.Fatalf("%s has %d rows after rejection", table, n)
		}
	}
}
func TestAcquireSeedOnceAllFilteredRejectsWithoutWrites(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	// Explicitly reproduce an unconfigured planner; the pure gate still rejects.
	service.targetAcquisitionConfigured = false
	service.targetProbe = nil
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, func(d generated.AnimeDraft) generated.AnimeDraft {
		d.Rules.Sections.FileFilter = &generated.FileFilterRulesOverride{}
		if err := d.Rules.Sections.FileFilter.FromFileFilterRulesReplace(generated.FileFilterRulesReplace{Mode: "replace", Value: generated.FileFilterRules{MustContain: `\.never$`, NotIn: ""}}); err != nil {
			t.Fatal(err)
		}
		return d
	})
	input.Seed = bindSeedIdentity(seed, snapshot)
	_, err := service.AcquireSeedOnce(context.Background(), input, "all-filtered-no-write-0001")
	if typedErrorCode(err) != "FILE_SELECTION_EMPTY" {
		t.Fatalf("got %v want FILE_SELECTION_EMPTY", err)
	}
	assertNoOneShotRows(t, db)
}
