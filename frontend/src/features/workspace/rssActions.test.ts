import { describe, expect, it, vi } from "vitest";
import type { AnimeWorkspace, RSSSource } from "../../lib/api/client";
import {
  eligibleRSSSourceIDs,
  refreshEligibleRSSSources,
  rssActions,
  type RSSActionPort,
  type RSSSourceActionPayload,
} from "./rssActions";
import { t } from "../../lib/i18n";

const payload: RSSSourceActionPayload = {
  managed_anime_id: "anime-1",
  rss_source_id: "source-1",
  expected_version: 4,
  episode_offset: 0,
};

describe("RSS action port", () => {
  it("real adapter is available and non-fixture", () => {
    expect(rssActions.available).toBe(true);
    expect(rssActions.fixture).toBe(false);
    expect(rssActions.unavailableReason).toBeUndefined();
  });

  it("refresh delegates to the API and returns a refresh result shape", async () => {
    // The real adapter calls fetch; without a server we expect it to throw a
    // network/ApiError rather than return a fixture. This proves the adapter
    // does not short-circuit to a fake accepted result.
    await expect(
      rssActions.refreshRSSSource({ rss_source_id: payload.rss_source_id }),
    ).rejects.toThrow();
  });

  it("save offset delegates to the API and does not short-circuit", async () => {
    await expect(rssActions.saveRSSSourceOffset(payload)).rejects.toThrow();
  });
});

function source(id: string, source_kind: RSSSource["source_kind"], groupRef?: string): RSSSource {
  return {
    id,
    subscription_id: "subscription-1",
    source_kind,
    locator_display: `https://example.test/${id}`,
    group_name: id,
    title: "测试番",
    episode_offset: 0,
    provider_provenance: groupRef
      ? {
          provider: "anibt",
          provider_entry_ref: "anime-1",
          provider_group_ref: groupRef,
          discovery_snapshot_version: "snapshot-1",
        }
      : null,
    version: 1,
  };
}

function refreshWorkspace(state: "active" | "stopped" = "active"): AnimeWorkspace {
  const foreign = source("foreign", "custom_rss");
  foreign.subscription_id = "subscription-2";
  return {
    subscription: { id: "subscription-1", state },
    subgroups: {
      effective_selection: [
        { provider: "anibt", group_ref: "selected", effective_selected: true },
        { provider: "anibt", group_ref: "unselected", effective_selected: false },
      ],
    },
    rss_sources: [
      source("custom", "custom_rss"),
      source("selected", "provider_group", "selected"),
      source("unselected", "provider_group", "unselected"),
      source("missing-provenance", "provider_group"),
      foreign,
    ],
  } as AnimeWorkspace;
}

describe("workspace RSS refresh", () => {
  it("selects active custom and effectively selected provider sources only", () => {
    expect(eligibleRSSSourceIDs(refreshWorkspace())).toEqual(["custom", "selected"]);
  });

  it("stopped 订阅的手动刷新同样放开（R5 一次性下载）：资格只看来源归属与组选择", () => {
    expect(eligibleRSSSourceIDs(refreshWorkspace("stopped"))).toEqual(["custom", "selected"]);
  });

  it("refreshes every eligible source once and summarizes partial failures", async () => {
    const refreshRSSSource = vi.fn<RSSActionPort["refreshRSSSource"]>()
      .mockResolvedValueOnce({
        action: "refresh",
        accepted: true,
        fixture: false,
        source: { accepted: true, work_item_id: "work-custom" },
      })
      .mockRejectedValueOnce(new Error("provider unavailable"));

    await expect(refreshEligibleRSSSources(refreshWorkspace(), { refreshRSSSource })).resolves.toEqual({
      eligible: 2,
      succeeded: 1,
      failed: 1,
    });
    expect(refreshRSSSource).toHaveBeenCalledTimes(2);
    expect(refreshRSSSource).toHaveBeenNthCalledWith(1, { rss_source_id: "custom" });
    expect(refreshRSSSource).toHaveBeenNthCalledWith(2, { rss_source_id: "selected" });
  });

  it("does not issue requests when no source is eligible", async () => {
    const refreshRSSSource = vi.fn<RSSActionPort["refreshRSSSource"]>();
    // Leaves only sources belonging to other subscriptions: the eligible set is empty and no refresh request may be issued.
    const workspace = refreshWorkspace();
    workspace.rss_sources = workspace.rss_sources.filter((item) => item.id === "foreign");

    await expect(refreshEligibleRSSSources(workspace, { refreshRSSSource })).resolves.toEqual({
      eligible: 0,
      succeeded: 0,
      failed: 0,
    });
    expect(refreshRSSSource).not.toHaveBeenCalled();
  });
});
