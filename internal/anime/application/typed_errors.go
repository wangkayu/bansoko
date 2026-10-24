package application

import (
	"errors"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

// MetadataMatchIdempotencyReusedError reports one idempotency key reused for a
// different metadata match request.
func MetadataMatchIdempotencyReusedError() error {
	return &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different metadata match request."}
}

func metadataMatchIdempotencyReusedError() error { return MetadataMatchIdempotencyReusedError() }

// CandidateDigestMismatchError reports an invalid or inconsistent saved candidate digest.
func CandidateDigestMismatchError() error {
	return &apperrors.Business{Status: 409, Code: "CANDIDATE_DIGEST_MISMATCH", Message: "候选草稿完整性校验失败，请重新加载最新配置。"}
}

func candidateDigestMismatchError() error { return CandidateDigestMismatchError() }

// CandidateNotWorkingError reports a candidate that is not an activatable
// working draft.
func CandidateNotWorkingError() error {
	return &apperrors.Business{Status: 409, Code: "CANDIDATE_NOT_WORKING", Message: "候选不是可激活的 working 草稿。"}
}

func candidateNotWorkingError() error { return CandidateNotWorkingError() }

// CandidateRevisionMismatchError reports a requested revision that does not
// match the candidate's current saved revision.
func CandidateRevisionMismatchError(current, requested int64) error {
	return &apperrors.Business{Status: 409, Code: "CANDIDATE_REVISION_MISMATCH", Message: fmt.Sprintf("候选当前保存版本为 %d，不能执行请求的版本 %d。", current, requested)}
}

func candidateRevisionMismatchError(current, requested int64) error {
	return CandidateRevisionMismatchError(current, requested)
}

// CandidateRevisionRequiredError classifies a legacy candidate that has no
// immutable revision. The user must re-save it; the server never fabricates a
// revision from the current global configuration.
func CandidateRevisionRequiredError() error {
	return &apperrors.Business{
		Status: 409, Code: "CANDIDATE_REVISION_REQUIRED",
		Message: "候选尚无不可变配置版本，请先保存该候选，再预览或激活。",
	}
}

func candidateRevisionRequiredError() error { return CandidateRevisionRequiredError() }

func draftInvalidError(violations []animedomain.FieldViolation) error {
	code := violations[0].Code
	details := make([]apperrors.FieldViolation, 0, len(violations))
	for _, violation := range violations {
		field := violation.Field
		details = append(details, apperrors.FieldViolation{Field: &field, Code: violation.Code, Message: violation.Message})
	}
	return &apperrors.Validation{Code: code, Details: details}
}

func sectionResolutionError(err error) error {
	var sectionErr *animedomain.SectionValidationError
	if errors.As(err, &sectionErr) {
		field := sectionErr.Field
		return &apperrors.Validation{Code: sectionErr.Code, Details: []apperrors.FieldViolation{{Field: &field, Code: sectionErr.Code, Message: sectionErr.Message}}}
	}
	return &apperrors.Business{Status: 422, Code: "OVERRIDE_MODE_INVALID", Message: "Section values must be complete replace values."}
}

// NamingVarsSnapshot returns the loaded naming-variable snapshot or a typed configuration error.
func NamingVarsSnapshot(field string) (map[string]map[string]string, error) {
	vars, _, err := namingvars.Current()
	if err != nil {
		name := field
		return nil, &apperrors.Validation{
			Code:    naming.ErrCodeNamingVarsUnknown,
			Details: []apperrors.FieldViolation{{Field: &name, Code: naming.ErrCodeNamingVarsUnknown, Message: "naming vars are not loaded; check the naming variable program"}},
		}
	}
	return vars, nil
}

func namingVarsSnapshot(field string) (map[string]map[string]string, error) {
	return NamingVarsSnapshot(field)
}

// BusinessSnapshotInvalidError reports a submitted draft whose frozen business
// snapshot does not digest to the value the draft claims. It is the same 422 a
// candidate save answers, so every entry point that admits a draft carrying a
// business snapshot refuses a tampered one before any write.
func BusinessSnapshotInvalidError(message string) error {
	return &apperrors.Business{Status: 422, Code: "BUSINESS_SNAPSHOT_INVALID", Message: message}
}

// VerifySubmittedBusinessDigest re-freezes the draft's business snapshot and
// compares it with the digest the draft carries. A draft without a snapshot is
// accepted here: the paths that need a snapshot (candidate save, one-shot,
// preview) fail closed on their own. A digest without a snapshot is always
// refused — the provenance describes nothing.
func VerifySubmittedBusinessDigest(draft animedomain.AnimeDraft) error {
	if draft.Business == nil {
		if draft.BusinessDigest != nil {
			return BusinessSnapshotInvalidError("business_digest requires a business snapshot")
		}
		return nil
	}
	frozen, err := draft.Business.Freeze()
	if err != nil {
		return BusinessSnapshotInvalidError(err.Error())
	}
	digest, err := frozen.Digest()
	if err != nil {
		return BusinessSnapshotInvalidError(err.Error())
	}
	if draft.BusinessDigest == nil || *draft.BusinessDigest != digest {
		return BusinessSnapshotInvalidError("business_digest does not match the submitted snapshot")
	}
	return nil
}
