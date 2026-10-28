package application

import (
	"context"
	"errors"
	"testing"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/worker"
)

type syncRecorder struct {
	observabilityport.Recorder
	failures  []observabilityport.DiagnosticFailureRequest
	successes []observabilityport.DiagnosticSuccessRequest
}

func (r *syncRecorder) RecordDiagnosticFailure(_ context.Context, req observabilityport.DiagnosticFailureRequest) error {
	r.failures = append(r.failures, req)
	return nil
}

func (r *syncRecorder) RecordDiagnosticSuccess(_ context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	r.successes = append(r.successes, req)
	return nil
}

func syncStep(t *testing.T, s *Service, code, task string) {
	t.Helper()
	out, err := s.withSyncDiagnostics(worker.Outcome{Kind: worker.OutcomeCommitted}, nil, code, task, errors.New("dial"))
	if err != nil {
		t.Fatal(err)
	}
	if out.AfterCommit != nil {
		out.AfterCommit(context.Background())
	}
}

func TestSyncFailuresOpenDownloaderEpisodeAfterThreeInARow(t *testing.T) {
	rec := &syncRecorder{}
	s := &Service{diagnostics: rec}
	syncStep(t, s, "qb_sync_succeeded", "t0") // first success after start closes once
	if len(rec.successes) != 1 {
		t.Fatalf("successes = %d", len(rec.successes))
	}
	syncStep(t, s, "qb_sync_succeeded", "t0") // ordinary success touches nothing
	if len(rec.successes) != 1 {
		t.Fatalf("ordinary success wrote: %d", len(rec.successes))
	}
	syncStep(t, s, "qb_sync_retryable", "t1")
	syncStep(t, s, "qb_sync_retryable", "t2")
	if len(rec.failures) != 0 {
		t.Fatalf("opened before the third failure: %+v", rec.failures)
	}
	syncStep(t, s, "qb_sync_retryable", "t3")
	syncStep(t, s, "qb_sync_retryable", "t4")
	if len(rec.failures) != 2 || rec.failures[1].AffectedID != "t4" || rec.failures[0].Condition.Kind != observabilityport.KindDownloader {
		t.Fatalf("failures = %+v", rec.failures)
	}
	syncStep(t, s, "qb_sync_succeeded", "t1")
	if len(rec.successes) != 2 {
		t.Fatalf("recovery did not close: %d", len(rec.successes))
	}
	// A run broken by a success starts counting again from one.
	syncStep(t, s, "qb_sync_retryable", "t5")
	syncStep(t, s, "qb_sync_succeeded", "t5")
	syncStep(t, s, "qb_sync_retryable", "t6")
	syncStep(t, s, "qb_sync_retryable", "t7")
	if len(rec.failures) != 2 {
		t.Fatalf("a broken run opened an episode: %d", len(rec.failures))
	}
	// Failures that are not connection-class neither count nor reset.
	syncStep(t, s, "qb_sync_failed", "t8")
	syncStep(t, s, "qb_sync_retryable", "t9")
	if len(rec.failures) != 3 {
		t.Fatalf("failures = %d", len(rec.failures))
	}
}
