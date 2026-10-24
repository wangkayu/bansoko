package transport

import (
	automationapplication "bansoko/internal/automation/application"
	"bansoko/internal/transport/httpapi/generated"
)

func Schedule(value automationapplication.Schedule) generated.AutomationSchedule {
	result := generated.AutomationSchedule{
		Enabled:        value.Enabled,
		IntervalUnit:   generated.AutomationIntervalUnit(value.IntervalUnit),
		IntervalValue:  value.IntervalValue,
		Kind:           generated.AutomationKind(value.Kind),
		LastDispatchAt: value.LastDispatchAt,
		NextRunAt:      value.NextRunAt,
		Version:        value.Version,
	}
	if value.LastRunOutcome != nil {
		result.LastRunOutcome = &generated.AutomationRunOutcome{
			FinishedAt: value.LastRunOutcome.FinishedAt,
			StartedAt:  value.LastRunOutcome.StartedAt,
			State:      generated.AutomationState(value.LastRunOutcome.State),
		}
	}
	return result
}

func Schedules(values []automationapplication.Schedule) []generated.AutomationSchedule {
	result := make([]generated.AutomationSchedule, 0, len(values))
	for _, value := range values {
		result = append(result, Schedule(value))
	}
	return result
}

func SchedulePatch(value generated.AutomationSchedulePatch) automationapplication.SchedulePatch {
	return automationapplication.SchedulePatch{
		Enabled:         value.Enabled,
		ExpectedVersion: int64(value.ExpectedVersion),
		IntervalUnit:    automationapplication.AutomationIntervalUnitValue(value.IntervalUnit),
		IntervalValue:   value.IntervalValue,
	}
}

func RunAccepted(value automationapplication.RunAccepted) generated.AutomationRunAccepted {
	return generated.AutomationRunAccepted{
		Accepted: generated.AutomationRunAcceptedAccepted(value.Accepted),
		RunId:    generated.ID(value.RunID),
		State:    generated.AutomationState(value.State),
	}
}

func DispatchRun(value automationapplication.DispatchRun) generated.AutomationDispatchRun {
	return generated.AutomationDispatchRun{
		DispatchErrorCount: value.DispatchErrorCount,
		EnqueuedCount:      value.EnqueuedCount,
		FinishedAt:         value.FinishedAt,
		Id:                 generated.ID(value.ID),
		Kind:               generated.AutomationKind(value.Kind),
		MergedCount:        value.MergedCount,
		RetentionUntil:     value.RetentionUntil,
		SkippedCount:       value.SkippedCount,
		StartedAt:          value.StartedAt,
		State:              generated.AutomationState(value.State),
		TargetCount:        value.TargetCount,
		Trigger:            generated.AutomationTrigger(value.Trigger),
	}
}
