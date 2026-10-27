import type { CopyShape } from "./types";
import type { zhCN } from "./zh-CN";
import { enErrorCopy } from "./en.error";
import { enNamingFactCopy, enReasonCopy } from "./en.reason";
import { enAnimeCopy, enCatalogCopy, enFileCopy, enReplacementCopy, enSettingsCopy } from "./en.core";
import { enSettingsConnectionsCopy } from "./en.settings-connections";
import { enSettingsProviderCopy } from "./en.settings-provider";
import { enSettingsNotificationsCopy } from "./en.settings-notifications";
import { enVarsEditorCopy } from "./en.vars-editor";
import { enSettingsTokenCopy } from "./en.settings-token";
import { enSettingsBackupCopy } from "./en.settings-backup";
import { enActCopy, enAddPresetCopy, enFeedSiteCopy, enGlobalSearchCopy, enGridCopy } from "./en.catalog";
import { enConfigCopy } from "./en.anime-config";
import { enFormsCopy, enMaintCopy, enPolicyCopy, enTabsCopy } from "./en.anime-tabs";
import { enNamingCopy, enRssCopy } from "./en.rss";
import { enAccountCopy, enAuthCopy, enBlacklistCopy, enLogCopy, enNavCopy, enQbCopy, enSearchCopy } from "./en.shell";
import { enStatusCopy, enUiCopy } from "./en.ui";
import { enAnibtTagsCopy } from "./en.anibt-tags";
import { enTemplateFactsCopy } from "./en.template-facts";
import { enFactsCopy } from "./facts.generated";
import { enTagFiltersCopy, enTitleTagsCopy } from "./en.title-tags";
import { enDownloadCompletionCopy } from "./en.download-completion";
import { enDownloadTimeoutsCopy } from "./en.download-timeouts";
import { enMetadataRefreshCopy } from "./en.metadata-refresh";
import { enRecordsCopy } from "./en.records";
import { enFileManagerCopy } from "./en.file-manager";
import { enQualityCompareCopy } from "./en.quality-compare";
import { enOneshotCopy } from "./en.oneshot";

// English copy catalog. The key set is the zh-CN catalog's; a missing or extra
// key fails typecheck.

export const en = {
  common: {
    operationFailed: "Operation failed. Try again.",
    unknownReason: "Unknown reason",
    unspecifiedReason: "Not specified",
    credentialSet: "Credentials set",
    credentialUnset: "No credentials",
    enabled: "Enabled",
    disabled: "Disabled",
    discardChanges: "Discard Changes",
    discardEditsTitle: "Discard Unsaved Changes?",
    versionConflict: "Content was changed elsewhere. Refresh and edit again.",
    conflictHint: "Refreshing loads the latest content. Unsaved local changes are not kept.",
    refreshLatest: "Refresh",
    timeAgo: {
      justNow: "Just now",
      minutes: "{n} min ago",
      hours: "{n} h ago",
      days: "{n} d ago",
    },
    format: {
      labelValue: "{label}: {value}",
      withNote: "{text} ({note})",
      parenthesized: "({text})",
      listSeparator: ", ",
      clauseSeparator: "; ",
      sentenceEnd: ".",
      sentenceGap: " ",
    },
  },

  errorDetails: {
    toggle: "Details",
    copy: "Copy",
    copied: "Copied",
    copyFailed: "Copy failed. The text is selected; copy it manually.",
    code: "Error code",
    status: "Status",
    message: "Server message",
    fields: "Fields",
    requestId: "Request ID",
  },

  verdict: {
    discovered: "Discovered",
    waiting_metadata: "Waiting for a Bangumi or TMDB link",
    subscription_stopped: "Subscription stopped",
    source_unavailable: "RSS feed unavailable",
    parse_failed: "Parse failed",
    accepted_pending_download: "Waiting to download",
    filtered: "Filtered",
    filteredWithReason: "Filtered ({reason})",
    adopted_existing: "Already on disk",
    acquisition_failed: "Failed",
    resume_baseline: "Present at resume",
    start_baseline: "Published before subscribing",
    awaiting_competition: "Awaiting comparison",
    submitted: "Sent to download client",
    reevaluating: "Re-evaluating",
    notSelected: "Not selected",
    unparsed: "Not parsed",
  },

  field: {
    title: "Title",
    name: "Name",
    username: "Username",
    password: "Password",
    reason: "Reason",
    locator: "RSS URL",
    url: "URL",
    endpoint: "URL",
    base_url: "Server URL",
    api_key: "API key",
    token: "Token",
    secret: "Secret",
    kind: "Type",
    provider: "Site",
    season: "Season",
    offset: "Episode offset",
    score: "Score",
    max_rss: "Max groups",
    interval: "Interval",
    interval_value: "Interval",
    interval_unit: "Interval unit",
    must_contain: "Must contain",
    must_not_contain: "Must not contain",
    exclude: "Exclude keywords",
    release_filter: "Title filter",
    file_filter: "File rules",
    rss_sources: "RSS feeds",
    subgroup: "Group",
    group: "Group",
    template: "Naming template",
    path: "Path",
    target_path: "Target path",
    category: "Category",
    tags: "Tags",
    save_path: "Save path",
    bgm: "Bangumi link",
    tmdb: "TMDB link",
    languages: "Languages",
    quality: "Quality",
    resolution: "Resolution",
    limit: "Limit",
    cursor: "Page position",
    query: "Keyword",
    expected_version: "Version",
  },

  secret: {
    title: "Sign-in credentials",
    keep: "Keep current credentials",
    replace: "Replace with new values",
    remove: "Remove credentials",
    keepHint: ": saved credentials are not changed",
    replaceHint: ": enter the new values below",
    removeHint: ": credentials are deleted on save",
  },

  anime: enAnimeCopy,
  catalog: enCatalogCopy,
  file: enFileCopy,
  replacement: enReplacementCopy,
  qualityCompare: enQualityCompareCopy,
  varsEditor: enVarsEditorCopy,
  settings: { ...enSettingsCopy, ...enSettingsConnectionsCopy, ...enSettingsProviderCopy, ...enSettingsNotificationsCopy, ...enSettingsTokenCopy, ...enSettingsBackupCopy },
  grid: enGridCopy,
  act: enActCopy,
  addPreset: enAddPresetCopy,
  globalSearch: enGlobalSearchCopy,
  feedSite: enFeedSiteCopy,
  config: enConfigCopy,
  tabs: enTabsCopy,
  maint: enMaintCopy,
  policy: enPolicyCopy,
  forms: enFormsCopy,
  rss: enRssCopy,
  naming: enNamingCopy,
  anibtTags: enAnibtTagsCopy,
  templateFacts: enTemplateFactsCopy,
  facts: enFactsCopy,
  tagFilters: enTagFiltersCopy,
  titleTags: enTitleTagsCopy,
  downloadCompletion: enDownloadCompletionCopy,
  downloadTimeouts: enDownloadTimeoutsCopy,
  metadataRefresh: enMetadataRefreshCopy,

  nav: enNavCopy,
  account: enAccountCopy,
  ui: enUiCopy,
  status: enStatusCopy,
  log: enLogCopy,
  auth: enAuthCopy,
  search: enSearchCopy,
  blacklist: enBlacklistCopy,
  qb: enQbCopy,
  records: enRecordsCopy,
  fileManager: enFileManagerCopy,
  oneshot: enOneshotCopy,

  error: enErrorCopy,
  reason: enReasonCopy,
  namingFact: enNamingFactCopy,
} as const satisfies CopyShape<typeof zhCN>;
