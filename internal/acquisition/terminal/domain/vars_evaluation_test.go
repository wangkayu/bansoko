package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"bansoko/internal/namingvars"
)

func TestVarsEvaluationRecordBindsFrozenInput(t *testing.T) {
	parse := func(v any) Value {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		p, e := ParseStrict(b)
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	known := func(v any) any { return map[string]any{"state": "known", "value": v, "reason_code": nil} }
	hash := "sha256:" + strings.Repeat("a", 64)
	inputs := map[string]any{"release_title": known("Title"), "files": []any{}, "facts": namingvars.BuildInput(namingvars.ReleaseEvidence{ReleaseTitle: "Title"}).Facts}
	output := map[string]any{"actual": map[string]string{"title": "Title"}}
	program := map[string]any{"schema_version": 1, "source": "vars.actual={title:input.release_title};", "digest": hash, "version": "naming-vars-v1@" + hash, "runtime_version": "naming-vars-v1", "timeout_ms": 50, "max_input_bytes": 262144, "max_output_bytes": 8192, "max_collection_len": 256, "max_string_bytes": 8192}
	vars := parse(map[string]any{"mode": "frozen", "program": program, "known_inputs": inputs, "output": output, "status": "ok", "reason_code": nil})
	record := map[string]any{"schema_version": 1, "acquisition_id": "root", "admission_digest": hash, "evaluation_id": "evaluation", "program_digest": hash, "program_version": "naming-vars-v1@" + hash, "program_runtime_version": "naming-vars-v1", "runtime_version": "naming-vars-v1", "input_digest": Digest("acquisition-vars-input:v1", Canonical(parse(inputs))), "output": output, "output_digest": Digest("acquisition-vars-output:v1", Canonical(parse(output))), "evaluated_at": "2026-10-08T01:02:03.123456Z"}
	if e := ValidateVarsEvaluation(parse(record), vars, "root", hash); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"acquisition_id", "admission_digest", "program_digest", "program_version", "program_runtime_version", "runtime_version", "input_digest", "output_digest", "evaluated_at"} {
		t.Run(key, func(t *testing.T) {
			old := record[key]
			record[key] = "wrong"
			defer func() { record[key] = old }()
			if e := ValidateVarsEvaluation(parse(record), vars, "root", hash); e == nil {
				t.Fatal("changed binding accepted")
			}
		})
	}
	record["output"] = map[string]any{"actual": map[string]string{"title": "Changed"}}
	if e := ValidateVarsEvaluation(parse(record), vars, "root", hash); e == nil {
		t.Fatal("changed output accepted")
	}
	record["output"] = output

	// The record, the program and its version name the same contract.
	record["runtime_version"] = "naming-vars-v2"
	if e := ValidateVarsEvaluation(parse(record), vars, "root", hash); e == nil {
		t.Fatal("record runtime differing from the program accepted")
	}
	record["runtime_version"] = "naming-vars-v1"
	withMediainfo := map[string]any{"release_title": known("Title"), "files": []any{}, "mediainfo": known(map[string]any{"kind": "legacy_empty"})}
	if e := Validate("VarsChoiceV1", parse(map[string]any{"mode": "frozen", "program": program, "known_inputs": withMediainfo, "output": output, "status": "ok", "reason_code": nil})); e == nil {
		t.Fatal("program with a mediainfo input accepted")
	}
}
