package workitems

import (
	"context"
	"errors"
	"testing"
	"time"

	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

// Each order holds the first operation open on the real row lock. The second
// operation either skips the locked candidate or waits and rechecks its predicate.
func TestClaimCompetitionBothOrders(t *testing.T) {
	for _, first := range []string{"worker-a", "worker-b"} {
		t.Run(first, func(t *testing.T) {
			db := openTestDB(t)
			store := newTestStore(t, db)
			ctx := context.Background()
			source := createSourceOwner(t, db, "w1-claim")
			id := createRefreshItem(t, store, source, "w1-claim", 3)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			row, err := persistence.New(tx).ClaimDue(ctx, persistence.ClaimDueParams{Kinds: []string{KindRSSRefresh}, LeaseSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			if row.ID != id {
				t.Fatal("first worker claimed wrong item")
			}
			if _, err := store.ClaimDue(ctx, "second", []string{KindRSSRefresh}, time.Minute); !errors.Is(err, ErrNoDueWork) {
				t.Fatalf("second claim=%v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			actual := readWorkItem(t, db, id)
			if actual.attempt != 1 || actual.fencingToken != 1 || actual.state != StateLeased {
				t.Fatalf("competing claim changed generation: %+v", actual)
			}
		})
	}
}

func TestHeartbeatFinishBothOrders(t *testing.T) {
	for _, first := range []string{"heartbeat", "finish"} {
		t.Run(first, func(t *testing.T) {
			db := openTestDB(t)
			store := newTestStore(t, db)
			ctx := context.Background()
			source := createSourceOwner(t, db, "w1-heartbeat")
			id := createRefreshItem(t, store, source, "w1-heartbeat", 3)
			claim, err := store.ClaimDue(ctx, "worker", []string{KindRSSRefresh}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if first == "heartbeat" {
				n, err := persistence.New(tx).RenewWorkItemLease(ctx, persistence.RenewWorkItemLeaseParams{ID: id, FencingToken: claim.FencingToken, ExtendTo: time.Now().Add(time.Minute)})
				if err != nil || n != 1 {
					t.Fatalf("renew=%d %v", n, err)
				}
			} else {
				if _, err := persistence.New(tx).LockWorkItemClaim(ctx, persistence.LockWorkItemClaimParams{ID: id, FencingToken: claim.FencingToken}); err != nil {
					t.Fatal(err)
				}
				if err := persistence.New(tx).FinishWorkItem(ctx, persistence.FinishWorkItemParams{ID: id, State: StateSucceeded, ExpectedOwnerVersion: claim.ExpectedOwnerVersion}); err != nil {
					t.Fatal(err)
				}
			}
			opctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			result := make(chan error, 1)
			t.Cleanup(func() {
				cancel()
				_ = tx.Rollback()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("competition goroutine leaked")
				}
			})
			go func() {
				defer close(done)
				if first == "heartbeat" {
					result <- store.Finish(opctx, claim, StateSucceeded, nil)
				} else {
					result <- store.RenewLease(opctx, claim, time.Now().Add(time.Minute))
				}
			}()
			fragment := "work_item_lock_claim"
			if first == "finish" {
				fragment = "work_item_renew_lease"
			}
			waitForLockWaitOnQuery(t, db, fragment)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if first == "heartbeat" && err != nil {
					t.Fatal(err)
				}
				if first == "finish" && !errors.Is(err, ErrStaleClaim) {
					t.Fatalf("heartbeat after finish=%v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("competition did not converge")
			}
			actual := readWorkItem(t, db, id)
			if actual.state != StateSucceeded || actual.fencingToken != claim.FencingToken {
				t.Fatalf("wrong terminal generation: %+v", actual)
			}
		})
	}
}

func TestRetirementClaimBothOrders(t *testing.T) {
	for _, first := range []string{"retirement", "claim"} {
		t.Run(first, func(t *testing.T) {
			db := openTestDB(t)
			store := newTestStore(t, db)
			ctx := context.Background()
			source := createSourceOwner(t, db, "w1-retirement")
			id := createRefreshItem(t, store, source, "w1-retirement", 3)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if first == "retirement" {
				if err := retireOwnerTxSQL(ctx, tx, "anime-"+source); err != nil {
					t.Fatal(err)
				}
				if _, err := store.ClaimDue(ctx, "second", []string{KindRSSRefresh}, time.Minute); !errors.Is(err, ErrNoDueWork) {
					t.Fatalf("claim during retirement = %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := persistence.New(tx).ClaimDue(ctx, persistence.ClaimDueParams{Kinds: []string{KindRSSRefresh}, LeaseSeconds: 60}); err != nil {
					t.Fatal(err)
				}
				opctx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				result := make(chan error, 1)
				t.Cleanup(func() {
					cancel()
					_ = tx.Rollback()
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("retirement goroutine leaked")
					}
				})
				go func() {
					defer close(done)
					rtx, err := db.BeginTx(opctx, nil)
					if err != nil {
						result <- err
						return
					}
					defer rtx.Rollback()
					if err := retireOwnerTxSQL(opctx, rtx, "anime-"+source); err != nil {
						result <- err
						return
					}
					result <- rtx.Commit()
				}()
				waitForLockWaitOnQuery(t, db, "managed_anime")
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("retirement did not converge")
				}
			}
			if _, err := store.ClaimDue(ctx, "after", []string{KindRSSRefresh}, time.Minute); !errors.Is(err, ErrNoDueWork) {
				t.Fatalf("retired claim=%v", err)
			}
			row := readWorkItem(t, db, id)
			if row.state != StateFailed {
				t.Fatalf("retirement state=%s", row.state)
			}
		})
	}
}
