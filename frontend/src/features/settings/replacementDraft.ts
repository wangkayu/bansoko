import type { QualityProfile, ReplacementPolicyDefaultInput, ReplacementPolicyDefaultView } from "../../lib/api/client";
import { profilesEqual } from "../replacement/qualityProfile";
import { t } from "../../lib/i18n";

// Draft model for the Global replacement page. It edits the creation default
// only: a new subscription copies the complete default, existing subscriptions
// never re-inherit it. The comparison profile is edited whole by the shared
// QualityCompareEditor. A replaced old file is always deleted directly, so
// there is no cleanup setting to edit.

export type AdmissionMode = ReplacementPolicyDefaultView["admission_mode"];

export const admissionOptions = (): { value: AdmissionMode; label: string; hint: string }[] => [
  { value: "all", label: t("replacement.admission.all"), hint: t("settings.replacement.admissionHint.all") },
  { value: "per_group", label: t("replacement.admission.per_group"), hint: t("settings.replacement.admissionHint.per_group") },
  { value: "single_copy", label: t("replacement.admission.single_copy"), hint: t("settings.replacement.admissionHint.single_copy") },
];

export type ReplacementDraft = {
  admission_mode: AdmissionMode;
  /** Ordered comparison items (profile-v1), edited whole. */
  quality_profile: QualityProfile;
};

export function draftFromView(view: ReplacementPolicyDefaultView): ReplacementDraft {
  return {
    admission_mode: view.admission_mode,
    quality_profile: structuredClone(view.quality_profile),
  };
}

export function draftsEqual(a: ReplacementDraft, b: ReplacementDraft): boolean {
  return a.admission_mode === b.admission_mode && profilesEqual(a.quality_profile, b.quality_profile);
}

export function inputFromDraft(view: ReplacementPolicyDefaultView, draft: ReplacementDraft): ReplacementPolicyDefaultInput {
  return {
    admission_mode: draft.admission_mode,
    quality_profile: draft.quality_profile,
    expected_version: view.version,
  };
}
