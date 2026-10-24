import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AnimeDraft, RSSListGroup, RSSListItem, RSSListView, SnapshotSeedExpandResult } from "../../lib/api/client";
import { RssWorkspace } from "./RssWorkspace";
import { t } from "../../lib/i18n";

const spies = vi.hoisted(() => ({
  preview: vi.fn(async () => undefined),
  refreshGroup: vi.fn<() => Promise<unknown[] | null>>(async () => null),
  expand: vi.fn<() => Promise<SnapshotSeedExpandResult>>(),
  deliver: vi.fn(async () => ({})),
}));

// An unexpanded seed: the server gives only RSS facts, and the download entry is "expand first", not delivery.
const PENDING_ITEM: RSSListItem = {
  snapshot_id: "snapshot-1",
  stable_seed_key: "seed-1",
  seed: { title: "Show - 03", published_at: "2026-01-01T00:00:00Z", size_bytes: 123, locator_display: "https://example.test/feed" },
  torrent: { state: "pending" },
  evaluation: { state: "pending" },
  files: [],
  download_action: { allowed: false, kind: "none", reason_code: "seed_not_expanded", reason: "该种子尚未展开" },
  library_overlap: { verdict: "undetermined", present_files: null, pending_files: null },
};

const EXPANDED_ITEM: RSSListItem = {
  ...PENDING_ITEM,
  torrent: {
    state: "available",
    manifest: { payload_digest: "digest", parser_version: "parser", torrent_version: "v1", torrent_name: "t", total_size_bytes: 123, file_count: 1, projection_digest: "projection" },
  },
  evaluation: { state: "evaluated", verdict: "accepted_pending_download" },
  download_action: { allowed: true, kind: "one_shot", reason_code: null, reason: null },
};

function viewWith(item: RSSListItem): RSSListView {
  const group: RSSListGroup = {
    scope: { provider: "anibt", provider_entry_ref: "entry", provider_group_ref: "group", rss_source_id: "source-1" },
    display_name: "Group",
    selection: { source: "configured" as const, effective_selected: true, selection_mode: "manual", count_in_limit: true },
    offset: { value: 0, editable: true, source: "link" },
    feed: { state: "fresh", snapshot_version: 7, content_hash: "hash" },
    items: [item],
  };
  return {
    mode: "current_input",
    config_identity: "draft:1@1",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "p", evaluator: "e", renderer: "r", safety: "s", torrent_parser: "t" },
    generated_at: "2026-01-01T00:00:00Z",
    groups: [group],
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
  };
}

let view: RSSListView = viewWith(PENDING_ITEM);

vi.mock("./useRssListWorkspace", () => ({
  useRssListWorkspace: () => ({
    view,
    pending: false,
    stale: false,
    refreshingGroup: undefined,
    error: undefined,
    refreshError: undefined,
    preview: spies.preview,
    refreshGroup: spies.refreshGroup,
  }),
}));

vi.mock("../../lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api/client")>();
  return { ...actual, expandSnapshotSeed: spies.expand, downloadSnapshotSeed: spies.deliver };
});

function renderWorkspace(draft: AnimeDraft) {
  render(
    <RssWorkspace
      provider="anibt"
      providerEntryRef="entry"
      draft={draft}
      enabled
      feedSnapshots={[]}
      onFeedSnapshotsChange={() => {}}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
}

function clickExpand() {
  fireEvent.click(screen.getByRole("button", { name: t("rss.expandFiles") }));
  fireEvent.click(screen.getByRole("button", { name: t("rss.expandSeed") }));
}

beforeEach(() => {
  view = viewWith(PENDING_ITEM);
  spies.preview.mockClear();
  spies.expand.mockClear();
  spies.deliver.mockClear();
  spies.refreshGroup.mockReset();
  spies.refreshGroup.mockResolvedValue(null);
  spies.expand.mockResolvedValue({ state: "available", torrent: { state: "available" }, cached: false });
});

describe("draft RSS test bench", () => {
  it("没有下载入口", () => {
    view = viewWith(EXPANDED_ITEM);
    renderWorkspace({ rss_sources: [] } as unknown as AnimeDraft);
    expect(screen.queryByRole("button", { name: t("rss.downloadAria", { title: "Show - 03" }) })).toBeNull();
  });

  it("展开未展开的种子只发一次展开请求，随后重读，不投递", async () => {
    renderWorkspace({ rss_sources: [] } as unknown as AnimeDraft);
    clickExpand();

    await waitFor(() => expect(spies.expand).toHaveBeenCalledTimes(1));
    // The expansion request carries identity only: provider, entry, group and snapshot version all come from this group's projection.
    expect(spies.expand).toHaveBeenCalledWith("snapshot-1", "seed-1", {
      provider: "anibt",
      provider_entry_ref: "entry",
      provider_group_ref: "group",
      snapshot_version: 7,
      content_hash: "hash",
    });
    await waitFor(() => expect(spies.preview).toHaveBeenCalledTimes(1));
    expect(spies.deliver).not.toHaveBeenCalled();
  });

  it("展开失败留在行内显示原因", async () => {
    spies.expand.mockResolvedValue({ state: "unavailable", torrent: { state: "pending" }, error_code: "torrent_fetch_failed", cached: false });
    renderWorkspace({ rss_sources: [] } as unknown as AnimeDraft);
    clickExpand();

    expect(await screen.findByText(t("reason.torrent_fetch_failed"))).toBeVisible();
    expect(spies.preview).not.toHaveBeenCalled();
  });

  it("刷新该组种子成功后带新的种子列表身份重读视图", async () => {
    const refreshed = [{ provider: "anibt", provider_entry_ref: "entry", provider_group_ref: "group", snapshot_id: "snapshot-2", snapshot_version: 8, content_hash: "hash-2" }];
    spies.refreshGroup.mockResolvedValue(refreshed);
    renderWorkspace({ rss_sources: [] } as unknown as AnimeDraft);
    fireEvent.click(screen.getByRole("button", { name: t("rss.groupRefresh") }));

    await waitFor(() => expect(spies.refreshGroup).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(spies.preview).toHaveBeenCalledWith(refreshed));
  });

  it("刷新失败不重读，错误由工作区显示", async () => {
    renderWorkspace({ rss_sources: [] } as unknown as AnimeDraft);
    fireEvent.click(screen.getByRole("button", { name: t("rss.groupRefresh") }));

    await waitFor(() => expect(spies.refreshGroup).toHaveBeenCalledTimes(1));
    expect(spies.preview).not.toHaveBeenCalled();
  });
});
