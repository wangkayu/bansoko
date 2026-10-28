package application

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

func TestStartBaselineApplies(t *testing.T) {
	baseline := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	on := releaseSnapshot{newReleasesOnly: true, startBaselineAt: sql.NullTime{Time: baseline, Valid: true}}
	cases := []struct {
		name      string
		snapshot  releaseSnapshot
		published time.Time
		want      bool
	}{
		{"before the creation", on, baseline.Add(-time.Second), true},
		{"no publish time", on, time.Time{}, true},
		{"at the creation", on, baseline, false},
		{"after the creation (pending period)", on, baseline.Add(time.Hour), false},
		{"option off", releaseSnapshot{}, baseline.Add(-time.Hour), false},
		{"explicit retry skips the gate", releaseSnapshot{newReleasesOnly: true, startBaselineAt: on.startBaselineAt, manualRetry: true}, time.Time{}, false},
	}
	for _, tc := range cases {
		got, err := startBaselineApplies(tc.snapshot, tc.published)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v err %v, want %v", tc.name, got, err, tc.want)
		}
	}
	if _, err := startBaselineApplies(releaseSnapshot{newReleasesOnly: true}, baseline); !errors.Is(err, errStartBaselineMissing) {
		t.Fatalf("the option without a baseline must fail closed, err=%v", err)
	}
}

func TestStartBaselineIsTerminalButRetryable(t *testing.T) {
	if !isTerminalReleaseVerdict(ReleaseVerdictStartBaseline) {
		t.Fatal("start_baseline must be terminal for automatic paths")
	}
	decision := ReleaseRetryability(ReleaseRetryFacts{Verdict: ReleaseVerdictStartBaseline, LifecycleState: "active", SubscriptionState: "active"})
	if !decision.Allowed {
		t.Fatalf("start_baseline must stay retryable: %+v", decision)
	}
	if got := observabilityport.ReleaseVerdictMessage(ReleaseVerdictStartBaseline); got != observabilityport.MessageReleaseStartBaseline {
		t.Fatalf("message = %q", got)
	}
}
