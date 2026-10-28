package port

import (
	"context"
)

func recordDiagnosticError(logger RuntimeLogger, kind, variant, targetID, result string) {
	if logger == nil {
		logger = NopRuntimeLogger{}
	}
	logger.Error(context.Background(), RuntimeEvent{
		Message:    "observability persistence failed",
		Component:  "observability",
		Operation:  "diagnostic_write",
		Outcome:    "failure",
		ReasonCode: result,
		OwnerType:  kind,
		OwnerID:    targetID,
		ErrorClass: variant,
	})
}

// RecordDiagnosticError reports a best-effort diagnostic persistence failure
// without exposing the underlying error, which may contain infrastructure details.
func RecordDiagnosticError(logger RuntimeLogger, kind, variant, targetID, result string) {
	recordDiagnosticError(logger, kind, variant, targetID, result)
}

// RecordRoutineError reports a best-effort routine persistence failure without
// exposing the underlying error, which may contain infrastructure details.
func RecordRoutineError(logger RuntimeLogger, kind, scopeKind, scopeID, result string) {
	if logger == nil {
		logger = NopRuntimeLogger{}
	}
	logger.Error(context.Background(), RuntimeEvent{
		Message:    "observability persistence failed",
		Component:  "observability",
		Operation:  "routine_write",
		Outcome:    "failure",
		ReasonCode: result,
		OwnerType:  scopeKind,
		OwnerID:    scopeID,
		ErrorClass: kind,
	})
}

// DiagnosticFailureAfterCommit returns the best-effort after-commit closure
// for one diagnostic failure. A failed diagnostic write never blocks the
// business path: it only emits a safe operator log line.
func DiagnosticFailureAfterCommit(recorder Recorder, logger RuntimeLogger, req DiagnosticFailureRequest) func(context.Context) {
	if recorder == nil {
		return nil
	}
	return func(ctx context.Context) {
		if err := recorder.RecordDiagnosticFailure(ctx, req); err != nil {
			recordDiagnosticError(logger, string(req.Condition.Kind), string(req.Condition.Variant), req.TargetID, "diagnostic_write_failed")
		}
	}
}

// DiagnosticSuccessAfterCommit returns the best-effort after-commit closure
// that closes the open episode for the same operation identity. Closing an
// episode that does not exist is a no-op.
func DiagnosticSuccessAfterCommit(recorder Recorder, logger RuntimeLogger, req DiagnosticSuccessRequest) func(context.Context) {
	if recorder == nil {
		return nil
	}
	return func(ctx context.Context) {
		if err := recorder.RecordDiagnosticSuccess(ctx, req); err != nil {
			recordDiagnosticError(logger, string(req.Kind), "", req.TargetID, "diagnostic_close_failed")
		}
	}
}
