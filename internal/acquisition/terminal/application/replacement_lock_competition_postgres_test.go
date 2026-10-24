package application

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func verifyReplacementApprovalLockOrderings(t *testing.T, owner, runtimeDB *sql.DB, path string) {
	t.Helper()
	for _, approvalFirst := range []bool{true, false} {
		label := "placement_first"
		if approvalFirst {
			label = "approval_first"
		}
		t.Run(label, func(t *testing.T) {
			for round := 0; round < 50; round++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				capture, err := CaptureReplacement(ctx, runtimeDB, "release", path)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				a, err := runtimeDB.BeginTx(ctx, nil)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				b, err := owner.BeginTx(ctx, nil)
				if err != nil {
					a.Rollback()
					cancel()
					t.Fatal(err)
				}
				var apid, bpid int
				if err = a.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&apid); err != nil {
					t.Fatal(err)
				}
				if err = b.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&bpid); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				waiter, blocker := bpid, apid
				if approvalFirst {
					if _, err = capture.recheck(ctx, a, "release", "anime-1"); err != nil {
						t.Fatal(err)
					}
					go func() {
						_, err := b.ExecContext(ctx, `UPDATE placements SET version=version+1 WHERE id='placement'`)
						done <- err
					}()
				} else {
					if _, err = b.ExecContext(ctx, `UPDATE placements SET version=version+1 WHERE id='placement'`); err != nil {
						t.Fatal(err)
					}
					waiter, blocker = apid, bpid
					go func() { _, err := capture.recheck(ctx, a, "release", "anime-1"); done <- err }()
				}
				ticker := time.NewTicker(time.Millisecond)
				blocked := false
				for !blocked {
					if err = owner.QueryRowContext(ctx, `SELECT $2::integer=ANY(pg_blocking_pids($1::integer))`, waiter, blocker).Scan(&blocked); err != nil {
						t.Fatal(err)
					}
					if !blocked {
						select {
						case err = <-done:
							t.Fatalf("round %d completed before lock barrier: %v", round, err)
						case <-ticker.C:
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
				}
				ticker.Stop()
				if approvalFirst {
					err = a.Commit()
				} else {
					err = b.Commit()
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if approvalFirst {
					if err != nil {
						t.Fatal(err)
					}
					if err = b.Commit(); err != nil {
						t.Fatal(err)
					}
				} else {
					if err == nil || (err.Error() != "replacement approval changed" && err.Error() != "replacement approval stale") {
						t.Fatalf("stale approval result: %v", err)
					}
					a.Rollback()
				}
				// Restore only owner-arranged versions in this disposable relational test.
				if _, err = owner.ExecContext(ctx, `UPDATE placements SET version=1 WHERE id='placement'`); err != nil {
					t.Fatal(err)
				}
				cancel()
			}
		})
	}
}
