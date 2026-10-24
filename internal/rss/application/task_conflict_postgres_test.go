package application

import (
	"context"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/workitems"
)

// TestDownloadTaskSameReleaseConflictRejectsSecondInsert pins the qB
// auto-chain task-level key conflict: two acceptances of the same download
// attempt of a release share one acceptance key, so a second task insert for
// the same release (different task id, different frozen payload) must fail and
// leave no partial rows behind. (A release owns a second task only as a later
// download attempt, after its first download failed.) The loser
// transaction is expected to retry and converge through the terminal-replay
// branch, so the first task's frozen input and its single qb_submit work item
// must stay untouched.
func TestDownloadTaskSameReleaseConflictRejectsSecondInsert(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, _ := seedRSSOwner(t, db, false, true)
	insertReleaseForEvaluation(t, db, sourceID, "release-key-conflict", "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")
	seedNativeTorrent(t, db, "torrent-key-conflict", "release-key-conflict")

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.loadReleaseSnapshot(context.Background(), "release-key-conflict")
	if err != nil {
		t.Fatal(err)
	}
	firstItem := acquisitiondomain.FeedItem{
		GUID: "guid:release-key-conflict", Title: "[Group] Test Show - 01 1080p",
		Enclosure: "https://downloads.example.test/show-01.torrent",
	}
	secondItem := acquisitiondomain.FeedItem{
		GUID: "guid:release-key-conflict", Title: "[OtherGroup] Test Show - 01 720p",
		Enclosure: "https://downloads.example.test/show-01-720p.torrent",
	}

	ctx := context.Background()

	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstEvidence, err := resolveReleaseParserEvidence(ctx, snapshot, firstItem.Title)
	if err != nil {
		t.Fatalf("resolve release parser evidence: %v", err)
	}
	firstTaskID, _, err := createDownloadTaskAndWorkItem(ctx, tx1, db, store, snapshot, firstItem, firstEvidence)
	if err != nil {
		tx1.Rollback()
		t.Fatalf("first task insert: %v", err)
	}
	// Mirror the production release_evaluate transaction: the circular
	// (deferred) ownership FK requires release.download_task_id to point at
	// the new task before commit.
	if _, err := tx1.ExecContext(ctx, `
UPDATE releases SET download_task_id = $1, verdict = 'accepted_pending_download', reason_code = 'accepted_pending_download'
WHERE id = 'release-key-conflict'`, firstTaskID); err != nil {
		tx1.Rollback()
		t.Fatalf("attach first task to release: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("commit first task: %v", err)
	}

	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondEvidence, err := resolveReleaseParserEvidence(ctx, snapshot, secondItem.Title)
	if err != nil {
		tx2.Rollback()
		t.Fatalf("resolve second release parser evidence: %v", err)
	}
	_, _, err = createDownloadTaskAndWorkItem(ctx, tx2, db, store, snapshot, secondItem, secondEvidence)
	if err == nil {
		tx2.Rollback()
		t.Fatal("different frozen payload under the same acceptance key must be rejected")
	}
	tx2.Rollback()
	if !strings.Contains(err.Error(), "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("second acceptance error = %v, want same-key payload conflict", err)
	}

	var taskCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM download_tasks WHERE release_id = 'release-key-conflict'`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("download task count = %d, want exactly 1", taskCount)
	}
	var frozenInput, effectKey string
	if err := db.QueryRowContext(ctx, `SELECT frozen_input::text, idempotency_key FROM download_tasks WHERE id = $1`, firstTaskID).Scan(&frozenInput, &effectKey); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frozenInput, "show-01.torrent") {
		t.Fatalf("first task frozen input was mutated by the conflict: %s", frozenInput)
	}
	if strings.Contains(frozenInput, "720p") {
		t.Fatalf("second payload leaked into the surviving task: %s", frozenInput)
	}
	var workItemCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_item_history WHERE owner_type = 'download_task' AND kind = 'qb_submit'`).Scan(&workItemCount); err != nil {
		t.Fatal(err)
	}
	if workItemCount != 1 {
		t.Fatalf("qb_submit work item count = %d, want exactly 1 (no orphan from the failed insert)", workItemCount)
	}
}
