// 「下载超时」设置（设置 → 下载与入库）：只有全局值，没有番剧单独设置。
// 术语以 copy-guide.md 为准：种子、下载器。

export const downloadTimeoutsCopy = {
  title: "下载超时",
  hint: "修改只影响之后开始下载的种子",
  helpLabel: "说明：下载超时",
  help: "开始下载时记下这两个时间，之后修改不影响已经在下载的种子。一次性下载使用同样的设置。",
  edit: "编辑",
  editTitle: "编辑下载超时",
  stallLabel: "无进度超时",
  stallHint: "下载一直没有进度，超过这个时间判定为下载失败",
  waitLabel: "最长下载时间",
  waitHint: "从开始下载算起，超过这个时间还没下完判定为下载失败",
  amountAria: "{field}：数值",
  unitAria: "{field}：单位",
  unit: {
    minutes: "分钟",
    hours: "小时",
    days: "天",
  },
  amount: {
    minutes: "{n} 分钟",
    hours: "{n} 小时",
    days: "{n} 天",
  },
  invalidNumber: "请输入大于 0 的整数",
  stallRange: "无进度超时需在 10 分钟到 7 天之间",
  waitRange: "最长下载时间需在 1 小时到 30 天之间",
  stallOverWait: "无进度超时不能超过最长下载时间",
  save: "保存",
  saving: "正在保存…",
  cancel: "取消",
  saved: "下载超时已保存",
  saveFailed: "保存下载超时失败",
} as const;
