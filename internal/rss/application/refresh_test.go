package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

type refreshCaptureLogger struct {
	events []observabilityport.RuntimeEvent
}

func (l *refreshCaptureLogger) Debug(_ context.Context, event observabilityport.RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *refreshCaptureLogger) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *refreshCaptureLogger) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	l.events = append(l.events, event)
}
func (l *refreshCaptureLogger) Error(_ context.Context, event observabilityport.RuntimeEvent) {
	l.events = append(l.events, event)
}

func TestRefreshTriggerIsManual(t *testing.T) {
	tests := []struct {
		name     string
		snapshot json.RawMessage
		want     bool
	}{
		{name: "manual trigger", snapshot: json.RawMessage(`{"trigger":"manual"}`), want: true},
		{name: "subscribe initial trigger", snapshot: json.RawMessage(`{"trigger":"subscribe_initial"}`), want: false},
		{name: "dispatcher item carries no snapshot", snapshot: nil, want: false},
		{name: "corrupt snapshot", snapshot: json.RawMessage(`{broken`), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := refreshTriggerIsManual(test.snapshot); got != test.want {
				t.Fatalf("refreshTriggerIsManual(%s) = %v, want %v", test.snapshot, got, test.want)
			}
		})
	}
}

func TestRefreshOutcomeLog(t *testing.T) {
	tests := []struct {
		name       string
		outcome    refreshLogOutcome
		wantReason string
		wantParts  []string
		wantEvents int
	}{
		{
			name:       "304",
			outcome:    refreshLogOutcome{notModified: true},
			wantReason: "rss_refresh_not_modified",
			wantParts:  []string{"no feed change", "HTTP 304"},
			wantEvents: 1,
		},
		{
			name:       "resume baseline",
			outcome:    refreshLogOutcome{resumeBaseline: true, newCount: 2, windowTotal: 7},
			wantReason: "rss_refresh_resume_baseline",
			wantParts:  []string{"resume baseline", "window=7", "stored=2", "releases were not evaluated"},
			wantEvents: 1,
		},
		{
			name:       "content but no new releases",
			outcome:    refreshLogOutcome{windowTotal: 4},
			wantReason: "rss_refresh_no_new_releases",
			wantParts:  []string{"found 4 feed item(s)", "no new releases"},
			wantEvents: 1,
		},
		{
			name:       "new releases",
			outcome:    refreshLogOutcome{newCount: 3, windowTotal: 9},
			wantReason: "rss_refresh_new_releases",
			wantParts:  []string{"stored 3 new release(s)", "window of 9 item(s)"},
			wantEvents: 1,
		},
		{
			name:       "empty window records nothing",
			outcome:    refreshLogOutcome{},
			wantEvents: 0,
		},
		{
			name:       "unselected source is not success",
			outcome:    refreshLogOutcome{skipReason: refreshSkipNotSelected},
			wantReason: refreshSkipNotSelected,
			wantParts:  []string{"skipped", "provider group is no longer selected"},
			wantEvents: 1,
		},
		{
			name:       "inactive subscription is not success",
			outcome:    refreshLogOutcome{skipReason: refreshSkipInactive},
			wantReason: refreshSkipInactive,
			wantParts:  []string{"skipped", "subscription is no longer active"},
			wantEvents: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := &refreshCaptureLogger{}
			service := &Service{logger: logger}
			effect := &circuitEffect{outcome: test.outcome}
			// The log lives in the after-commit hook, so invoking it models the
			// runtime calling the hook only once the owner transaction committed.
			service.circuitAfterCommit(sourceSnapshot{sourceID: "source-1"}, effect, nil)(context.Background())
			if len(logger.events) != test.wantEvents {
				t.Fatalf("events = %+v, want %d", logger.events, test.wantEvents)
			}
			if test.wantEvents == 0 {
				return
			}
			event := logger.events[0]
			if event.ReasonCode != test.wantReason {
				t.Fatalf("reason = %q, want %q", event.ReasonCode, test.wantReason)
			}
			if test.outcome.skipReason != "" {
				if event.Outcome != "skipped" {
					t.Fatalf("skipped refresh reported outcome %q", event.Outcome)
				}
			} else if event.Outcome != "success" {
				t.Fatalf("confirmed refresh outcome = %q", event.Outcome)
			}
			for _, want := range test.wantParts {
				if !strings.Contains(event.Message, want) {
					t.Fatalf("message %q lacks %q", event.Message, want)
				}
			}
			if event.OwnerType != "rss_source_runtime" || event.OwnerID != "source-1" {
				t.Fatalf("event lost source identity: %+v", event)
			}
		})
	}
}
