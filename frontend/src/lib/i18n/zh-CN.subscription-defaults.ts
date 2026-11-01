// 「忽略多视频文件种子」全局默认（设置 → 下载与入库）：只作为新建订阅的初始值，
// 已有订阅不受影响，每个订阅仍可在自己的规则里修改。
// 术语以 copy-guide.md 为准：种子、订阅。

export const subscriptionDefaultsCopy = {
  title: "忽略多视频文件种子",
  description: "新建订阅时的默认开关；已有订阅不受影响，仍可在各订阅的规则里单独修改",
  saved: "订阅默认已保存",
  saveFailed: "保存订阅默认失败",
  loadFailed: "加载订阅默认设置失败",
} as const;
