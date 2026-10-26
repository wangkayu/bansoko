import { errorCopy } from "./zh-CN.error";
import { namingFactCopy, reasonCopy } from "./zh-CN.reason";
import { animeCopy, catalogCopy, fileCopy, retentionCopy, settingsCopy } from "./zh-CN.core";
import { settingsConnectionsCopy } from "./zh-CN.settings-connections";
import { settingsProviderCopy } from "./zh-CN.settings-provider";
import { settingsNotificationsCopy } from "./zh-CN.settings-notifications";
import { varsEditorCopy } from "./zh-CN.vars-editor";
import { settingsTokenCopy } from "./zh-CN.settings-token";
import { settingsBackupCopy } from "./zh-CN.settings-backup";
import { gridCopy, actCopy, addPresetCopy, feedSiteCopy, globalSearchCopy } from "./zh-CN.catalog";
import { configCopy } from "./zh-CN.anime-config";
import { tabsCopy, maintCopy, policyCopy, formsCopy } from "./zh-CN.anime-tabs";
import { rssCopy, namingCopy } from "./zh-CN.rss";
import { accountCopy, authCopy, blacklistCopy, logCopy, navCopy, qbCopy, searchCopy } from "./zh-CN.shell";
import { statusCopy, uiCopy } from "./zh-CN.ui";
import { anibtTagsCopy } from "./zh-CN.anibt-tags";
import { templateFactsCopy } from "./zh-CN.template-facts";
import { factsCopy } from "./facts.generated";
import { tagFiltersCopy, titleTagsCopy } from "./zh-CN.title-tags";
import { downloadCompletionCopy } from "./zh-CN.download-completion";
import { downloadTimeoutsCopy } from "./zh-CN.download-timeouts";
import { metadataRefreshCopy } from "./zh-CN.metadata-refresh";
import { recordsCopy } from "./zh-CN.records";
import { fileManagerCopy } from "./zh-CN.file-manager";
import { qualityCompareCopy } from "./zh-CN.quality-compare";
import { oneshotCopy } from "./zh-CN.oneshot";

// zh-CN 文案表。按区域分组：common / errorDetails / field / verdict / error / reason。

export const zhCN = {
  common: {
    operationFailed: "操作失败，请重试",
    unknownReason: "未知原因",
    unspecifiedReason: "未说明",
    credentialSet: "已设置凭据",
    credentialUnset: "未设置凭据",
    enabled: "已启用",
    disabled: "已停用",
    discardChanges: "放弃修改",
    discardEditsTitle: "放弃未保存的修改？",
    versionConflict: "内容已被修改，请刷新后重新编辑",
    conflictHint: "刷新后将载入最新内容，本地未保存的修改不会保留",
    refreshLatest: "刷新载入最新",
    timeAgo: {
      justNow: "刚刚",
      minutes: "{n} 分钟前",
      hours: "{n} 小时前",
      days: "{n} 天前",
    },
    // 代码里拼接文字用的标点（`lib/i18n/format.ts`）。
    format: {
      labelValue: "{label}：{value}",
      withNote: "{text}（{note}）",
      parenthesized: "（{text}）",
      listSeparator: "、",
      clauseSeparator: "；",
      sentenceEnd: "。",
      sentenceGap: "",
    },
  },

  // 错误“详情”折叠区（toast / Alert 共用）。
  errorDetails: {
    toggle: "详情",
    copy: "复制",
    copied: "已复制",
    copyFailed: "复制失败，已选中文本，请手动复制",
    code: "错误码",
    status: "状态",
    message: "服务端信息",
    fields: "字段",
    requestId: "请求 ID",
  },

  // 种子状态标签（三处共用）。
  verdict: {
    discovered: "已发现",
    waiting_metadata: "等待关联 Bangumi 或 TMDB",
    subscription_stopped: "订阅已停止",
    source_unavailable: "RSS 源不可用",
    parse_failed: "解析失败",
    accepted_pending_download: "等待下载",
    filtered: "已过滤",
    filteredWithReason: "已过滤（{reason}）",
    adopted_existing: "已有文件",
    acquisition_failed: "获取失败",
    resume_baseline: "恢复时已存在",
    start_baseline: "订阅前发布",
    submitted: "已提交下载",
    reevaluating: "重新评估中",
    notSelected: "未选择",
    unparsed: "未解析",
  },

  // 校验错误字段的中文名，按字段路径最后一段（去掉数组下标）查询。
  field: {
    title: "标题",
    name: "名称",
    username: "用户名",
    password: "密码",
    reason: "操作原因",
    locator: "RSS 地址",
    url: "地址",
    endpoint: "地址",
    base_url: "服务地址",
    api_key: "密钥",
    token: "令牌",
    secret: "密钥",
    kind: "类型",
    provider: "站点",
    season: "季度",
    offset: "集数偏移",
    score: "评分",
    max_rss: "最多订阅字幕组数",
    interval: "间隔",
    interval_value: "间隔",
    interval_unit: "间隔单位",
    must_contain: "必须包含",
    must_not_contain: "必须不含",
    exclude: "排除关键词",
    release_filter: "标题过滤",
    file_filter: "文件筛选",
    rss_sources: "RSS 源",
    subgroup: "字幕组",
    group: "字幕组",
    template: "命名模板",
    path: "路径",
    target_path: "目标路径",
    category: "分类",
    tags: "标签",
    save_path: "保存路径",
    bgm: "Bangumi 关联",
    tmdb: "TMDB 关联",
    languages: "语言",
    quality: "清晰度",
    resolution: "分辨率",
    limit: "数量",
    cursor: "翻页位置",
    query: "关键词",
    expected_version: "版本",
  },


  secret: {
    title: "登录凭据",
    keep: "保留现有凭据",
    replace: "替换为新值",
    remove: "移除凭据",
    keepHint: "：不修改已保存的凭据",
    replaceHint: "：在下方填写新的内容",
    removeHint: "：保存后凭据将被删除",
  },

  anime: animeCopy,
  catalog: catalogCopy,
  file: fileCopy,
  retention: retentionCopy,
  qualityCompare: qualityCompareCopy,
  // 设置区域：c2 的 settingsCopy 打底；四个分文件各自持有的命名空间覆盖同名键。
  varsEditor: varsEditorCopy,
  settings: { ...settingsCopy, ...settingsConnectionsCopy, ...settingsProviderCopy, ...settingsNotificationsCopy, ...settingsTokenCopy, ...settingsBackupCopy },
  grid: gridCopy,
  act: actCopy,
  addPreset: addPresetCopy,
  globalSearch: globalSearchCopy,
  feedSite: feedSiteCopy,
  config: configCopy,
  tabs: tabsCopy,
  maint: maintCopy,
  policy: policyCopy,
  forms: formsCopy,
  rss: rssCopy,
  naming: namingCopy,
  anibtTags: anibtTagsCopy,
  templateFacts: templateFactsCopy,
  facts: factsCopy,
  tagFilters: tagFiltersCopy,
  titleTags: titleTagsCopy,
  downloadCompletion: downloadCompletionCopy,
  downloadTimeouts: downloadTimeoutsCopy,
  metadataRefresh: metadataRefreshCopy,

  nav: navCopy,
  account: accountCopy,
  ui: uiCopy,
  status: statusCopy,
  log: logCopy,
  auth: authCopy,
  search: searchCopy,
  blacklist: blacklistCopy,
  qb: qbCopy,
  records: recordsCopy,
  fileManager: fileManagerCopy,
  oneshot: oneshotCopy,

  error: errorCopy,
  reason: reasonCopy,
  namingFact: namingFactCopy,
} as const;
