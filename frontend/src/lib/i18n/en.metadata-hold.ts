// Releasing without metadata (Settings → Subscription policy): a Global switch
// only, with no per-anime setting.

export const enMetadataHoldCopy = {
  title: "Release without metadata",
  description: "Keeps fetching and importing a subscription with no Bangumi or TMDB link, using the manually entered title and season; the missing link is still marked",
  saved: "Release without metadata saved",
  saveFailed: "Saving the release-without-metadata setting failed",
  loadFailed: "Loading the release-without-metadata setting failed",
} as const;
