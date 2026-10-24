package transport

import (
	"testing"
	"time"

	automationapplication "bansoko/internal/automation/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestScheduleMapping(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	value := Schedule(automationapplication.Schedule{
		Enabled:        true,
		IntervalUnit:   "hour",
		IntervalValue:  2,
		Kind:           "rss_refresh",
		LastDispatchAt: &now,
		LastRunOutcome: &automationapplication.RunOutcome{State: "queued", StartedAt: &now},
		NextRunAt:      &now,
		Version:        4,
	})
	if !value.Enabled || value.IntervalUnit != generated.AutomationIntervalUnit("hour") || value.Kind != generated.AutomationKind("rss_refresh") || value.Version != 4 {
		t.Fatalf("schedule=%+v", value)
	}
	if value.LastRunOutcome == nil || value.LastRunOutcome.State != generated.AutomationState("queued") || value.LastRunOutcome.StartedAt == nil {
		t.Fatalf("outcome=%+v", value.LastRunOutcome)
	}
}

func TestSchedulePatchMapping(t *testing.T) {
	value := SchedulePatch(generated.AutomationSchedulePatch{
		Enabled:         true,
		ExpectedVersion: 7,
		IntervalUnit:    generated.AutomationIntervalUnit("day"),
		IntervalValue:   3,
	})
	if !value.Enabled || value.ExpectedVersion != 7 || value.IntervalUnit != "day" || value.IntervalValue != 3 {
		t.Fatalf("patch=%+v", value)
	}
}

func TestRunMappings(t *testing.T) {
	accepted := RunAccepted(automationapplication.RunAccepted{Accepted: true, RunID: "run-1", State: "queued"})
	if !accepted.Accepted || accepted.RunId != "run-1" || accepted.State != generated.AutomationState("queued") {
		t.Fatalf("accepted=%+v", accepted)
	}

	run := DispatchRun(automationapplication.DispatchRun{ID: "run-2", Kind: "metadata_match", State: "completed", Trigger: "manual", RetentionUntil: time.Unix(20, 0).UTC()})
	if run.Id != "run-2" || run.Kind != generated.AutomationKind("metadata_match") || run.State != generated.AutomationState("completed") || run.Trigger != generated.AutomationTrigger("manual") {
		t.Fatalf("run=%+v", run)
	}
}
