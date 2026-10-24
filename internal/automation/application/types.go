package application

import "time"

type AutomationKindValue string
type AutomationIntervalUnitValue string
type AutomationStateValue string
type AutomationTriggerValue string

type RunOutcome struct {
	FinishedAt *time.Time
	StartedAt  *time.Time
	State      AutomationStateValue
}

type Schedule struct {
	Enabled        bool
	IntervalUnit   AutomationIntervalUnitValue
	IntervalValue  int
	Kind           AutomationKindValue
	LastDispatchAt *time.Time
	LastRunOutcome *RunOutcome
	NextRunAt      *time.Time
	Version        int64
}

type SchedulePatch struct {
	Enabled         bool
	ExpectedVersion int64
	IntervalUnit    AutomationIntervalUnitValue
	IntervalValue   int
}

type RunAccepted struct {
	Accepted bool
	RunID    string
	State    AutomationStateValue
}

type DispatchRun struct {
	DispatchErrorCount int
	EnqueuedCount      int
	FinishedAt         *time.Time
	ID                 string
	Kind               AutomationKindValue
	MergedCount        int
	RetentionUntil     time.Time
	SkippedCount       int
	StartedAt          *time.Time
	State              AutomationStateValue
	TargetCount        int
	Trigger            AutomationTriggerValue
}
