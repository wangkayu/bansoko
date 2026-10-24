package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// seedFailedRSSRetryWorkItem creates one failed rss_refresh WorkItem for a
// seeded rss_source_runtime owner and returns its id.
func seedFailedRSSRetryWorkItem(t *testing.T, db *sql.DB, store *workitems.Store, source, itemID string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: source},
		Kind:                 workitems.KindRSSRefresh,
		IdempotencyKey:       "w4-effect-" + itemID,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `SELECT public.work_item_close(id,'failed') FROM work_items WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestManualRetryPrelockExhaustionIsZeroWriteBusy proves the W4 exhaustion
// surface: a contended fixed lock set yields the entry-specific zero-write 409
// after the bounded attempts. The retirement reason code is never borrowed.
func TestManualRetryPrelockExhaustionIsZeroWriteBusy(t *testing.T) {
	db := animetest.OpenTestDB(t)
	const source = "source-w4-busy"
	animetest.SeedReleaseOwnerChain(t, db, source)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES($1)`, source); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	failedID := seedFailedRSSRetryWorkItem(t, db, store, source, "w4-busy")

	// Hold the failed work item row so the prelock's NOWAIT aborts on every
	// attempt.
	blocker, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `SELECT id FROM work_item_outcomes WHERE id=$1 FOR UPDATE`, failedID); err != nil {
		t.Fatal(err)
	}

	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, source, failedID, "retry", "w4-busy-request-key")
	elapsed := time.Since(start)

	var business *apperrors.Business
	if !errors.As(err, &business) {
		t.Fatalf("exhausted prelock must be a typed business error, got %v", err)
	}
	if business.Status != 409 || business.Code != "MANUAL_RETRY_BUSY" {
		t.Fatalf("exhausted prelock must be 409 MANUAL_RETRY_BUSY, got %d %s", business.Status, business.Code)
	}
	if business.Code == "MANAGED_ANIME_RETIREMENT_BUSY" {
		t.Fatal("manual retry must never borrow the retirement reason code")
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("exhaustion must have retried the whole transaction with backoff, elapsed=%s", elapsed)
	}

	// Zero writes: no replacement work item, no idempotency record.
	var replacement int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND id<>$2`, source, failedID).Scan(&replacement); err != nil {
		t.Fatal(err)
	}
	if replacement != 0 {
		t.Fatalf("exhausted prelock must create no replacement work item, found %d", replacement)
	}
	var records int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records WHERE kind='manual_retry' AND owner_id=$1`, source).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatalf("exhausted prelock must record no idempotency, found %d", records)
	}
}

// TestManualRetryPrelockCoversOwnerRow proves the owner row is inside the fixed
// prelock, not only the work item: holding the owner row alone exhausts the
// entry with the same zero-write 409.
func TestManualRetryPrelockCoversOwnerRow(t *testing.T) {
	db := animetest.OpenTestDB(t)
	const source = "source-w4-owner"
	animetest.SeedReleaseOwnerChain(t, db, source)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES($1)`, source); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	failedID := seedFailedRSSRetryWorkItem(t, db, store, source, "w4-owner")

	blocker, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `SELECT rss_source_id FROM rss_source_runtime WHERE rss_source_id=$1 FOR UPDATE`, source); err != nil {
		t.Fatal(err)
	}

	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	err = retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, source, failedID, "retry", "w4-owner-request-key")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 409 || business.Code != "MANUAL_RETRY_BUSY" {
		t.Fatalf("owner-row contention must exhaust to 409 MANUAL_RETRY_BUSY, got %v", err)
	}
	var replacement int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND id<>$2`, source, failedID).Scan(&replacement); err != nil {
		t.Fatal(err)
	}
	if replacement != 0 {
		t.Fatalf("owner-row contention must write no replacement, found %d", replacement)
	}
}

// TestManualRetryPrelockDoesNotRetryBusinessErrors proves only a 55P03 from the
// fixed prelock is retried: a business rejection returns immediately.
func TestManualRetryPrelockDoesNotRetryBusinessErrors(t *testing.T) {
	db := animetest.OpenTestDB(t)
	const source = "source-w4-noretry"
	animetest.SeedReleaseOwnerChain(t, db, source)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES($1)`, source); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// A work item that does not exist is a business rejection (NotFound), not a
	// lock conflict: it must return on the first attempt.
	err = retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, source, "missing-work-item", "retry", "w4-noretry-request-key")
	elapsed := time.Since(start)
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("a missing work item must stay NotFound, got %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("a business rejection must not be retried with backoff, elapsed=%s", elapsed)
	}
}

// TestManualRetryPrelockSuccessPathStillWorks is the positive control: an
// uncontended retry still creates the replacement and records its idempotency,
// so the prelock has not turned the entry into a permanent rejection.
func TestManualRetryPrelockSuccessPathStillWorks(t *testing.T) {
	db := animetest.OpenTestDB(t)
	const source = "source-w4-ok"
	animetest.SeedReleaseOwnerChain(t, db, source)
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES($1)`, source); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	failedID := seedFailedRSSRetryWorkItem(t, db, store, source, "w4-ok")
	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, source, failedID, "retry", "w4-ok-request-key"); err != nil {
		t.Fatalf("uncontended manual retry must succeed: %v", err)
	}
	var replacement, state string
	if err := db.QueryRowContext(ctx, `SELECT id,state FROM work_item_history WHERE owner_id=$1 AND id<>$2`, source, failedID).Scan(&replacement, &state); err != nil {
		t.Fatal(err)
	}
	if state != "available" {
		t.Fatalf("replacement %s must be available, got %s", replacement, state)
	}
	// Idempotent replay under the same key must still be a zero-write replay.
	if err := retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, source, failedID, "retry", "w4-ok-request-key"); err != nil {
		t.Fatalf("replay must succeed: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND id<>$2`, source, failedID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("replay must not create a second replacement, found %d", count)
	}
}
