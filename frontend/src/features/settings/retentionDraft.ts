import type { QualityProfile, RetentionPolicyDefaultInput, RetentionPolicyDefaultView } from "../../lib/api/client";
import { profilesEqual } from "../retention/qualityProfile";
import { t } from "../../lib/i18n";

// Draft model for the Global "retention and cleanup" page. It edits the creation default
// only: a new subscription copies the complete default, existing subscriptions
// never re-inherit it. The comparison profile is edited whole by the shared
// QualityCompareEditor; saving merges the cleanup fields into the loaded
// cleanup object and keeps what the page does not own (its version).

export type AdmissionMode = RetentionPolicyDefaultView["admission_mode"];
export type CleanupEffect = "quarantine" | "unlink" | "purge";

export const admissionOptions = (): { value: AdmissionMode; label: string; hint: string }[] => [
  { value: "all", label: t("retention.admission.all"), hint: t("settings.retention.admissionHint.all") },
  { value: "per_group", label: t("retention.admission.per_group"), hint: t("settings.retention.admissionHint.per_group") },
  { value: "single_copy", label: t("retention.admission.single_copy"), hint: t("settings.retention.admissionHint.single_copy") },
];

export const cleanupOptions = (): { value: CleanupEffect; label: string; hint: string }[] => [
  { value: "quarantine", label: t("settings.retention.cleanup.quarantine"), hint: t("settings.retention.cleanupHint.quarantine") },
  { value: "unlink", label: t("settings.retention.cleanup.unlink"), hint: t("settings.retention.cleanupHint.unlink") },
  { value: "purge", label: t("settings.retention.cleanup.purge"), hint: t("settings.retention.cleanupHint.purge") },
];

export type RetentionDraft = {
  admission_mode: AdmissionMode;
  /** Ordered comparison items (profile-v1), edited whole. */
  quality_profile: QualityProfile;
  cleanup_effect: CleanupEffect;
  grace_seconds: string;
};

const CLEANUP_EFFECTS = new Set<string>(["quarantine", "unlink", "purge"] satisfies CleanupEffect[]);

export function draftFromView(view: RetentionPolicyDefaultView): RetentionDraft {
  const cleanup = view.cleanup_policy ?? {};
  return {
    admission_mode: view.admission_mode,
    quality_profile: structuredClone(view.quality_profile),
    cleanup_effect: CLEANUP_EFFECTS.has(String(cleanup.effect)) ? (cleanup.effect as CleanupEffect) : "quarantine",
    grace_seconds: String(cleanup.grace_seconds ?? 0),
  };
}

export function draftsEqual(a: RetentionDraft, b: RetentionDraft): boolean {
  return a.admission_mode === b.admission_mode
    && a.cleanup_effect === b.cleanup_effect
    && a.grace_seconds.trim() === b.grace_seconds.trim()
    && profilesEqual(a.quality_profile, b.quality_profile);
}

export function graceSecondsValue(draft: RetentionDraft): number | undefined {
  const text = draft.grace_seconds.trim();
  if (!/^\d+$/.test(text)) return undefined;
  const value = Number(text);
  return Number.isSafeInteger(value) ? value : undefined;
}

/** Returns a user-facing problem, or undefined when the draft can be saved. */
export function validateDraft(draft: RetentionDraft): string | undefined {
  if (graceSecondsValue(draft) === undefined) return t("settings.retention.graceInvalid");
  return undefined;
}

export function inputFromDraft(view: RetentionPolicyDefaultView, draft: RetentionDraft): RetentionPolicyDefaultInput {
  return {
    admission_mode: draft.admission_mode,
    quality_profile: draft.quality_profile,
    cleanup_policy: { ...view.cleanup_policy, effect: draft.cleanup_effect, grace_seconds: graceSecondsValue(draft) ?? 0 },
    expected_version: view.version,
  };
}

export function graceDaysLabel(seconds: number | undefined): string {
  if (seconds === undefined) return "";
  if (seconds === 0) return t("settings.retention.graceNone");
  const days = seconds / 86400;
  return t("settings.retention.graceDays", { n: Number.isInteger(days) ? days : days.toFixed(1) });
}
