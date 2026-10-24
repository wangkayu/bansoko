import type { AnimeDraft } from "../../lib/api/client";
import { createEmptyDraft } from "../../lib/draft";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";

// Catalog entry -> zero-config AnimeDraft: title, metadata bindings and the RSS source come from
//   entry facts, while subgroups and rules copy the global values when the default settings load. A
//   custom candidate's origin_entry_ref must be null: the Candidate id is a separate resource id and
//   must never be written back into origin_entry_ref as a self-reference.

export function draftFromEntry(entry: CatalogEntryView): AnimeDraft {
  const draft = createEmptyDraft();
  const origin = entry.source;
  const match = entry.metadataMatch;
  draft.origin = origin;
  // A provider (anibt|mikan) keeps its providerEntryRef; custom must be null.
  draft.origin_entry_ref = origin === "anibt" || origin === "mikan" ? entry.providerEntryRef : null;
  draft.title = entry.title;
  draft.title_override = null;
  draft.instance_label = entry.instanceLabel ?? entry.title;
  draft.metadata = {
    bgm: match.bgmId ? { work_id: match.bgmId } : null,
    tmdb:
      match.tmdbWorkId && match.tmdbMediaKind && match.tmdbSeason !== undefined
        ? {
            work_id: match.tmdbWorkId,
            media_kind: match.tmdbMediaKind,
            season: match.tmdbSeason,
          }
        : null,
  };
  draft.subgroups = { max_rss: 3, entries: [] };

  if (entry.rssUrl && (origin === "custom" || entry.groupName)) {
    draft.rss_sources = [
      {
        source_kind: origin === "custom" ? "custom_rss" : "provider_group",
        locator: entry.rssUrl,
        locator_display: entry.rssUrl,
        group_name: entry.groupName ?? "",
        title: entry.title,
        episode_offset: 0,
      },
    ];
  } else if (origin !== "custom") {
    // Dynamic providers may be subscribed before group discovery returns any source.
    draft.rss_sources = [];
  }
  return draft;
}
