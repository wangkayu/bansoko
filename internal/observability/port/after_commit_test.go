package port

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/persistence/txscope"
)

// after_commit: best-effort diagnostic closures. A failed diagnostic write
// never blocks the business path — it only emits a safe operator log line, and
// nil recorder returns nil closure.

func TestConditionCatalogIsUniqueAndValidatesItsRequests(t *testing.T) {
	seen := map[Condition]bool{}
	for _, spec := range ConditionCatalog() {
		if seen[spec.Condition] {
			t.Fatalf("duplicate condition %+v", spec.Condition)
		}
		seen[spec.Condition] = true
		if spec.Audience == AudienceInternal {
			continue
		}
		if spec.Retry == RetrySettings && spec.Settings == "" {
			t.Errorf("%+v retries via settings but names no settings target", spec.Condition)
		}
		req := DiagnosticFailureRequest{Condition: spec.Condition, Severity: SeverityWarning, TargetID: "t-1"}
		if !req.Validate() {
			t.Errorf("catalogued condition %+v rejected by Validate", spec.Condition)
		}
	}
	if (DiagnosticFailureRequest{Condition: Condition{Kind: KindMetadata, Variant: "nope"}, Severity: SeverityWarning, TargetID: "t"}).Validate() {
		t.Fatal("uncatalogued variant accepted")
	}
	if (DiagnosticFailureRequest{Condition: Condition{Kind: KindInternal}, Severity: SeverityError, TargetID: "not.a.source"}).Validate() {
		t.Fatal("unknown internal source accepted")
	}
	if !InternalFailure(InternalAutomationDispatch, "").Validate() {
		t.Fatal("internal failure rejected")
	}
}

type recordingRecorder struct {
	failures []DiagnosticFailureRequest
	success  []DiagnosticSuccessRequest
	err      error
}

func (r *recordingRecorder) RecordDiagnosticFailure(_ context.Context, req DiagnosticFailureRequest) error {
	r.failures = append(r.failures, req)
	return r.err
}
func (r *recordingRecorder) RecordDiagnosticSuccess(_ context.Context, req DiagnosticSuccessRequest) error {
	r.success = append(r.success, req)
	return r.err
}
func (r *recordingRecorder) UpsertRoutine(context.Context, RoutineRequest) error { return nil }
func (r *recordingRecorder) UpsertRoutineTx(context.Context, txscope.Tx, RoutineRequest) error {
	return nil
}
func (r *recordingRecorder) RecordDiagnosticFailureTx(context.Context, txscope.Tx, DiagnosticFailureRequest) error {
	return nil
}
func (r *recordingRecorder) RecordDiagnosticSuccessTx(context.Context, txscope.Tx, DiagnosticSuccessRequest) error {
	return nil
}

// captureRuntimeLogger records emitted runtime events for assertions.
type captureRuntimeLogger struct {
	events []RuntimeEvent
}

func (l *captureRuntimeLogger) Debug(_ context.Context, event RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *captureRuntimeLogger) Info(_ context.Context, event RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *captureRuntimeLogger) Warn(_ context.Context, event RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *captureRuntimeLogger) Error(_ context.Context, event RuntimeEvent) {
	l.events = append(l.events, event)
}

func TestDiagnosticFailureAfterCommitInvokesRecorder(t *testing.T) {
	rec := &recordingRecorder{}
	req := InternalFailure(InternalRSSRefresh, "")
	closure := DiagnosticFailureAfterCommit(rec, nil, req)
	if closure == nil {
		t.Fatal("closure must not be nil for a configured recorder")
	}
	closure(context.Background())
	if len(rec.failures) != 1 || rec.failures[0].TargetID != InternalRSSRefresh {
		t.Fatalf("failure not recorded: %+v", rec.failures)
	}
}

func TestDiagnosticSuccessAfterCommitInvokesRecorder(t *testing.T) {
	rec := &recordingRecorder{}
	closure := DiagnosticSuccessAfterCommit(rec, nil, InternalSuccess(InternalRSSRefresh))
	closure(context.Background())
	if len(rec.success) != 1 || rec.success[0].Kind != KindInternal {
		t.Fatalf("success not recorded: %+v", rec.success)
	}
}

func TestAfterCommitNilRecorderReturnsNilClosure(t *testing.T) {
	if closure := DiagnosticFailureAfterCommit(nil, nil, DiagnosticFailureRequest{}); closure != nil {
		t.Fatal("nil recorder must yield nil closure")
	}
	if closure := DiagnosticSuccessAfterCommit(nil, nil, DiagnosticSuccessRequest{}); closure != nil {
		t.Fatal("nil recorder must yield nil closure")
	}
}

func TestFailedDiagnosticWriteLogsButDoesNotError(t *testing.T) {
	logger := &captureRuntimeLogger{}
	rec := &recordingRecorder{err: errors.New("write failed")}
	closure := DiagnosticFailureAfterCommit(rec, logger, InternalFailure(InternalRSSRefresh, ""))
	closure(context.Background())
	if len(logger.events) != 1 || logger.events[0].ReasonCode != "diagnostic_write_failed" {
		t.Fatalf("expected structured diagnostic_write_failed event, got %+v", logger.events)
	}
	if logger.events[0].OwnerType != string(KindInternal) || logger.events[0].OwnerID != InternalRSSRefresh {
		t.Fatalf("event lost typed identity: %+v", logger.events[0])
	}
}
