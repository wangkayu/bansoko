package domain

// VarsEvaluationRecordV1 is independent admission evidence. Its timestamp and
// evaluation identity never enter Config.vars or an acquisition business input.
func init() {
	register("VarsEvaluationRecordV1", Obj(
		F("schema_version", Int(1, 1)), F("acquisition_id", IDType), F("admission_digest", DigestOf), F("evaluation_id", IDType),
		F("program_digest", DigestOf), F("program_version", Text(1, 128)), F("program_runtime_version", Text(1, 128)), F("runtime_version", Text(1, 128)),
		F("input_digest", DigestOf), F("output", Ref("VarsMapV1")), F("output_digest", DigestOf), F("evaluated_at", Time),
	))
}

// ValidateVarsEvaluation verifies the record against the original closed frozen
// input. Entity protocol and record-digest checks belong to the persistence owner.
func ValidateVarsEvaluation(record, vars Value, root, admission string) error {
	if err := Validate("VarsEvaluationRecordV1", record); err != nil {
		return err
	}
	if err := Validate("VarsChoiceV1", vars); err != nil {
		return err
	}
	r := record.Object
	v := vars.Object
	if v["mode"].String != "frozen" || v["status"].String != "ok" || v["reason_code"].Kind != KindNull {
		return reader{}.fail(CodeConstraintViolated)
	}
	p := v["program"].Object
	if r["acquisition_id"].String != root || r["admission_digest"].String != admission || r["program_digest"].String != p["digest"].String || r["program_version"].String != p["version"].String || r["program_runtime_version"].String != p["runtime_version"].String || r["runtime_version"].String != p["runtime_version"].String || !varsRuntimeVersion(p["runtime_version"].String) || r["input_digest"].String != Digest("acquisition-vars-input:v1", Canonical(v["known_inputs"])) || string(Canonical(r["output"])) != string(Canonical(v["output"])) || r["output_digest"].String != Digest("acquisition-vars-output:v1", Canonical(r["output"])) {
		return reader{}.fail(CodeConstraintViolated)
	}
	return nil
}
