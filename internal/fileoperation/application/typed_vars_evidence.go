package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"encoding/json"
	"errors"
)

func capturedVarsResult(b *artifactPreparedBatch) (any, error) {
	bad := func() (any, error) { return nil, errors.New("VARS_EVALUATION_EVIDENCE_INCONSISTENT") }
	var capture struct {
		Artifact struct {
			Root string `json:"acquisition_id"`
		} `json:"artifact"`
		Protocol *struct {
			Version   *int    `json:"version"`
			Digest    *string `json:"digest"`
			Admission string  `json:"admission_digest"`
		} `json:"vars_protocol"`
		Evidence *struct {
			Canonical string `json:"canonical_utf8"`
			Digest    string `json:"evidence_digest"`
		} `json:"vars_evaluation"`
	}
	if err := json.Unmarshal(b.capture.Facts, &capture); err != nil {
		return nil, err
	}
	if capture.Protocol == nil || capture.Protocol.Version == nil || (*capture.Protocol.Version != 0 && *capture.Protocol.Version != 1) {
		return bad()
	}
	input, err := protocol.ParseStrict(b.input)
	if err != nil {
		return nil, err
	}
	vars := input.Object["config"].Object["vars"]
	if vars.Object["mode"].String == "disabled" {
		if capture.Evidence != nil || capture.Protocol.Digest != nil {
			return bad()
		}
		return map[string]any{"state": "absent", "value": nil, "reason_code": nil}, nil
	}
	if *capture.Protocol.Version == 0 {
		if capture.Evidence != nil || capture.Protocol.Digest != nil {
			return bad()
		}
		return frozenUnavailable("VARS_EVALUATION_RECORD_UNAVAILABLE"), nil
	}
	if *capture.Protocol.Version != 1 || capture.Evidence == nil || capture.Protocol.Digest == nil {
		return bad()
	}
	record, err := protocol.ParseStrict([]byte(capture.Evidence.Canonical))
	if err != nil {
		return bad()
	}
	if string(protocol.Canonical(record)) != capture.Evidence.Canonical || protocol.Digest("acquisition-vars-evaluation:v1", []byte(capture.Evidence.Canonical)) != capture.Evidence.Digest || capture.Evidence.Digest != *capture.Protocol.Digest {
		return bad()
	}
	if err := protocol.ValidateVarsEvaluation(record, vars, capture.Artifact.Root, capture.Protocol.Admission); err != nil {
		return bad()
	}
	r := record.Object
	value := map[string]json.RawMessage{}
	for _, key := range []string{"program_digest", "input_digest", "output", "runtime_version", "evaluated_at"} {
		value[key] = protocol.Canonical(r[key])
	}
	return frozenKnown(value), nil
}
