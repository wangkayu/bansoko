package application

import (
	"context"
	"errors"
	"fmt"
	"os"

	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// typedPublicationInvoker is the filesystem-adapter port for the typed
// publication chain: one synchronous invocation owns every mutation, grant
// callbacks run before each syscall, and every receipt is recorded through the
// owner-wired sink. The concrete adapter (OSFileExecutor) provides all four
// capabilities; no test fake may implement this port for production claims.
type typedPublicationInvoker interface {
	domain.PublicationPreparer
	NewPublicationInvocation() (*filesystem.PublicationInvocation, error)
	EnsurePublicationParent(root, target string) error
}

// typedExecutionAdapter returns the typed invocation port. The production
// filesystem adapter is the only implementation; a service configured with an
// executor that lacks it fails closed instead of improvising a typed lane.
func (s *Service) typedExecutionAdapter() (typedPublicationInvoker, error) {
	if s == nil || s.executor == nil {
		return nil, errors.New("file operation service is not configured")
	}
	invoker, ok := s.executor.(typedPublicationInvoker)
	if !ok {
		return nil, errors.New("executor lacks the typed publication invocation port")
	}
	return invoker, nil
}

// runTypedPublication drives the full typed chain for one claim, mirroring the
// production order proven by the S4 chain evidence: attempt ticket → frozen
// preparation → permit → single adapter invocation (dispatch/step
// grants + receipts + join through the trusted sink) → independent proof read.
// Any failure returns without classifying the external effect: the persistent
// evidence owns the record of what happened (receipts stay unknown on error).
//
// This lane is reachable ONLY from handleTypedExecution (the runtime worker
// entry for prepared operations); the permit id is derived deterministically
// from the operation so a re-claim binds to the same evidence row.
func (s *Service) runTypedPublication(ctx context.Context, claim workitems.Claim, operation operationRecord) (worker.Outcome, error) {
	invoker, portErr := s.typedExecutionAdapter()
	if portErr != nil {
		return s.finishTypedFailure(ctx, claim, "typed_publication_unsupported", portErr)
	}
	invocation, invocationErr := invoker.NewPublicationInvocation()
	if invocationErr != nil {
		return worker.Outcome{}, invocationErr
	}
	session := invocation.Session()
	permit := typedPublicationPermitID(claim.Owner.ID)
	attempt, attemptErr := s.startFileAttempt(ctx, claim, session.ExecutorSession)
	if attemptErr != nil {
		if code := typedAttemptFailureCode(attemptErr); code != "" {
			return s.finishTypedFailure(ctx, claim, code, attemptErr)
		}
		return worker.Outcome{}, attemptErr
	}
	if !attempt.Granted {
		return s.finishTypedFailure(ctx, claim, attemptBudgetReason(attempt.ReasonCode), fmt.Errorf("file attempt budget: %s", attempt.ReasonCode))
	}
	input, inputErr := typedPublicationInput(claim, operation)
	if inputErr != nil {
		return s.finishTypedFailure(ctx, claim, "source_facts_invalid", inputErr)
	}
	// B5b: the attempt grant is held; create the missing target parent (and
	// so the subtitle's, which is the same directory). The target is absolute,
	// so the walk starts at the filesystem root with nofollow components.
	if parentErr := invoker.EnsurePublicationParent("", input.TargetPath); parentErr != nil {
		if ctx.Err() != nil {
			return worker.Outcome{}, fmt.Errorf("typed publication interrupted: %w", parentErr)
		}
		return s.finishTypedFailure(ctx, claim, "target_parent_invalid", parentErr)
	}
	// The attempt ticket bounds the preparation reads too; the runtime handler
	// deadline is only a hang ceiling for storage stages. An overrun here
	// precedes the permit, so nothing was dispatched and the claim retries on
	// a fresh attempt of the same bounded budget.
	prepCtx, cancelPrep := context.WithDeadline(ctx, attempt.Ticket.AttemptUntil)
	prep, prepErr := invoker.PreparePublication(prepCtx, input, permit)
	overran := preparationOverran(ctx, prepCtx, prepErr)
	cancelPrep()
	if overran {
		return worker.Retry("file_attempt_deadline", "file preparation exceeded its attempt deadline before any publication", defaultRetryAfter, nil), nil
	}
	if prepErr != nil {
		return s.typedPublicationFaultOutcome(ctx, claim, operation, prepErr)
	}
	created, permitErr := s.prepareFilePermit(ctx, claim, session.ExecutorSession, permit, input, prep)
	if permitErr != nil {
		return s.typedPublicationFaultOutcome(ctx, claim, operation, permitErr)
	}
	if !created {
		// The permit is deterministic per operation: an existing row bound to
		// another session/token is never grafted onto; end visibly.
		return s.finishTypedFailure(ctx, claim, "FILE_PREPARE_SESSION_MISMATCH", errors.New("publication permit already prepared for another session"))
	}
	callbacks := s.typedPublicationCallbacks(claim, permit, input)
	var executeErr error
	switch domain.OperationKind(operation.operation) {
	case domain.OperationHardlink:
		if operation.collisionPolicy == string(domain.CollisionBlock) {
			executeErr = invocation.ExecuteHardlink(ctx, input, prep, callbacks)
		} else {
			executeErr = invocation.ExecuteCopy(ctx, input, prep, callbacks)
		}
	default:
		executeErr = invocation.ExecuteCopy(ctx, input, prep, callbacks)
	}
	if executeErr != nil {
		// The recorded receipts keep the exact per-step state (confirmed vs
		// unknown). Proof reading decides reachability; no synthetic retry.
		return s.typedPublicationFaultOutcome(ctx, claim, operation, executeErr)
	}
	if _, proofErr := s.loadPublicationProof(ctx, permit); proofErr != nil {
		return s.typedPublicationFaultOutcome(ctx, claim, operation, proofErr)
	}
	prepared, prepareErr := s.prepareSuccessfulFileResult(ctx, claim, permit)
	if prepareErr != nil {
		return s.typedFinishBlockedOutcome(ctx, claim, operation, prepareErr)
	}
	return s.finishPreparedFileSuccess(ctx, claim, prepared)
}

// preparationOverran reports a preparation failure caused by the attempt
// ticket deadline alone: the handler itself is still live (no lease loss,
// drain or runtime ceiling), so the failure is a retryable budget overrun.
func preparationOverran(handler, preparation context.Context, err error) bool {
	return err != nil && handler.Err() == nil && errors.Is(preparation.Err(), context.DeadlineExceeded)
}

// typedPublicationPermitID derives the deterministic evidence id: one typed
// operation carries exactly one publication permit for its lifetime, so a
// re-claim and the original execution bind to the same row.
func typedPublicationPermitID(operationID string) string {
	return "pub-" + operationID
}

// typedPublicationInput maps the frozen owner row to the adapter input. The
// fingerprint/size facts are the frozen operation columns; paths are absolute
// canonical values frozen at prepare.
func typedPublicationInput(claim workitems.Claim, operation operationRecord) (domain.FileOperation, error) {
	fingerprint, fingerprintErr := decodeFingerprint(operation.sourceFingerprint)
	if fingerprintErr != nil || !validSourcePath(operation.sourcePath) {
		return domain.FileOperation{}, fmt.Errorf("frozen source fingerprint invalid: %w", fingerprintErr)
	}
	input := domain.FileOperation{
		ID:                operation.id,
		Operation:         domain.OperationKind(operation.operation),
		SourcePath:        operation.sourcePath,
		TargetPath:        operation.targetPath,
		State:             domain.OperationPlanned,
		SourceFingerprint: fingerprint.Value,
		CollisionPolicy:   domain.CollisionPolicy(operation.collisionPolicy),
		Intent:            domain.OperationIntent(operation.intent),
		IdempotencyKey:    operation.idempotencyKey,
		FencingToken:      claim.FencingToken,
		Version:           operation.version,
	}
	if fingerprint.HasSize {
		input.SourceSizeBytes = fingerprint.SizeBytes
		input.SourceSizeKnown = true
	}
	if operation.expectedSizeBytes.Valid {
		input.ExpectedSizeBytes = operation.expectedSizeBytes.Int64
		input.ExpectedSizeKnown = true
	}
	return input, nil
}

// typedPublicationFaultOutcome maps a typed-chain failure to the bounded
// product outcomes: an interrupted publication leaves per-step evidence
// unknown — the item stays reclaimable and the next claim re-reads the exact
// persisted state. Only a proven-not-exist source is a terminal no-effect
// classification, mirroring the legacy contract.
func (s *Service) typedPublicationFaultOutcome(ctx context.Context, claim workitems.Claim, operation operationRecord, cause error) (worker.Outcome, error) {
	if ctx.Err() != nil {
		// The run itself was stopped (lease lost, deadline, drain): report it
		// like a crash; the next claim converges from persistent evidence.
		return worker.Outcome{}, fmt.Errorf("typed publication interrupted: %w", cause)
	}
	if errors.Is(cause, os.ErrNotExist) {
		if _, statErr := os.Lstat(operation.sourcePath); errors.Is(statErr, os.ErrNotExist) {
			return s.finishTypedFailure(ctx, claim, "SOURCE_MISSING", cause)
		}
	}
	// Inherent filesystem limits (hardlink across devices or unsupported,
	// invalid or too-long names, permissions, a changed source or target) are
	// terminal per-file failures with their own code. None is retried and
	// none switches to another operation kind.
	if code := domain.PublicationFailureCode(cause); domain.TerminalPublicationCode(code) {
		return s.finishTypedFailure(ctx, claim, code, cause)
	}
	if isRetryable(cause) {
		return worker.Retry("typed_publication_transient", safeReason(cause), defaultRetryAfter, nil), nil
	}
	return s.finishTypedFailure(ctx, claim, "typed_publication_failed", cause)
}

// attemptBudgetReason bounds the attempt-grant reason code to a stable
// fail-closed product code.
func attemptBudgetReason(reason string) string {
	if reason == "" {
		return "FILE_ATTEMPT_BUDGET_DENIED"
	}
	return boundedCode(reason, "FILE_ATTEMPT_BUDGET_DENIED")
}
