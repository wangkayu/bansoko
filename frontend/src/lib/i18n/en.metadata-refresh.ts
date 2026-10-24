// Periodic metadata refresh (Settings → Subscription policy): a Global switch
// only, with no per-anime setting.

export const enMetadataRefreshCopy = {
  title: "Periodic metadata refresh",
  description: "Re-fetches Bangumi and TMDB titles and episode titles for subscribed anime every 24 hours; stopped subscriptions are not refreshed",
  saved: "Periodic metadata refresh saved",
  saveFailed: "Saving periodic metadata refresh failed",
  loadFailed: "Loading periodic metadata refresh settings failed",
} as const;
