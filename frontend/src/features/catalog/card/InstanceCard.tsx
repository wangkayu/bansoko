import { AlertTriangle, CircleStop, Copy, Eraser, FolderOpen, LoaderCircle, MoreHorizontal, Pause, Play, ScrollText } from "lucide-react";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { DropdownMenu, type MenuEntry } from "../../../ui/base";
import { Poster } from "./Poster";
import { ProgressTrack } from "./ProgressTrack";
import { t } from "../../../lib/i18n";
import { labelValue } from "../../../lib/i18n/format";
import { lifecycleStateLabel, metadataHoldBlocking, subscriptionStateLabel } from "../../../lib/labels";
import { importedCount, progressDone, progressSummary } from "../../../lib/progress";
import { useRecordLink } from "../../../shell/recordLinkContext";
import type { RouteAnimeTab } from "../../../shell/routeDialogs";

// The subscribed poster card (after the card redesign): a 2:3 poster fills the card, leaving one
//   compact info strip at the bottom — the title (two lines at most; with a subscription note an
//   extra line beneath), the progress summary line (imported n/total, by count only) and the thin
//   progress bar; at phone widths the strip never takes more than the poster's bottom third.
//   - The whole card has exactly one open target: the poster's click area opens the config (retired
//     is the same workspace's read-only mode); the face no longer has a separate configure button.
//   - The top-left state pill appears only when attention is needed: not yet started / stopped /
//     retired, an RSS warning (clicking goes to the RSS tab) or a metadata hold; while running no
//     pill appears.
//   - The top-right menu: start subscription (only when not yet started) / stop, start / duplicate /
//     execution records / files / unsubscribe. Retired keeps execution records, files (read-only) and
//     delete local records (read-only viewing goes through the poster click and is not repeated in
//     the menu). Files opens the per-anime files dialog (a standalone dialog, not a tab inside the
//     config dialog). Shown on desktop hover/focus, persistent on touch, and kept visible while the
//     menu is open.
//   - Bangumi / TMDB bindings, the original name and external links moved to the config dialog's
//     title area and no longer show on the face.
// Hiding is a browse-area curation action and never appears on an instance card. A subscribed
//   instance offers no one-shot browse entry: browse seeds lives only on the catalog/search candidate
//   cards, independent of subscription state.

export function InstanceCard({
  entry,
  busy,
  onOpenConfig,
  onToggleState,
  onDuplicate,
  onUnsubscribe,
  onCleanupLocalData,
  onOpenFiles,
}: {
  entry: CatalogEntryView;
  busy?: boolean;
  /** tab defaults to the section's default tab; the RSS warning pill passes "rss". */
  onOpenConfig: (entry: CatalogEntryView, tab?: RouteAnimeTab) => void;
  /** The stop/start bidirectional toggle; when not yet started it becomes start subscription. Not passed means not rendered. */
  onToggleState?: (entry: CatalogEntryView) => void;
  /** Duplicates into a new not-yet-started subscription (retired offers none); not passed means not rendered. */
  onDuplicate?: (entry: CatalogEntryView) => void;
  /** Unsubscribes (the irreversible lifecycle boundary); not passed means not rendered. */
  onUnsubscribe?: (entry: CatalogEntryView) => void;
  /** Deletes a retired subscription's local records (no qB data, no media files); not passed means not rendered. */
  onCleanupLocalData?: (entry: CatalogEntryView) => void;
  /** Opens the per-anime files dialog (read-only when retired); not passed means not rendered. */
  onOpenFiles?: (entry: CatalogEntryView) => void;
}) {
  // Execution records is a route-carried global dialog; outside the shell (isolated component tests) no opener exists and the item does not render.
  const openRecord = useRecordLink();
  const retired = entry.lifecycleState === "retired";
  const stopped = entry.subscriptionState === "stopped";
  const pending = entry.subscriptionState === "pending";
  const rssBad = !retired && (entry.rssHealth === "failed" || entry.rssHealth === "degraded");
  const rssLabel = entry.rssHealth === "failed" ? t("grid.card.rssFailed") : t("grid.card.rssDegraded");
  const rssShort = entry.rssHealth === "failed" ? t("grid.card.rssFailedShort") : t("grid.card.rssDegradedShort");
  const rssAria = entry.rssHealthReason ? labelValue(rssLabel, entry.rssHealthReason) : rssLabel;
  // The blocking reason is a single short chip (TMDB not connected); while running the hover grows
//   into the long form ("automatic downloads are paused: …"); not-yet-started / stopped never
//   download anyway, so their hover stays short. Retired is read-only and no longer hinted.
  const blocking = retired ? undefined : metadataHoldBlocking(entry.metadataHold);
  const held = blocking !== undefined;
  // The same workspace with two entry labels: active is the config-editing surface, retired the read-only viewing one.
  const openLabel = retired
    ? t("grid.card.viewFor", { title: entry.title })
    : t("grid.card.openConfig", { title: entry.title });

  const progress = entry.episodeProgress;
  const hasEpisodes = importedCount(progress) > 0;
  const summary = progressSummary(progress);
  const shortSummary = progressSummary(progress, true);
  const done = progressDone(progress);

  // The menu items (tailored to the instance's capabilities): start subscription (not yet started) / stop, start -> duplicate -> execution records -> files -> unsubscribe / delete local records.
  const managedAnimeID = entry.managedAnimeRef;
  const menuItems: MenuEntry[] = [
    ...(onToggleState && !retired
      ? [
          {
            key: "toggle-state",
            icon: busy ? (
              <LoaderCircle className="catalog-pw-menu-icon" />
            ) : stopped || pending ? (
              <Play className="catalog-pw-menu-icon" />
            ) : (
              <Pause className="catalog-pw-menu-icon" />
            ),
            label: pending ? t("grid.card.begin") : stopped ? t("grid.card.start") : t("grid.card.pause"),
            disabled: busy,
            onSelect: () => onToggleState(entry),
          },
        ]
      : []),
    ...(onDuplicate && !retired
      ? [
          {
            key: "duplicate",
            icon: <Copy className="catalog-pw-menu-icon" />,
            label: t("grid.card.duplicate"),
            disabled: busy,
            onSelect: () => onDuplicate(entry),
          },
        ]
      : []),
    ...(openRecord && managedAnimeID
      ? [
          {
            key: "records",
            icon: <ScrollText className="catalog-pw-menu-icon" />,
            label: t("records.title"),
            onSelect: () => openRecord({ managedAnimeID }),
          },
        ]
      : []),
    ...(onOpenFiles && managedAnimeID
      ? [
          {
            key: "files",
            icon: <FolderOpen className="catalog-pw-menu-icon" />,
            label: t("grid.card.files"),
            onSelect: () => onOpenFiles(entry),
          },
        ]
      : []),
    ...(onUnsubscribe && !retired
      ? [
          {
            key: "unsubscribe",
            danger: true,
            icon: <CircleStop className="catalog-pw-menu-icon" />,
            label: t("catalog.endConfirm"),
            disabled: busy,
            onSelect: () => onUnsubscribe(entry),
          },
        ]
      : []),
    ...(retired && onCleanupLocalData
      ? [
          {
            key: "cleanup",
            danger: true,
            icon: <Eraser className="catalog-pw-menu-icon" />,
            label: t("catalog.deleteLocal"),
            disabled: busy,
            onSelect: () => onCleanupLocalData(entry),
          },
        ]
      : []),
  ];

  return (
    <article className={"catalog-pw" + (stopped ? " catalog-pw-stopped" : "") + (retired ? " catalog-pw-retired" : "")}>
      <Poster entry={entry} scrim />

      {/* The only open target: the whole poster is open-config (retired is the same workspace's
          read-only mode). The info strip is a pointer-transparent display layer; the hover tip here
          carries the truncated full name. */}
      <button type="button" aria-label={openLabel} title={entry.title} onClick={() => onOpenConfig(entry)} className="catalog-pw-open" />

      {/* The top-left state pill: only when attention is needed. The RSS warning is a button straight to the config's RSS tab. */}
      {(retired || stopped || pending || rssBad || held) && (
        <div className="catalog-pw-status">
          {retired ? (
            <span className="catalog-pw-chip catalog-pw-chip-muted">{lifecycleStateLabel("retired")}</span>
          ) : stopped ? (
            <span className="catalog-pw-chip catalog-pw-chip-muted">{subscriptionStateLabel("stopped")}</span>
          ) : pending ? (
            <span className="catalog-pw-chip catalog-pw-chip-muted">{subscriptionStateLabel("pending")}</span>
          ) : null}
          {rssBad && (
            <button type="button" title={rssAria} aria-label={rssAria} onClick={() => onOpenConfig(entry, "rss")} className="catalog-pw-chip catalog-pw-chip-alert">
              <AlertTriangle className="catalog-pw-chip-icon" aria-hidden />
              {rssShort}
            </button>
          )}
          {blocking && (
            <span
              className="catalog-pw-chip catalog-pw-chip-hold"
              title={entry.subscriptionState === "active" ? blocking.long : blocking.short}
            >
              {blocking.short}
            </span>
          )}
        </div>
      )}

      {menuItems.length > 0 && (
        <DropdownMenu
          items={menuItems}
          trigger={
            <button type="button" aria-label={t("grid.card.moreFor", { title: entry.title })} title={t("grid.card.more")} className="catalog-pw-more">
              <MoreHorizontal aria-hidden className="catalog-pw-more-icon" />
            </button>
          }
        />
      )}

      {/* The bottom info strip: title / progress summary / thin progress bar (the broadcast day comes from the wall's row label). Pure display, pointer-transparent to the click area. */}
      <div className="catalog-pw-overlay">
        <h3 className="catalog-pw-title-text" title={entry.title}>
          {entry.title}
        </h3>
        {/* The subscription note: distinguishes the cards of several subscriptions to one anime; not shown when it equals the title (nothing filled in). */}
        {entry.instanceLabel && entry.instanceLabel !== entry.title && (
          <p className="catalog-pw-label text-caption" title={entry.instanceLabel}>
            {entry.instanceLabel}
          </p>
        )}
        <p className="catalog-pw-meta text-caption">
          <span
            className={
              "catalog-pw-progress-text" +
              (!hasEpisodes ? " catalog-pw-progress-text-empty" : done ? " catalog-pw-progress-text-done" : "")
            }
            title={summary}
          >
            {/* Two summary variants switch by card width (index.css container queries): a narrow card shows the short summary with the prefix dropped. */}
            {shortSummary === summary ? (
              summary
            ) : (
              <>
                <span className="catalog-pw-progress-full">{summary}</span>
                <span className="catalog-pw-progress-short">{shortSummary}</span>
              </>
            )}
          </span>
        </p>
        <ProgressTrack entry={entry} />
      </div>
    </article>
  );
}
