import type { ReactNode } from "react";
import type { ParsedReleaseFacts, RSSListFeedStatus, RSSListItem } from "../../lib/api/client";
import type { StatePillTone } from "../../ui";
import { t, type CopyKey } from "../../lib/i18n";
import { fileUnitsSummary, groupBoundSubtitles } from "../../lib/boundSubtitles";

/**
 * 统一 RSS 工作区的渲染模型。
 *
 * 两种来源（订阅前草稿 / 已订阅 owner）在事实层面本来就不同：草稿从「精确 provider 快照
 * + 求值」组合，owner 从「已持久化的 Release 链」读取。它们共享的是**层级与呈现规则**，
 * 不是字段：字幕组 → （可选分桶）→ 种子 → 文件。
 *
 * 因此这个模型固定层级，字段按来源可选；来源特有的深层信息通过 `detail` / `meta` /
 * `actions` 以节点形式注入，由各自的 adapter 负责，面板本身不做任何来源判定。
 */

/** An episode list's display: consecutive runs merge into ranges, zero-padded to two digits, e.g. 01-04, 01, 03, 07. */
export function episodesRangeLabel(episodes: readonly number[]): string {
  const sorted = [...new Set(episodes)].sort((a, b) => a - b);
  const pad = (n: number) => String(n).padStart(2, "0");
  const parts: string[] = [];
  for (let start = 0; start < sorted.length;) {
    let end = start;
    while (end + 1 < sorted.length && sorted[end + 1] === sorted[end] + 1) end++;
    parts.push(end > start ? `${pad(sorted[start])}-${pad(sorted[end])}` : pad(sorted[start]));
    start = end + 1;
  }
  return parts.join(", ");
}

/**
 * 种子文件列表的摘要：有文件行时按视频单元计数（视频 · 字幕 · 其他文件，见
 * fileUnitsSummary），没有文件行时退回种子结构给出的文件总数。
 */
export function manifestSummary(item: RSSListItem): string | undefined {
  if (item.files.length > 0) {
    const groups = groupBoundSubtitles(item.files, (file) => file.source_path, (file) => file.bound_video_source_path);
    return fileUnitsSummary(groups, (file) => ({ kind: file.media_kind, included: file.included, target: file.rendered_target }));
  }
  return item.torrent.manifest ? t("rss.manifest", { n: item.torrent.manifest.file_count }) : undefined;
}

/** A seed row's episode label: a collection shows the episodes planned for import, a single episode shows that one. */
export function releaseEpisodeLabel(facts: ParsedReleaseFacts | undefined | null): string | undefined {
  if (facts?.coverage?.episodes.length) return t("rss.episodes", { range: episodesRangeLabel(facts.coverage.episodes) });
  return facts?.episode == null ? undefined : t("rss.episode", { n: facts.episode });
}

export type RssFileModel = {
  key: string;
  path: string;
  sizeBytes?: number | null;
  mediaKind?: string;
  language?: string;
  /** Whether the subtitle language came from the file name or the release title (sub.lang_source). */
  languageSource?: "file" | "release";
  /** The server's binding: the video this subtitle follows (a source path within the same seed). */
  boundVideoPath?: string;
  /** Whether this file passed; stays true when the owner side has no per-file verdict, with the detail explaining. */
  included: boolean;
  reason?: ReactNode;
  /** The reason code when not passed (a nested subtitle's badge uses it to tell same-name-not-kept from filtered). */
  reasonCode?: string;
  /** The rendered target (or the frozen target path). */
  target?: string;
  /** Naming information empty in the target ("empty: year"); the target is still valid. */
  targetNote?: string;
  /** Why the target failed to render or validate. */
  targetError?: string;
  /** A note when the target cannot render yet (e.g. a naming variable not ready until after subscribing); not a failure. */
  targetPending?: string;
};

export type RssDownloadModel = {
  enabled: boolean;
  ariaLabel: string;
  hint: string;
  busy: boolean;
  onStart: () => void;
};

/**
 * 惰性种子的展开入口与它的展开态。存在这个模型就说明该行还没有文件列表：服务端把它判成
 * 未展开（pending），面板只渲染入口与状态，不渲染文件区。
 *
 * idle 显示入口按钮；loading 显示进行中；failed 显示错误文案与重试。展开失败不写缓存，
 * 重试就是再调一次，所以 failed 仍是同一个入口。
 */
export type RssExpandModel = {
  state: "idle" | "loading" | "failed";
  label: string;
  hint: string;
  /** The error copy when failed (the server's reason or a network failure note). */
  error?: string;
  onStart: () => void;
};

export type RssItemModel = {
  key: string;
  title: string;
  /** Compact facts between title and state (owner: quality / languages). */
  leadingMeta?: ReactNode;
  publishedAt?: string;
  sizeBytes?: number | null;
  episodeLabel?: string;
  /** The state badge; states that are not seed properties (waiting_metadata etc.) omit the badge. */
  status?: { label: string; tone: StatePillTone };
  /** The server judged this filtered; the "show filtered" toggle hides on this flag, never by comparing label text. */
  filtered?: boolean;
  /** The note line under the state (the verdict reason etc.). */
  statusDetail?: ReactNode;
  /** The seed's structure summary (manifest: name/version/size/parser). */
  manifestSummary?: string;
  /** The note shown when the structure is unavailable. */
  manifestUnavailable?: string;
  /** A lazy seed's expand entry; its presence means this row has no file list yet (the server judged it unexpanded). */
  expand?: RssExpandModel;
  /** The adapter requires this row open in this render (e.g. expanding the seed before a download click); after merging in, collapsing stays with the user. */
  open?: boolean;
  extras?: ReactNode;
  files: RssFileModel[];
  /** Source-specific deep nodes (owner: the state chain, target satisfaction, failure and retry, landing paths). */
  detail?: ReactNode;
  download?: RssDownloadModel;
  /** Top-level source-specific action nodes (owner: the processing chain, retry). */
  actions?: ReactNode;
};

export type RssGroupSection = {
  key: string;
  label: string;
  items: RssItemModel[];
};

export type RssGroupModel = {
  key: string;
  displayName: string;
  /** Compact facts such as the score. */
  scoreLabel?: string;
  /** Source-specific additions to the name row (owner: import progress, EP badges). */
  meta?: ReactNode;
  /** busy: the group list is being refreshed right now (pulsing dot). */
  feed: { label: string; tone: StatePillTone; busy?: boolean };
  feedDetail?: ReactNode;
  /** Source-specific group actions (owner: retry refresh, adjust offset). */
  actions?: ReactNode;
  /** The group's default refresh entry (draft: refresh that group's provider snapshot). */
  onRefresh?: () => void;
  refreshBusy?: boolean;
  /** Buckets: used when the owner buckets by episode; a single-group source leaves it empty and renders items directly. */
  sections?: RssGroupSection[];
  items: RssItemModel[];
  emptyText?: string;
};

export type RssWorkspaceModel = {
  groups: RssGroupModel[];
  emptyText?: string;
};

/**
 * 服务端种子列表状态的产品文案：draft 与 owner 共用同一份，禁止各自发明第二套词。
 * 状态本身永远来自服务端 `group.feed.state`，前端不重新判定新鲜度。
 */
const RSS_FEED_STATE_KEYS = {
  fresh: { labelKey: "rss.feed.fresh", tone: "online" },
  stale: { labelKey: "rss.feed.expired", tone: "warning" },
  missing: { labelKey: "rss.feed.expired", tone: "warning" },
  superseded: { labelKey: "rss.feed.expired", tone: "warning" },
  corrupt: { labelKey: "rss.feed.failed", tone: "offline" },
  refresh_required: { labelKey: "rss.feed.expired", tone: "warning" },
  snapshot_required: { labelKey: "rss.feed.expired", tone: "warning" },
} as const satisfies Record<RSSListFeedStatus["state"], { labelKey: CopyKey; tone: StatePillTone }>;

export function rssFeedState(state: RSSListFeedStatus["state"]): { label: string; tone: StatePillTone } {
  const entry = RSS_FEED_STATE_KEYS[state];
  return { label: t(entry.labelKey), tone: entry.tone };
}
