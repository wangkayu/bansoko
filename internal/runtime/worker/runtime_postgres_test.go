package worker

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// openPGTestDB opens the disposable schema for worker integration tests.
func openPGTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.ResetDisposableSchema(t)
}

// pgStore wraps the real Store.
func pgStore(t *testing.T, db *sql.DB) *workitems.Store {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// createPGItem seeds the minimal owner chain and one refresh work item.
func createPGItem(t *testing.T, db *sql.DB, store *workitems.Store, id, effectKey string, maxAttempts int) string {
	t.Helper()
	ctx := context.Background()
	animeID := "anime-" + id
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Title')`, animeID); err != nil {
		t.Fatal(err)
	}
	subID := "sub-" + id
	if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ($1, $2, 'custom', 'PG worker', 'active')`, subID, animeID); err != nil {
		t.Fatal(err)
	}
	sourceID := "src-" + id
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ($1, $2, 'custom_rss', 'https://example.test/feed.xml', 'https://example.test/feed.xml', 'TestGroup', 'Title')`, sourceID, subID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)`, sourceID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	itemID, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID},
		Kind:                 workitems.KindRSSRefresh,
		IdempotencyKey:       "pg-test-" + effectKey,
		MaxAttempts:          maxAttempts,
		ExpectedOwnerVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return itemID
}

func pgTestRegistry() *Registry {
	reg, err := NewRegistry(
		KindPolicy{Kind: workitems.KindRSSRefresh, Lane: LaneLight, Lease: 3 * time.Second, Heartbeat: 300 * time.Millisecond, Deadline: 10 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
	)
	if err != nil {
		panic(err)
	}
	return reg
}

// TestPGHeartbeatPreventsSecondWorkerReclaim proves a live heartbeat keeps the
// claim from being reclaimed by a second worker, and stopping the heartbeat
// lets the second worker reclaim.
func TestPGHeartbeatPreventsSecondWorkerReclaim(t *testing.T) {
	db := openPGTestDB(t)
	store := pgStore(t, db)
	createPGItem(t, db, store, "hb", "hb", 5)

	claim, err := store.ClaimDue(context.Background(), "worker-a", []string{workitems.KindRSSRefresh}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Heartbeat every 300ms keeps the 3s lease alive.
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := store.RenewLease(context.Background(), claim, time.Now().UTC().Add(3*time.Second)); err != nil {
					return
				}
			}
		}
	}()
	// A second worker must NOT reclaim while the heartbeat runs.
	if _, err := store.ClaimDue(context.Background(), "worker-b", []string{workitems.KindRSSRefresh}, 3*time.Second); !errors.Is(err, workitems.ErrNoDueWork) {
		t.Fatalf("second worker must not reclaim while heartbeating, got %v", err)
	}
	close(stop)
	// Force lease expiry and reclaim.
	fixtureDB := disposabledb.Owner(t)
	if _, err := fixtureDB.ExecContext(context.Background(), `UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	reclaim, err := store.ClaimDue(context.Background(), "worker-b", []string{workitems.KindRSSRefresh}, 3*time.Second)
	if err != nil {
		t.Fatalf("reclaim after heartbeat stop: %v", err)
	}
	if reclaim.FencingToken != claim.FencingToken+1 {
		t.Fatalf("reclaim fencing = %d, want %d", reclaim.FencingToken, claim.FencingToken+1)
	}
	// The old worker's finish must now be rejected (stale fencing).
	if err := store.Finish(context.Background(), claim, workitems.StateSucceeded, nil); !errors.Is(err, workitems.ErrStaleClaim) {
		t.Fatalf("stale worker finish must fail, got %v", err)
	}
}

// TestPGCancelRequestStopsHeartbeatAndReclaimWorks proves RequestCancel makes
// the heartbeat fail closed and the item becomes reclaimable after expiry.
func TestPGCancelRequestStopsHeartbeatAndReclaimWorks(t *testing.T) {
	db := openPGTestDB(t)
	store := pgStore(t, db)
	createPGItem(t, db, store, "cancel", "cancel", 5)

	claim, err := store.ClaimDue(context.Background(), "worker-a", []string{workitems.KindRSSRefresh}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(context.Background(), claim, "maintenance"); err != nil {
		t.Fatal(err)
	}
	// Heartbeat must now fail closed.
	if err := store.RenewLease(context.Background(), claim, time.Now().UTC().Add(3*time.Second)); !errors.Is(err, workitems.ErrStaleClaim) {
		t.Fatalf("cancelled heartbeat must fail closed, got %v", err)
	}
	// Finish with the old token is also rejected once the lease expires.
	fixtureDB := disposabledb.Owner(t)
	if _, err := fixtureDB.ExecContext(context.Background(), `UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(context.Background(), claim, workitems.StateSucceeded, nil); !errors.Is(err, workitems.ErrStaleClaim) {
		t.Fatalf("cancelled claim finish must fail, got %v", err)
	}
	if _, err := store.ClaimDue(context.Background(), "worker-b", []string{workitems.KindRSSRefresh}, 3*time.Second); !errors.Is(err, workitems.ErrNoDueWork) {
		t.Fatalf("cancel must quarantine, not restart business: %v", err)
	}
}

// TestPGRuntimeHeartbeatAndDrain exercises the full Runtime against the real
// store: a handler that sleeps long enough for heartbeats, then a drain that
// waits for it to finish and commit exactly once.
func TestPGRuntimeHeartbeatAndDrain(t *testing.T) {
	db := openPGTestDB(t)
	store := pgStore(t, db)
	itemID := createPGItem(t, db, store, "runtime", "runtime", 5)

	var finished atomic.Bool
	handlers := map[string]Handler{
		workitems.KindRSSRefresh: HandlerFunc(func(ctx context.Context, claim workitems.Claim) (Outcome, error) {
			time.Sleep(1200 * time.Millisecond) // spans several heartbeats
			finished.Store(true)
			return Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ok"}, nil), nil
		}),
	}
	rt, err := NewRuntime(store, pgTestRegistry(), handlers, "pg-worker-a", testRuntimeLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt.Start()
	time.Sleep(500 * time.Millisecond) // claim dispatched, handler heartbeating
	if err := rt.Drain(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("drain must wait for the handler")
	}
	var state string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM work_item_history WHERE id = $1`, itemID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != workitems.StateSucceeded {
		t.Fatalf("item state = %s, want succeeded", state)
	}
}
