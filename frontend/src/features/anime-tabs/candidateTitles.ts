import type { AnimeMetadataDraft, TitleSuggestion } from "../../lib/api/client";
import type { MetadataCandidate } from "../../lib/metadata/types";

// The workspace read lists the titles of the bound works, but only those already cached for
// them: a metadata PATCH writes the binding without caching its titles, so a work just matched
// here would offer nothing until something else caches them. The candidate picked in the match
// dialog already carries its titles, so the tabs keep them keyed by the binding and offer them
// while that binding stays in effect. Nothing is requested; a candidate that carries no title
// (a manually entered TMDB ID, an upstream match without one) adds nothing.

/** The titles a picked candidate carries, under the source names the read model uses. */
export function candidateTitleSuggestions(candidate: MetadataCandidate): TitleSuggestion[] {
  if (candidate.provider === "tmdb" && candidate.detailsStatus === "not_applicable") return [];
  const [cnSource, originalSource] =
    candidate.provider === "tmdb" ? (["tmdb_cn", "tmdb_original"] as const) : (["bgm_cn", "bgm"] as const);
  const titles: TitleSuggestion[] = [];
  const cn = candidate.title.trim();
  if (cn) titles.push({ source: cnSource, text: cn });
  const original = candidate.originalTitle?.trim();
  if (original) titles.push({ source: originalSource, text: original });
  return titles;
}

/** The kept titles whose binding is still in the value; clearing or replacing a binding drops its batch. */
export function boundTitleSuggestions(
  matched: Readonly<Record<string, TitleSuggestion[]>>,
  metadata: AnimeMetadataDraft,
): TitleSuggestion[] {
  const bindings = [
    metadata.bgm ? `bgm:${metadata.bgm.work_id}` : undefined,
    metadata.tmdb ? `tmdb:${metadata.tmdb.work_id}` : undefined,
  ];
  return bindings.flatMap((key) => (key ? (matched[key] ?? []) : []));
}
