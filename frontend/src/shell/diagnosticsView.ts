import type { DiagnosticEpisode } from "../lib/api/client";
import { t, tryT } from "../lib/i18n";
import { formatShortDateTime } from "../lib/labels";
import { joinMeta } from "./logRuns";

// Pure helpers behind the diagnostics tab: one row per episode, worded from the closed
// condition catalog (`log.diag.<kind>.<variant>`); no paths and no raw error text are shown.

export type DiagType = "site" | "external" | "downloader" | "library" | "notify";

export const DIAG_TYPES: readonly DiagType[] = ["site", "external", "downloader", "library", "notify"];

export function diagType(kind: DiagnosticEpisode["kind"]): DiagType {
  return kind === "metadata" || kind === "update" ? "external" : kind;
}

export function diagTypeLabel(type: DiagType): string {
  return tryT(`log.diag.type${type.charAt(0).toUpperCase()}${type.slice(1)}`) ?? type;
}

/** Settings area behind a "settings" retry, as a router path. */
export function settingsPath(target: string, id: string): string | undefined {
  switch (target) {
    case "metadata_connection":
      return id ? `/settings/connections?connection=${encodeURIComponent(id)}` : "/settings/connections";
    case "downloader_connection":
      return "/settings/download";
    case "notification_target":
      return "/settings/notifications";
    default:
      return undefined;
  }
}

export function diagSentence(episode: DiagnosticEpisode): string {
  return tryT(`log.diag.${episode.kind}.${episode.variant}`, { name: episode.target.name, n: episode.affected?.count ?? 1 }) ?? t("log.diag.open");
}

/** What happens next: a per-condition phrase when the catalog has one, otherwise the retry mode's. */
export function diagRetryText(episode: DiagnosticEpisode): string {
  return tryT(`log.diag.retry.cond.${episode.kind}_${episode.variant}`) ?? tryT(`log.diag.retry.mode.${episode.retry.mode}`) ?? "";
}

export function diagTargetLabel(episode: DiagnosticEpisode): string {
  return joinMeta(tryT(`log.diag.target.${episode.kind}`), episode.target.name, episode.target.host);
}

/** Today shows the clock time, earlier days the short date and time. */
export function diagTime(value: string, now = new Date()): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  if (date.toDateString() === now.toDateString()) {
    return date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
  }
  return formatShortDateTime(value);
}

export function diagClassLabel(errorClass: string | undefined, status?: number): string | undefined {
  if (!errorClass) return undefined;
  const label = tryT(`log.diag.class.${errorClass}`) ?? t("log.diag.class.unknown");
  return errorClass === "http_status" && status ? `${label} ${status}` : label;
}

export function diagViaLabel(via: string | undefined): string | undefined {
  return via ? tryT(`log.diag.via.${via}`) : undefined;
}

/** How long the failures lasted, from the first to the last occurrence; empty for a single occurrence. */
export function diagSpan(episode: DiagnosticEpisode): string | undefined {
  if (episode.occurrence_count <= 1) return undefined;
  const seconds = Math.max(0, Math.round((Date.parse(episode.last_failed_at) - Date.parse(episode.first_failed_at)) / 1000));
  if (seconds < 60) return t("log.diag.withinSeconds", { n: Math.max(1, seconds) });
  if (seconds < 3600) return t("log.diag.withinMinutes", { n: Math.ceil(seconds / 60) });
  return t("log.diag.withinHours", { n: Math.ceil(seconds / 3600) });
}

export function isOpen(episode: DiagnosticEpisode): boolean {
  return !episode.succeeded_after_at;
}

/** Open episodes by last failure, newest first; recovered ones by recovery time, newest first. */
export function splitEpisodes(episodes: readonly DiagnosticEpisode[]): { open: DiagnosticEpisode[]; recovered: DiagnosticEpisode[] } {
  const time = (value: string) => Date.parse(value);
  const open = episodes.filter(isOpen).sort((a, b) => time(b.last_failed_at) - time(a.last_failed_at));
  const recovered = episodes
    .filter((episode) => !isOpen(episode))
    .sort((a, b) => time(b.succeeded_after_at ?? b.last_failed_at) - time(a.succeeded_after_at ?? a.last_failed_at));
  return { open, recovered };
}
