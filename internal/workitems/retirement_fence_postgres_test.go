package workitems

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
)

// These tests exercise the migration-089 lifecycle fence through the real
// Store APIs on a disposable PostgreSQL schema. They prove that retirement and
// an external-effect admission serialize on the lifecycle row and that exactly
// one of them can win, and that a stale Finish/Retry/checkpoint cannot write a
// retired owner's WorkItem or owner row.

// createQBSubmitItem builds one leased effect-bearing WorkItem by hand: the
// download-task owner chain is inserted directly because the fence under test
// is the admission gate, not the creator.
func createQBSubmitItem(t *testing.T, db *sql.DB, store *Store, sourceID, suffix string) (itemID string, releaseID, taskID string) {
	t.Helper()
	ctx := context.Background()
	releaseID = "release-" + suffix
	taskID = "task-" + suffix
	// The release/download-task owner link is a DEFERRABLE INITIALLY DEFERRED
	// composite foreign key, so both rows must be written in one transaction
	// with the release pointing at the task by commit.
	seedTx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer seedTx.Rollback()
	if _, err := seedTx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ($1, $2, $3, '{}', '{}', 'discovered', 'discovered')`, releaseID, sourceID, "stable-"+suffix); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, seedTx, "acq-"+suffix, taskID, releaseID, "idem-"+suffix, `{}`, `{}`, "queued", `null`)
	if _, err := seedTx.ExecContext(ctx, `
UPDATE releases SET download_task_id=$1, verdict='submitted', reason_code='submitted' WHERE id=$2`, taskID, releaseID); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	itemID, err = store.Create(ctx, tx, WorkItemSpec{
		Owner:                OwnerRef{Type: OwnerDownloadTask, ID: taskID},
		Kind:                 KindQBSubmit,
		IdempotencyKey:       "idem-" + suffix,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		t.Fatalf("create qb_submit item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return itemID, releaseID, taskID
}

func claimQBSubmit(t *testing.T, store *Store, itemID string) Claim {
	t.Helper()
	claim, err := store.ClaimDue(context.Background(), "worker-retirement", []string{KindQBSubmit}, time.Minute)
	if err != nil {
		t.Fatalf("claim qb_submit: %v", err)
	}
	if claim.ID != itemID {
		t.Fatalf("claimed %s, want %s", claim.ID, itemID)
	}
	return claim
}

// retireOwnerTxSQL is the retirement transaction body under test: lock the
// lifecycle row, terminate un-admitted work at the still-active epoch, then
// advance the lifecycle. It intentionally mirrors
// Service.DeleteAnimeSubscription and never sets a session capability.
func retireOwnerTxSQL(ctx context.Context, tx *sql.Tx, managedAnimeID string) error {
	var epoch int64
	if err := tx.QueryRowContext(ctx, `
SELECT fence_epoch FROM managed_anime
WHERE id = $1 FOR UPDATE`, managedAnimeID).Scan(&epoch); err != nil {
		return err
	}
	if err := RetireUnadmittedTx(ctx, tx, managedAnimeID, epoch); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1, retired_at = CURRENT_TIMESTAMP,
    retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND lifecycle_state = 'active' AND fence_epoch = $2`, managedAnimeID, epoch)
	return err
}

func waitForLockWaitOnQuery(t *testing.T, db *sql.DB, queryFragment string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := db.QueryRowContext(context.Background(), `
SELECT EXISTS (
    SELECT 1 FROM pg_stat_activity
    WHERE datname = current_database()
      AND pid <> pg_backend_pid()
      AND wait_event_type = 'Lock'
      AND cardinality(pg_blocking_pids(pid)) > 0
      AND query LIKE '%' || $1 || '%'
)`, queryFragment).Scan(&waiting)
		if err != nil {
			t.Fatalf("inspect lock wait: %v", err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no session waited on %q within the deadline", queryFragment)
}

// TestRetirementAdmissionGateReturnsTypedRetirement proves the effect admission
// gate itself refuses a retired owner's claim instead of creating an external
// effect after retirement.
func TestRetirementAdmissionGateReturnsTypedRetirement(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	sourceID := createSourceOwner(t, db, "src-gate-retire")
	itemID, _, _ := createQBSubmitItem(t, db, store, sourceID, "gate-retire")
	claim := claimQBSubmit(t, store, itemID)

	if err := store.AdmitManagedAnimeEffect(ctx, claim); err != nil {
		t.Fatalf("active owner admission: %v", err)
	}
	var admittedAt *time.Time
	if err := db.QueryRowContext(ctx, `SELECT external_effect_admitted_at FROM work_item_history WHERE id = $1`, itemID).Scan(&admittedAt); err != nil {
		t.Fatal(err)
	}
	if admittedAt == nil {
		t.Fatal("effect-bearing admission did not record external_effect_admitted_at")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := retireOwnerTxSQL(ctx, tx, "anime-"+sourceID); err != nil {
		t.Fatalf("retire owner: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Already-admitted work is preserved: retirement must not rewrite the row.
	row := readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.fencingToken != claim.FencingToken {
		t.Fatalf("retirement rewrote admitted work: %+v", row)
	}
	if err := store.AdmitManagedAnimeEffect(ctx, claim); !errors.Is(err, ErrOwnerRetired) {
		t.Fatalf("post-retirement admission = %v, want ErrOwnerRetired", err)
	}
}

// TestRetirementWinsRaceAgainstEffectAdmission blocks the admission gate on the
// lifecycle row, lets retirement win, and proves the gate then resolves to the
// single legal outcome: no external effect is admitted.
func TestRetirementWinsRaceAgainstEffectAdmission(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	sourceID := createSourceOwner(t, db, "src-race-retire")
	itemID, _, _ := createQBSubmitItem(t, db, store, sourceID, "race-retire")
	claim := claimQBSubmit(t, store, itemID)

	retireTx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer retireTx.Rollback()
	var epoch int64
	if err := retireTx.QueryRowContext(context.Background(), `
SELECT fence_epoch FROM managed_anime WHERE id = $1 FOR UPDATE`, "anime-"+sourceID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}

	admissionCtx, cancelAdmission := context.WithCancel(context.Background())
	admissionErr := make(chan error, 1)
	admissionDone := make(chan struct{})
	t.Cleanup(func() {
		cancelAdmission()
		_ = retireTx.Rollback()
		select {
		case <-admissionDone:
		case <-time.After(10 * time.Second):
			t.Error("admission goroutine did not stop during cleanup")
		}
	})
	go func() {
		defer close(admissionDone)
		admissionErr <- store.AdmitManagedAnimeEffect(admissionCtx, claim)
	}()
	waitForLockWaitOnQuery(t, db, "work_item_admit_managed_effect")

	if err := RetireUnadmittedTx(context.Background(), retireTx, "anime-"+sourceID, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := retireTx.ExecContext(context.Background(), `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1, retired_at = CURRENT_TIMESTAMP,
    retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND lifecycle_state = 'active' AND fence_epoch = $2`, "anime-"+sourceID, epoch); err != nil {
		t.Fatal(err)
	}
	if err := retireTx.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-admissionErr:
		if !errors.Is(err, ErrOwnerRetired) {
			t.Fatalf("blocked admission resolved to %v, want ErrOwnerRetired", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked admission did not resolve after retirement committed")
	}

	row := readWorkItem(t, db, itemID)
	if row.state != StateFailed || row.terminalAt == nil {
		t.Fatalf("retirement did not terminate the un-admitted item: %+v", row)
	}
	var admittedAt *time.Time
	if err := db.QueryRowContext(context.Background(), `SELECT external_effect_admitted_at FROM work_item_history WHERE id = $1`, itemID).Scan(&admittedAt); err != nil {
		t.Fatal(err)
	}
	if admittedAt != nil {
		t.Fatal("a losing admission still recorded an external effect")
	}
}

// TestRetiredOwnerRejectsStaleFinishRetryAndCheckpoint proves every stale
// writer entry point fails closed once the lifecycle moved on.
func TestRetiredOwnerRejectsStaleFinishRetryAndCheckpoint(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	ctx := context.Background()
	sourceID := createSourceOwner(t, db, "src-stale-writers")
	itemID := createRefreshItem(t, store, sourceID, "stale-writer-effect", 3)
	claim, err := store.ClaimDue(ctx, "worker-stale", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := retireOwnerTxSQL(ctx, tx, "anime-"+sourceID); err != nil {
		t.Fatalf("retire owner: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := store.Finish(ctx, claim, StateSucceeded, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); !errors.Is(err, ErrOwnerRetired) {
		t.Fatalf("stale Finish = %v, want ErrOwnerRetired", err)
	}
	if _, err := store.Retry(ctx, claim, "rss_transient", "transient", time.Minute, runtimeOwnerWriter(sourceID, claim.ExpectedOwnerVersion)); !errors.Is(err, ErrOwnerRetired) {
		t.Fatalf("stale Retry = %v, want ErrOwnerRetired", err)
	}
	checkpointTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointTx.Rollback()
	if err := store.AdvanceClaimOwnerVersion(ctx, txscope.Wrap(checkpointTx), claim, claim.ExpectedOwnerVersion+1); !errors.Is(err, ErrOwnerRetired) {
		t.Fatalf("stale checkpoint = %v, want ErrOwnerRetired", err)
	}

	row := readWorkItem(t, db, itemID)
	if row.state != StateFailed {
		t.Fatalf("retirement left the un-admitted item live: %+v", row)
	}
	version, health := readRuntimeVersion(t, db, sourceID)
	if version != 1 || health == "ready" {
		t.Fatalf("stale writer reached the retired owner row: version=%d health=%s", version, health)
	}
}
