import type { QualityProfile, RetentionPolicyDefaultInput, RetentionPolicyDefaultView } from "../../lib/api/client";
import { profilesEqual } from "../retention/qualityProfile";
import { t } from "../../lib/i18n";

// Draft model for the Global "retention and cleanup" page. It edits the creation default
// only: a new subscription copies the complete default, existing subscriptions
// never re-inherit it. The comparison profile is edited whole by the shared
// QualityCompareEditor. A replaced old file is always deleted directly, so
// there is no cleanup setting to edit.

export type AdmissionMode = RetentionPolicyDefaultView["admission_mode"];

export const admissionOptions = (): { value: AdmissionMode; label: string; hint: string }[] => [
  { value: "all", label: t("retention.admission.all"), hint: t("settings.retention.admissionHint.all") },
  { value: "per_group", label: t("retention.admission.per_group"), hint: t("settings.retention.admissionHint.per_group") },
  { value: "single_copy", label: t("retention.admission.single_copy"), hint: t("settings.retention.admissionHint.single_copy") },
];

export type RetentionDraft = {
  admission_mode: AdmissionMode;
  /** Ordered comparison items (profile-v1), edited whole. */
  quality_profile: QualityProfile;
};

export function draftFromView(view: RetentionPolicyDefaultView): RetentionDraft {
  return {
    admission_mode: view.admission_mode,
    quality_profile: structuredClone(view.quality_profile),
  };
}

export function draftsEqual(a: RetentionDraft, b: RetentionDraft): boolean {
  return a.admission_mode === b.admission_mode && profilesEqual(a.quality_profile, b.quality_profile);
}

export function inputFromDraft(view: RetentionPolicyDefaultView, draft: RetentionDraft): RetentionPolicyDefaultInput {
  return {
    admission_mode: draft.admission_mode,
    quality_profile: draft.quality_profile,
    expected_version: view.version,
  };
}
