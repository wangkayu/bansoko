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

func (d *Dispatcher) logDispatchResult(ctx context.Context, runID, kind, trigger string, c dispatchCounts, interrupted bool) {
	event := observabilityport.RuntimeEvent{
		Operation: kind, OwnerType: "automation_dispatch_run", OwnerID: runID,
		Outcome: "success", ReasonCode: "dispatch_completed",
	}
	var summary string
	switch kind {
	case "rss_refresh":
		summary = fmt.Sprintf("%d sources considered; %d refreshes queued, %d already queued or running, %d skipped", c.targetCount, c.enqueued, c.merged, c.skipped)
	case "group_discovery":
		summary = fmt.Sprintf("%d subscriptions considered; discovery completed for %d, %d RSS refreshes queued", c.targetCount, c.committed, c.enqueued)
	case "metadata_match":
		summary = fmt.Sprintf("%d subscriptions considered; metadata saved for %d, %d release evaluations queued, %d already queued or running", c.targetCount, c.committed, c.enqueued, c.merged)
	case "catalog_refresh":
		summary = fmt.Sprintf("%d catalog targets considered; %d seasons checked, %d refreshes already running, %d skipped", c.targetCount, c.enqueued, c.merged, c.skipped)
	default:
		summary = fmt.Sprintf("%d targets considered", c.targetCount)
	}
	label := automationRunLabel(kind, trigger)
	if interrupted {
		event.Message = fmt.Sprintf("Automation run stopped before finishing %s: %s.", label, summary)
		event.Outcome, event.ReasonCode = "failure", "dispatch_failed"
		d.logWarn(ctx, event)
		return
	}
	if c.errors > 0 {
		event.Message = fmt.Sprintf("Automation run reported %d errors during %s: %s.", c.errors, label, summary)
		event.Outcome, event.ReasonCode = "failure", "dispatch_failed"
		if c.committed+c.enqueued+c.merged > 0 {
			event.Outcome, event.ReasonCode = "partial", "dispatch_partial"
		}
		d.logWarn(ctx, event)
		return
	}
	event.Message = fmt.Sprintf("Automation run finished %s: %s.", label, summary)
	d.logInfo(ctx, event)
}
