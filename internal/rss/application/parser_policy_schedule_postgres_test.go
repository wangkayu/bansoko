package application

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"context"
	"strings"
	"testing"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// A parser kind/profile change must schedule reevaluation for exactly the live,
// unsubmitted Releases of that instance: a Release that already produced a
// download task is never re-evaluated, and a terminal verdict is skipped. The
// call is idempotent because it merges into an existing live evaluation.
func TestScheduleUnsubmittedReleaseReevaluationEnqueuesOnlyEligibleReleases(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	ctx := context.Background()

	insertReleaseForEvaluation(t, db, sourceID, "reeval-eligible", "[Group] Test Show - 01 1080p", "https://downloads.example.test/01.torrent")
	insertReleaseForEvaluation(t, db, sourceID, "reeval-second", "[Group] Test Show - 02 1080p", "https://downloads.example.test/02.torrent")
	insertReleaseForEvaluation(t, db, sourceID, "reeval-terminal", "[Group] Test Show - 03 1080p", "https://downloads.example.test/03.torrent")
	insertReleaseForEvaluation(t, db, sourceID, "reeval-submitted", "[Group] Test Show - 04 1080p", "https://downloads.example.test/04.torrent")

	// One Release is already terminal, another already produced a download task.
	// The download-task FK is a deferred two-way reference
	// ((release_id, id) -> releases(id, download_task_id)), so the task row
	// and the release pointer must be written in one transaction.
	if _, err := db.ExecContext(ctx, `UPDATE releases SET verdict = 'acquisition_failed' WHERE id = 'reeval-terminal'`); err != nil {
		t.Fatal(err)
	}
	seedTx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, seedTx, "reeval-root", "task-reeval", "reeval-submitted", "task-reeval-key", `{}`, `{}`, "queued", `null`)
	// A release carrying a download task must also carry a matching verdict.
	if _, err := seedTx.ExecContext(ctx, `
UPDATE releases SET download_task_id = 'task-reeval', verdict = 'submitted'
WHERE id = 'reeval-submitted'`); err != nil {
		_ = seedTx.Rollback()
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatalf("commit download task seed: %v", err)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}

	schedule := func() {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.ScheduleUnsubmittedReleaseReevaluationTx(ctx, txscope.Wrap(tx), "anime-rss-test"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("schedule reevaluation: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	count := func(releaseID string) int {
		t.Helper()
		var total int
		if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history
WHERE owner_type = 'release' AND owner_id = $1 AND kind = 'release_evaluate'`, releaseID).Scan(&total); err != nil {
			t.Fatal(err)
		}
		return total
	}

	schedule()

	for _, releaseID := range []string{"reeval-eligible", "reeval-second"} {
		if got := count(releaseID); got != 1 {
			t.Fatalf("eligible release %s has %d evaluation work items, want 1", releaseID, got)
		}
	}
	for _, releaseID := range []string{"reeval-terminal", "reeval-submitted"} {
		if got := count(releaseID); got != 0 {
			t.Fatalf("ineligible release %s has %d evaluation work items, want 0", releaseID, got)
		}
	}

	// The trigger records why the evaluation was scheduled.
	var snapshot string
	if err := db.QueryRowContext(ctx, `
SELECT input_snapshot::text FROM work_item_history
WHERE owner_type = 'release' AND owner_id = 'reeval-eligible' AND kind = 'release_evaluate'`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, "parser_policy_change") {
		t.Fatalf("input snapshot %s does not record the trigger", snapshot)
	}

	// Re-running merges instead of duplicating.
	schedule()
	if got := count("reeval-eligible"); got != 1 {
		t.Fatalf("second schedule created %d evaluation work items, want the live one merged", got)
	}
}
