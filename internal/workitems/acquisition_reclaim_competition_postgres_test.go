package workitems

import (
	"context"
	"testing"
	"time"

	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
)

// Each marker-first order is established by a held row lock, not goroutine
// scheduling. Reclamation must wait and then retain the running acquisition.
func TestAcquisitionReclaimMarkerLockOrders(t *testing.T) {
	db := openTestDB(t)
	const marker = "reclaim-lock-orders"
	createOneShotChain(t, db, marker, []string{"retained.mkv"})
	insertOneShotMarker(t, db, marker, OneShotStateRetentionPending, oneShotRetentionAt(time.Now().Add(-time.Hour)))
	owner := disposabledb.Owner(t)
	service, err := NewOneShotRetentionService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := acquisitionfixture.Snapshot(t, owner)
	for _, first := range []string{"marker", "reclaim"} {
		for round := 0; round < 50; round++ {
			tx, err := owner.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var pid int
			if err := tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`SELECT id FROM one_shot_acquisitions WHERE id=$1 FOR UPDATE`, marker); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			if first == "marker" {
				go func() {
					got, e := service.reclaimOne(context.Background(), marker)
					if e == nil && got {
						e = ErrInvalidSpec
					}
					done <- e
				}()
			} else {
				// Hold the same production reclaim prefix before letting the competing
				// marker transaction run. Rollback leaves no synthetic marker state.
				if _, err := lockOneShotMarkerForUpdateTx(context.Background(), tx, marker); err != nil {
					t.Fatal(err)
				}
				go func() {
					other, e := db.BeginTx(context.Background(), nil)
					if e != nil {
						done <- e
						return
					}
					defer other.Rollback()
					_, e = lockOneShotMarkerForUpdateTx(context.Background(), other, marker)
					done <- e
				}()
			}
			deadline := time.Now().Add(5 * time.Second)
			waiting := false
			for time.Now().Before(deadline) {
				if err := owner.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				tx.Rollback()
				t.Fatalf("order=%s round=%d no observed lock wait", first, round)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("order=%s round=%d: %v", first, round, err)
			}
			if first == "reclaim" {
				got, e := service.reclaimOne(context.Background(), marker)
				if e != nil || got {
					t.Fatalf("reclaim=%v err=%v", got, e)
				}
			}
		}
	}
	if before != acquisitionfixture.Snapshot(t, owner) {
		t.Fatal("marker competition changed retained graph")
	}
}
