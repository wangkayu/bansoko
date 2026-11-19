package application

import (
	"context"
	"errors"

	observabilityport "bansoko/internal/observability/port"
)

type dispatchResult struct {
	state       AutomationStateValue
	counts      dispatchCounts
	interrupted bool
}

func dispatchOutcome(ctx context.Context, counts dispatchCounts, err error) dispatchResult {
	if errors.Is(err, context.Canceled) || (ctx.Err() != nil && (err != nil || counts.errors > 0)) {
		return unconfirmedDispatch()
	}
	return settleDispatch(counts, err)
}

func settleDispatch(counts dispatchCounts, err error) dispatchResult {
	if err != nil && counts.errors == 0 {
		counts.errors = 1
	}
	state := AutomationStateValue("completed")
	if counts.errors > 0 {
		state = "failed"
		if counts.completed > 0 || counts.enqueued > 0 || counts.merged > 0 {
			state = "partial"
		}
	}
	return dispatchResult{state: state, counts: counts, interrupted: err != nil}
}

func unconfirmedDispatch() dispatchResult {
	return dispatchResult{state: "unconfirmed"}
}

func (r dispatchResult) routineOutcome() (observabilityport.RoutineOutcome, observabilityport.RoutineResultCode) {
	switch r.state {
	case "unconfirmed":
		return observabilityport.RoutineOutcomeWarning, observabilityport.RoutineResultDispatchUnconfirmed
	case "completed":
		return observabilityport.RoutineOutcomeSuccess, observabilityport.RoutineResultDispatchCompleted
	case "partial":
		return observabilityport.RoutineOutcomeWarning, observabilityport.RoutineResultDispatchPartial
	default:
		return observabilityport.RoutineOutcomeFailure, observabilityport.RoutineResultDispatchFailed
	}
}
