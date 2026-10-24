package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/acquisition/terminal/varsevidence"
	"bansoko/internal/namingvars"
	"context"
)

// Admission retains one trusted completion across whole-transaction retries.
// Its time and identity never enter Config.vars or the business input digest.
type varsEvaluationCompletion struct {
	evaluationID string
	evaluatedAt  string
	output       namingvars.Vars
	completion   varsevidence.Completion
}
type varsEvaluator = varsevidence.Evaluator
type varsEvidenceClock = varsevidence.Clock

var serverVarsEvidenceClock = varsevidence.ServerClock

func evaluateVarsCompletion(ctx context.Context, program varsEvaluator, input namingvars.Input, clock varsEvidenceClock) (varsEvaluationCompletion, error) {
	c, err := varsevidence.Evaluate(ctx, program, input, clock)
	if err != nil {
		return varsEvaluationCompletion{}, err
	}
	return varsEvaluationCompletion{evaluationID: c.Identity(), evaluatedAt: c.Time(), output: c.Output(), completion: c}, nil
}
func (c varsEvaluationCompletion) record(vars protocol.Value, root, admission string) (string, error) {
	return c.completion.Record(vars, root, admission)
}
