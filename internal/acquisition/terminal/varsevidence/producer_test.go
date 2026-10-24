package varsevidence

import (
	"bansoko/internal/namingvars"
	"context"
	"testing"
	"time"
)

type mutableEvaluator struct{ output namingvars.Vars }

func (e mutableEvaluator) Evaluate(_ namingvars.Input, _ time.Duration) (namingvars.Vars, error) {
	return e.output, nil
}
func TestCompletionOwnsValidatedOutput(t *testing.T) {
	original := namingvars.Vars{"actual": {"title": "original"}}
	completion, err := Evaluate(context.Background(), mutableEvaluator{original}, namingvars.Input{}, ServerClock)
	if err != nil {
		t.Fatal(err)
	}
	original["actual"]["title"] = "mutated"
	copy := completion.Output()
	if copy["actual"]["title"] != "original" {
		t.Fatal("evaluator alias changed completion")
	}
	copy["actual"]["title"] = "consumer"
	if completion.Output()["actual"]["title"] != "original" {
		t.Fatal("consumer alias changed completion")
	}
}
