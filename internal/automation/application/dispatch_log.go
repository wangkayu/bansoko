package application

import (
	"context"
	"fmt"

	observabilityport "bansoko/internal/observability/port"
)

func automationRunLabel(kind, trigger string) string {
	name := "automation"
	switch kind {
	case "group_discovery":
		name = "subtitle group discovery"
	case "rss_refresh":
		name = "RSS refresh scheduling"
	case "metadata_match":
		name = "metadata matching"
	case "catalog_refresh":
		name = "catalog refresh"
	}
	if trigger == "manual" {
		return "manual " + name
	}
	return "scheduled " + name
}

func (d *Dispatcher) logDispatchResult(ctx context.Context, runID, kind, trigger string, result dispatchResult) {
	outcome, code := result.routineOutcome()
	event := observabilityport.RuntimeEvent{
		Operation: kind, OwnerType: "automation_dispatch_run", OwnerID: runID,
		Outcome: string(outcome), ReasonCode: string(code),
	}
	if result.state == "partial" {
		event.Outcome = "partial"
	}
	label := automationRunLabel(kind, trigger)
	if result.state == "unconfirmed" {
		event.Outcome = "unconfirmed"
		event.Message = fmt.Sprintf("Completion of %s was not confirmed.", label)
		d.logWarn(ctx, event)
		return
	}
	c := result.counts
	var summary string
	switch kind {
	case "rss_refresh":
		summary = fmt.Sprintf("%d sources considered; %d refreshes queued, %d already queued or running, %d skipped", c.targetCount, c.enqueued, c.merged, c.skipped)
	case "group_discovery":
		summary = fmt.Sprintf("%d subscriptions considered; discovery completed for %d, %d RSS refreshes queued", c.targetCount, c.completed, c.enqueued)
	case "metadata_match":
		summary = fmt.Sprintf("%d subscriptions considered; metadata saved for %d, %d release evaluations queued, %d already queued or running", c.targetCount, c.completed, c.enqueued, c.merged)
	case "catalog_refresh":
		summary = fmt.Sprintf("%d catalog targets considered; %d seasons checked, %d refreshes already running, %d skipped", c.targetCount, c.completed, c.merged, c.skipped)
	default:
		summary = fmt.Sprintf("%d targets considered", c.targetCount)
	}
	if result.state != "completed" {
		if result.interrupted {
			event.Message = fmt.Sprintf("Automation run stopped before finishing %s: %s.", label, summary)
		} else {
			event.Message = fmt.Sprintf("Automation run reported %d errors during %s: %s.", c.errors, label, summary)
		}
		d.logWarn(ctx, event)
		return
	}
	event.Message = fmt.Sprintf("Automation run finished %s: %s.", label, summary)
	d.logInfo(ctx, event)
}
