package migrations

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLifecycleShareFencesRetirement(t *testing.T) {
	for _, order := range []string{"accept_first", "retire_first"} {
		t.Run(order, func(t *testing.T) {
			db := installedDB(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			seedAcceptableRelease(t, db, "lifecycle-share")
			p := validQueued(t, "acq-lifecycle-share", "key-lifecycle-share", "lifecycle-share")
			retire := `UPDATE managed_anime SET lifecycle_state='retired',fence_epoch=fence_epoch+1,lifecycle_version=lifecycle_version+1,retired_at=CURRENT_TIMESTAMP,retired_subscription_version=1 WHERE id='anime-lifecycle-share'`
			if order == "retire_first" {
				if _, err := db.ExecContext(ctx, retire); err != nil {
					t.Fatal(err)
				}
				if _, err := callQueuedAccept(t, db, p); err == nil || !strings.Contains(err.Error(), "ACQ_QUALIFICATION_MANAGEMENT_CHANGED") {
					t.Fatalf("retired admission: %v", err)
				}
				var roots int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&roots); err != nil || roots != 0 {
					t.Fatalf("retired roots=%d err=%v", roots, err)
				}
				return
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var pid int
			if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err := callQueuedAccept(t, tx, p); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := db.ExecContext(ctx, retire); done <- err }()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var blocked bool
				if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("retirement escaped SHARE: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var roots int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions WHERE id='acq-lifecycle-share'`).Scan(&roots); err != nil || roots != 1 {
				t.Fatalf("accepted root=%d err=%v", roots, err)
			}
		})
	}
}
