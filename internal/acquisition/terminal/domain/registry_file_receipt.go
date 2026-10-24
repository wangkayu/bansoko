package domain

// File publication receipts describe facts from a trusted invocation. Shape
// validation never grants dispatch or proves the facts came from an executor;
// the owner-held recorder separately binds them to persisted permissions.
var fileStepOrder = []string{"stage_temp", "reserve_version", "publish_target", "source_cleanup", "temp_cleanup"}

func init() {
	register("ReceiptIdentityV1", Obj(
		F("schema_version", Int(1, 1)), F("permit_id", IDType), F("root_id", IDType),
		F("original_work_id", IDType), F("fencing_token", Int(1, maxI64)),
		F("executor_session", Text(1, 128)), F("spec_digest", DigestOf),
	))
	register("ReceiptEmitterV1", Union("mode", map[string]*objectSchema{
		"dispatch_session": Obj(F("session", Text(1, 128))),
		"lookup_ticket":    Obj(F("ticket_id", IDType), F("ordinal", Int(1, 100)), F("session", Text(1, 128))),
	}))
	register("AbsenceObservationV1", Obj(F("location", Ref("LocationV1")), F("parent", Ref("PathIdentityV1")), F("observed_at", Time), F("method", Enum("lstat_enoent"))))
	register("FileStepProofV1", Union("kind", map[string]*objectSchema{
		"created_temp":   Obj(F("object", Ref("PathObjectV1")), F("parent", Ref("PathIdentityV1")), F("verified_source_digest", DigestOf), F("flushed", Bool)),
		"reserved_path":  Obj(F("object", Ref("PathObjectV1")), F("parent", Ref("PathIdentityV1")), F("base_path", Path), F("candidate_ordinal", Int(1, maxI64)), F("reservation_method", Enum("exclusive_create"))),
		"published":      Obj(F("source_evidence_digest", DigestOf), F("target", Ref("PathObjectV1")), F("parent", Ref("PathIdentityV1")), F("atomic_method", Enum("no_replace", "replace", "verified_noop")), F("displaced", FVOf(Ref("PathObjectV1"))), F("dependency_digest", DigestOf)),
		"removed_object": Obj(F("before", Ref("PathObjectV1")), F("parent", Ref("PathIdentityV1")), F("after", Ref("AbsenceObservationV1")), F("mutation_receipt_digest", DigestOf)),
		"no_effect":      Obj(F("step", Enum(fileStepOrder...)), F("condition_digest", DigestOf), F("reason_code", ReasonOf), F("closure_digest", DigestOf)),
	}))
	register("StepObservationV1", Obj(
		F("state", Enum("not_required", "not_started", "started", "confirmed", "no_effect", "unknown")),
		F("proof", Nullable(Ref("FileStepProofV1"))), F("observed_at", Nullable(Time)), F("reason_code", Nullable(ReasonOf)),
	).Check(checkFileStepObservation))
	fields := make([]field, 0, len(fileStepOrder))
	for _, step := range fileStepOrder {
		fields = append(fields, F(step, Ref("StepObservationV1")))
	}
	register("FileCheckpointV1", Obj(fields...).Check(checkFileCheckpoint))
	register("FileReceiptV1", Obj(F("checkpoint", Ref("FileCheckpointV1")), F("closure", FVOf(Ref("ClosureV1")))))
	register("FileReceiptEnvelopeV1", Obj(F("identity", Ref("ReceiptIdentityV1")), F("emitter", Ref("ReceiptEmitterV1")), F("observed_at", Time), F("payload", Ref("FileReceiptV1"))).Check(func(r reader, m map[string]Value) error {
		if m["emitter"].Object["mode"].String == "dispatch_session" && m["emitter"].Object["session"].String != m["identity"].Object["executor_session"].String {
			return constraint(r, "emitter")
		}
		return nil
	}))
	register("FileResolutionV1", Obj(F("schema_version", Int(1, 1)), F("checkpoint_digest", DigestOf), F("effect_class", Enum("unknown", "no_external_effect", "confirmed")), F("closure_digests", Array(DigestOf, 0, 8).Ordered(SortedUnique))))
}

func checkFileStepObservation(r reader, m map[string]Value) error {
	state := m["state"].String
	proof := m["proof"]
	observed := m["observed_at"]
	reason := m["reason_code"]
	switch state {
	case "not_required", "not_started":
		if proof.Kind != KindNull || observed.Kind != KindNull || reason.Kind != KindNull {
			return r.fail(CodeConstraintViolated)
		}
	case "started":
		if proof.Kind != KindNull || observed.Kind == KindNull || reason.Kind != KindNull {
			return r.fail(CodeConstraintViolated)
		}
	case "unknown":
		if proof.Kind != KindNull || observed.Kind == KindNull || reason.Kind == KindNull {
			return r.fail(CodeConstraintViolated)
		}
	case "confirmed", "no_effect":
		if proof.Kind == KindNull || observed.Kind == KindNull {
			return r.fail(CodeConstraintViolated)
		}
		if (state == "no_effect") != (proof.Object["kind"].String == "no_effect") {
			return constraint(r, "proof")
		}
	}
	return nil
}

func checkFileCheckpoint(r reader, m map[string]Value) error {
	kinds := map[string]string{"stage_temp": "created_temp", "reserve_version": "reserved_path", "publish_target": "published", "source_cleanup": "removed_object", "temp_cleanup": "removed_object"}
	for _, step := range fileStepOrder {
		proof := m[step].Object["proof"]
		if proof.Kind == KindNull {
			continue
		}
		kind := proof.Object["kind"].String
		if kind == "no_effect" {
			if proof.Object["step"].String != step {
				return constraint(r, step)
			}
		} else if kind != kinds[step] {
			return constraint(r, step)
		}
	}
	return nil
}
