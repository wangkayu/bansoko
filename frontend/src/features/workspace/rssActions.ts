import {
  patchRSSSourceOffset,
  refreshRSSSource,
  newUUID,
  type AnimeWorkspace,
  type RSSRefreshAccepted,
  type RSSSource,
} from "../../lib/api/client";

export type RSSSourceActionPayload = {
  managed_anime_id: string;
  rss_source_id: string;
  expected_version: number;
  /** Required integer in -1000..1000; 0 means no offset. */
  episode_offset: number;
};

export type RefreshRSSSourcePayload = Pick<RSSSourceActionPayload, "rss_source_id">;
export type SaveRSSSourceOffsetPayload = RSSSourceActionPayload;

export type RSSActionResult =
  | { action: "refresh"; accepted: true; fixture: false; source: RSSRefreshAccepted }
  | { action: "save_offset"; accepted: true; fixture: false; source: RSSSource };

export interface RSSActionPort {
  /** Whether the current runtime has a real RSS action endpoint. */
  readonly available: boolean;
  /** Whether accepted actions are local fixture acknowledgements. */
  readonly fixture: boolean;
  readonly unavailableReason?: string;
  refreshRSSSource(payload: RefreshRSSSourcePayload): Promise<RSSActionResult>;
  saveRSSSourceOffset(payload: SaveRSSSourceOffsetPayload): Promise<RSSActionResult>;
}

export type RSSRefreshSummary = {
  eligible: number;
  succeeded: number;
  failed: number;
};

export function eligibleRSSSourceIDs(workspace: AnimeWorkspace): string[] {
  // Manual refresh is open to stopped sources (matching the backend's rss_source_actions) — a
  //   one-shot download's stopped instance relies on manual refresh to pull releases;
  //   SubscriptionState only has active|stopped, so no exclusion by state happens here; eligibility
  //   looks only at source ownership and group selection.
  const selectedGroups = new Set(
    workspace.subgroups.effective_selection
      .filter((selection) => selection.effective_selected)
      .map((selection) => `${selection.provider}:${selection.group_ref}`),
  );

  return workspace.rss_sources
    .filter((source) => {
      if (source.subscription_id !== workspace.subscription.id) return false;
      if (source.source_kind === "custom_rss") return true;
      const provenance = source.provider_provenance;
      return provenance !== null && provenance !== undefined
        && selectedGroups.has(`${provenance.provider}:${provenance.provider_group_ref}`);
    })
    .map((source) => source.id);
}

export async function refreshEligibleRSSSources(
  workspace: AnimeWorkspace,
  actions: Pick<RSSActionPort, "refreshRSSSource"> = rssActions,
): Promise<RSSRefreshSummary> {
  const sourceIDs = eligibleRSSSourceIDs(workspace);
  const results = await Promise.allSettled(
    sourceIDs.map((rss_source_id) => actions.refreshRSSSource({ rss_source_id })),
  );
  const succeeded = results.filter((result) => result.status === "fulfilled").length;
  return { eligible: sourceIDs.length, succeeded, failed: results.length - succeeded };
}

/** Real adapter backed by the HTTP API. */
export const realRSSActions: RSSActionPort = {
  available: true,
  fixture: false,
  async refreshRSSSource(payload) {
    const source = await refreshRSSSource(payload.rss_source_id, newUUID());
    return { action: "refresh", accepted: true, fixture: false, source };
  },
  async saveRSSSourceOffset(payload) {
    const source = await patchRSSSourceOffset(
      payload.managed_anime_id,
      payload.rss_source_id,
      {
        expected_version: payload.expected_version,
        episode_offset: payload.episode_offset,
      },
    );
    return { action: "save_offset", accepted: true, fixture: false, source };
  },
};

export const rssActions: RSSActionPort = realRSSActions;
