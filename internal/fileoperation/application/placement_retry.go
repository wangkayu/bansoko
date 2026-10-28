package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// PlacementSinkError is the typed placement-admission failure returned by the
// PlacementSink port. Code is the stable, bounded reason persisted into the
// owner-visible diagnostic episode and the durable placement obligation; Err
// stays internal.
type PlacementSinkError struct {
	Code string
	Err  error
}

func (e *PlacementSinkError) Error() string {
	if e == nil {
		return "placement admission failed"
	}
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *PlacementSinkError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// PlacementSinkReason extracts the stable reason code from a sink failure. A
// sink that returns a plain error is still classified, never silently dropped.
func PlacementSinkReason(err error) string {
	var sinkErr *PlacementSinkError
	if errors.As(err, &sinkErr) && strings.TrimSpace(sinkErr.Code) != "" {
		return boundedCode(sinkErr.Code, placementDiagnosticErrorCode)
	}
	return placementDiagnosticErrorCode
}

// The placement-admission stage shares one diagnostic episode identity per
// file operation, so a failed admission and its later idempotent settle close
// the same visible episode. The stable reason code travels in the episode
// details instead of multiplying episode identities.
const (
	placementDiagnosticOperation = "placement_admit"
	placementDiagnosticErrorCode = "placement_sink_failed"
	placementRetryMaxAttempts    = 3
)

// Placement obligation states (placement_obligations.state). The
// obligation is the durable, independent stage state of the post-completion
// placement handoff: it never rewrites the completed file operation.
const (
	placementObligationPending      = "pending"
	placementObligationWaitingRetry = "waiting_retry"
	placementObligationAdmitted     = "admitted"
	placementObligationFailed       = "failed"
)

// placementContinuationSchemaVersion is the unambiguous typed discriminator of
// the post-completion placement handoff continuation. A normal file_execute
// snapshot never carries this shape, and a continuation never validates as a
// normal file_execute input: the discriminator cannot be confused with an
// ordinary effect claim in either direction.
const placementContinuationSchemaVersion = "file-execute-continuation/v1"

type placementContinuationInput struct {
	SchemaVersion   string `json:"schema_version"`
	Continuation    string `json:"continuation"`
	PlacementRetry  bool   `json:"placement_retry"`
	FileOperationID string `json:"file_operation_id"`
	// OneShot carries the source claim's one-shot reference:
	// a placement continuation is a kind=file_execute WorkItem, so the
	// marker-first gate keyed on input_snapshot ? 'one_shot' must see the same
	// reference the original claim carried, and the reclaim proof must find
	// it. Nil only for non-one-shot (prepared) graphs.
	OneShot *workitems.OneShotRef `json:"one_shot,omitempty"`
}

func marshalPlacementContinuationInput(operationID string, oneShot *workitems.OneShotRef) (json.RawMessage, error) {
	return json.Marshal(placementContinuationInput{
		SchemaVersion:   placementContinuationSchemaVersion,
		Continuation:    "placement",
		PlacementRetry:  true,
		FileOperationID: operationID,
		OneShot:         oneShot,
	})
}

const (
	continuationNormal = iota
	continuationIsContinuation
	continuationMalformed
)

// classifyPlacementContinuation inspects a file_execute claim input snapshot:
//
//   - continuationNormal: no placement intent; ordinary file_execute handling.
//   - continuationIsContinuation: the strict typed discriminator matches.
//   - continuationMalformed: the snapshot declares placement intent but does
//     not satisfy the strict schema. The claim must fail closed: it is never
//     treated as a normal file_execute claim (which could redo or mis-settle a
//     file effect) and never silently dropped.
func classifyPlacementContinuation(claim workitems.Claim) (placementContinuationInput, int) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(claim.InputSnapshot, &probe); err != nil || probe == nil {
		// Unparseable snapshots never pass the store claim gate (the one-shot
		// structural precondition rejects them); still fail closed when the
		// placement marker is visible in the raw bytes.
		if bytes.Contains(claim.InputSnapshot, []byte(`"placement_retry"`)) || bytes.Contains(claim.InputSnapshot, []byte(`"continuation"`)) {
			return placementContinuationInput{}, continuationMalformed
		}
		return placementContinuationInput{}, continuationNormal
	}
	_, hasRetry := probe["placement_retry"]
	_, hasContinuation := probe["continuation"]
	if !hasRetry && !hasContinuation {
		return placementContinuationInput{}, continuationNormal
	}
	var input placementContinuationInput
	if err := json.Unmarshal(claim.InputSnapshot, &input); err != nil {
		return placementContinuationInput{}, continuationMalformed
	}
	if input.SchemaVersion != placementContinuationSchemaVersion ||
		input.Continuation != "placement" ||
		!input.PlacementRetry ||
		strings.TrimSpace(input.FileOperationID) == "" ||
		input.FileOperationID != claim.Owner.ID {
		return placementContinuationInput{}, continuationMalformed
	}
	return input, continuationIsContinuation
}

// createPlacementObligationTx records the durable placement handoff obligation
// and its typed continuation WorkItem inside the FileOperation completion
// transaction, so the post-completion stage can never be lost to a process
// crash after the file effect commits. Idempotent per file operation: a
// replayed completion merges the one live continuation item instead of
// duplicating it. For a one-shot source claim the marker row is
// locked FIRST (before the obligation insert) and must still be running on the
// claimed epoch — a reclaiming marker returns workitems.ErrOneShotReclaiming
// with zero writes (no obligation, no continuation, no owner change), and the
// continuation WorkItem carries the claim's exact one_shot reference so the
// marker-first gate and the reclaim proof cover it. Returns the continuation
// WorkItem id.
func (s *Service) createPlacementObligationTx(ctx context.Context, tx *sql.Tx, operationID string, ownerVersion int64, idempotencyKey string, oneShot *workitems.OneShotRef) (string, error) {
	if oneShot != nil {
		// Marker-first: the row lock is held to transaction end, so the
		// obligation/continuation writes below happen only under a running
		// marker on the claimed epoch. Never insert-then-check.
		if err := workitems.ValidateOneShotMarkerTx(ctx, txscope.Wrap(tx), *oneShot); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO placement_obligations (file_operation_id, state, max_attempts)
VALUES ($1, $2, $3)
ON CONFLICT (file_operation_id) DO NOTHING`,
		operationID, placementObligationPending, placementRetryMaxAttempts); err != nil {
		return "", fmt.Errorf("create placement obligation: %w", err)
	}
	snapshot, err := marshalPlacementContinuationInput(operationID, oneShot)
	if err != nil {
		return "", fmt.Errorf("encode placement continuation input: %w", err)
	}
	continuationID, _, err := s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: operationID},
		Kind:                 workitems.KindFileExecute,
		IdempotencyKey:       idempotencyKey,
		InputSnapshot:        snapshot,
		MaxAttempts:          placementRetryMaxAttempts,
		ExpectedOwnerVersion: ownerVersion,
		OneShot:              oneShot,
	})
	if err != nil {
		return "", fmt.Errorf("create placement continuation work item: %w", err)
	}
	return continuationID, nil
}

type placementObligationRecord struct {
	state    string
	attempts int
}

func (s *Service) loadPlacementObligation(ctx context.Context, operationID string) (placementObligationRecord, bool, error) {
	var record placementObligationRecord
	err := s.db.QueryRowContext(ctx, `
SELECT state, attempts FROM placement_obligations WHERE file_operation_id = $1`, operationID).
		Scan(&record.state, &record.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return placementObligationRecord{}, false, nil
	}
	if err != nil {
		return placementObligationRecord{}, false, fmt.Errorf("read placement obligation: %w", err)
	}
	return record, true, nil
}

// handlePlacementContinuation settles the independent post-completion
// placement/index/replacement-trigger handoff for one completed file effect.
// It never re-runs the file effect: the file operation stays terminal and the
// durable obligation row carries the stage state, attempts, and reason.
func (s *Service) handlePlacementContinuation(ctx context.Context, claim workitems.Claim, input placementContinuationInput) (worker.Outcome, error) {
	if s == nil || s.db == nil {
		return worker.Outcome{}, errors.New("file operation service is not configured")
	}
	obligation, found, err := s.loadPlacementObligation(ctx, claim.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if !found {
		// A committed continuation item always has a committed obligation row
		// (same transaction); a missing row is inconsistency, never a stage
		// attempt. Fail closed, terminal and visible.
		reason := "placement continuation has no durable obligation"
		outcome := worker.Finish(workitems.StateFailed, workitems.OwnerResult{
			State:      placementObligationFailed,
			ReasonCode: "placement_obligation_missing",
			Reason:     reason,
		}, s.placementObligationWriter(claim))
		return worker.WithAfterCommit(outcome, s.placementFailureDiagnosticAfterCommit(claim.Owner.ID, "placement_obligation_missing", errors.New(reason))), nil
	}
	if obligation.state == placementObligationAdmitted {
		// Already settled by an earlier attempt (or the immediate kick): settle
		// the continuation item idempotently without re-running the handoff.
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{
			State:      placementObligationAdmitted,
			ReasonCode: "placement_already_admitted",
			Reason:     "placement handoff was already settled",
		}, s.placementObligationWriter(claim)), nil
	}
	if s.placementSink == nil {
		return worker.Retry("placement_sink_unwired", "placement admission is not configured on this server", s.placementRetryDelay(), s.placementObligationWriter(claim)), nil
	}
	if err := s.placementSink.AdmitFromFileOperation(ctx, claim.Owner.ID); err != nil {
		reasonCode := PlacementSinkReason(err)
		return worker.Retry(reasonCode, safeReason(err), s.placementRetryDelay(), s.placementObligationWriter(claim)), nil
	}
	return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{
		State:      placementObligationAdmitted,
		ReasonCode: "placement_admitted",
		Reason:     "verified placement admission completed",
	}, s.placementObligationWriter(claim)), nil
}

// handleMalformedPlacementContinuation fail-closes a claim that declares
// placement-continuation intent without satisfying the strict discriminator.
// It never touches the file effect, terminalizes the item, and freezes the
// obligation (when present) as failed so the state stays explainable.
func (s *Service) handleMalformedPlacementContinuation(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if s == nil || s.db == nil {
		return worker.Outcome{}, errors.New("file operation service is not configured")
	}
	reason := "placement continuation input snapshot is malformed"
	outcome := worker.Finish(workitems.StateFailed, workitems.OwnerResult{
		State:      placementObligationFailed,
		ReasonCode: "placement_continuation_malformed",
		Reason:     reason,
	}, s.placementObligationWriter(claim))
	return worker.WithAfterCommit(outcome, s.placementFailureDiagnosticAfterCommit(claim.Owner.ID, "placement_continuation_malformed", errors.New(reason))), nil
}

// placementObligationWriter applies one placement-stage transition inside the
// WorkItem fencing transaction and returns the (never mutated) file operation
// owner version. The completed file operation is never rewritten: success
// settles the obligation, a retryable failure records attempts/reason, and the
// store's attempt budget maps to the terminal failed state. Admission is
// sticky: a late transition can never reopen an admitted obligation.
func (s *Service) placementObligationWriter(claim workitems.Claim) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("placement obligation writer transaction is nil")
		}
		var ownerVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM file_operations WHERE id = $1 FOR UPDATE`, claim.Owner.ID).Scan(&ownerVersion); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("%w: %q", ErrOperationNotFound, claim.Owner.ID)
			}
			return 0, fmt.Errorf("lock file operation for placement obligation: %w", err)
		}
		switch result.State {
		case placementObligationAdmitted:
			if _, err := tx.ExecContext(ctx, `
UPDATE placement_obligations
SET state = $2, attempts = LEAST(attempts + 1, max_attempts), reason_code = NULL, reason = NULL,
    settled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE file_operation_id = $1 AND state <> $2`, claim.Owner.ID, placementObligationAdmitted); err != nil {
				return 0, fmt.Errorf("settle placement obligation: %w", err)
			}
			s.recordPlacementDiagnosticSuccessTx(ctx, tx, claim.Owner.ID)
		case placementObligationWaitingRetry, placementObligationFailed:
			reasonCode := boundedCode(result.ReasonCode, placementDiagnosticErrorCode)
			reason := boundedReason(result.Reason, maxReasonLength)
			if _, err := tx.ExecContext(ctx, `
UPDATE placement_obligations
SET state = $2, attempts = LEAST(attempts + 1, max_attempts), reason_code = $3, reason = $4, updated_at = CURRENT_TIMESTAMP
WHERE file_operation_id = $1 AND state <> $5`, claim.Owner.ID, result.State, nullableObligationReason(reasonCode), nullableObligationReason(reason), placementObligationAdmitted); err != nil {
				return 0, fmt.Errorf("record placement obligation failure: %w", err)
			}
			s.recordPlacementDiagnosticFailureTx(ctx, tx, claim.Owner.ID, reasonCode, reason)
		default:
			return 0, fmt.Errorf("invalid placement obligation transition state %q", result.State)
		}
		return ownerVersion, nil
	}
}

func nullableObligationReason(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Service) recordPlacementDiagnosticSuccessTx(ctx context.Context, tx *sql.Tx, operationID string) {
	if s.diagnostics == nil {
		return
	}
	if err := s.diagnostics.RecordDiagnosticSuccessTx(ctx, txscope.Wrap(tx), observabilityport.DiagnosticSuccessRequest{
		Kind: observabilityport.KindInternal, TargetID: observabilityport.InternalPlacementSink,
	}); err != nil && s.logger != nil {
		s.logger.Error(ctx, observabilityport.RuntimeEvent{
			Message:    "placement diagnostic success could not be recorded",
			Component:  "fileoperation",
			Operation:  placementDiagnosticOperation,
			Outcome:    "failure",
			OwnerType:  workitems.OwnerFileOperation,
			OwnerID:    operationID,
			ErrorClass: "placement_sink",
		})
	}
}

func (s *Service) recordPlacementDiagnosticFailureTx(ctx context.Context, tx *sql.Tx, operationID, reasonCode, reason string) {
	if s.diagnostics == nil {
		return
	}
	request := observabilityport.InternalFailure(observabilityport.InternalPlacementSink, "")
	request.Severity = observabilityport.SeverityWarning
	if err := s.diagnostics.RecordDiagnosticFailureTx(ctx, txscope.Wrap(tx), request); err != nil && s.logger != nil {
		s.logger.Error(ctx, observabilityport.RuntimeEvent{
			Message: "placement diagnostic failure could not be recorded", Component: "fileoperation",
			Operation: placementDiagnosticOperation, Outcome: "failure", ReasonCode: reasonCode,
			OwnerType: workitems.OwnerFileOperation, OwnerID: operationID, ErrorClass: "placement_sink",
		})
	}
}

// placementFailureDiagnosticAfterCommit records a placement-stage fault when no
// fencing transaction is available (terminal malformed/missing-obligation
// paths). Best-effort visibility only: the durable obligation row and the
// terminal WorkItem are the authoritative records.
func (s *Service) placementFailureDiagnosticAfterCommit(operationID, reasonCode string, cause error) func(context.Context) {
	request := observabilityport.InternalFailure(observabilityport.InternalPlacementSink, "")
	request.Severity = observabilityport.SeverityWarning
	return observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger, request)
}

// placementRetryDelay is the backoff between placement-stage attempts. It is
// the production default unless a test-only composition overrides it; the
// override never changes the attempt budget or the state machine.
func (s *Service) placementRetryDelay() time.Duration {
	if s != nil && s.placementRetryAfter > 0 {
		return s.placementRetryAfter
	}
	return defaultRetryAfter
}

// SetPlacementRetryDelay overrides the placement-stage retry backoff. It exists
// for disposable fixture sessions that must observe the real retry and
// exhaustion transitions within a browser test; it is never wired from a
// production configuration path.
func (s *Service) SetPlacementRetryDelay(delay time.Duration) {
	if s != nil {
		s.placementRetryAfter = delay
	}
}

func placementFailureDetails(details map[string]string) json.RawMessage {
	encoded, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	return encoded
}

func boundedReason(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}
