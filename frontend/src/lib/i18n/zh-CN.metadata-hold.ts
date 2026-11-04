// 「无元数据放行」设置（设置 → 订阅策略）：只有全局开关，没有番剧单独设置。
// 术语以 copy-guide.md 为准：番剧、订阅；BGM 写作 Bangumi，与 TMDB 并列。

export const metadataHoldCopy = {
  title: "无元数据放行",
  description: "订阅缺少 Bangumi 或 TMDB 关联时，仍按手动填写的名称与季集信息获取并入库；缺失的关联照常标注",
  saved: "无元数据放行已保存",
  saveFailed: "保存无元数据放行失败",
  loadFailed: "加载无元数据放行设置失败",
} as const;
