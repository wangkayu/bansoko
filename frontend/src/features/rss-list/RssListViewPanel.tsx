import { useEffect, useState, type ReactNode } from "react";
import type { RssExpandModel, RssFileModel, RssGroupModel, RssItemModel, RssWorkspaceModel } from "./model";
import { ChevronDown, ChevronRight, Download, Loader2 } from "lucide-react";
import { formatBytes, formatDateTime } from "../../lib/labels";
import { StatePill, Button } from "../../ui";
import { FilteredWithReason } from "../../ui/ReasonText";
import { BoundSubtitles, type BoundSubtitleItem } from "../../ui/BoundSubtitles";
import { groupBoundSubtitles } from "../../lib/boundSubtitles";
import { t, type CopyKey } from "../../lib/i18n";

// The unified RSS workspace's sole renderer: subgroups → (optional buckets) → seeds → files.
//
// The panel knows nothing of draft/subscribed: state labels, download admission reasons and
// source-specific details all come from the adapter. It handles only hierarchy, expansion state,
// density and accessibility (the single downloadable icon, real aria-controls for expandables).

const MEDIA_KIND_LABEL_KEYS: Record<string, CopyKey> = {
  video: "rss.mediaKind.video",
  subtitle: "rss.mediaKind.subtitle",
  other: "rss.mediaKind.other",
};

function mediaKindLabel(kind: string): string {
  const key = MEDIA_KIND_LABEL_KEYS[kind];
  return key ? t(key) : kind;
}

/** One subtitle in the nested list: the same facts as a video row, just hung beneath the video. */
function subtitleItem(file: RssFileModel): BoundSubtitleItem {
  return {
    key: file.key,
    path: file.path,
    sizeBytes: file.sizeBytes,
    language: file.language,
    languageSource: file.languageSource,
    included: file.included,
    reasonCode: file.reasonCode,
    target: file.target,
    targetNote: file.targetNote,
    targetProblem: file.targetError ? t("rss.nameFailed", { message: file.targetError }) : file.targetPending,
  };
}

/** One file of a seed; beneath a video row are the external subtitles the server bound to it: a format badge, details collapsed by default. */
function FileRow({ file, subtitles }: { file: RssFileModel; subtitles: RssFileModel[] }) {
  return (
    <li className="seed-file-row">
      <p className="seed-row-title text-caption" title={file.path}>
        {file.path}
      </p>
      <p className="seed-row-meta seed-row-meta-nowrap text-caption">
        {file.sizeBytes !== undefined && file.sizeBytes !== null
          ? `${formatBytes(file.sizeBytes)} · `
          : ""}
        {file.mediaKind ? mediaKindLabel(file.mediaKind) : ""}
        {file.language ? ` · ${file.language}` : ""}
        {file.included ? null : <> · <FilteredWithReason reason={file.reason} /></>}
      </p>
      {file.target && (
        <p className="seed-row-meta text-caption" title={file.target}>
          → {file.target}
        </p>
      )}
      {file.targetNote && (
        <p className="seed-row-meta text-caption text-muted">{file.targetNote}</p>
      )}
      {file.targetError && (
        <p className="seed-row-meta text-caption text-danger">
          {t("rss.nameFailed", { message: file.targetError })}
        </p>
      )}
      {file.targetPending && (
        <p className="seed-row-meta text-caption">{file.targetPending}</p>
      )}
      <BoundSubtitles videoName={file.path} subtitles={subtitles.map(subtitleItem)} videoExcluded={!file.included} />
    </li>
  );
}

/** A lazy seed's expand area: the entry button plus expansion state. The file list renders from this row only after a successful expand and re-read. */
function ExpandPanel({ expand }: { expand: RssExpandModel }) {
  return (
    <div className="seed-group-panel-row">
      <p className="seed-preview-hint text-caption">{expand.hint}</p>
      <Button size="sm" variant="secondary" disabled={expand.state === "loading"} onClick={expand.onStart}>
        {expand.state === "loading" && <Loader2 className="icon-sm-fixed icon-spin" />}
        {expand.label}
      </Button>
      {expand.error && (
        <p role="alert" className="seed-preview-hint text-caption text-danger">
          {expand.error}
        </p>
      )}
    </div>
  );
}

/** All rows of the model: taken by bucket when buckets exist, matching GroupBody's rendering terms. */
function allItems(model: RssWorkspaceModel): RssItemModel[] {
  return model.groups.flatMap((group) =>
    group.sections && group.sections.length > 0 ? group.sections.flatMap((section) => section.items) : group.items,
  );
}

function ItemRow({
  item,
  expanded,
  onToggle,
}: {
  item: RssItemModel;
  expanded: boolean;
  onToggle: () => void;
}) {
  // Episodes, publish time and size sit on the second row's right; missing items show no placeholder text.
  const stats = [
    item.episodeLabel,
    item.publishedAt ? formatDateTime(item.publishedAt) : undefined,
    item.sizeBytes !== undefined && item.sizeBytes !== null ? formatBytes(item.sizeBytes) : undefined,
  ].filter((part): part is string => !!part);
  const panelID = `rss-item-panel-${item.key.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  return (
    <li className="seed-row">
      {/* First row: the title owns the line; second row: tags left, episodes·time·size with state and actions right. */}
      <button
        type="button"
        className="seed-item-toggle"
        aria-expanded={expanded}
        aria-controls={panelID}
        aria-label={expanded ? t("rss.collapseFiles") : t("rss.expandFiles")}
        onClick={onToggle}
      >
        {expanded ? (
          <ChevronDown className="icon-sm-fixed" />
        ) : (
          <ChevronRight className="icon-sm-fixed" />
        )}
        <span className="seed-row-title text-caption" title={item.title}>
          {item.title}
        </span>
      </button>
      <div className="seed-row-facts">
        <div className="seed-row-tags">{item.leadingMeta}</div>
        <span className="seed-row-side">
          {stats.length > 0 && (
            <span className="seed-row-stats text-caption">
              {stats.map((part, index) => (
                <span key={index} className={index === 0 && item.episodeLabel ? "seed-row-ep" : undefined}>
                  {part}
                </span>
              ))}
            </span>
          )}
          {item.status && <StatePill tone={item.status.tone}>{item.status.label}</StatePill>}
          {item.actions}
          {item.download && (
            <Button
              size="sm"
              variant="ghost"
              aria-label={item.download.ariaLabel}
              title={item.download.hint}
              disabled={!item.download.enabled || item.download.busy}
              onClick={item.download.onStart}
            >
              {item.download.busy ? (
                <Loader2 className="icon-sm-fixed icon-spin" />
              ) : (
                <Download className="icon-sm-fixed" />
              )}
            </Button>
          )}
        </span>
      </div>

      {item.statusDetail && <p className="seed-preview-hint text-caption">{item.statusDetail}</p>}
      {item.extras}
      {item.download && !item.download.enabled && (
        <p className="seed-preview-hint text-caption" data-download-blocked="true">
          {t("rss.downloadBlocked", { hint: item.download.hint })}
        </p>
      )}

      {expanded && (
        <div id={panelID} className="seed-group-panel">
          {item.expand ? <ExpandPanel expand={item.expand} /> : (item.detail ?? (
            <>
              {item.manifestSummary ? (
                <p className="seed-preview-hint text-caption">{item.manifestSummary}</p>
              ) : (
                <p className="seed-preview-hint text-caption">
                  {item.manifestUnavailable ?? t("rss.manifestMissing")}
                </p>
              )}
              {item.files.length > 0 && (
                <ul className="seed-list">
                  {groupBoundSubtitles(item.files, (file) => file.path, (file) => file.boundVideoPath).map(({ file, subtitles }) => (
                    <FileRow key={file.key} file={file} subtitles={subtitles} />
                  ))}
                </ul>
              )}
            </>
          ))}
        </div>
      )}
    </li>
  );
}

function ItemList({
  items,
  expandedItems,
  onToggleItem,
}: {
  items: RssItemModel[];
  expandedItems: Set<string>;
  onToggleItem: (key: string) => void;
}) {
  return (
    <ul className="seed-list">
      {items.map((item) => (
        <ItemRow
          key={item.key}
          item={item}
          expanded={expandedItems.has(item.key)}
          onToggle={() => onToggleItem(item.key)}
        />
      ))}
    </ul>
  );
}

function GroupBody({
  group,
  showFiltered,
  expandedItems,
  onToggleItem,
}: {
  group: RssGroupModel;
  showFiltered: boolean;
  expandedItems: Set<string>;
  onToggleItem: (key: string) => void;
}) {
  const visible = (items: RssItemModel[]) => items.filter((item) => showFiltered || !item.filtered);
  if (group.sections && group.sections.length > 0) {
    return (
      <>
        {group.sections.map((section) => {
          const items = visible(section.items);
          if (items.length === 0) return null;
          return (
            <div key={section.key} className="seed-section">
              <h3 className="seed-section-label text-badge">{section.label}</h3>
              <ItemList items={items} expandedItems={expandedItems} onToggleItem={onToggleItem} />
            </div>
          );
        })}
      </>
    );
  }
  const items = visible(group.items);
  if (items.length === 0) {
    return (
      <p className="seed-preview-hint text-caption">
        {group.items.length === 0
          ? (group.emptyText ?? t("rss.emptyGroup"))
          : t("rss.noMatchFiltered")}
      </p>
    );
  }
  return <ItemList items={items} expandedItems={expandedItems} onToggleItem={onToggleItem} />;
}

export function RssListViewPanel({
  model,
  showFiltered,
  toolbar,
  badge,
  notice,
}: {
  model: RssWorkspaceModel;
  showFiltered: boolean;
  /** Caller-owned controls and actions; capability/preview policy stays with the adapter. */
  toolbar?: ReactNode;
  /** Caller-owned mode/status indicator shown alongside the toolbar. */
  badge?: ReactNode;
  /** Caller-owned notices shown above the RSS groups. */
  notice?: ReactNode;
}) {
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set());
  const [expandedItems, setExpandedItems] = useState<Set<string>>(() => new Set());

  // The adapter may require a row to open (expanding the seed before a download click). The request
//   merges in only when it itself changes; collapsing stays with the user afterwards — a controlled
//   toggle would leave the collapse button dead.
  const openSeedKeys = allItems(model)
    .filter((item) => item.open)
    .map((item) => item.key)
    .sort()
    .join("\u0000");
  useEffect(() => {
    if (openSeedKeys === "") return;
    setExpandedItems((prev) => {
      const next = new Set(prev);
      for (const key of openSeedKeys.split("\u0000")) next.add(key);
      return next.size === prev.size ? prev : next;
    });
  }, [openSeedKeys]);

  function toggleGroup(key: string) {
    setExpandedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function toggleItem(key: string) {
    setExpandedItems((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  return (
    <>
      {(toolbar || badge) && (
        <div className="seed-group-panel-row">
          {toolbar}
          {badge}
        </div>
      )}
      {notice}
      {model.groups.length === 0 ? (
        <p className="seed-preview-hint text-caption">
          {model.emptyText ?? t("rss.emptyNoGroups")}
        </p>
      ) : (
        <ul className="seed-group-list">
      {model.groups.map((group, index) => {
        const open = expandedGroups.has(group.key);
        const panelID = `rss-group-panel-${index}`;
        return (
          <li key={group.key} className="seed-group">
            {/* The group header and the source actions are siblings: the action buttons must not nest inside the expand button (nested buttons are invalid
                且会让内层控件不可达）。 */}
            <div className="seed-group-header-row">
              <button
                type="button"
                className="seed-group-header"
                aria-expanded={open}
                aria-controls={panelID}
                aria-label={open ? t("rss.collapseGroup", { name: group.displayName }) : t("rss.expandGroup", { name: group.displayName })}
                onClick={() => toggleGroup(group.key)}
              >
                {open ? (
                  <ChevronDown className="icon-sm-fixed" />
                ) : (
                  <ChevronRight className="icon-sm-fixed" />
                )}
                <span className="seed-group-name text-body">{group.displayName}</span>
                {group.meta}
                {group.feedDetail && (
                  <span className="seed-preview-hint text-caption">{group.feedDetail}</span>
                )}
                <StatePill tone={group.feed.tone} busy={group.feed.busy} className="tw:shrink-0 tw:whitespace-nowrap">{group.feed.label}</StatePill>
              </button>
              {group.actions}
            </div>
            {open && (
              <div id={panelID} className="seed-group-panel">
                {group.onRefresh && (
                  <div className="seed-group-panel-row">
                    <p className="seed-preview-hint text-caption">
                      {t("rss.groupRefreshHint")}
                    </p>
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={group.refreshBusy}
                      onClick={group.onRefresh}
                    >
                      {group.refreshBusy ? t("act.refreshing") : t("rss.groupRefresh")}
                    </Button>
                  </div>
                )}
                <GroupBody
                  group={group}
                  showFiltered={showFiltered}
                  expandedItems={expandedItems}
                  onToggleItem={toggleItem}
                />
              </div>
            )}
          </li>
        );
        })}
        </ul>
      )}
    </>
  );
}
