import type { DownloadCompletionAction, DownloadCompletionRules, DownloadCompletionSeedLimits } from "./api/client";
import { t } from "./i18n";
import type { SelectOption } from "../ui/base";

// The global "after download completes" settings (Settings → Download and import): the four
// handling choices, the optional seeding cap when seeding continues, and the conversion
// between form and stored values. The cap is handed to the downloader, which enforces it; the
// product keeps no timer.

export const DOWNLOAD_COMPLETION_ACTIONS: readonly DownloadCompletionAction[] = ["seed", "pause", "remove", "remove_with_data"];

export type SeedLimitAction = DownloadCompletionSeedLimits["at_limit"];
export const SEED_LIMIT_ACTIONS: readonly SeedLimitAction[] = ["stop", "remove", "remove_with_data"];

export type SeedTimeUnit = "hours" | "minutes";

/** The cap range, matching the backend (qBittorrent's own limits). */
export const MAX_SEEDING_TIME_MINUTES = 525600;
export const MIN_SEED_RATIO = 0.01;
export const MAX_SEED_RATIO = 9998;

export const downloadCompletionOptions = (): SelectOption<DownloadCompletionAction>[] =>
  DOWNLOAD_COMPLETION_ACTIONS.map((action) => ({ value: action, label: t(`downloadCompletion.action.${action}`) }));

export const seedLimitActionOptions = (): SelectOption<SeedLimitAction>[] =>
  SEED_LIMIT_ACTIONS.map((action) => ({ value: action, label: t(`downloadCompletion.atLimit.${action}`) }));

export const seedTimeUnitOptions = (): SelectOption<SeedTimeUnit>[] => [
  { value: "hours", label: t("downloadCompletion.unit.hours") },
  { value: "minutes", label: t("downloadCompletion.unit.minutes") },
];

/** Supplementary notes the selected action needs: the delete-files warning; the keep-seeding note when the import method is move. */
export function downloadCompletionNote(action: DownloadCompletionAction, importMethod?: string): string | undefined {
  if (action === "remove_with_data") return t("downloadCompletion.removeDataWarning");
  if (action === "seed" && importMethod === "move") return t("downloadCompletion.moveSeedNote");
  return undefined;
}

/** Reads the stored value; an unknown or missing handling displays as keep seeding, matching the backend's read. */
export function downloadCompletionRules(value: Record<string, unknown> | undefined): DownloadCompletionRules {
  const action = DOWNLOAD_COMPLETION_ACTIONS.find((candidate) => candidate === value?.action) ?? "seed";
  const raw = value?.seed_limits as Partial<DownloadCompletionSeedLimits> | undefined;
  if (action !== "seed" || !raw || typeof raw !== "object") return { action };
  const atLimit = SEED_LIMIT_ACTIONS.find((candidate) => candidate === raw.at_limit);
  if (!atLimit) return { action };
  const limits: DownloadCompletionSeedLimits = { at_limit: atLimit };
  if (typeof raw.seeding_time_minutes === "number") limits.seeding_time_minutes = raw.seeding_time_minutes;
  if (typeof raw.ratio === "number") limits.ratio = raw.ratio;
  return { action, seed_limits: limits };
}

/** The edit form's state: numbers are kept as input text and validated on submit. */
export type DownloadCompletionForm = {
  action: DownloadCompletionAction;
  limited: boolean;
  time: string;
  unit: SeedTimeUnit;
  ratio: string;
  atLimit: SeedLimitAction;
};

export function downloadCompletionForm(rules: DownloadCompletionRules): DownloadCompletionForm {
  const limits = rules.seed_limits;
  const minutes = limits?.seeding_time_minutes;
  const wholeHours = minutes !== undefined && minutes % 60 === 0;
  return {
    action: rules.action,
    limited: limits !== undefined,
    time: minutes === undefined ? "" : String(wholeHours ? minutes / 60 : minutes),
    unit: minutes === undefined || wholeHours ? "hours" : "minutes",
    ratio: limits?.ratio === undefined ? "" : String(limits.ratio),
    atLimit: limits?.at_limit ?? "stop",
  };
}

/** Converts the form to the stored value; returns the first problem's message when invalid. */
export function downloadCompletionValue(form: DownloadCompletionForm): { value: DownloadCompletionRules } | { error: string } {
  if (form.action !== "seed" || !form.limited) return { value: { action: form.action } };
  const limits: DownloadCompletionSeedLimits = { at_limit: form.atLimit };
  const time = form.time.trim();
  const ratio = form.ratio.trim();
  if (time === "" && ratio === "") return { error: t("downloadCompletion.limitRequired") };
  if (time !== "") {
    const amount = Number(time);
    const minutes = form.unit === "hours" ? amount * 60 : amount;
    if (!Number.isFinite(minutes) || !Number.isInteger(minutes) || minutes < 1 || minutes > MAX_SEEDING_TIME_MINUTES) {
      return { error: t("downloadCompletion.timeInvalid") };
    }
    limits.seeding_time_minutes = minutes;
  }
  if (ratio !== "") {
    const value = Number(ratio);
    if (!Number.isFinite(value) || value < MIN_SEED_RATIO || value > MAX_SEED_RATIO) {
      return { error: t("downloadCompletion.ratioInvalid") };
    }
    limits.ratio = value;
  }
  return { value: { action: "seed", seed_limits: limits } };
}

/** The seeding cap in the summary, e.g. "48 hours · ratio 2". */
export function seedLimitsSummary(limits: DownloadCompletionSeedLimits | undefined): string {
  if (!limits) return t("downloadCompletion.noLimit");
  const parts: string[] = [];
  const minutes = limits.seeding_time_minutes;
  if (minutes !== undefined) {
    parts.push(minutes % 60 === 0
      ? t("downloadCompletion.timeHours", { n: minutes / 60 })
      : t("downloadCompletion.timeMinutes", { n: minutes }));
  }
  if (limits.ratio !== undefined) parts.push(t("downloadCompletion.ratioValue", { n: limits.ratio }));
  return parts.join(" · ");
}
