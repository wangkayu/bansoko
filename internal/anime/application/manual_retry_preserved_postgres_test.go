package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

func TestManualRetryPreservedRSSRuntime(t *testing.T) {
	db := animetest.OpenTestDB(t)
	const source = "source-preserved-retry"
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := store.Create(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: source}, Kind: workitems.KindRSSRefresh, IdempotencyKey: "preserved-rss-effect", MaxAttempts: 3, ExpectedOwnerVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `SELECT public.work_item_close(id,'failed') FROM work_items WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	retry, err := NewManualRetryService(db, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, source, id, "retry", "preserved-rss-request"); err != nil {
		t.Fatal(err)
	}
	var replacement string
	var attempt int
	var key, state string
	if err := db.QueryRowContext(ctx, `SELECT id,attempt,idempotency_key,state FROM work_item_history WHERE owner_id=$1 AND id<>$2`, source, id).Scan(&replacement, &attempt, &key, &state); err != nil {
		t.Fatal(err)
	}
	if attempt != 0 || key != "preserved-rss-effect" || state != "available" {
		t.Fatalf("replacement %s %d %s %s", replacement, attempt, key, state)
	}
	if err := retry.ManualRetry(ctx, workitems.OwnerRSSSourceRuntime, source, id, "retry", "preserved-rss-request"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id=$1`, source).Scan(&count); err != nil || count != 2 {
		t.Fatalf("replay count=%d err=%v", count, err)
	}
	claim, err := store.ClaimDue(ctx, "preserved-rss-worker", []string{workitems.KindRSSRefresh}, time.Minute)
	if err != nil || claim.ID != replacement {
		t.Fatalf("replacement claim=%v err=%v", claim, err)
	}
	before := stageRetryClosureSnapshot(t, db)
	err = retry.ManualRetry(ctx, workitems.OwnerRelease, "non-acquisition-release", id, "retry", "unsupported-request")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 422 || business.Code != "OWNER_TYPE_INVALID" {
		t.Fatalf("non-acquisition unsupported owner=%v", err)
	}
	if stageRetryClosureSnapshot(t, db) != before {
		t.Fatal("unsupported owner wrote database")
	}
}
