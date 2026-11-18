package workitems

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	observabilityapplication "bansoko/internal/observability/application"
)

// decodeStoreEvent writes one lifecycle event through the real runtime logger
// and returns the redacted record an owner would read.
func decodeStoreEvent(t *testing.T, event storeEvent) map[string]any {
	t.Helper()
	var output bytes.Buffer
	store := &Store{Logger: observabilityapplication.NewJSONRuntimeLogger(&output)}
	store.logStoreEvent(event)
	line := strings.TrimSpace(output.String())
	if line == "" {
		t.Fatal("store emitted no runtime record")
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("decode store runtime record: %v; line=%q", err, line)
	}
	return record
}

func TestStoreLifecycleMessagesAreFixedAndCarryKindAndWorkItem(t *testing.T) {
	owner := OwnerRef{Type: OwnerRSSSourceRuntime, ID: "source-1"}
	for _, test := range []struct {
		name       string
		event      storeEvent
		message    string
		operation  string
		reason     string
		stateFrom  string
		stateAfter string
		attempt    float64
		hasAttempt bool
	}{
		{
			name:       "create",
			event:      workItemCreatedEvent(owner, "work-1", KindRSSRefresh),
			message:    "work item created",
			operation:  "create",
			reason:     "work_item_created",
			stateAfter: StateAvailable,
		},
		{
			name:      "merge",
			event:     workItemMergedEvent(owner, "work-1", KindRSSRefresh, false),
			message:   "work item merged",
			operation: "merge",
			reason:    "work_item_merged",
		},
		{
			name:       "claim",
			event:      workItemClaimedEvent(Claim{ID: "work-1", Kind: KindRSSRefresh, Owner: owner, Attempt: 2}, StateAvailable),
			message:    "work item claimed",
			operation:  "claim",
			reason:     "work_item_claimed",
			stateFrom:  StateAvailable,
			stateAfter: StateLeased,
			attempt:    2,
			hasAttempt: true,
		},
		{
			name:       "reclaim",
			event:      workItemClaimedEvent(Claim{ID: "work-1", Kind: KindRSSRefresh, Owner: owner, Attempt: 3}, StateLeased),
			message:    "work item reclaimed",
			operation:  "reclaim",
			reason:     "work_item_claimed",
			stateFrom:  StateLeased,
			stateAfter: StateLeased,
			attempt:    3,
			hasAttempt: true,
		},
		{
			name:       "finish succeeded",
			event:      workItemFinishedEvent(Claim{ID: "work-1", Kind: KindRSSRefresh, Owner: owner, Attempt: 2}, StateSucceeded),
			message:    "work item finished",
			operation:  "finish",
			reason:     "work_item_finished",
			stateFrom:  StateLeased,
			stateAfter: StateSucceeded,
			attempt:    2,
			hasAttempt: true,
		},
		{
			name:       "finish failed",
			event:      workItemFinishedEvent(Claim{ID: "work-1", Kind: KindRSSRefresh, Owner: owner, Attempt: 3}, StateFailed),
			message:    "work item finished",
			operation:  "finish",
			reason:     "work_item_finished",
			stateFrom:  StateLeased,
			stateAfter: StateFailed,
			attempt:    3,
			hasAttempt: true,
		},
		{
			name:       "retry scheduled",
			event:      workItemRetriedEvent(Claim{ID: "work-1", Kind: KindRSSRefresh, Owner: owner, Attempt: 1}, StateAvailable, false),
			message:    "work item retry scheduled",
			operation:  "retry",
			reason:     "work_item_retried",
			stateFrom:  StateLeased,
			stateAfter: StateAvailable,
			attempt:    1,
			hasAttempt: true,
		},
		{
			name:       "retry exhausted",
			event:      workItemRetriedEvent(Claim{ID: "work-1", Kind: KindRSSRefresh, Owner: owner, Attempt: 3}, StateFailed, true),
			message:    "work item retry exhausted",
			operation:  "retry_exhausted",
			reason:     "work_item_retried",
			stateFrom:  StateLeased,
			stateAfter: StateFailed,
			attempt:    3,
			hasAttempt: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := decodeStoreEvent(t, test.event)
			for key, want := range map[string]string{
				"message":      test.message,
				"component":    "workitems",
				"operation":    test.operation,
				"outcome":      "info",
				"reason_code":  test.reason,
				"owner_type":   owner.Type,
				"owner_id":     owner.ID,
				"work_item_id": "work-1",
				"kind":         KindRSSRefresh,
				"state_before": test.stateFrom,
				"state_after":  test.stateAfter,
			} {
				if want == "" {
					if _, ok := record[key]; ok {
						t.Fatalf("%s must be omitted when unknown: %v", key, record)
					}
					continue
				}
				if got := record[key]; got != want {
					t.Fatalf("%s = %v, want %v; record=%v", key, got, want, record)
				}
			}
			attempt, hasAttempt := record["attempt"]
			if hasAttempt != test.hasAttempt {
				t.Fatalf("attempt present = %t, want %t; record=%v", hasAttempt, test.hasAttempt, record)
			}
			if test.hasAttempt && attempt != test.attempt {
				t.Fatalf("attempt = %v, want %v", attempt, test.attempt)
			}
			if _, ok := record["retryable"]; ok {
				t.Fatalf("successful lifecycle transition must not assert retryability: %v", record)
			}
			for _, forbidden := range []string{"idempotency_key_hash", "fencing_token", "max_attempts", "worker_id", "state_before=", "state_after="} {
				if strings.Contains(fmt.Sprint(record), forbidden) {
					t.Fatalf("record leaked retired summary evidence %q: %v", forbidden, record)
				}
			}
		})
	}
}

func TestStoreClaimRejectionKeepsFixedMessageAndAttempt(t *testing.T) {
	owner := OwnerRef{Type: OwnerRSSSourceRuntime, ID: "source-1"}
	record := decodeStoreEvent(t, workItemClaimRejectedEvent(Claim{
		ID: "work-1", Kind: KindRSSRefresh, Owner: owner, Attempt: 4,
	}, "one_shot_claim_invalid"))

	if got, want := record["message"], "work item claim rejected invalid one-shot input"; got != want {
		t.Fatalf("message = %v, want %v", got, want)
	}
	if got, want := record["level"], "warn"; got != want {
		t.Fatalf("level = %v, want %v", got, want)
	}
	if got, want := record["outcome"], "failure"; got != want {
		t.Fatalf("outcome = %v, want %v", got, want)
	}
	if got, want := record["kind"], KindRSSRefresh; got != want {
		t.Fatalf("kind = %v, want %v", got, want)
	}
	if got, want := record["attempt"], float64(4); got != want {
		t.Fatalf("attempt = %v, want %v", got, want)
	}
	if _, ok := record["retryable"]; ok {
		t.Fatalf("claim rejection must not fabricate retryability: %v", record)
	}
}

func TestStoreLifecycleMessageNeverContainsDynamicFacts(t *testing.T) {
	owner := OwnerRef{Type: OwnerRSSSourceRuntime, ID: "source-private-locator"}
	claim := Claim{ID: "work-1", Kind: KindRSSRefresh, Owner: owner, Attempt: 2}
	for _, event := range []storeEvent{
		workItemCreatedEvent(owner, claim.ID, claim.Kind),
		workItemMergedEvent(owner, claim.ID, claim.Kind, false),
		workItemClaimedEvent(claim, StateLeased),
		workItemFinishedEvent(claim, StateSucceeded),
	} {
		if strings.Contains(event.Message, "=") || strings.Contains(event.Message, owner.ID) {
			t.Fatalf("lifecycle message carries dynamic facts: %q", event.Message)
		}
	}
}

func TestStoreOneShotFinishKeepsBoundedDisposition(t *testing.T) {
	owner := OwnerRef{Type: "download_task", ID: "task-1"}
	event := workItemFinishedEvent(Claim{ID: "work-1", Kind: "qb_submit", Owner: owner, Attempt: 2}, StateSucceeded)
	event.Disposition = string(DispositionConfirmed)
	record := decodeStoreEvent(t, event)
	if got, want := record["disposition"], string(DispositionConfirmed); got != want {
		t.Fatalf("disposition = %v, want %v", got, want)
	}
	if got, want := record["message"], "work item finished"; got != want {
		t.Fatalf("message = %v, want %v", got, want)
	}
}

func TestFinishStageErrorPreservesCauseSemantics(t *testing.T) {
	sentinel := errors.New("underlying store failure")
	err := finishStageError(FinishStageCommit, fmt.Errorf("commit finish transaction: %w", sentinel))
	if !errors.Is(err, sentinel) {
		t.Fatal("finish stage wrapper lost errors.Is")
	}
	var staged *FinishStageError
	if !errors.As(err, &staged) {
		t.Fatal("finish stage wrapper lost errors.As")
	}
	if staged.Stage != FinishStageCommit || !staged.Stage.IsValid() {
		t.Fatalf("stage = %q, want %q", staged.Stage, FinishStageCommit)
	}
	if !errors.Is(finishStageError(FinishStageLock, ErrStaleClaim), ErrStaleClaim) {
		t.Fatal("finish stage wrapper lost ErrStaleClaim identity")
	}
	if FinishStage("arbitrary").IsValid() {
		t.Fatal("an arbitrary stage must not be accepted by the bounded taxonomy")
	}
}
