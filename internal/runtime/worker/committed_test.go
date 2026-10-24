package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

func TestCommittedOutcomeDoesNotRepeatTransition(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong_token", "missing_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			store := &fakeStore{claim: testClaim(workitems.KindQBSubmit)}
			w := NewWorker(store, "committed-worker", time.Minute)
			hooks := 0
			if err := w.Register(workitems.KindQBSubmit, HandlerFunc(func(_ context.Context, c workitems.Claim) (Outcome, error) {
				o := Committed(c, workitems.OwnerResult{State: "downloading"})
				if scenario == "wrong_token" {
					o.CommittedClaim.Token++
				}
				if scenario == "missing_receipt" {
					o.CommittedClaim = nil
				}
				o.AfterCommit = func(context.Context) { hooks++ }
				return o, nil
			})); err != nil {
				t.Fatal(err)
			}
			handled, err := w.RunOnce(context.Background())
			if !handled {
				t.Fatal("claim not handled")
			}
			if scenario == "valid" {
				if err != nil || hooks != 1 {
					t.Fatalf("committed result err=%v hooks=%d", err, hooks)
				}
			} else if !errors.Is(err, ErrInvalidOutcome) || hooks != 0 {
				t.Fatalf("invalid receipt err=%v hooks=%d", err, hooks)
			}
			if len(store.finishCalls) != 0 || len(store.retryCalls) != 0 {
				t.Fatal("committed receipt repeated a durable transition")
			}
		})
	}
}
