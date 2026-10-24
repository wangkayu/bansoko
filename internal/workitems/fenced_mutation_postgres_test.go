package workitems

import (
	"context"
	"errors"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
)

func TestMutateClaimSynchronizesVersionAndAllowsUnchangedVersion(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	source := createSourceOwner(t, db, "src-mutate")
	id := createRefreshItem(t, store, source, "effect-mutate", 3)
	claim, err := store.ClaimDue(context.Background(), "worker", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.MutateClaim(context.Background(), claim, func(context.Context, txscope.Tx, OwnerResult) (int64, error) { return claim.ExpectedOwnerVersion, nil })
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ExpectedOwnerVersion != 1 || readWorkItem(t, db, id).expectedOwnerVersion != 1 {
		t.Fatal("unchanged owner version was not synchronized")
	}
	advanced, err := store.MutateClaim(context.Background(), unchanged, runtimeOwnerWriter(source, 1))
	if err != nil {
		t.Fatal(err)
	}
	if advanced.ExpectedOwnerVersion != 2 || readWorkItem(t, db, id).expectedOwnerVersion != 2 {
		t.Fatalf("advanced claim=%+v row=%+v", advanced, readWorkItem(t, db, id))
	}
}

func TestRetryWithoutOwnerWriterRejectsAdvancedOwnerVersion(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	source := createSourceOwner(t, db, "src-retry-stale-version")
	itemID := createRefreshItem(t, store, source, "effect-retry-stale-version", 3)
	claim, err := store.ClaimDue(context.Background(), "worker", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := store.MutateClaim(context.Background(), claim, runtimeOwnerWriter(source, claim.ExpectedOwnerVersion))
	if err != nil {
		t.Fatal(err)
	}
	advanced, err = store.MutateClaim(context.Background(), advanced, runtimeOwnerWriter(source, advanced.ExpectedOwnerVersion))
	if err != nil {
		t.Fatal(err)
	}
	if advanced.ExpectedOwnerVersion != claim.ExpectedOwnerVersion+2 {
		t.Fatalf("mutate claim version = %d, want %d", advanced.ExpectedOwnerVersion, claim.ExpectedOwnerVersion+2)
	}

	if _, err := store.Retry(context.Background(), claim, "STALE_VERSION", "stale entry claim", time.Minute, nil); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("retry with stale owner version = %v, want ErrStaleClaim", err)
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.expectedOwnerVersion != advanced.ExpectedOwnerVersion {
		t.Fatalf("stale retry changed the work item: %+v", row)
	}
	version, health := readRuntimeVersion(t, db, source)
	if version != advanced.ExpectedOwnerVersion || health != "ready" {
		t.Fatalf("stale retry changed the owner: version=%d health=%s", version, health)
	}

	if _, err := store.RetryWithoutAttemptBudget(context.Background(), claim, "STALE_VERSION", "stale entry claim", time.Minute, nil); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("budgetless retry with stale owner version = %v, want ErrStaleClaim", err)
	}
	row = readWorkItem(t, db, itemID)
	if row.state != StateLeased || row.expectedOwnerVersion != advanced.ExpectedOwnerVersion {
		t.Fatalf("stale budgetless retry changed the work item: %+v", row)
	}
}

func TestRetryWithoutOwnerWriterReturnsAvailable(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	source := createSourceOwner(t, db, "src-retry-nil-writer")
	itemID := createRefreshItem(t, store, source, "effect-retry-nil-writer", 3)
	claim, err := store.ClaimDue(context.Background(), "worker", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := store.Retry(context.Background(), claim, "NIL_WRITER_RETRY", "retry without owner writer", time.Minute, nil)
	if err != nil {
		t.Fatalf("retry without owner writer: %v", err)
	}
	if outcome.Terminal {
		t.Fatal("retry below attempt budget must not be terminal")
	}
	row := readWorkItem(t, db, itemID)
	if row.state != StateAvailable || row.leaseUntil != nil || row.expectedOwnerVersion != claim.ExpectedOwnerVersion {
		t.Fatalf("nil writer retry row = %+v", row)
	}
	if row.lastErrorCode == nil || *row.lastErrorCode != "NIL_WRITER_RETRY" {
		t.Fatalf("nil writer retry reason = %+v", row.lastErrorCode)
	}
	version, health := readRuntimeVersion(t, db, source)
	if version != 1 || health != "unknown" {
		t.Fatalf("nil writer retry must not mutate owner: version=%d health=%s", version, health)
	}
}

func TestMutateClaimRollsBackOwnerAndRejectsStale(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	source := createSourceOwner(t, db, "src-mutate-rollback")
	id := createRefreshItem(t, store, source, "effect-mutate-rollback", 3)
	claim, err := store.ClaimDue(context.Background(), "worker", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.MutateClaim(context.Background(), claim, func(ctx context.Context, scope txscope.Tx, _ OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if _, e := tx.ExecContext(ctx, `UPDATE rss_source_runtime SET version=version+1 WHERE rss_source_id=$1`, source); e != nil {
			return 0, e
		}
		return 0, errors.New("rollback")
	})
	if err == nil {
		t.Fatal("expected callback failure")
	}
	version, _ := readRuntimeVersion(t, db, source)
	if version != 1 || readWorkItem(t, db, id).expectedOwnerVersion != 1 {
		t.Fatal("fenced mutation did not roll back")
	}
	if _, err := workItemFixtureDB(t).ExecContext(context.Background(), `UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	current, err := store.ClaimDue(context.Background(), "worker-2", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MutateClaim(context.Background(), claim, func(context.Context, txscope.Tx, OwnerResult) (int64, error) {
		t.Fatal("stale callback must not run")
		return 1, nil
	}); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale mutation=%v", err)
	}
	if current.FencingToken == claim.FencingToken {
		t.Fatal("fixture did not advance fencing token")
	}
}
