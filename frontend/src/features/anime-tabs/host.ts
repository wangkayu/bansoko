import type { ReactNode } from "react";
import type {
  AnimeDraft,
  AnimeWorkspace,
  BusinessPresetView,
  CustomSnapshotFetchInput,
  PreviewFeedSnapshotRef,
  ProviderGroupDiscoveryGroup,
  RSSListObservationRef,
} from "../../lib/api/client";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import type { WorkspaceCapability } from "../workspace/capability";

// The sole parameter surface of an anime's four tabs (metadata/subgroups/rules/RSS): before
// subscribing (a draft, the in-memory draft saved as a whole candidate then activated) and after
// (an owner, each surface saving with its own expected_version CAS) share one tab implementation.
// The host discriminated union is the two modes' only difference — the save path and the
// mode-specific actions.

export type DraftTabHost = {
  mode: "draft";
  draft: AnimeDraft;
  /** The last saved draft: tab names' unsaved marks are computed against it; unmarked when absent. */
  savedDraft?: AnimeDraft;
  onDraftChange: (next: AnimeDraft) => void;
  /**
   * 草稿是否已具备组合读条件（纯客户端判定，例如已填写标题）。
   * 读取进度由 RSS 工作区自己持有，不在 host 上重复一份。
   */
  previewable: boolean;
  /** The read-only subgroup discovery entry of a provider entry; custom has no provider entry to discover and omits it. */
  subgroupDiscovery?: { provider: "anibt" | "mikan"; providerEntryRef: string };
  /**
   * custom 草稿测试台的源快照主体：已保存的预设修订（来源 URL 由服务端从修订读）。
   * 草稿未保存、或未保存的修改改了来源时缺省：测试台列出该组但不抓取。
   */
  customSnapshot?: CustomSnapshotFetchInput;
  /**
   * 统一 RSS 工作区当前可用的精确 feed 快照身份（逐组）。它是草稿读模型的输入：
   * 只有这些持久化快照存在时，工作区才渲染种子行，行级下载才引用稳定身份而不是
   * live fetch。
   */
  feedSnapshots: PreviewFeedSnapshotRef[];
  /** The snapshot-identity backfill after each workspace read/refresh (clearing included). */
  onFeedSnapshotsChange: (refs: PreviewFeedSnapshotRef[]) => void;
  /**
   * 最近一次字幕组发现的结果。发现是跨 tab 的事实：字幕组 tab 与 RSS tab 都能
   * 发起它，字幕组 tab 显示发现到的组，RSS 工作区用观察身份列出草稿里尚未配置的
   * 字幕组。它不写入草稿。
   */
  observation?: RSSListObservationRef;
  observedGroups: ProviderGroupDiscoveryGroup[];
  onDiscovered: (groups: ProviderGroupDiscoveryGroup[], observation: RSSListObservationRef) => void;
  /** The business configuration at the rules tab's end (the replacement policy etc.): it belongs to the rules and must not float outside the tabs. */
  rulesExtra?: ReactNode;
  /** The whole-page action at the rules tab's top right ("use defaults everywhere"). */
  rulesHeaderAction?: ReactNode;
  /** The current default settings (the rules section cards' "modified" marks compare against it); omitted while loading or on failure, so nothing is marked. */
  businessPreset?: BusinessPresetView;
};

export type OwnerTabHost = {
  mode: "owner";
  animeID: string;
  workspace: AnimeWorkspace;
  /**
   * 本工作区的 UI 能力权威。retired owner 打开同一 workspace 时这里是只读模式：
   * 历史（Release/DownloadTask/Artifact/FileOperation/执行记录/log）照常可读，所有
   * 配置保存与 refresh/download/retry mutation 由 capability.can(...) 统一关闭。
   */
  capability: WorkspaceCapability;
  /** The re-read entry after an owner save/retry/refresh (polling window reset and catalog invalidation included). */
  onChanged: () => void;
  /**
   * 规则 tab 末尾的附加内容。订阅设置修订（BusinessRevisionEditor）曾放在这里，
   * 它与规则 tab 的单一保存重复，现不再传入；组件保留，恢复时在 SubscribedBody 传回。
   */
  rulesExtra?: ReactNode;
};

export type AnimeTabHost = DraftTabHost | OwnerTabHost;
