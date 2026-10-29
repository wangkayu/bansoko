package domain

import "time"

// The terminal receipt: the one immutable record of how an acquisition ended. It
// is written once, by the settler, in the same transaction that finishes the
// root, and it never changes afterwards; later evidence is read separately and
// never rewrites it.

// Outcome is how an acquisition ended.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	// OutcomeCancelled is never a success and always carries a primary failure
	// whose reason is one of the cancellation reasons: ReasonOwnerRetired (the
	// retirement disposition: the owner retired while the root was still
	// running), or one of the settler's download cancellations before import
	// (ReasonDownloaderTaskRemoved, ReasonProviderGroupNotAllowed,
	// ReasonUserCancelled).
	OutcomeCancelled Outcome = "cancelled"
)

// The cancellation reasons. No other reason may end a root cancelled.
const (
	// ReasonOwnerRetired is the retirement disposition's reason.
	ReasonOwnerRetired Reason = "OWNER_RETIRED"
	// ReasonDownloaderTaskRemoved is the download task's failure code after two
	// consecutive definitive absent observations in qB. The task row stays failed with this code; the root settles it
	// as cancelled at the submit or download_wait stage.
	ReasonDownloaderTaskRemoved Reason = "DOWNLOADER_TASK_REMOVED"
	// ReasonProviderGroupNotAllowed is the download task's failure code after
	// its provider group left the subscription's selection before import.
	ReasonProviderGroupNotAllowed Reason = "PROVIDER_GROUP_NOT_ALLOWED"
	// ReasonUserCancelled is the download task's failure code after an explicit
	// cancel of the execution record before import.
	ReasonUserCancelled Reason = "USER_CANCELLED"
)

// IsDownloadCancellation reports a download task failure code the settler
// ends as cancelled when it stopped the submit or download stage.
func IsDownloadCancellation(code Reason) bool {
	switch code {
	case ReasonDownloaderTaskRemoved, ReasonProviderGroupNotAllowed, ReasonUserCancelled:
		return true
	}
	return false
}

// RequirementsStatus is how far the file requirements got. Only sealed means the
// manifest was verified and every file was classified required or excluded.
type RequirementsStatus string

const (
	RequirementsNotReached RequirementsStatus = "not_reached"
	RequirementsIncomplete RequirementsStatus = "incomplete"
	RequirementsSealed     RequirementsStatus = "sealed"
)

// Stage is a fixed stage of an acquisition, in execution order.
type Stage string

const (
	StageSubmit       Stage = "submit"
	StageDownloadWait Stage = "download_wait"
	StageInspect      Stage = "inspect"
	StagePlan         Stage = "plan"
	StageFileExec     Stage = "file_execution"
	StageSettlement   Stage = "settlement"
)

var stageNames = []string{
	string(StageSubmit), string(StageDownloadWait), string(StageInspect),
	string(StagePlan), string(StageFileExec), string(StageSettlement),
}

// Failure is the safe, bounded reason an acquisition failed: a fixed stage, a
// stable reason code and the file it concerned when there was one. It never
// carries a raw error, a work item or a permit.
type Failure struct {
	Stage      Stage
	ReasonCode Reason
	FileRef    *ID
}

func (f Failure) value() Value {
	file := null()
	if f.FileRef != nil {
		file = str(string(*f.FileRef))
	}
	return obj(map[string]Value{"stage": str(string(f.Stage)), "reason_code": str(string(f.ReasonCode)), "file_ref": file})
}

// Counts is the settled tally of a sealed requirement set.
type Counts struct {
	Total, Required, Excluded, Succeeded, Failed int64
}

func (c Counts) value() Value {
	return obj(map[string]Value{
		"total": i64(c.Total), "required": i64(c.Required), "excluded": i64(c.Excluded),
		"succeeded": i64(c.Succeeded), "failed": i64(c.Failed),
	})
}

// EffectSummary is the conservative statement, made when the root settled, about
// the outward effects. Complete needs every fact checked with no unknown effect
// and no gap; it is not the business outcome. A confirmed partial effect and an
// unknown one can both be true.
type EffectSummary struct {
	Complete, HasConfirmed, HasUnknown bool
}

func (e EffectSummary) value() Value {
	return obj(map[string]Value{
		"complete": boolean(e.Complete), "has_confirmed": boolean(e.HasConfirmed), "has_unknown": boolean(e.HasUnknown),
	})
}

// TerminalReceipt is native_terminal_v1. The pointers are the nullable members:
// counts and the digests exist only for a sealed set, and the failure only for a
// failed outcome.
type TerminalReceipt struct {
	Outcome            Outcome
	RequirementsStatus RequirementsStatus
	Counts             *Counts
	RequirementsDigest *DigestText
	TargetDigest       *DigestText
	PrimaryFailure     *Failure
	Effects            EffectSummary
	SettledAt          time.Time
}

// Value is the receipt in the registry's shape, ready to validate or encode.
func (t TerminalReceipt) Value() Value {
	counts, requirements, targets, failure := null(), null(), null(), null()
	if t.Counts != nil {
		counts = t.Counts.value()
	}
	if t.RequirementsDigest != nil {
		requirements = str(string(*t.RequirementsDigest))
	}
	if t.TargetDigest != nil {
		targets = str(string(*t.TargetDigest))
	}
	if t.PrimaryFailure != nil {
		failure = t.PrimaryFailure.value()
	}
	return obj(map[string]Value{
		"schema_version": i64(1), "outcome": str(string(t.Outcome)), "requirements_status": str(string(t.RequirementsStatus)),
		"counts": counts, "requirements_digest": requirements, "target_digest": targets,
		"primary_failure": failure, "effect_summary": t.Effects.value(), "settled_at": str(FormatTime(t.SettledAt)),
	})
}

// Canonical is the receipt's canonical JSON bytes.
func (t TerminalReceipt) Canonical() []byte { return Canonical(t.Value()) }

// FormatTime is the one canonical time text: UTC, RFC 3339 with the fraction
// trimmed of trailing zeros and no finer than a microsecond, which is exactly
// what the Time schema accepts.
func FormatTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

func init() {
	register("FailureV1", Obj(
		F("stage", Enum(stageNames...)), F("reason_code", Custom(func(r reader, v Value) error { _, err := r.reason(v); return err })),
		F("file_ref", Nullable(IDType)),
	))
	register("CountsV1", Obj(
		F("total", Size), F("required", Size), F("excluded", Size), F("succeeded", Size), F("failed", Size),
	).Check(func(r reader, m map[string]Value) error {
		c := func(k string) int64 { return m[k].Int }
		if c("required") != c("succeeded")+c("failed") {
			return r.at("required").fail(CodeConstraintViolated)
		}
		if c("total") != c("required")+c("excluded") {
			return r.at("total").fail(CodeConstraintViolated)
		}
		return nil
	}))
	register("EffectSummaryV1", Obj(F("complete", Bool), F("has_confirmed", Bool), F("has_unknown", Bool)))
	register("TerminalReceiptV1", Obj(
		F("schema_version", Int(1, 1)), F("outcome", Enum(string(OutcomeSucceeded), string(OutcomeFailed), string(OutcomeCancelled))),
		F("requirements_status", Enum(string(RequirementsNotReached), string(RequirementsIncomplete), string(RequirementsSealed))),
		F("counts", Nullable(Ref("CountsV1"))), F("requirements_digest", Nullable(DigestOf)), F("target_digest", Nullable(DigestOf)),
		F("primary_failure", Nullable(Ref("FailureV1"))), F("effect_summary", Ref("EffectSummaryV1")), F("settled_at", Time),
	).Check(func(r reader, m map[string]Value) error {
		sealed := m["requirements_status"].String == string(RequirementsSealed)
		isNull := func(k string) bool { return m[k].Kind == KindNull }
		// A set that never sealed has no tally and no digests; a sealed one has the tally
		// and the requirements digest (the target digest is absent when no target set was
		// ever built, for instance after a plan failure).
		if sealed && (isNull("counts") || isNull("requirements_digest")) {
			return r.at("counts").fail(CodeConstraintViolated)
		}
		if !sealed {
			for _, k := range []string{"counts", "requirements_digest", "target_digest"} {
				if !isNull(k) {
					return r.at(k).fail(CodeConstraintViolated)
				}
			}
		}
		effects := m["effect_summary"].Object
		switch m["outcome"].String {
		case string(OutcomeSucceeded):
			// Success needs a sealed set with something required, nothing failed, no
			// unknown effect and every fact checked.
			if !sealed {
				return r.at("requirements_status").fail(CodeConstraintViolated)
			}
			counts := m["counts"].Object
			if counts["required"].Int <= 0 || counts["failed"].Int != 0 {
				return r.at("counts").fail(CodeConstraintViolated)
			}
			if !isNull("primary_failure") {
				return r.at("primary_failure").fail(CodeConstraintViolated)
			}
			if effects["has_unknown"].Bool || !effects["complete"].Bool {
				return r.at("effect_summary").fail(CodeConstraintViolated)
			}
		case string(OutcomeFailed):
			if isNull("primary_failure") {
				return r.at("primary_failure").fail(CodeConstraintViolated)
			}
		case string(OutcomeCancelled):
			if isNull("primary_failure") {
				return r.at("primary_failure").fail(CodeConstraintViolated)
			}
			failure := m["primary_failure"].Object
			switch failure["reason_code"].String {
			case string(ReasonOwnerRetired):
				// Retirement settles without checking every fact.
				if effects["complete"].Bool {
					return r.at("effect_summary").fail(CodeConstraintViolated)
				}
			case string(ReasonDownloaderTaskRemoved), string(ReasonProviderGroupNotAllowed), string(ReasonUserCancelled):
				// Only a download that never completed can have been deleted in the
				// downloader or cancelled: the failure is always the submit or
				// download stage's.
				if stage := failure["stage"].String; stage != string(StageSubmit) && stage != string(StageDownloadWait) {
					return r.at("primary_failure").fail(CodeConstraintViolated)
				}
			default:
				return r.at("primary_failure").fail(CodeConstraintViolated)
			}
		}
		// Complete never coexists with an unknown effect.
		if effects["complete"].Bool && effects["has_unknown"].Bool {
			return r.at("effect_summary").fail(CodeConstraintViolated)
		}
		return nil
	}))
}
