// Package varsevidence owns the trusted successful Vars evaluation completion.
// It is shared by admission and owner-arranged new-protocol acceptance tests.
package varsevidence

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/namingvars"
	"github.com/google/uuid"
)

type Evaluator interface {
	Evaluate(namingvars.Input, time.Duration) (namingvars.Vars, error)
}
type Clock func() (time.Time, error)
type Completion struct {
	id, at string
	output namingvars.Vars
}

func ServerClock() (time.Time, error) { return time.Now().UTC().Truncate(time.Microsecond), nil }
func (c Completion) Identity() string { return c.id }
func (c Completion) Time() string     { return c.at }
func (c Completion) Output() namingvars.Vars {
	var out namingvars.Vars
	b, _ := json.Marshal(c.output)
	_ = json.Unmarshal(b, &out)
	return out
}
func value(v any) (protocol.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return protocol.Value{}, err
	}
	return protocol.ParseStrict(b)
}
func Evaluate(ctx context.Context, program Evaluator, input namingvars.Input, clock Clock) (Completion, error) {
	if err := ctx.Err(); err != nil {
		return Completion{}, err
	}
	output, err := program.Evaluate(input, namingvars.DefaultTimeout)
	if err != nil {
		return Completion{}, err
	}
	v, err := value(output)
	if err != nil {
		return Completion{}, err
	}
	if err := protocol.Validate("VarsMapV1", v); err != nil {
		return Completion{}, err
	}
	if err := output.Validate(); err != nil {
		return Completion{}, err
	}
	if err := ctx.Err(); err != nil {
		return Completion{}, err
	}
	at, err := clock()
	if err != nil {
		return Completion{}, err
	}
	if at.IsZero() || at.Location() != time.UTC || at.Nanosecond()%1000 != 0 {
		return Completion{}, errors.New("vars evidence clock must supply UTC microseconds")
	}
	// Retain an independent validated output; an evaluator cannot mutate it later.
	var owned namingvars.Vars
	if err := json.Unmarshal(protocol.Canonical(v), &owned); err != nil {
		return Completion{}, err
	}
	return Completion{id: uuid.NewString(), at: protocol.FormatTime(at), output: owned}, nil
}
func (c Completion) Record(vars protocol.Value, root, admission string) (string, error) {
	if vars.Object["mode"].String == "disabled" {
		return "", nil
	}
	if c.id == "" || c.at == "" {
		return "", errors.New("VARS_EVALUATION_EVIDENCE_INCONSISTENT")
	}
	p := vars.Object["program"].Object
	output, err := value(c.output)
	if err != nil {
		return "", err
	}
	record, err := value(map[string]any{"schema_version": 1, "acquisition_id": root, "admission_digest": admission, "evaluation_id": c.id, "program_digest": p["digest"].String, "program_version": p["version"].String, "program_runtime_version": p["runtime_version"].String, "runtime_version": p["runtime_version"].String, "input_digest": protocol.Digest("acquisition-vars-input:v1", protocol.Canonical(vars.Object["known_inputs"])), "output": c.output, "output_digest": protocol.Digest("acquisition-vars-output:v1", protocol.Canonical(output)), "evaluated_at": c.at})
	if err != nil {
		return "", err
	}
	if err := protocol.ValidateVarsEvaluation(record, vars, root, admission); err != nil {
		return "", err
	}
	canonical := protocol.Canonical(record)
	if len(canonical) > 65536 {
		return "", errors.New("VARS_EVALUATION_EVIDENCE_INCONSISTENT")
	}
	return string(canonical), nil
}
