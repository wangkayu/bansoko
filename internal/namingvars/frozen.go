package namingvars

import (
	"encoding/json"
	"fmt"
	"time"
)

// FrozenEvaluation is admission evidence. Consumers use Output, never reevaluate
// the source on retry. Input includes only already captured facts.
type FrozenEvaluation struct {
	Program    ProgramRef `json:"program"`
	Input      Input      `json:"input"`
	Output     Vars       `json:"output,omitempty"`
	Status     string     `json:"status"`
	Diagnostic string     `json:"diagnostic,omitempty"`
}

// Freeze evaluates input once for admission evidence. A deterministic failure
// (invalid program, missing fallback, oversized output) is frozen as
// Status "error". A time-limit interrupt is not a fact about the program, so
// Freeze returns ErrEvaluationInterrupted and no record; the caller retries
// its stage rather than freezing every file of the release as unrenderable.
func (p Program) Freeze(input Input) (FrozenEvaluation, error) {
	return p.freeze(input, 0)
}

// FreezeEvaluation is the display-only variant for previews: an interrupt is
// shown as an error record. Admission paths use Freeze.
func (p Program) FreezeEvaluation(input Input) FrozenEvaluation {
	frozen, err := p.freeze(input, 0)
	if err != nil {
		frozen.Status, frozen.Output, frozen.Diagnostic = "error", nil, err.Error()
	}
	return frozen
}

func (p Program) freeze(input Input, timeout time.Duration) (FrozenEvaluation, error) {
	frozen, err := p.evaluate(input, timeout)
	if IsInterrupted(err) {
		return frozen, err
	}
	if err != nil {
		frozen.Diagnostic = err.Error()
	}
	return frozen, nil
}

// Dry evaluates input exactly as Freeze does (the same input round trip, the
// same strict evaluation and limits) and also returns the evaluation error,
// so an editor can show where a program failed. The result is display
// evidence only: it is never stored.
func (p Program) Dry(input Input) (FrozenEvaluation, error) {
	return p.evaluate(input, 0)
}

// evaluate is the one evaluation behind Freeze and Dry. A non-nil error is
// the evaluation error; frozen then carries Status "error" and no output.
func (p Program) evaluate(input Input, timeout time.Duration) (FrozenEvaluation, error) {
	frozen := FrozenEvaluation{Program: p.Ref(), Status: "error"}
	raw, err := json.Marshal(input)
	if err != nil {
		return frozen, fmt.Errorf("encode Vars facts: %v", err)
	}
	if err = json.Unmarshal(raw, &frozen.Input); err != nil {
		return frozen, err
	}
	output, err := p.Evaluate(frozen.Input, timeout)
	if err != nil {
		return frozen, err
	}
	frozen.Output = output
	frozen.Status = "ok"
	return frozen, nil
}
