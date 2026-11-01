// 「忽略多视频文件种子」的全局默认（设置 → 默认规则）：只作为新建订阅的初始值，
// 已有订阅不受影响，每个订阅仍可在自己的规则里修改。
// 术语以 copy-guide.md 为准：种子、订阅。

export const subscriptionDefaultsCopy = {
  title: "忽略多视频文件种子",
  summaryLabel: "新订阅默认",
  on: "开启",
  off: "关闭",
  description: "新建订阅时的默认值；已有订阅不受影响，仍可在各订阅的规则里单独修改",
} as const;
