package application

import (
	"bansoko/internal/library/domain"
	"bansoko/internal/workitems"
	"context"
	"fmt"
	"testing"
	"time"
)

type failingPlanStableObserver struct {
	FileObserver
	failure error
}

func (o failingPlanStableObserver) ObservePath(context.Context, string) (domain.PathObservation, error) {
	return domain.PathObservation{}, o.failure
}

type temporaryProofReadError struct{}

func (temporaryProofReadError) Error() string   { return "fixture temporary read unavailable" }
func (temporaryProofReadError) Temporary() bool { return true }

func TestTypedObservedSatisfactionRetryBudgetExhaustion(t *testing.T) {
	// A transient target observation failure retries the planning stage
	// within its budget and then fails the stage as TARGET_UNREADABLE.
	for _, mode := range []string{"deadline", "temporary"} {
		t.Run(mode, func(t *testing.T) {
			db, s, c, _, _ := preparedObservedBatch(t)
			cause := error(fmt.Errorf("fixture stat: %w", context.DeadlineExceeded))
			code := domain.CodeTargetUnreadable
			if mode == "temporary" {
				cause = temporaryProofReadError{}
			}
			s.observer = failingPlanStableObserver{FileObserver: s.observer, failure: cause}
			ctx := context.Background()
			store := &workitems.Store{DB: db}
			max := c.MaxAttempts
			for attempt := 1; attempt <= max; attempt++ {
				if c.Attempt != attempt {
					t.Fatalf("attempt reset=%d want %d", c.Attempt, attempt)
				}
				out, err := s.handlePostProcessPlan(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				if out.WriteOwner != nil || out.PostTransition != nil {
					t.Fatal("generic writer")
				}
				var state, planState string
				var count int
				if err := db.QueryRow(`SELECT w.state,p.state FROM work_item_history w JOIN postprocessing_plans p ON p.id=w.owner_id WHERE w.id=$1`, c.ID).Scan(&state, &planState); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM file_operations`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatal("unknown fallback execute")
				}
				if attempt < max {
					if state != "available" || planState != "queued" {
						t.Fatalf("early seal %s/%s", state, planState)
					}
					if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET available_at=statement_timestamp()-interval '1 second' WHERE id=$1`, c.ID); err != nil {
						t.Fatal(err)
					}
					c, err = store.ClaimDue(ctx, "observed-budget", []string{workitems.KindPostProcessPlan}, time.Minute)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					if state != "failed" || planState != "failed" {
						t.Fatalf("budget not closed %s/%s", state, planState)
					}
					var failure string
					if err := db.QueryRow(`SELECT planning_diagnostic->>'reason_code' FROM postprocessing_plans WHERE id=$1`, c.Owner.ID).Scan(&failure); err != nil {
						t.Fatal(err)
					}
					if failure != code {
						t.Fatalf("stage code=%s want %s", failure, code)
					}
					before := planGraphSnapshot(t, db)
					assertCommittedReentry(t, s, "artifact-file-test", nil, "confirmed", "")
					if planGraphSnapshot(t, db) != before {
						t.Fatal("terminal proof wrote")
					}
				}
			}
			if !transientObservation(cause) {
				t.Fatal("lost failure kind")
			}
		})
	}
}
