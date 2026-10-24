package domain

import (
	"strconv"
	"time"
)

// The other two immutable receipts. The accept receipt is what the caller gets
// back when an acquisition is admitted, and what a replay of the same request key
// returns; it never changes afterwards, so a requirement or target digest that
// arrives later is not written back into it. The delivery result is the record of
// how one required file ended.

// AdmissionKind is how the acquisition was admitted. The kind fixes the HTTP
// status of the admission: a queued download is 202, an adopted existing result
// is 200.
type AdmissionKind string

const (
	AdmissionDownloadQueued  AdmissionKind = "download_queued"
	AdmissionAdoptedExisting AdmissionKind = "adopted_existing"
)

// HTTPStatus is the status the admission answered with.
func (k AdmissionKind) HTTPStatus() int {
	if k == AdmissionAdoptedExisting {
		return 200
	}
	return 202
}

// ConfigIdentity is the safe projection of the input's configuration identity. The
// receipt fixes the saved revision as decimal text from the start, so a replay or a
// later read never changes its type.
type ConfigIdentity struct {
	Kind            string // saved_revision, submitted or one_shot
	CandidateRef    ID
	Revision        int64
	ManagedAnimeRef FV[ID]
	IntentDigest    DigestText
}

func (c ConfigIdentity) value() Value {
	managed := c.ManagedAnimeRef.value(func(id ID) Value { return str(string(id)) })
	switch c.Kind {
	case "saved_revision":
		return obj(map[string]Value{
			"kind": str(c.Kind), "candidate_ref": str(string(c.CandidateRef)),
			"revision": str(strconv.FormatInt(c.Revision, 10)), "managed_anime_ref": managed,
		})
	case "submitted":
		return obj(map[string]Value{"kind": str(c.Kind), "intent_digest": str(string(c.IntentDigest)), "managed_anime_ref": managed})
	default:
		return obj(map[string]Value{"kind": str(c.Kind), "intent_digest": str(string(c.IntentDigest))})
	}
}

// AcceptReceipt is the accept receipt of an acquisition.
type AcceptReceipt struct {
	AcquisitionID         ID
	Kind                  AdmissionKind
	DownloadTaskID        *ID
	Config                ConfigIdentity
	AdmissionDigest       DigestText
	RequirementsDigest    *DigestText
	TargetDigest          *DigestText
	AcceptedAt            time.Time
	ReplayGuaranteedUntil time.Time
}

// Value is the receipt in the registry's shape.
func (a AcceptReceipt) Value() Value {
	task, requirements, targets := null(), null(), null()
	if a.DownloadTaskID != nil {
		task = str(string(*a.DownloadTaskID))
	}
	if a.RequirementsDigest != nil {
		requirements = str(string(*a.RequirementsDigest))
	}
	if a.TargetDigest != nil {
		targets = str(string(*a.TargetDigest))
	}
	return obj(map[string]Value{
		"schema_version": i64(1), "acquisition_id": str(string(a.AcquisitionID)), "admission_kind": str(string(a.Kind)),
		"download_task_id": task, "config_identity": a.Config.value(), "admission_digest": str(string(a.AdmissionDigest)),
		"requirements_digest": requirements, "target_digest": targets,
		"accepted_at": str(FormatTime(a.AcceptedAt)), "replay_guaranteed_until": str(FormatTime(a.ReplayGuaranteedUntil)),
	})
}

// Canonical is the receipt's canonical JSON bytes.
func (a AcceptReceipt) Canonical() []byte { return Canonical(a.Value()) }

// CompletionMode is how a successful file was completed.
type CompletionMode string

const (
	CompletionExecuted CompletionMode = "executed"
	CompletionAdopted  CompletionMode = "adopted"
	CompletionRetain   CompletionMode = "retain"
)

// SourceCleanup is the settled state of the source cleanup a move requires.
type SourceCleanup string

const (
	CleanupNotRequired SourceCleanup = "not_required"
	CleanupCompleted   SourceCleanup = "completed"
	CleanupFailed      SourceCleanup = "failed"
	CleanupUncertain   SourceCleanup = "uncertain"
)

// DeliveryResult is the typed result of one required file. FinalObject is an
// PathObjectV1 value, already validated; a failure may keep a partial real
// final object.
type DeliveryResult struct {
	FileRef        ID
	Outcome        Outcome
	Completion     *CompletionMode
	Effect         EffectClass
	FinalObject    *Value
	SourceCleanup  SourceCleanup
	ReasonCode     *Reason
	EvidenceDigest DigestText
}

// Value is the result in the registry's shape.
func (d DeliveryResult) Value() Value {
	completion, final, reason := null(), null(), null()
	if d.Completion != nil {
		completion = str(string(*d.Completion))
	}
	if d.FinalObject != nil {
		final = *d.FinalObject
	}
	if d.ReasonCode != nil {
		reason = str(string(*d.ReasonCode))
	}
	return obj(map[string]Value{
		"schema_version": i64(1), "file_ref": str(string(d.FileRef)), "outcome": str(string(d.Outcome)),
		"completion_mode": completion, "effect_class": str(string(d.Effect)), "final_object": final,
		"source_cleanup": str(string(d.SourceCleanup)), "reason_code": reason, "evidence_digest": str(string(d.EvidenceDigest)),
	})
}

// Canonical is the result's canonical JSON bytes.
func (d DeliveryResult) Canonical() []byte { return Canonical(d.Value()) }

// positiveI64Text is a canonical positive decimal int64: no sign, no leading zero.
var positiveI64Text = Custom(func(r reader, v Value) error {
	if v.Kind != KindString || v.String == "" || v.String[0] == '0' || len(v.String) > 19 {
		return r.fail(CodeTypeInvalid)
	}
	for i := 0; i < len(v.String); i++ {
		if v.String[i] < '0' || v.String[i] > '9' {
			return r.fail(CodeTypeInvalid)
		}
	}
	if n, err := strconv.ParseInt(v.String, 10, 64); err != nil || n < 1 {
		return r.fail(CodeTypeInvalid)
	}
	return nil
})

func init() {
	register("ConfigIdentityReceiptV1", Union("kind", map[string]*objectSchema{
		"saved_revision": Obj(F("candidate_ref", IDType), F("revision", positiveI64Text), F("managed_anime_ref", FVOf(IDType))),
		"submitted":      Obj(F("intent_digest", DigestOf), F("managed_anime_ref", FVOf(IDType))),
		"one_shot":       Obj(F("intent_digest", DigestOf)),
	}))
	register("AcceptReceiptV1", Obj(
		F("schema_version", Int(1, 1)), F("acquisition_id", IDType),
		F("admission_kind", Enum(string(AdmissionDownloadQueued), string(AdmissionAdoptedExisting))),
		F("download_task_id", Nullable(IDType)), F("config_identity", Ref("ConfigIdentityReceiptV1")), F("admission_digest", DigestOf),
		F("requirements_digest", Nullable(DigestOf)), F("target_digest", Nullable(DigestOf)),
		F("accepted_at", Time), F("replay_guaranteed_until", Time),
	).Check(func(r reader, m map[string]Value) error {
		isNull := func(k string) bool { return m[k].Kind == KindNull }
		switch m["admission_kind"].String {
		case string(AdmissionDownloadQueued):
			// A queued admission always names the task that was queued.
			if isNull("download_task_id") {
				return r.at("download_task_id").fail(CodeConstraintViolated)
			}
		case string(AdmissionAdoptedExisting):
			// An adopted one has no task and was admitted over a complete set.
			if !isNull("download_task_id") {
				return r.at("download_task_id").fail(CodeConstraintViolated)
			}
			if isNull("requirements_digest") || isNull("target_digest") {
				return r.at("requirements_digest").fail(CodeConstraintViolated)
			}
		}
		// The guaranteed replay window never starts before the acceptance.
		accepted, _ := time.Parse(time.RFC3339Nano, m["accepted_at"].String)
		until, _ := time.Parse(time.RFC3339Nano, m["replay_guaranteed_until"].String)
		if until.Before(accepted) {
			return r.at("replay_guaranteed_until").fail(CodeConstraintViolated)
		}
		return nil
	}))

	register("DeliveryResultV1", Obj(
		F("schema_version", Int(1, 1)), F("file_ref", IDType),
		F("outcome", Enum(string(OutcomeSucceeded), string(OutcomeFailed))),
		F("completion_mode", Nullable(Enum(string(CompletionExecuted), string(CompletionAdopted), string(CompletionRetain)))),
		F("effect_class", Enum(string(EffectUnknown), string(EffectNone), string(EffectConfirmed))),
		F("final_object", Nullable(Ref("PathObjectV1"))),
		F("source_cleanup", Enum(string(CleanupNotRequired), string(CleanupCompleted), string(CleanupFailed), string(CleanupUncertain))),
		F("reason_code", Nullable(Custom(func(r reader, v Value) error { _, err := r.reason(v); return err }))),
		F("evidence_digest", DigestOf),
	).Check(func(r reader, m map[string]Value) error {
		isNull := func(k string) bool { return m[k].Kind == KindNull }
		switch m["outcome"].String {
		case string(OutcomeSucceeded):
			// A success says how it completed, has the object it produced, has no failure
			// reason, never leaves the source cleanup failed or uncertain, and never leaves
			// its effect unknown.
			if isNull("completion_mode") {
				return r.at("completion_mode").fail(CodeConstraintViolated)
			}
			if isNull("final_object") {
				return r.at("final_object").fail(CodeConstraintViolated)
			}
			if !isNull("reason_code") {
				return r.at("reason_code").fail(CodeConstraintViolated)
			}
			if cleanup := m["source_cleanup"].String; cleanup == string(CleanupFailed) || cleanup == string(CleanupUncertain) {
				return r.at("source_cleanup").fail(CodeConstraintViolated)
			}
			if m["effect_class"].String == string(EffectUnknown) {
				return r.at("effect_class").fail(CodeConstraintViolated)
			}
		case string(OutcomeFailed):
			// A failure has a reason and no completion mode; it may keep a partial real object.
			if isNull("reason_code") {
				return r.at("reason_code").fail(CodeConstraintViolated)
			}
			if !isNull("completion_mode") {
				return r.at("completion_mode").fail(CodeConstraintViolated)
			}
		}
		return nil
	}))
}
