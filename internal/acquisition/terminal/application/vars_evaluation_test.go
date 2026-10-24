package application

import (
	"bansoko/internal/namingvars"
	"context"
	"errors"
	"testing"
	"time"
)

type varsEvaluationFunc func(namingvars.Input, time.Duration) (namingvars.Vars, error)

func (f varsEvaluationFunc) Evaluate(i namingvars.Input, d time.Duration) (namingvars.Vars, error) {
	return f(i, d)
}

func TestVarsCompletionSamplesOnlyValidatedSuccess(t *testing.T) {
	for _, scenario := range []string{"success", "empty", "error", "cancel", "invalid", "clock_unavailable", "nanoseconds"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := false
			clocks := 0
			evaluator := varsEvaluationFunc(func(_ namingvars.Input, budget time.Duration) (namingvars.Vars, error) {
				if budget != namingvars.DefaultTimeout {
					t.Fatal("budget changed")
				}
				done = true
				switch scenario {
				case "error":
					return nil, errors.New("evaluation failed")
				case "cancel":
					cancel()
				case "invalid":
					return namingvars.Vars{"": {"value": "x"}}, nil
				case "empty":
					return namingvars.Vars{}, nil
				}
				return namingvars.Vars{"actual": {"value": "ok"}}, nil
			})
			clock := func() (time.Time, error) {
				clocks++
				if !done {
					t.Fatal("clock preceded evaluation")
				}
				if scenario == "clock_unavailable" {
					return time.Time{}, errors.New("clock unavailable")
				}
				n := 123456000
				if scenario == "nanoseconds" {
					n++
				}
				return time.Date(2026, 10, 8, 1, 2, 3, n, time.UTC), nil
			}
			result, err := evaluateVarsCompletion(ctx, evaluator, namingvars.Input{}, clock)
			success := scenario == "success" || scenario == "empty"
			if success {
				if err != nil || result.evaluationID == "" || result.evaluatedAt != "2026-10-08T01:02:03.123456Z" {
					t.Fatalf("%+v %v", result, err)
				}
			} else {
				if err == nil || result.evaluationID != "" || result.evaluatedAt != "" {
					t.Fatalf("false completion %+v %v", result, err)
				}
			}
			if (scenario == "error" || scenario == "cancel" || scenario == "invalid") && clocks != 0 {
				t.Fatal("clock sampled unsuccessful run")
			}
		})
	}
}
