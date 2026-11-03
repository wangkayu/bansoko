import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Dialog, StateBlock } from "../../ui/base";
import { readProviderEntrySynopsis } from "../../lib/api/client";
import { catalogKeys } from "../../lib/queryKeys";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { broadcastDayLabel } from "../../lib/i18n/broadcastDay";
import { t } from "../../lib/i18n";
import { Poster } from "./card/Poster";
import { seasonLabel } from "./season";
import { splitSynopsis } from "./synopsisText";
import { ExternalValue } from "./card/ExternalValue";

// The synopsis reader (design B): the browse/search card's top-right book entry opens this
// dialog without opening the anime config. The source page is read only now, never while the
// list loads, and each entry keeps its last answer in the query cache. The header carries a
// small poster, the title, the source-stated facts and tags; the body is one reading column.
// A page without a synopsis is a normal empty state. When the whole text embeds a [简介原文]
// marker with content on both sides, it splits into 「简介 / 简介原文」 tabs; any other shape is
// one block.

// Match the backend's per-entry TTL: reopening a dialog keeps its last answer.
const SYNOPSIS_CACHE_MS = 10 * 60 * 1000;

function safeExternalHref(href: string | null | undefined): string | undefined {
  if (!href) return undefined;
  try {
    const parsed = new URL(href);
    return parsed.protocol === "https:" || parsed.protocol === "http:" ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

function formatLabel(format: CatalogEntryView["releaseFormat"]): string | undefined {
  switch (format) {
    case "tv":
      return t("grid.card.synopsis.formatTv");
    case "movie":
      return t("grid.card.synopsis.formatMovie");
    case "ova":
      return t("grid.card.synopsis.formatOva");
    case "ona":
      return t("grid.card.synopsis.formatOna");
    default:
      return undefined;
  }
}

function SynopsisSkeleton() {
  return (
    <div className="synopsis-skeleton" role="status" aria-label={t("grid.card.synopsis.loading")}>
      <span />
      <span />
      <span />
      <span />
      <span />
    </div>
  );
}

export function SynopsisDialog({
  entry,
  provider,
  onRequestClose,
}: {
  entry: CatalogEntryView;
  provider: "anibt" | "mikan";
  onRequestClose: () => void;
}) {
  const query = useQuery({
    queryKey: catalogKeys.synopsis(provider, entry.providerEntryRef),
    queryFn: () => readProviderEntrySynopsis(provider, entry.providerEntryRef),
    staleTime: SYNOPSIS_CACHE_MS,
    gcTime: SYNOPSIS_CACHE_MS,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [tab, setTab] = useState<"summary" | "original">("summary");
  const data = query.data;
  const split = splitSynopsis(data?.synopsis);
  const chips: string[] = [];
  if (provider === "mikan") {
    if (data?.weekday) chips.push(broadcastDayLabel(data.weekday));
    if (data?.episodes !== null && data?.episodes !== undefined) {
      chips.push(t("grid.card.synopsis.episodes", { n: data.episodes }));
    }
  } else {
    const format = formatLabel(entry.releaseFormat);
    if (format) chips.push(format);
    const season = data?.season ? seasonLabel(data.season) : undefined;
    if (season) chips.push(season);
    if (data?.episodes !== null && data?.episodes !== undefined) {
      chips.push(t("grid.card.synopsis.episodes", { n: data.episodes }));
    }
    if (entry.score !== undefined) {
      chips.push(t("grid.card.synopsis.score", { score: entry.score.toFixed(1) }));
    }
  }
  const tags = provider === "anibt" ? data?.tags ?? [] : [];
  const officialSite = safeExternalHref(data?.official_site);
  const bangumi = safeExternalHref(data?.bgm_url);
  const sourceHref = safeExternalHref(data?.source_url ?? entry.sourceUrl);
  const sourceSite = provider === "mikan" ? "Mikan" : "AniBT";

  return (
    <Dialog
      open
      onRequestClose={onRequestClose}
      tier="M"
      reader
      focusTitle
      title={<span className="synopsis-title">{entry.title}</span>}
      headerStart={
        <div className="synopsis-poster">
          <Poster entry={entry} />
        </div>
      }
      description={
        <span className="synopsis-head">
          {provider === "anibt" && entry.originalTitle ? (
            <span className="synopsis-original" title={entry.originalTitle}>
              {entry.originalTitle}
            </span>
          ) : null}
          {chips.length > 0 ? (
            <span className="synopsis-chips">
              {chips.map((chip) => (
                <span key={chip} className="synopsis-chip">
                  {chip}
                </span>
              ))}
            </span>
          ) : null}
          {tags.length > 0 ? (
            <span className="synopsis-tags">
              {tags.map((tag) => (
                <span key={tag} className="synopsis-tag">
                  {tag}
                </span>
              ))}
            </span>
          ) : null}
          {officialSite || bangumi ? (
            <span className="synopsis-links">
              {officialSite ? <ExternalValue href={officialSite}>{t("grid.card.synopsis.officialSite")}</ExternalValue> : null}
              {bangumi ? <ExternalValue href={bangumi}>{t("grid.card.synopsis.bangumi")}</ExternalValue> : null}
            </span>
          ) : null}
        </span>
      }
      bodyClassName="synopsis-body"
      footer={
        <>
          {sourceHref ? (
            <a href={sourceHref} target="_blank" rel="noopener noreferrer" className="synopsis-source">
              {t("grid.card.synopsis.sourceLink", { site: sourceSite })}
            </a>
          ) : null}
          <Button variant="secondary" size="sm" onClick={onRequestClose}>
            {t("ui.close")}
          </Button>
        </>
      }
    >
      {query.isPending ? (
        <SynopsisSkeleton />
      ) : query.isError ? (
        <StateBlock
          kind="error"
          title={t("grid.card.synopsis.failed")}
          action={
            <Button size="sm" onClick={() => void query.refetch()}>
              {t("grid.card.synopsis.retry")}
            </Button>
          }
        />
      ) : !data?.synopsis ? (
        <StateBlock
          kind="empty"
          title={t("grid.card.synopsis.empty")}
          description={t("grid.card.synopsis.emptyHint")}
        />
      ) : split ? (
        <div>
          <div className="synopsis-tabs" role="tablist" aria-label={t("grid.card.synopsis.summary")}>
            <button
              type="button"
              role="tab"
              aria-selected={tab === "summary"}
              className={"synopsis-tab" + (tab === "summary" ? " synopsis-tab-active" : "")}
              onClick={() => setTab("summary")}
            >
              {t("grid.card.synopsis.summary")}
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={tab === "original"}
              className={"synopsis-tab" + (tab === "original" ? " synopsis-tab-active" : "")}
              onClick={() => setTab("original")}
            >
              {t("grid.card.synopsis.original")}
            </button>
          </div>
          <div className="synopsis-text" role="tabpanel">
            {tab === "summary" ? split.summary : split.original}
          </div>
        </div>
      ) : (
        <div className="synopsis-text">{data.synopsis}</div>
      )}
    </Dialog>
  );
}
