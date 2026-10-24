package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// TestManualFileExecutionRetryRetiredPreservesGraph proves the per-owner
// manual retry no longer admits a file operation: a failed file effect is
// retried only through the record's import retry. Every request is refused
// before any write.
func TestManualFileExecutionRetryRetiredPreservesGraph(t *testing.T) {
	db := animetest.OpenTestDB(t)
	animetest.SeedReleaseOwnerChain(t, db, "source-retired-file-retry")
	animetest.SeedCompletedFileOp(t, db, "source-retired-file-retry", 1, false, "failed")
	op := "op-rel-ep-1"
	var work string
	if err := db.QueryRow(`SELECT id FROM work_item_history WHERE owner_type='file_operation' AND owner_id=$1 AND kind='file_execute' ORDER BY id LIMIT 1`, op).Scan(&work); err != nil {
		t.Fatal(err)
	}
	owner := disposabledb.Owner(t)
	defer owner.Close()
	before := acquisitionfixture.Snapshot(t, owner)
	retry, err := NewManualRetryService(db, &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ owner, work string }{{op, "missing-work"}, {"wrong-owner", work}, {op, work}} {
		err := retry.ManualRetry(context.Background(), workitems.OwnerFileOperation, request.owner, request.work, "retired file execution", "retired-file-retry-key")
		animetest.AssertBusinessCode(t, err, 422, "OWNER_TYPE_INVALID")
		if acquisitionfixture.Snapshot(t, owner) != before {
			t.Fatal("refused retry changed graph")
		}
	}
}
