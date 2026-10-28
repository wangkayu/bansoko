package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/observability/outbound"
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

var dialFailure = outbound.WithFailure(errors.New("dial"), outbound.Failure{Host: "qb.lan", Class: outbound.ClassConnectionRefused, Via: outbound.ViaDirect})

func syncStep(t *testing.T, s *Service, code, task string) {
	t.Helper()
	syncStepWith(t, s, code, task, dialFailure)
}

func syncStepWith(t *testing.T, s *Service, code, task string, cause error) {
	t.Helper()
	out, err := s.withSyncDiagnostics(worker.Outcome{Kind: worker.OutcomeCommitted}, nil, code, task, cause)
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

func TestSyncFailureRecordsOutboundFacts(t *testing.T) {
	rec := &syncRecorder{}
	s := &Service{diagnostics: rec}
	for _, task := range []string{"t1", "t2", "t3"} {
		syncStep(t, s, "qb_sync_retryable", task)
	}
	got := rec.failures[0].Failure
	if got.Host != "qb.lan" || got.Class != outbound.ClassConnectionRefused || got.Via != outbound.ViaDirect {
		t.Fatalf("failure facts = %+v", got)
	}
}

// A retryable failure without outbound facts (an HTTP 5xx answer) is not
// evidence the downloader is unreachable.
func TestSyncFailureWithoutFactsDoesNotCount(t *testing.T) {
	rec := &syncRecorder{}
	s := &Service{diagnostics: rec}
	for _, task := range []string{"t1", "t2", "t3", "t4"} {
		syncStepWith(t, s, "qb_sync_retryable", task, errors.New("qB request failed with HTTP 503"))
	}
	if len(rec.failures) != 0 {
		t.Fatalf("opened without facts: %+v", rec.failures)
	}
}

func TestSyncAuthFailureOpensAtOnceAndResetsRun(t *testing.T) {
	rec := &syncRecorder{}
	s := &Service{diagnostics: rec}
	syncStep(t, s, "qb_sync_succeeded", "t0")
	syncStep(t, s, "qb_sync_retryable", "t1")
	syncStep(t, s, "qb_sync_retryable", "t2")
	syncStepWith(t, s, "qb_sync_retryable", "t3", fmt.Errorf("login: %w", qbittorrent.ErrAuthFailed))
	if len(rec.failures) != 1 || rec.failures[0].Condition.Variant != observabilityport.VariantAuthFailed || rec.failures[0].AffectedID != "t3" {
		t.Fatalf("failures = %+v", rec.failures)
	}
	syncStep(t, s, "qb_sync_retryable", "t4") // run restarted
	if len(rec.failures) != 1 {
		t.Fatalf("auth failure did not reset the run: %+v", rec.failures)
	}
	before := len(rec.successes)
	syncStep(t, s, "qb_sync_succeeded", "t5")
	if len(rec.successes) != before+1 {
		t.Fatal("success after refused credentials did not close the episode")
	}
}

func TestSubmitFailureDiagnosticMapsTypedCause(t *testing.T) {
	cases := map[string]struct {
		code     string
		cause    error
		kind     observabilityport.ConditionKind
		variant  observabilityport.ConditionVariant
		hostFact string
	}{
		"transport": {"qb_submit_prepare_failed", fmt.Errorf("prepare: %w", dialFailure), observabilityport.KindDownloader, observabilityport.VariantUnreachable, "qb.lan"},
		"auth":      {"qb_submit_prepare_failed", fmt.Errorf("prepare: %w", qbittorrent.ErrAuthFailed), observabilityport.KindDownloader, observabilityport.VariantAuthFailed, ""},
		"config":    {"qb_connection_unavailable", errors.New("runtime config"), observabilityport.KindInternal, "", ""},
		"other":     {"qb_submit_prepare_failed", errors.New("qB request failed with HTTP 500"), observabilityport.KindInternal, "", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := &syncRecorder{}
			s := &Service{diagnostics: rec}
			s.submitFailureDiagnostic("task-1", tc.code, tc.cause)(context.Background())
			if len(rec.failures) != 1 {
				t.Fatalf("failures = %+v", rec.failures)
			}
			got := rec.failures[0]
			if got.Condition.Kind != tc.kind || got.Condition.Variant != tc.variant || got.Failure.Host != tc.hostFact {
				t.Fatalf("request = %+v", got)
			}
			if tc.kind == observabilityport.KindDownloader && got.AffectedID != "task-1" {
				t.Fatalf("affected = %q", got.AffectedID)
			}
		})
	}
}
