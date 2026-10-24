package domain

import (
	"reflect"
	"testing"
	"time"
)

// objectFixture is a valid PathObjectV1 taken from the golden fixtures.
func objectFixture(t *testing.T) Value {
	t.Helper()
	g := loadGolden(t)
	defs := map[string]Value{}
	for name, raw := range g.Definitions {
		v, err := ParseStrict(raw)
		if err != nil {
			t.Fatalf("definition %s: %v", name, err)
		}
		defs[name] = v
	}
	v := expandFixture(t, defs, defs["object"], nil)
	if err := Validate("PathObjectV1", v); err != nil {
		t.Fatalf("the fixture object must be valid: %v", err)
	}
	return v
}

var acceptedAt = time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC)

func queuedReceipt() AcceptReceipt {
	task := ID("task-1")
	return AcceptReceipt{
		AcquisitionID: "acq-1", Kind: AdmissionDownloadQueued, DownloadTaskID: &task,
		Config:          ConfigIdentity{Kind: "saved_revision", CandidateRef: "cand-1", Revision: 7, ManagedAnimeRef: Known(ID("anime-1"))},
		AdmissionDigest: *dg("a"), AcceptedAt: acceptedAt, ReplayGuaranteedUntil: acceptedAt.Add(30 * 24 * time.Hour),
	}
}

func adoptedReceipt() AcceptReceipt {
	r := queuedReceipt()
	r.Kind, r.DownloadTaskID = AdmissionAdoptedExisting, nil
	r.RequirementsDigest, r.TargetDigest = dg("b"), dg("c")
	return r
}

func TestAcceptReceiptShapesAndHTTPStatus(t *testing.T) {
	if AdmissionDownloadQueued.HTTPStatus() != 202 || AdmissionAdoptedExisting.HTTPStatus() != 200 {
		t.Fatal("a queued admission is 202 and an adopted one is 200")
	}
	configs := []ConfigIdentity{
		{Kind: "saved_revision", CandidateRef: "cand-1", Revision: 9007199254740993, ManagedAnimeRef: Known(ID("anime-1"))},
		{Kind: "saved_revision", CandidateRef: "cand-1", Revision: 1, ManagedAnimeRef: Absent[ID]()},
		{Kind: "submitted", IntentDigest: *dg("d"), ManagedAnimeRef: Unavailable[ID]("INSTANCE_RETIRED")},
		{Kind: "one_shot", IntentDigest: *dg("e")},
	}
	for _, config := range configs {
		for _, build := range []func() AcceptReceipt{queuedReceipt, adoptedReceipt} {
			r := build()
			r.Config = config
			v := r.Value()
			if err := Validate("AcceptReceiptV1", v); err != nil {
				t.Errorf("%s %s must be valid: %v", r.Kind, config.Kind, err)
			}
			parsed, err := ParseStrict(r.Canonical())
			if err != nil || !reflect.DeepEqual(parsed, v) {
				t.Errorf("%s %s: canonical round trip failed: %v", r.Kind, config.Kind, err)
			}
		}
	}
	// A queued admission may already hold an early requirement set; the digests are nullable.
	early := queuedReceipt()
	early.RequirementsDigest = dg("b")
	if err := Validate("AcceptReceiptV1", early.Value()); err != nil {
		t.Errorf("a queued receipt with an early requirements digest: %v", err)
	}
	// The saved revision is decimal text from the start.
	if got := queuedReceipt().Value().Object["config_identity"].Object["revision"]; got.Kind != KindString || got.String != "7" {
		t.Fatalf("revision must be text, got %+v", got)
	}
}

func TestAcceptReceiptRegistryRules(t *testing.T) {
	queued, adopted := queuedReceipt().Value(), adoptedReceipt().Value()
	reject := func(name string, v Value, code Code, path string) {
		t.Helper()
		assertRejected(t, name, "AcceptReceiptV1", v, code, path)
	}

	reject("a queued admission with no task", with(queued, "/download_task_id", null()), CodeConstraintViolated, "/download_task_id")
	reject("an adopted admission that names a task", with(adopted, "/download_task_id", str("task-1")), CodeConstraintViolated, "/download_task_id")
	reject("an adopted admission with no requirements digest", with(adopted, "/requirements_digest", null()), CodeConstraintViolated, "/requirements_digest")
	reject("an adopted admission with no target digest", with(adopted, "/target_digest", null()), CodeConstraintViolated, "/requirements_digest")
	reject("a replay window that ends before the acceptance", with(queued, "/replay_guaranteed_until", str("2026-10-04T01:59:59Z")),
		CodeConstraintViolated, "/replay_guaranteed_until")
	reject("an unknown admission kind", with(queued, "/admission_kind", str("queued")), CodeUnionBranchInvalid, "/admission_kind")
	reject("an unknown schema version", with(queued, "/schema_version", i64(2)), CodeConstraintViolated, "/schema_version")
	reject("a revision that is a number", with(queued, "/config_identity/revision", i64(7)), CodeTypeInvalid, "/config_identity/revision")
	reject("a revision with a leading zero", with(queued, "/config_identity/revision", str("07")), CodeTypeInvalid, "/config_identity/revision")
	reject("a zero revision", with(queued, "/config_identity/revision", str("0")), CodeTypeInvalid, "/config_identity/revision")
	reject("a negative revision", with(queued, "/config_identity/revision", str("-1")), CodeTypeInvalid, "/config_identity/revision")
	reject("a revision beyond int64", with(queued, "/config_identity/revision", str("9223372036854775808")), CodeTypeInvalid, "/config_identity/revision")
	reject("an unknown config identity kind", with(queued, "/config_identity/kind", str("draft")), CodeUnionBranchInvalid, "/config_identity/kind")
	reject("a malformed digest", with(queued, "/admission_digest", str("sha256:xyz")), CodeTypeInvalid, "/admission_digest")
	reject("an accepted time with a trailing zero", with(queued, "/accepted_at", str("2026-10-04T02:00:00.100000Z")), CodeTypeInvalid, "/accepted_at")
}

func TestDeliveryResultShapes(t *testing.T) {
	object := objectFixture(t)
	completion := func(m CompletionMode) *CompletionMode { return &m }
	reason := func(r Reason) *Reason { return &r }

	valid := []DeliveryResult{
		{FileRef: "f1", Outcome: OutcomeSucceeded, Completion: completion(CompletionExecuted), Effect: EffectConfirmed, FinalObject: &object,
			SourceCleanup: CleanupCompleted, EvidenceDigest: *dg("a")},
		{FileRef: "f2", Outcome: OutcomeSucceeded, Completion: completion(CompletionAdopted), Effect: EffectNone, FinalObject: &object,
			SourceCleanup: CleanupNotRequired, EvidenceDigest: *dg("a")},
		{FileRef: "f3", Outcome: OutcomeSucceeded, Completion: completion(CompletionRetain), Effect: EffectNone, FinalObject: &object,
			SourceCleanup: CleanupNotRequired, EvidenceDigest: *dg("a")},
		// A failure keeps a partial real object, or none at all.
		{FileRef: "f4", Outcome: OutcomeFailed, Effect: EffectConfirmed, FinalObject: &object, SourceCleanup: CleanupFailed,
			ReasonCode: reason("SOURCE_CLEANUP_FAILED"), EvidenceDigest: *dg("a")},
		{FileRef: "f5", Outcome: OutcomeFailed, Effect: EffectUnknown, SourceCleanup: CleanupUncertain,
			ReasonCode: reason("PUBLISH_UNCERTAIN"), EvidenceDigest: *dg("a")},
		{FileRef: "f6", Outcome: OutcomeFailed, Effect: EffectNone, SourceCleanup: CleanupNotRequired,
			ReasonCode: reason("DEPENDENCY_FAILED"), EvidenceDigest: *dg("a")},
	}
	for _, d := range valid {
		v := d.Value()
		if err := Validate("DeliveryResultV1", v); err != nil {
			t.Errorf("%s must be valid: %v", d.FileRef, err)
		}
		parsed, err := ParseStrict(d.Canonical())
		if err != nil || !reflect.DeepEqual(parsed, v) {
			t.Errorf("%s: canonical round trip failed: %v", d.FileRef, err)
		}
	}

	good, failed := valid[0].Value(), valid[3].Value()
	reject := func(name string, v Value, code Code, path string) {
		t.Helper()
		assertRejected(t, name, "DeliveryResultV1", v, code, path)
	}
	reject("a success with no completion mode", with(good, "/completion_mode", null()), CodeConstraintViolated, "/completion_mode")
	reject("a success with no final object", with(good, "/final_object", null()), CodeConstraintViolated, "/final_object")
	reject("a success with a failure reason", with(good, "/reason_code", str("OOPS")), CodeConstraintViolated, "/reason_code")
	reject("a success whose source cleanup failed", with(good, "/source_cleanup", str("failed")), CodeConstraintViolated, "/source_cleanup")
	reject("a success whose source cleanup is uncertain", with(good, "/source_cleanup", str("uncertain")), CodeConstraintViolated, "/source_cleanup")
	reject("a success with an unknown effect", with(good, "/effect_class", str("unknown")), CodeConstraintViolated, "/effect_class")
	reject("a failure with no reason", with(failed, "/reason_code", null()), CodeConstraintViolated, "/reason_code")
	reject("a failure that names a completion mode", with(failed, "/completion_mode", str("executed")), CodeConstraintViolated, "/completion_mode")
	reject("an unknown outcome", with(good, "/outcome", str("skipped")), CodeUnionBranchInvalid, "/outcome")
	reject("an unknown completion mode", with(good, "/completion_mode", str("copied")), CodeUnionBranchInvalid, "/completion_mode")
	reject("a lower-case reason", with(failed, "/reason_code", str("lower")), CodeTypeInvalid, "/reason_code")
	// A final object must be real object evidence: a closed object, not an arbitrary one.
	if err := Validate("DeliveryResultV1", with(good, "/final_object", obj(map[string]Value{"path": str("/x")}))); err == nil {
		t.Error("a final object that is not object evidence must be rejected")
	}
	reject("an unknown schema version", with(good, "/schema_version", i64(2)), CodeConstraintViolated, "/schema_version")
}
