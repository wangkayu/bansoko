package port

import (
	"context"
	"errors"
	"sort"
	"testing"

	"bansoko/internal/persistence/txscope"
)

// after_commit: best-effort diagnostic closures. A failed diagnostic write
// never blocks the business path — it only emits a safe operator log line, and
// nil recorder returns nil closure.

func TestDiagnosticComponentsCoverAcceptedComponentsInStableOrder(t *testing.T) {
	components := DiagnosticComponents()
	if !sort.SliceIsSorted(components, func(i, j int) bool { return components[i] < components[j] }) {
		t.Fatal("DiagnosticComponents() is not sorted")
	}
	if len(components) != len(diagnosticComponents) {
		t.Fatalf("DiagnosticComponents() returned %d entries; allowlist has %d", len(components), len(diagnosticComponents))
	}
	for _, component := range components {
		if !IsDiagnosticComponent(component) {
			t.Errorf("enumerated component %q is not accepted by the write-path validator", component)
		}
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
	req := DiagnosticFailureRequest{Identity: RSSRefreshFailure(), ScopeID: "s-1"}
	closure := DiagnosticFailureAfterCommit(rec, nil, req)
	if closure == nil {
		t.Fatal("closure must not be nil for a configured recorder")
	}
	closure(context.Background())
	if len(rec.failures) != 1 || rec.failures[0].Identity.Component != ComponentRSS {
		t.Fatalf("failure not recorded: %+v", rec.failures)
	}
}

func TestDiagnosticSuccessAfterCommitInvokesRecorder(t *testing.T) {
	rec := &recordingRecorder{}
	req := DiagnosticSuccessRequest{Identity: RSSRefreshFailure(), ScopeID: "s-1"}
	closure := DiagnosticSuccessAfterCommit(rec, nil, req)
	closure(context.Background())
	if len(rec.success) != 1 || rec.success[0].Identity.ErrorCode != ErrorRSSRefreshFailed {
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
	req := DiagnosticFailureRequest{Identity: RSSRefreshFailure(), ScopeID: "s-1"}
	closure := DiagnosticFailureAfterCommit(rec, logger, req)
	// The closure must not panic/return an error to the caller; it just logs.
	closure(context.Background())
	if len(logger.events) != 1 || logger.events[0].ReasonCode != "diagnostic_write_failed" {
		t.Fatalf("expected structured diagnostic_write_failed event, got %+v", logger.events)
	}
	if logger.events[0].ErrorClass != string(ErrorRSSRefreshFailed) || logger.events[0].Component != string(ComponentRSS) {
		t.Fatalf("event lost typed identity: %+v", logger.events[0])
	}
}
