package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/workitems"
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

func TestRefreshLogSubjectDropsUnsafeLabelsIndividually(t *testing.T) {
	snapshot := sourceSnapshot{
		title: "Example Show", groupName: "https://example.test/private-feed?token=unlogged",
		locator: "https://example.test/other-private-feed",
	}
	if got := refreshLogSubject(snapshot); got != `RSS refresh for "Example Show"` {
		t.Fatalf("unexpected subject: %q", got)
	}
}

func TestRefreshReadFailureMessageUsesOnlyConfirmedFacts(t *testing.T) {
	const code = "feed_fetch_failed"
	cause := errors.New("private upstream response")
	if got := refreshReadFailureMessage(code, cause); got != "could not read the feed" {
		t.Fatalf("unexpected fallback: %q", got)
	}
	status := outbound.WithFailure(cause, outbound.Failure{Class: outbound.ClassHTTPStatus, Status: 503})
	if got := refreshReadFailureMessage(code, status); got != "the feed request returned HTTP 503" {
		t.Fatalf("unexpected status detail: %q", got)
	}
	if got := refreshReadFailureMessage(acquisitiondomain.FeedFailureNoUsableItems, cause); got != "the feed contained no usable release entries" {
		t.Fatalf("unexpected feed detail: %q", got)
	}
}

func TestRefreshSkippedLogCarriesClaimAndPlainLanguage(t *testing.T) {
	logger := &refreshCaptureLogger{}
	service := &Service{logger: logger}
	claim := workitems.Claim{
		ID: "refresh-skip-1", Kind: workitems.KindRSSRefresh, Attempt: 3,
		Owner: workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: "source-skip-1"},
	}
	service.logRefreshSkipped(context.Background(), claim, sourceSnapshot{title: "Example Show", groupName: "Example Group"},
		refreshSkipNotSelected, "no feed was requested because the provider group is not selected")
	if len(logger.events) != 1 {
		t.Fatalf("events = %+v, want one skipped event", logger.events)
	}
	event := logger.events[0]
	if event.Outcome != "skipped" || event.ReasonCode != refreshSkipNotSelected {
		t.Fatalf("unexpected skip classification: %+v", event)
	}
	if event.WorkItemID != claim.ID || event.OwnerID != claim.Owner.ID || event.Attempt != claim.Attempt {
		t.Fatalf("skip lost claim context: %+v", event)
	}
	if !strings.Contains(event.Message, "Example Show") || !strings.Contains(event.Message, "no feed was requested") {
		t.Fatalf("skip message is not self-explanatory: %q", event.Message)
	}
}

func TestRefreshFailureLogReportsStageWithoutRawError(t *testing.T) {
	logger := &refreshCaptureLogger{}
	service := &Service{logger: logger}
	claim := workitems.Claim{ID: "refresh-fail-1", Kind: workitems.KindRSSRefresh, Attempt: 1}
	service.logRefreshFailure(context.Background(), claim, sourceSnapshot{}, "read_feed", "http_status", "the feed request returned HTTP 503")
	if len(logger.events) != 1 {
		t.Fatalf("events = %+v, want one failure event", logger.events)
	}
	event := logger.events[0]
	if event.Outcome != "failure" || event.Stage != "read_feed" || event.WorkItemID != claim.ID {
		t.Fatalf("failure lacks stage or claim: %+v", event)
	}
	if event.Message != "RSS refresh: the feed request returned HTTP 503" {
		t.Fatalf("unexpected safe failure message: %q", event.Message)
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
			outcome:    refreshLogOutcome{committed: true, notModified: true},
			wantReason: "rss_refresh_not_modified",
			wantParts:  []string{"the feed did not change", "HTTP 304"},
			wantEvents: 1,
		},
		{
			name:       "resume baseline",
			outcome:    refreshLogOutcome{committed: true, resumeBaseline: true, newCount: 2, windowTotal: 7},
			wantReason: "rss_refresh_resume_baseline",
			wantParts:  []string{"resume baseline", "2 new entries out of 7 checked", "no releases were evaluated"},
			wantEvents: 1,
		},
		{
			name:       "content but no new releases",
			outcome:    refreshLogOutcome{committed: true, windowTotal: 4},
			wantReason: "rss_refresh_no_new_releases",
			wantParts:  []string{"checked 4 entries", "no new releases"},
			wantEvents: 1,
		},
		{
			name:       "new releases",
			outcome:    refreshLogOutcome{committed: true, newCount: 3, windowTotal: 9},
			wantReason: "rss_refresh_new_releases",
			wantParts:  []string{"checked 9 entries", "saved 3 new releases"},
			wantEvents: 1,
		},
		{
			name:       "committed empty feed",
			outcome:    refreshLogOutcome{committed: true},
			wantReason: "rss_refresh_empty_feed",
			wantParts:  []string{"the feed contained no release entries"},
			wantEvents: 1,
		},
		{
			name:       "no committed owner result",
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
			claim := workitems.Claim{
				ID: "refresh-1", Kind: workitems.KindRSSRefresh, Attempt: 2,
				Owner: workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: "source-1"},
			}
			// The log lives in the after-commit hook, so invoking it models the
			// runtime calling the hook only once the owner transaction committed.
			snapshot := sourceSnapshot{sourceID: "source-1", title: "Example Show", groupName: "Example Group"}
			service.circuitAfterCommit(claim, snapshot, effect, nil)(context.Background())
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
			if !strings.HasPrefix(event.Message, `RSS refresh for "Example Show" ("Example Group"): `) {
				t.Fatalf("message lost source display labels: %q", event.Message)
			}
			for _, want := range test.wantParts {
				if !strings.Contains(event.Message, want) {
					t.Fatalf("message %q lacks %q", event.Message, want)
				}
			}
			if event.OwnerType != claim.Owner.Type || event.OwnerID != claim.Owner.ID ||
				event.WorkItemID != claim.ID || event.Kind != claim.Kind || event.Attempt != claim.Attempt {
				t.Fatalf("event lost refresh claim identity: %+v", event)
			}
		})
	}
}
