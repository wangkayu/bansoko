// 「元数据周期刷新」设置（设置 → 订阅策略）：只有全局开关，没有番剧单独设置。
// 术语以 copy-guide.md 为准：番剧、订阅；BGM 写作 Bangumi，与 TMDB 并列。

export const metadataRefreshCopy = {
  title: "元数据周期刷新",
  description: "订阅中的番剧每 24 小时重新获取一次 Bangumi、TMDB 标题和剧集标题；已停止的订阅不刷新",
  saved: "元数据周期刷新已保存",
  saveFailed: "保存元数据周期刷新失败",
  loadFailed: "加载元数据周期刷新设置失败",
} as const;
