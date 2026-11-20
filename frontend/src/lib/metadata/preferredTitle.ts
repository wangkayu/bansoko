import { t } from "../i18n";
import type { components } from "../api/generated";
import type { MetadataCandidate } from "./types";

type Source = components["schemas"]["SubscriptionTitle"]["source"];
const sources = ["keep", "tmdb_cn", "tmdb_original", "bgm_cn", "bgm"] as const satisfies readonly Source[];

export function preferredTitleSource(raw: unknown): Source {
  return sources.find((source) => source === raw) ?? "keep";
}

export function preferredTitleOptions() {
  return sources.map((value) => ({ value, label: t(`subscriptionDefaults.preferredTitle.${value}`) }));
}

export type MatchTitleOffer = { current: string; initial: boolean; source: Source };

export function matchTitleChoice(candidate: MetadataCandidate, offer: MatchTitleOffer) {
  const localized = candidate.titleCN?.trim() ?? "";
  const original = candidate.originalTitle?.trim() ?? "";
  const preferred = candidate.provider === "tmdb"
    ? offer.source === "tmdb_cn" ? localized : offer.source === "tmdb_original" ? original : ""
    : offer.source === "bgm_cn" ? localized : offer.source === "bgm" ? original : "";
  const manualID = candidate.provider === "tmdb" && candidate.detailsStatus === "not_applicable" && candidate.mediaKind !== "movie";
  const title = manualID ? "" : preferred || candidate.title.trim() || original;
  return { title, checked: !manualID && offer.initial && preferred !== "" && preferred !== offer.current.trim() };
}
