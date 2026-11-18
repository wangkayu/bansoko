package application

import (
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"
)

func TestCompetitionConclusionEvent(t *testing.T) {
	claim := workitems.Claim{
		ID:    "work-1",
		Kind:  workitems.KindSubscriptionCompetition,
		Owner: workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"},
	}
	next := func(trigger string, retry int) *competitionNext {
		return &competitionNext{trigger: trigger, retry: retry}
	}
	for _, test := range []struct {
		name       string
		conclusion competitionConclusion
		wantReason string
		wantParts  []string
	}{
		{
			name:       "inactive",
			conclusion: competitionConclusion{inactive: true},
			wantReason: "competition_inactive",
			wantParts:  []string{"subscription is inactive", "resume enqueues a comparison"},
		},
		{
			name:       "subscription gone",
			conclusion: competitionConclusion{gone: true},
			wantReason: "competition_subscription_gone",
			wantParts:  []string{"subscription no longer exists"},
		},
		{
			name:       "waiting with unprocessed releases",
			conclusion: competitionConclusion{applied: 1, unprocessed: 3},
			wantReason: "competition_waiting_for_trigger",
			wantParts:  []string{"applied 1 decision(s)", "3 release(s) are left unprocessed", "wait for a new trigger"},
		},
		{
			name:       "waiting with nothing left",
			conclusion: competitionConclusion{applied: 2},
			wantReason: "competition_waiting_for_trigger",
			wantParts:  []string{"applied 2 decision(s)", "no next comparison is scheduled"},
		},
		{
			name:       "delayed retry",
			conclusion: competitionConclusion{applied: 1, unprocessed: 1, next: next(competition.TriggerRoundRetry, 1)},
			wantReason: "competition_retry_scheduled",
			wantParts:  []string{"applied 1 decision(s)", "1 release(s) are left unprocessed", "comparison retry 1/3 is scheduled"},
		},
		{
			name:       "gate wait",
			conclusion: competitionConclusion{next: next(competition.TriggerGateWait, 0)},
			wantReason: "competition_gate_wait",
			wantParts:  []string{"waits for the evaluation gate"},
		},
		{
			name:       "round moved",
			conclusion: competitionConclusion{unprocessed: 2, next: next(competition.TriggerRoundMoved, 0)},
			wantReason: "competition_round_moved",
			wantParts:  []string{"moved before commit", "2 release(s) are left unprocessed", "another comparison is scheduled immediately"},
		},
		{
			name:       "follow up",
			conclusion: competitionConclusion{applied: 1, next: next(competition.TriggerFollowUp, 0)},
			wantReason: "competition_follow_up_scheduled",
			wantParts:  []string{"another comparison is scheduled immediately"},
		},
		{
			name:       "other next round",
			conclusion: competitionConclusion{next: next("other_trigger", 0)},
			wantReason: "competition_next_round_scheduled",
			wantParts:  []string{"another comparison is scheduled"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := competitionConclusionEvent(claim, test.conclusion)
			if event.ReasonCode != test.wantReason {
				t.Fatalf("reason = %q, want %q", event.ReasonCode, test.wantReason)
			}
			if event.Component != "rss" || event.Operation != "competition" {
				t.Fatalf("event identity = %s/%s", event.Component, event.Operation)
			}
			if event.OwnerID != "anime-1" || event.WorkItemID != "work-1" || event.Kind != workitems.KindSubscriptionCompetition {
				t.Fatalf("event owner/work = %+v", event)
			}
			for _, want := range test.wantParts {
				if !strings.Contains(event.Message, want) {
					t.Fatalf("message %q lacks %q", event.Message, want)
				}
			}
			for _, forbidden := range []string{"anime-1", "release-", "a-1080", "error"} {
				if strings.Contains(event.Message, forbidden) {
					t.Fatalf("message carries dynamic fact %q: %q", forbidden, event.Message)
				}
			}
		})
	}
}

func TestCompetitionMovedEvent(t *testing.T) {
	claim := workitems.Claim{
		ID:    "work-1",
		Kind:  workitems.KindSubscriptionCompetition,
		Owner: workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-1"},
	}
	found := competitionMovedEvent(claim, 2, true)
	if found.ReasonCode != "competition_round_moved" || found.Outcome != "success" {
		t.Fatalf("found moved event = %+v", found)
	}
	for _, want := range []string{"lost a lock race", "2 release(s) are left unprocessed", "fresh comparison is scheduled immediately"} {
		if !strings.Contains(found.Message, want) {
			t.Fatalf("moved message %q lacks %q", found.Message, want)
		}
	}
	gone := competitionMovedEvent(claim, 2, false)
	if gone.ReasonCode != "competition_subscription_gone" || gone.Outcome != "skipped" {
		t.Fatalf("gone moved event = %+v", gone)
	}
	if strings.Contains(gone.Message, "scheduled immediately") {
		t.Fatalf("missing subscription promised a comparison: %q", gone.Message)
	}
}

// The postgres fixture runs the real owner transaction; the conclusion is
// emitted only from the committed round and reports the decided writes apart
// from the releases left unprocessed.
func TestCompetitionConclusionLogAfterCommitPostgres(t *testing.T) {
	f := newCompetitionFixture(t, "single_copy")
	f.service.SetDownloaderAdmission(&selectiveAdmission{fail: "a-1080"})
	logger := &refreshCaptureLogger{}
	f.service.logger = logger
	f.markRefreshed("source-rss-test", "source-beta")
	f.addRelease("a-1080", "source-rss-test", "[Alpha] Test Show - 01 [1080p]", 1)
	f.addRelease("b-720", "source-beta", "[Beta] Test Show - 01 [720p]", 1)
	f.evaluate("a-1080", "b-720")

	f.mustRunCompetition("competition_decided")

	var conclusion *observabilityport.RuntimeEvent
	for index := range logger.events {
		event := &logger.events[index]
		if event.Operation == "competition" {
			if conclusion != nil {
				t.Fatalf("duplicate competition conclusion events: %+v and %+v", *conclusion, *event)
			}
			conclusion = event
		}
	}
	if conclusion == nil {
		t.Fatal("the committed round emitted no conclusion")
	}
	if conclusion.ReasonCode != "competition_retry_scheduled" {
		t.Fatalf("reason = %q, want competition_retry_scheduled; event=%+v", conclusion.ReasonCode, *conclusion)
	}
	for _, want := range []string{"applied 1 decision(s)", "1 release(s) are left unprocessed", "comparison retry 1/3 is scheduled"} {
		if !strings.Contains(conclusion.Message, want) {
			t.Fatalf("message %q lacks %q", conclusion.Message, want)
		}
	}
	for _, forbidden := range []string{"a-1080", "b-720"} {
		if strings.Contains(conclusion.Message, forbidden) {
			t.Fatalf("message leaked a release id %q: %q", forbidden, conclusion.Message)
		}
	}
}
