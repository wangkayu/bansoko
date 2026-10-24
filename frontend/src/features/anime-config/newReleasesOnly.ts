import type { AnimeDraft } from "../../lib/api/client";

/** The draft's "fetch new seeds only": turning it off removes the field, matching a draft that never set it. */
export function setDraftNewReleasesOnly(draft: AnimeDraft, next: boolean): AnimeDraft {
  const { new_releases_only: _previous, ...rest } = draft;
  return next ? { ...rest, new_releases_only: true } : rest;
}
