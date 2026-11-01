// "Ignore multi-video releases" Global default (Settings → Default rules): only
// the initial value of a new subscription; existing subscriptions keep their
// own value and stay editable in each subscription's rules tab.

import type { CopyShape } from "./types";
import type { subscriptionDefaultsCopy } from "./zh-CN.subscription-defaults";

export const enSubscriptionDefaultsCopy = {
  title: "Ignore multi-video releases",
  summaryLabel: "New subscriptions",
  on: "On",
  off: "Off",
  description: "The default for new subscriptions; existing subscriptions are unaffected and keep their own switch in their rules",
} as const satisfies CopyShape<typeof subscriptionDefaultsCopy>;
