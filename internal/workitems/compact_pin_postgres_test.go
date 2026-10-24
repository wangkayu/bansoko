package workitems

import (
	"context"
	"testing"
	"time"

	"bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/testutil/disposabledb"
)

// PRUNE-046: acquisitions.compact_pinned keeps CompactExpired off roots whose
// detail_expired compaction cannot succeed by itself. The triggers hold it
// exact while a Release points at the root's task or a placement obligation
// exists; acquisition_compact agrees on both sides (release_bound while
// pinned, compacted once the pin is gone), and CompactExpired picks the root up
// as soon as it is unpinned.
func TestCompactPinFollowsReleaseBindingAndObligations(t *testing.T) {
	db := openTestDB(t)
	owner := disposabledb.Owner(t)
	ctx := context.Background()
	const markerID = "m-compact-pin"
	root, release, task := "acq-"+markerID, "rel-"+markerID, "task-"+markerID
	foID := "fo-" + markerID

	pinned := func() bool {
		t.Helper()
		var p bool
		if err := owner.QueryRowContext(ctx, `SELECT compact_pinned FROM acquisitions WHERE id=$1`, root).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := owner.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}

	files := createOneShotChain(t, db, markerID, []string{"pin.mkv"})
	if !pinned() {
		t.Fatal("a root whose task a Release points at is not pinned")
	}
	failOneShotFileOperation(t, db, foID, "art-"+markerID, files[0], "/library/pin.mkv")
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle root: status=%q err=%v", status, err)
	}
	var detailUntil time.Time
	if err := owner.QueryRowContext(ctx, `SELECT detail_until FROM acquisitions WHERE id=$1`, root).Scan(&detailUntil); err != nil {
		t.Fatal(err)
	}
	expired := detailUntil.Add(time.Second)
	settler.Now = func() time.Time { return expired }
	// compactStatus asks acquisition_compact itself, in a rolled-back
	// transaction, what a detail_expired compaction would do now.
	compactStatus := func() string {
		t.Helper()
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT acquisition_compact($1,'detail_expired',$2)->>'status'`, root, expired).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	compactExpired := func() int {
		t.Helper()
		n, err := settler.CompactExpired(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if status := compactStatus(); status != "release_bound" {
		t.Fatalf("pinned root compacts as %q, want release_bound", status)
	}
	if n := compactExpired(); n != 0 || !pinned() {
		t.Fatalf("CompactExpired compacted %d pinned roots", n)
	}

	// A placement obligation pins on its own.
	exec(`INSERT INTO placement_obligations(file_operation_id, state) VALUES ($1, 'pending')`, foID)
	// A Release retry detaches the failed task: the obligation still pins.
	exec(`UPDATE releases SET download_task_id=NULL, verdict='acquisition_failed' WHERE id=$1`, release)
	if !pinned() {
		t.Fatal("the obligation alone does not pin")
	}
	if status := compactStatus(); status != "dependents_present" {
		t.Fatalf("obligation-pinned root compacts as %q, want dependents_present", status)
	}
	// Rebinding the Release pins again; an evaluation that rewrites the same
	// task id is not a rebind.
	exec(`UPDATE releases SET download_task_id=$2, verdict='submitted' WHERE id=$1`, release, task)
	exec(`UPDATE releases SET download_task_id=$2 WHERE id=$1`, release, task)
	exec(`DELETE FROM placement_obligations WHERE file_operation_id=$1`, foID)
	if !pinned() {
		t.Fatal("the rebound Release does not pin")
	}
	exec(`UPDATE releases SET download_task_id=NULL, verdict='acquisition_failed' WHERE id=$1`, release)
	if pinned() {
		t.Fatal("a root with no bound Release and no obligation is still pinned")
	}

	// Unpinned and expired: acquisition_compact would compact it, and the
	// next CompactExpired pass does.
	if status := compactStatus(); status != "compacted" {
		t.Fatalf("unpinned root compacts as %q, want compacted", status)
	}
	if n := compactExpired(); n != 1 {
		t.Fatalf("CompactExpired compacted %d roots, want the unpinned one", n)
	}
	var record string
	var completionOpen bool
	if err := owner.QueryRowContext(ctx, `SELECT record_state, completion_open FROM acquisitions WHERE id=$1`, root).Scan(&record, &completionOpen); err != nil {
		t.Fatal(err)
	}
	if record != "compact" || completionOpen {
		t.Fatalf("root after compaction record_state=%s completion_open=%v", record, completionOpen)
	}
	if n := compactExpired(); n != 0 {
		t.Fatalf("a compacted root was read again (%d)", n)
	}
}
