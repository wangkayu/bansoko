package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
)

type indexRecorder struct {
	observabilityport.Recorder
	failures  []observabilityport.DiagnosticFailureRequest
	successes []observabilityport.DiagnosticSuccessRequest
}

func (r *indexRecorder) RecordDiagnosticFailure(_ context.Context, req observabilityport.DiagnosticFailureRequest) error {
	r.failures = append(r.failures, req)
	return nil
}

func (r *indexRecorder) RecordDiagnosticSuccess(_ context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	r.successes = append(r.successes, req)
	return nil
}

func TestIndexReachabilityDiagnostics(t *testing.T) {
	recorder := &indexRecorder{}
	service := &Service{diagnostics: recorder}
	ctx := context.Background()
	unreachable := outbound.WithFailure(&apperrors.Business{Status: 503, Code: "UPDATE_CHANNEL_UNAVAILABLE"}, outbound.Failure{Host: "updates.example", Class: outbound.ClassTimeout, Via: outbound.ViaDirect})

	// A refused or unverifiable index is not a reachability fact.
	service.recordIndexReachability(ctx, Check{Status: "check_failed"}, errors.New("signature rejected"))
	if len(recorder.failures) != 0 {
		t.Fatalf("verification refusal recorded as unreachable: %+v", recorder.failures)
	}

	service.recordIndexReachability(ctx, Check{Status: "check_failed"}, unreachable)
	service.recordIndexReachability(ctx, Check{Status: "check_failed"}, unreachable)
	if len(recorder.failures) != 2 || recorder.failures[0].Condition.Kind != observabilityport.KindUpdate || recorder.failures[0].Failure.Host != "updates.example" {
		t.Fatalf("failures = %+v", recorder.failures)
	}

	// A check that returned before reading the index closes nothing.
	service.recordIndexReachability(ctx, Check{}, nil)
	if len(recorder.successes) != 0 {
		t.Fatalf("closed without reading the index: %+v", recorder.successes)
	}
	service.recordIndexReachability(ctx, Check{Status: "up_to_date"}, nil)
	if len(recorder.successes) != 1 || recorder.successes[0].Kind != observabilityport.KindUpdate {
		t.Fatalf("successes = %+v", recorder.successes)
	}
}
