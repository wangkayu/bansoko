package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Every retry of the typed file lane is writer-less (the operation stays
// pending while the evidence decides), so each one must survive the attempt
// budget: the store quarantines it for the recovery lane instead of failing
// the WorkItem and stranding the operation. A rolled-back finish of a closed
// proof is the case that stranded deployed operations.
func TestTypedFileRetriesSurviveAttemptBudget(t *testing.T) {
	s := &Service{}
	claim := workitems.Claim{ID: "work-1", Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "op-1"}, Kind: workitems.KindFileExecute}
	cases := []struct {
		name string
		run  func() (worker.Outcome, error)
		code string
	}{
		{"finish_rolled_back", func() (worker.Outcome, error) {
			return s.typedFinishBlockedOutcome(context.Background(), claim, operationRecord{}, fmt.Errorf("%w: FILE_NOTIFICATION_PROOF_MISMATCH", ErrFinishNotCommitted))
		}, "file_proof_capture_transient"},
		{"proof_capture_transient", func() (worker.Outcome, error) {
			return s.typedFinishBlockedOutcome(context.Background(), claim, operationRecord{}, RetryableError{Err: errors.New("connection reset")})
		}, "file_proof_capture_transient"},
		{"publication_transient", func() (worker.Outcome, error) {
			return s.typedPublicationFaultOutcome(context.Background(), claim, operationRecord{}, RetryableError{Err: errors.New("connection reset")})
		}, "typed_publication_transient"},
		{"attempt_deadline", func() (worker.Outcome, error) {
			return typedRetryUntilResolved("file_attempt_deadline", "file preparation exceeded its attempt deadline before any publication"), nil
		}, "file_attempt_deadline"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.run()
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != worker.OutcomeRetry || !out.RetryUntilResolved || out.WriteOwner != nil || out.ReasonCode != c.code || out.RetryAfter != defaultRetryAfter {
				t.Fatalf("retry outcome=%+v", out)
			}
		})
	}
}
