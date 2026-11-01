import type { AnimeDraft } from "../../lib/api/client";

/** The draft's "ignore multi-video releases": turning it off removes the field, matching a draft that never set it. */
export function setDraftIgnoreMultiVideo(draft: AnimeDraft, next: boolean): AnimeDraft {
  const { ignore_multi_video_releases: _previous, ...rest } = draft;
  return next ? { ...rest, ignore_multi_video_releases: true } : rest;
}
