import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AnimeWorkspace, BusinessPresetView, Release, RSSListView, RSSSource } from "../../lib/api/client";
import { presetRuleSections } from "../anime-config/businessDraft";
import { preset } from "../../testing/businessPreset";
import { AnimeTabs } from "./AnimeTabs";
import { rssActions } from "../workspace/rssActions";
import { workspaceCapability } from "../workspace/capability";
import { t } from "../../lib/i18n";
import { labelValue } from "../../lib/i18n/format";
import { titleTagFullLabel } from "../../lib/titleTags";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

// With forceMount the rules tab also mounts: NamingTemplateFields needs a QueryClient, so the
// naming catalog/preview requests get an empty catalog and a failing preview as fallback —
// irrelevant to the assertions.
function ownerRssListView(overrides: Partial<RSSListView> = {}): RSSListView {
  return {
    mode: "owner",
    config_identity: "owner-config-1",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "parser-v1", evaluator: "evaluator-v1", renderer: "renderer-v1", safety: "safety-v1", torrent_parser: "torrent-v1" },
    generated_at: "2026-09-24T00:00:00Z",
    groups: [],
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
    ...overrides,
  };
}

function ownerGroup(rssSourceID: string, groupName = rssSourceID, selected = true, score = 0) {
  return {
    scope: { provider: "anibt" as const, provider_entry_ref: "anime-1", provider_group_ref: rssSourceID, rss_source_id: rssSourceID },
    display_name: groupName,
    score,
    selection: { source: "configured" as const, effective_selected: selected, selection_mode: "auto" as const },
    offset: { value: 0, editable: true, source: "link" as const },
    feed: { state: "fresh" as const, snapshot_id: "snapshot-1", snapshot_version: 1, content_hash: "hash-1" },
    items: [],
  };
}

// With forceMount the rules tab also mounts: NamingTemplateFields needs a QueryClient, so the
// naming catalog/preview requests get an empty catalog and a failing preview as fallback —
// irrelevant to the assertions.
function stubAuxiliaryFetch(view = ownerRssListView({ groups: [
  ownerGroup("custom", "custom"), ownerGroup("selected", "selected"),
]})) {
  const calls: { url: string; body?: unknown }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/anime/rss-list-view")) {
      calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      return new Response(JSON.stringify(view), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    if (url.includes("/template-catalogs/naming")) {
      return new Response(
        JSON.stringify({ language_version: "1", catalog_version: "1", variables: [], functions: [] }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    if (url.includes("/metadata/search")) {
      return new Response(
        JSON.stringify({
          candidates: [
            {
              provider: "tmdb",
              provider_work_id: "265010",
              title: "匹配到的番名",
              media_kind: "tv",
              details_status: "available",
              seasons: [{ season_number: 1, name: "第一季", episode_count: 12, air_date: "2024-01-01", is_special: false }],
              suggested_season: 1,
              score: 0.9,
            },
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
      status: 400,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return { calls, fetchMock };
}

// Owner-mode convenience render: wraps flat params into AnimeTabs's host discriminated union.
function renderTabs(props: {
  animeID: string;
  workspace: AnimeWorkspace;
  onChanged: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  initialTab?: string;
}) {
  const { animeID, workspace, onChanged, onDirtyChange, initialTab } = props;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AnimeTabs
        host={{ mode: "owner", animeID, workspace, capability: workspaceCapability(workspace.subscription.lifecycle_state), onChanged }}
        onDirtyChange={onDirtyChange}
        initialTab={initialTab}
      />
    </QueryClientProvider>,
  );
}

// The single-save model: the owner dialog's footer has one save button submitting the current tab's every change.
function ownerFooter(): HTMLElement {
  const footer = document.querySelector<HTMLElement>(".anime-config-dialog-footer-owner");
  expect(footer).not.toBeNull();
  return footer!;
}

function footerSave(): HTMLButtonElement {
  return within(ownerFooter()).getByRole("button", { name: t("act.save") }) as HTMLButtonElement;
}

/** Changes the title-filter's exclude regex: produces one unsaved change in the rules tab. */
function editReleaseFilter(value = "sample") {
  const section = screen.getByText(t("tabs.sectionName.release_filter")).closest("section");
  expect(section).not.toBeNull();
  fireEvent.change(within(section as HTMLElement).getByLabelText(t("settings.rules.titleNot")), { target: { value } });
}

function clickFooterSave() {
  const save = footerSave();
  expect(save).toBeEnabled();
  fireEvent.click(save);
}

// The unsaved mark beside a tab name is visual only (aria-hidden): the tab's accessible name is unchanged.
function tabNamed(name: string): HTMLElement {
  return screen.getByRole("tab", { name });
}

function hasUnsavedDot(tab: HTMLElement): boolean {
  return tab.querySelector("[data-unsaved-dot]") !== null;
}

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

function workspaceFixture(): AnimeWorkspace {
  return {
    managed_anime: { id: "anime-1", title: "测试番", title_override: null, version: 1 },
    placement_handoffs: [],
    subscription: {
      id: "subscription-1",
      managed_anime_id: "anime-1",
      origin: "anibt",
      origin_entry_ref: "anime-1",
      instance_label: null,
      state: "active",
      lifecycle_state: "active",
      lifecycle_epoch: 1,
      version: 1,
      new_releases_only: false,
    },
    metadata: { managed_anime_id: "anime-1", bgm: null, tmdb: null, version: 1 },
    metadata_hold: { held: true, missing: ["bgm", "tmdb"] },
    subgroups: {
      managed_anime_id: "anime-1",
      subgroups: { max_rss: 3, entries: [] },
      effective_selection: [
        {
          available: true, availability_generation: 1,
          id: "selection-selected",
          subscription_id: "subscription-1",
          provider: "anibt",
          group_ref: "selected",
          display_name: "selected",
          selection_mode: "auto",
          manual_selected: null,
          effective_selected: true,
          effective_score: 90,
          version: 1,
        },
        {
          available: true, availability_generation: 1,
          id: "selection-unselected",
          subscription_id: "subscription-1",
          provider: "anibt",
          group_ref: "unselected",
          display_name: "unselected",
          selection_mode: "manual",
          manual_selected: false,
          effective_selected: false,
          effective_score: 50,
          version: 1,
        },
      ],
      rss_sources: [],
      version: 1,
    },
    rules: {
      managed_anime_id: "anime-1",
      sections: {
        parser: { mode: "replace", value: { episode_patterns: [] } },
        release_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
        file_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
        naming: {
          mode: "replace",
          value: {
            language: "js-naming-1",
            unknown: { directory_template: "", video_filename_template: "" },
            tv: { directory_template: "{title}", video_filename_template: "{title}" },
            movie: { directory_template: "{title}", video_filename_template: "{title}" },
            subtitle_suffix_template: "",
          },
        },
        file_effect: { mode: "replace", value: { operation: "hardlink", collision_policy: "versioned" } },
        tag_filters: { mode: "replace", value: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } } },
      },
      effective: {
        parser: { episode_patterns: [] },
        release_filter: { must_contain: "", not_in: "" },
        file_filter: { must_contain: "", not_in: "" },
        naming: {
          language: "js-naming-1",
          unknown: { directory_template: "", video_filename_template: "" },
          tv: { directory_template: "{title}", video_filename_template: "{title}" },
          movie: { directory_template: "{title}", video_filename_template: "{title}" },
          subtitle_suffix_template: "",
        },
        file_effect: { operation: "hardlink", collision_policy: "versioned" },
      },
      effective_tag_filters: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } },
      has_anibt_source: false,
      version: 1,
    },
    rss_sources: [
      source("custom", "custom_rss"),
      source("selected", "provider_group", "selected"),
      source("unselected", "provider_group", "unselected"),
      source("missing-provenance", "provider_group"),
    ],
    rss_source_runtimes: [],
    releases: [],
    download_tasks: [],
    artifacts: [],
    file_operations: [],
    logs: [],
    episode_progress: null,
  };
}

describe("AnimeTabs RSS read model and refresh", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("renders owner groups in the order and with the facts returned by the server", async () => {
    const view = ownerRssListView({ groups: [
      ownerGroup("selected", "服务端排序第一", false, 4),
      ownerGroup("custom", "服务端排序第二", true, 98),
    ] });
    const { calls } = stubAuxiliaryFetch(view);
    const workspace = workspaceFixture();
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rss" });
    await screen.findByRole("button", { name: /展开 服务端排序第一/ }, { timeout: 5000 });
    expect(screen.getAllByRole("button", { name: /展开/ }).map((button) => button.textContent)).toEqual([
      expect.stringContaining("服务端排序第一"),
      expect.stringContaining("服务端排序第二"),
    ]);
    expect(calls).toContainEqual({
      url: "/api/v1/anime/rss-list-view",
      body: { input_source: "owner", managed_anime_id: "anime-1", limit_per_group: 100 },
    });
  });

  it("refreshes each eligible source once without using global automation", async () => {
    stubAuxiliaryFetch();
    const refresh = vi.spyOn(rssActions, "refreshRSSSource").mockImplementation(async ({ rss_source_id }) => ({
      action: "refresh",
      accepted: true,
      fixture: false,
      source: { accepted: true, work_item_id: `work-${rss_source_id}` },
    }));
    const onChanged = vi.fn();

    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged, initialTab: "rss" });
    await screen.findByRole("checkbox", { name: t("rss.showFiltered") });
    fireEvent.click(screen.getByRole("button", { name: t("rss.refresh") }));

    await waitFor(() => expect(refresh).toHaveBeenCalledTimes(2));
    expect(refresh).toHaveBeenCalledWith({ rss_source_id: "custom" });
    expect(refresh).toHaveBeenCalledWith({ rss_source_id: "selected" });
    expect(onChanged).toHaveBeenCalledTimes(1);
  });
});


describe("AnimeTabs 四 Tab 人工路径", () => {
  beforeEach(() => {
    stubAuxiliaryFetch();
  });

  it("订阅后偏移弹窗：整数偏移提示可见，保存走来源 offset CAS", async () => {
    const patch = vi.spyOn(rssActions, "saveRSSSourceOffset").mockImplementation(async () => ({
      action: "save_offset", accepted: true, fixture: false,
      source: {
        id: "selected", subscription_id: "subscription-1", source_kind: "provider_group",
        locator_display: "https://example.test/selected", group_name: "selected", title: "测试番",
        episode_offset: 3, version: 4,
      },
    }));
    const workspace = workspaceFixture();
    workspace.rss_sources = [source("selected", "provider_group", "selected")];
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rss" });
    await screen.findByRole("button", { name: t("rss.expandGroup", { name: "selected" }) });
    fireEvent.click(screen.getByRole("button", { name: t("rss.offsetTitle", { name: "selected" }) }));
    expect(screen.getByText(t("rss.offsetFieldHint"))).toBeInTheDocument();

    fireEvent.change(screen.getByRole("spinbutton", { name: t("field.offset") }), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: t("rss.saveOffset") }));
    await waitFor(() => expect(patch).toHaveBeenCalledWith(expect.objectContaining({ episode_offset: 3 })));
  });

  it("偏移弹窗：默认显示当前整数，空值或越界不发送", async () => {
    const patch = vi.spyOn(rssActions, "saveRSSSourceOffset");
    const workspace = workspaceFixture();
    workspace.rss_sources = [source("selected", "provider_group", "selected")];
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rss" });
    await screen.findByRole("button", { name: t("rss.expandGroup", { name: "selected" }) });
    fireEvent.click(screen.getByRole("button", { name: t("rss.offsetTitle", { name: "selected" }) }));
    const input = screen.getByRole("spinbutton", { name: t("field.offset") });
    expect(input).toHaveValue(0);

    for (const bad of ["", "1001", "-1001"]) {
      fireEvent.change(input, { target: { value: bad } });
      fireEvent.click(screen.getByRole("button", { name: t("rss.saveOffset") }));
      expect(await screen.findByText(t("rss.offsetInvalid"))).toBeInTheDocument();
    }
    expect(patch).not.toHaveBeenCalled();
  });

  it("owner 模式只渲染 Metadata、Subgroups、Rules、RSS 四个 Tab", () => {
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged: () => {}, initialTab: "metadata" });

    expect(screen.getAllByRole("tab")).toHaveLength(4);
    for (const name of [t("tabs.names.metadata"), t("field.subgroup"), t("tabs.names.rules"), "RSS"]) {
      expect(screen.getByRole("tab", { name })).toBeInTheDocument();
    }
  });
  it("字幕组 owner 使用刷新字幕组语言", () => {
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged: () => {}, initialTab: "subgroups" });
    expect(screen.getByRole("button", { name: t("config.discovery.refresh") })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重新发现字幕组" })).not.toBeInTheDocument();
  });
});
// Seed retry: no confirmation dialog, one click re-accepts under the current rules; the outcome
// gets a one-line notice and reason codes surface only through the copy catalog.
describe("AnimeTabs 种子重试", () => {
  type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

  function retryTargetRelease(): Release {
    return {
      id: "release-1",
      rss_source_id: "custom",
      stable_item_key: "stable-1",
      raw_facts: { title: "[测试字幕组] 测试番 - 01", published_at: "2026-07-21T09:00:00Z" },
      source_facts: { identity_kind: "guid", identity_value: "stable-1" },
      parsed_facts: { episode: 1, group_name: "测试字幕组", special: false },
      verdict: "filtered",
      reason_code: "filtered_by_rules",
      version: 1,
    };
  }

  function stubRetryFetch(retry: () => Response) {
    const fetchMock = vi.fn<FetchStub>(async (input, init) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming")) {
        return new Response(JSON.stringify({ language_version: "1", catalog_version: "1", variables: [], functions: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (url.includes("/anime/rss-list-view")) {
        return new Response(JSON.stringify(ownerRssListView({ groups: [{ ...ownerGroup("custom"), items: [retryItem()] }] })), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (init?.method === "POST" && url.includes("/releases/release-1/retry")) return retry();
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), { status: 400, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function json(status: number, body: Record<string, unknown>) {
    return () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  }

  function retryItem() {
    return {
      snapshot_id: "release:release-1", stable_seed_key: "stable-1",
      seed: { title: "[测试字幕组] 测试番 - 01", locator_display: "https://example.test/seed" },
      torrent: { state: "not_applicable" as const },
      evaluation: { state: "evaluated" as const, verdict: "accepted_pending_download" as const, reason_code: "accepted", parsed_facts: { episode: 1, group_name: "测试字幕组", special: false } },
      files: [], download_action: { allowed: true, kind: "retry" as const },
      // No file rows means no comparable write set: the server's verdict is undetermined.
      library_overlap: { verdict: "undetermined" as const, present_files: null, pending_files: null, reason_code: "no_rendered_targets" },
    };
  }

  async function clickRetry() {
    const workspace = workspaceFixture();
    workspace.releases = [retryTargetRelease()];
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rss" });
    await screen.findByRole("button", { name: t("rss.expandGroup", { name: "custom" }) });
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "custom" }) }));
    fireEvent.click(screen.getByRole("button", { name: t("rss.retryRelease") }));
  }

  it("一次点击即提交重试，没有确认框", async () => {
    const fetchMock = stubRetryFetch(json(200, { release_id: "release-1", verdict: "accepted_pending_download", outcome: "accepted", download_task_id: "task-2" }));
    await clickRetry();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("rss.releaseRetry.accepted")));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    const calls = fetchMock.mock.calls.filter(([input, init]) => String(input).includes("/releases/release-1/retry") && init?.method === "POST");
    expect(calls).toHaveLength(1);
    expect(new Headers(calls[0][1]?.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("订阅中的重试重新进入比较", async () => {
    stubRetryFetch(json(200, { release_id: "release-1", verdict: "awaiting_competition", outcome: "awaiting", reason_code: "awaiting_competition" }));
    await clickRetry();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.info).toHaveBeenCalledWith(t("rss.releaseRetry.awaiting")));
  });

  it("被过滤的结果用一句话说明原因，不显示原始码", async () => {
    stubRetryFetch(json(200, { release_id: "release-1", verdict: "filtered", outcome: "filtered", reason_code: "filtered_by_rules" }));
    await clickRetry();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.info).toHaveBeenCalledWith(t("rss.releaseRetry.filteredBecause", { reason: t("reason.filtered_by_rules") })));
  });

  it("不可重试时说明原因", async () => {
    stubRetryFetch(json(409, { code: "RELEASE_NOT_RETRYABLE", message: "The release cannot be retried now.", details: [{ code: "download_in_progress", message: "The download is still in progress." }] }));
    await clickRetry();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(t("rss.releaseRetry.blocked", { reason: t("reason.download_in_progress") })));
  });
});

// Custom subscriptions' subgroups: no discovery, no scoring, one fixed group displayed; provider
// subscriptions show the single-form empty state of the current provider only.
describe("AnimeTabs Subgroups custom 固定 group", () => {
  beforeEach(() => {
    stubAuxiliaryFetch();
  });

  it("custom 订阅显示固定 group 行与说明，不显示 provider 空态与发现入口", () => {
    const workspace = workspaceFixture();
    workspace.subscription = { ...workspace.subscription, origin: "custom" };
    workspace.rss_sources = [{ ...source("custom-src", "custom_rss"), group_name: "SampleGroup 固定源" }];

    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "subgroups" });

    // The RSS tab stays mounted via forceMount, so group names also appear in source group headers; the assertion targets the fixed row's own content.
    const note = screen.getByText(t("forms.sources.fixedNote"));
    expect(note.parentElement).toHaveTextContent("固定字幕组 SampleGroup 固定源");
    expect(screen.queryByText(/尚未发现/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("config.discovery.refresh") })).not.toBeInTheDocument();
  });

  it("provider 订阅展示当前 provider 空态与发现入口", () => {
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged: () => {}, initialTab: "subgroups" });

    expect(screen.getByText(new RegExp(t("forms.sources.noGroups")))).toBeInTheDocument();
    expect(screen.queryByText(t("forms.sources.fixedNote"))).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("config.discovery.refresh") })).toBeInTheDocument();
  });
});

// Tab 2's policy switches collect GroupActions sent with the subgroups PATCH (the backend's
// applyGroupActions); for one group the last switch wins; with no action triggered an empty array
// is still sent, preserving the backend's CAS semantics.
describe("AnimeTabs Subgroups group_actions", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

  function subgroupsWithEntries(): AnimeWorkspace {
    const workspace = workspaceFixture();
    workspace.subgroups.subgroups = {
      max_rss: 3,
      entries: [
        {
          provider: "anibt",
          group_ref: "g-1",
          display_name: "一号组",
          selection_mode: "auto",
          manual_selected: null,
          effective_selected: true,
          score: 80,
        },
      ],
    };
    return workspace;
  }

  function stubSubgroupsPatchFetch() {
    const fetchMock = vi.fn<FetchStub>(async (input, init) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming")) {
        return new Response(
          JSON.stringify({ language_version: "1", catalog_version: "1", variables: [], functions: [] }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (init?.method === "PATCH" && url.includes("/anime/anime-1/subgroups")) {
        return new Response(JSON.stringify({ accepted: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
        status: 400,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function patchBodies(fetchMock: ReturnType<typeof vi.fn<FetchStub>>) {
    return fetchMock.mock.calls
      .filter(([input, init]) => String(input).includes("/anime/anime-1/subgroups") && init?.method === "PATCH")
      .map(([, init]) => JSON.parse(String(init?.body)) as Record<string, unknown>);
  }

  // The metadata tab's title input is in the document (owner: empty = no override); an empty
//   origin title keeps parity with the old fixtures and metadata stays clean.
  function workspaceWithProxyTitle(workspace: AnimeWorkspace): AnimeWorkspace {
    return { ...workspace, managed_anime: { ...workspace.managed_anime, title: "" } };
  }

  it("策略切换后保存携带 group_actions，同组以最后动作为准", async () => {
    const fetchMock = stubSubgroupsPatchFetch();
    renderTabs({ animeID: "anime-1", workspace: workspaceWithProxyTitle(subgroupsWithEntries()), onChanged: () => {}, initialTab: "subgroups" });

    const policy = screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "一号组" }) });
    fireEvent.click(within(policy).getByRole("radio", { name: t("forms.sources.policyAlways") }));
    fireEvent.click(within(policy).getByRole("radio", { name: t("forms.sources.policyNever") }));
    expect(hasUnsavedDot(tabNamed(t("tabs.names.subgroups")))).toBe(true);

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.subgroups.saved")));

    const bodies = patchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({
      expected_version: 1,
      group_actions: [{ provider: "anibt", group_ref: "g-1", action: "force_exclude" }],
    });
  });

  it("未触发动作时保存仍携带空 group_actions 数组", async () => {
    const fetchMock = stubSubgroupsPatchFetch();
    renderTabs({ animeID: "anime-1", workspace: workspaceWithProxyTitle(subgroupsWithEntries()), onChanged: () => {}, initialTab: "subgroups" });

    // Changing max_rss triggers dirty but switches no policy.
    fireEvent.click(screen.getByRole("button", { name: t("forms.sources.maxButton") }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(t("forms.sources.maxField")), { target: { value: "5" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.sources.maxApply") }));
    expect(within(ownerFooter()).getByRole("status")).toHaveTextContent(t("act.unsavedCount", { n: 1 }));

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.subgroups.saved")));

    const bodies = patchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({ group_actions: [] });
    expect((bodies[0]!.subgroups as { max_rss: number }).max_rss).toBe(5);
  });
});

describe("AnimeTabs owner 编辑保活", () => {
  beforeEach(() => {
    stubAuxiliaryFetch();
  });

  function boundWorkspace(): AnimeWorkspace {
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: { work_id: "bgm-1" },
      tmdb: null,
      version: 1,
    };
    return workspace;
  }

  it("切换 Tab 后未保存编辑与 dirty 标记保留，并上抛 onDirtyChange", () => {
    const onDirtyChange = vi.fn();
    renderTabs({
      animeID: "anime-1",
      workspace: boundWorkspace(),
      onChanged: () => {},
      onDirtyChange,
      initialTab: "metadata",
    });

    // The footer has one save button; tab panels have no saves of their own.
    expect(screen.getAllByRole("button", { name: t("act.save"), hidden: true })).toHaveLength(1);
    expect(footerSave()).toBeDisabled();
    expect(onDirtyChange).not.toHaveBeenCalledWith(true);

    // Clearing the BGM binding → the metadata tab goes dirty
    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.bangumi") }) }));
    expect(within(ownerFooter()).getByRole("status")).toHaveTextContent(t("act.unsavedCount", { n: 1 }));
    expect(hasUnsavedDot(tabNamed(t("tabs.names.metadata")))).toBe(true);
    expect(footerSave()).toBeEnabled();
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    // Switch to rules: save covers the current tab only, rules unchanged → disabled; the metadata tab's mark remains.
    fireEvent.click(tabNamed(t("tabs.names.rules")));
    expect(footerSave()).toBeDisabled();
    expect(hasUnsavedDot(tabNamed(t("tabs.names.metadata")))).toBe(true);
    expect(hasUnsavedDot(tabNamed(t("tabs.names.rules")))).toBe(false);
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);

    // The RSS tab has nothing saveable: no save button rendered.
    fireEvent.click(tabNamed(t("tabs.names.rss")));
    expect(screen.queryByRole("button", { name: t("act.save"), hidden: true })).not.toBeInTheDocument();

    // Switching back: the editing state is not destroyed by unmount (the clear button no longer appears = local edit kept)
    fireEvent.click(tabNamed(t("tabs.names.metadata")));
    expect(screen.queryByRole("button", { name: t("forms.clearAria", { label: t("forms.bangumi") }) })).not.toBeInTheDocument();
    expect(footerSave()).toBeEnabled();
  });

  it("retired 只读：footer 没有保存按钮", () => {
    const workspace = boundWorkspace();
    workspace.subscription = { ...workspace.subscription, lifecycle_state: "retired" };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });
    expect(screen.queryByRole("button", { name: t("act.save"), hidden: true })).not.toBeInTheDocument();
  });

  it("保存规则显示同一响应中的重评数量且不重复调用显式重评", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming")) {
        return new Response(
          JSON.stringify({ language_version: "1", catalog_version: "1", variables: [], functions: [] }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (init?.method === "PATCH" && url.includes("/anime/anime-1/rules")) {
        return new Response(
          JSON.stringify({
            rules: workspaceFixture().rules,
            reevaluation: { managed_anime_id: "anime-1", accepted: true, scheduled_count: 3 },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
        status: 400,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    const onChanged = vi.fn();
    renderTabs({ animeID: "anime-1", workspace: boundWorkspace(), onChanged, initialTab: "rules" });

    editReleaseFilter();
    clickFooterSave();

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.rules.saved", { n: 3 })));
    expect(onChanged).toHaveBeenCalledTimes(1);
    const rulePatchCalls = fetchMock.mock.calls.filter(
      ([input, init]) => String(input).includes("/anime/anime-1/rules") && init?.method === "PATCH",
    );
    const reevaluateCalls = fetchMock.mock.calls.filter(
      ([input, init]) => String(input).includes("/anime/anime-1/releases/reevaluate") && init?.method === "POST",
    );
    expect(rulePatchCalls).toHaveLength(1);
    expect(reevaluateCalls).toHaveLength(0);
  });

  function stubParserFetch(stored: "regex" | "reltitle") {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/file-maintenance/parser-policy/anime/")) {
        return new Response(
          JSON.stringify({ managed_anime_id: "anime-1", parser_kind: stored, source_default_version: 1, version: 1 }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url.includes("/parser-catalog")) {
        return new Response(
          JSON.stringify([
            { id: "regex", implementation_version: "regex-v1", output_contract_version: "episode-evidence-v1", display_name: "Regex rules", capabilities: ["release_title"], config_version: "regex-config-v1" },
            { id: "reltitle", implementation_version: "reltitle@x", output_contract_version: "episode-evidence-v1", display_name: "reltitle", capabilities: ["release_title"], config_version: "reltitle-config-v1" },
          ]),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), { status: 400, headers: { "Content-Type": "application/json" } });
    }));
  }

  it("集数解析器卡在「解析」段内、集数识别规则之上；选 reltitle 即隐藏规则，选回正则恢复", async () => {
    stubParserFetch("regex");
    renderTabs({ animeID: "anime-1", workspace: boundWorkspace(), onChanged: () => {}, initialTab: "rules" });

    const parserSection = screen.getByText(t("tabs.sectionName.parser")).closest("section");
    expect(parserSection).not.toBeNull();
    const parser = within(parserSection as HTMLElement);
    // The card sits inside the "parse episode numbers from RSS titles" section, so users pick the parser right there.
    expect(await parser.findByRole("radiogroup", { name: t("policy.parser.kindLabel") })).toBeInTheDocument();
    expect(parser.getByText(t("policy.parser.title"))).toBeInTheDocument();
    expect(parser.getByText(t("settings.rules.episodePatterns"))).toBeInTheDocument();

    await userEvent.click(parser.getByRole("radio", { name: t("policy.parser.kindReltitle") }));
    expect(parser.queryByText(t("settings.rules.episodePatterns"))).not.toBeInTheDocument();
    expect(parser.getByText(t("forms.rules.episodePatternsUnused"))).toBeInTheDocument();

    await userEvent.click(parser.getByRole("radio", { name: t("policy.parser.kindRegex") }));
    expect(parser.getByText(t("settings.rules.episodePatterns"))).toBeInTheDocument();
  });

  it("已保存为 reltitle 时直接隐藏集数识别规则", async () => {
    stubParserFetch("reltitle");
    renderTabs({ animeID: "anime-1", workspace: boundWorkspace(), onChanged: () => {}, initialTab: "rules" });

    const parserSection = screen.getByText(t("tabs.sectionName.parser")).closest("section");
    const parser = within(parserSection as HTMLElement);
    expect(await parser.findByText(t("forms.rules.episodePatternsUnused"))).toBeInTheDocument();
    expect(parser.getByRole("radio", { name: t("policy.parser.kindReltitle") })).toHaveAttribute("aria-checked", "true");
    expect(parser.queryByText(t("settings.rules.episodePatterns"))).not.toBeInTheDocument();
  });

  it("集数解析器卡的未保存选择上报到弹窗，关闭前会询问", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const ok = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      if (url.includes("/file-maintenance/parser-policy/anime/")) {
        return ok({ managed_anime_id: "anime-1", parser_kind: "regex", source_default_version: 1, version: 1 });
      }
      if (url.includes("/parser-catalog")) {
        return ok([
          { id: "regex", implementation_version: "regex-v1", output_contract_version: "episode-evidence-v1", display_name: "Regex rules", capabilities: ["release_title"], config_version: "regex-config-v1" },
          { id: "reltitle", implementation_version: "reltitle@x", output_contract_version: "episode-evidence-v1", display_name: "reltitle episode parser", capabilities: ["release_title"], config_version: "reltitle-config-v1" },
        ]);
      }
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), { status: 400, headers: { "Content-Type": "application/json" } });
    }));
    const onDirtyChange = vi.fn();
    renderTabs({ animeID: "anime-1", workspace: boundWorkspace(), onChanged: () => {}, onDirtyChange, initialTab: "rules" });

    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    expect(onDirtyChange).not.toHaveBeenCalledWith(true);
    await userEvent.click(screen.getByRole("radio", { name: t("policy.parser.kindReltitle") }));
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    expect(hasUnsavedDot(tabNamed(t("tabs.names.rules")))).toBe(true);
    expect(footerSave()).toBeEnabled();
  });

  function stubRulesAndParserFetch(opts: { parserConflict?: boolean } = {}) {
    const order: string[] = [];
    const ok = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming")) {
        return ok({ language_version: "1", catalog_version: "1", variables: [], functions: [] });
      }
      if (url.includes("/file-maintenance/parser-policy/anime/")) {
        if (init?.method === "PUT") {
          order.push("parser");
          if (opts.parserConflict) {
            return new Response(JSON.stringify({ code: "OWNER_VERSION_CONFLICT", current_version: 4, message: "conflict" }), {
              status: 409,
              headers: { "Content-Type": "application/json" },
            });
          }
          const body = JSON.parse(String(init.body)) as { parser_kind: string };
          return ok({ managed_anime_id: "anime-1", parser_kind: body.parser_kind, source_default_version: 1, version: 2 });
        }
        return ok({ managed_anime_id: "anime-1", parser_kind: "regex", source_default_version: 1, version: 1 });
      }
      if (url.includes("/parser-catalog")) {
        return ok([
          { id: "regex", implementation_version: "regex-v1", output_contract_version: "episode-evidence-v1", display_name: "Regex rules", capabilities: ["release_title"], config_version: "regex-config-v1" },
          { id: "reltitle", implementation_version: "reltitle@x", output_contract_version: "episode-evidence-v1", display_name: "reltitle", capabilities: ["release_title"], config_version: "reltitle-config-v1" },
        ]);
      }
      if (init?.method === "PATCH" && url.includes("/anime/anime-1/rules")) {
        order.push("rules");
        return ok({
          rules: workspaceFixture().rules,
          reevaluation: { managed_anime_id: "anime-1", accepted: true, scheduled_count: 3 },
        });
      }
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), { status: 400, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    return order;
  }

  it("规则 Tab 的一个保存按顺序提交规则与集数解析器，只弹一条成功提示", async () => {
    const order = stubRulesAndParserFetch();
    vi.clearAllMocks();
    renderTabs({ animeID: "anime-1", workspace: boundWorkspace(), onChanged: () => {}, initialTab: "rules" });

    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    await userEvent.click(screen.getByRole("radio", { name: t("policy.parser.kindReltitle") }));
    editReleaseFilter();
    expect(within(ownerFooter()).getByRole("status")).toHaveTextContent(t("act.unsavedCount", { n: 2 }));

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.rules.saved", { n: 3 })));
    expect(toast.success).toHaveBeenCalledTimes(1);
    expect(order).toEqual(["rules", "parser"]);
  });

  it("后一部分保存失败：先前部分已保存，footer 报出失败的部分且不弹成功提示", async () => {
    const order = stubRulesAndParserFetch({ parserConflict: true });
    vi.clearAllMocks();
    renderTabs({ animeID: "anime-1", workspace: boundWorkspace(), onChanged: () => {}, initialTab: "rules" });

    await screen.findByRole("radiogroup", { name: t("policy.parser.kindLabel") });
    await userEvent.click(screen.getByRole("radio", { name: t("policy.parser.kindReltitle") }));
    editReleaseFilter();

    clickFooterSave();
    await waitFor(() =>
      expect(within(ownerFooter()).getByRole("alert")).toHaveTextContent(t("act.partFailed", { part: t("policy.parser.title") })),
    );
    expect(order).toEqual(["rules", "parser"]);
    const { toast } = await import("sonner");
    expect(toast.success).not.toHaveBeenCalled();
    // The parser card shows the conflict-refresh block in place; the rules tab still has unsaved changes.
    expect(screen.getByRole("button", { name: t("common.refreshLatest") })).toBeInTheDocument();
    expect(hasUnsavedDot(tabNamed(t("tabs.names.rules")))).toBe(true);
    expect(footerSave()).toBeEnabled();
  });
});

describe("AnimeTabs owner RSS read model preview", () => {
  function viewItem(downloadKind: "retry" | "none" = "retry") {
    return {
      snapshot_id: "release:release-1",
      stable_seed_key: "stable-1",
      seed: { title: "[G] 测试番 - 01", published_at: "2026-09-01T00:00:00Z", size_bytes: 1024, locator_display: "https://example.test/seed" },
      torrent: { state: "not_applicable" as const },
      evaluation: { state: "evaluated" as const, verdict: "accepted_pending_download" as const, reason_code: "accepted", parsed_facts: { episode: 1, group_name: "G", special: false } },
      files: [{ file_ref: "file-1", ordinal: 0, source_path: "测试番/01.mkv", filename: "01.mkv", size_bytes: 512, media_kind: "video" as const, included: false, reason_code: "file_not_in_matched", safety: "valid" as const }],
      download_action: { allowed: downloadKind === "retry", kind: downloadKind, reason_code: downloadKind === "retry" ? null : "not_allowed" },
      // The only file is not included and the write set is empty: absent and 0/0 (exactly how the server treats a decidable seed that needs no writes).
      library_overlap: { verdict: "absent" as const, present_files: 0, pending_files: 0 },
    };
  }

  function previewView(): RSSListView {
    return ownerRssListView({ mode: "owner_draft", groups: [{ ...ownerGroup("selected", "selected", true, 90), items: [viewItem()] }] });
  }

  function stubPreviewFetch(view = previewView()) {
    const calls: { url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming")) {
        return new Response(JSON.stringify({ language_version: "1", catalog_version: "1", variables: [], functions: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (url.includes("/anime/rss-list-view")) {
        const body = init?.body ? JSON.parse(String(init.body)) as { input_source?: string } : {};
        calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
        return new Response(JSON.stringify({ ...view, mode: body.input_source === "owner_draft" ? "owner_draft" : "owner" }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), { status: 400, headers: { "Content-Type": "application/json" } });
    }));
    return calls;
  }

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("owner_draft preview shows disabled actions, server file exclusion reason, and never fetches a provider", async () => {
    const calls = stubPreviewFetch();
    const workspace = workspaceFixture();
    workspace.subgroups.subgroups.entries = [
      { provider: "anibt", group_ref: "selected", display_name: "selected", selection_mode: "auto", manual_selected: null, effective_selected: true, score: 90 },
    ];
    workspace.releases = [{
      id: "release-1", rss_source_id: "selected", stable_item_key: "stable-1",
      raw_facts: { title: "[G] 测试番 - 01" }, source_facts: { identity_kind: "guid", identity_value: "stable-1" },
      parsed_facts: { episode: 1, group_name: "G", special: false }, verdict: "accepted_pending_download", reason_code: "accepted", version: 1,
    }];
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rules" });

    fireEvent.click(screen.getByRole("button", { name: t("tabs.rules.preview") }));
    await screen.findByText(t("rss.previewBadge"));
    await screen.findByRole("button", { name: t("rss.expandGroup", { name: "selected" }) });
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "selected" }) }));
    await screen.findByRole("button", { name: t("rss.expandFiles") });
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandFiles") }));
    expect(screen.getByText(new RegExp(t("rss.previewDisabled")))).toBeInTheDocument();
    expect(calls).toContainEqual({
      url: "/api/v1/anime/rss-list-view",
      body: { input_source: "owner_draft", managed_anime_id: "anime-1", rules_preview: workspace.rules.sections, limit_per_group: 100 },
    });
    // File-level filtering shows as the row's fate badge: file_* is a rule exclusion, the specific reason on hover.
    expect(screen.getByText(t("rss.fileExcluded.excluded_by_rule"))).toHaveAttribute("title", t("reason.file_not_in_matched"));
    expect(screen.getByRole("button", { name: /处理过程：预览模式下不可操作/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: labelValue(t("rss.retryRelease"), t("rss.previewDisabled")) })).toBeDisabled();
    expect(screen.getByRole("button", { name: t("rss.refresh") })).toBeDisabled();
    expect(screen.getByRole("button", { name: t("rss.scan") })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: t("rss.showFiltered") })).toHaveAttribute("aria-disabled", "true");
    const inputSources = calls.map(({ body }) => body && typeof body === "object" && "input_source" in body ? body.input_source : undefined);
    expect(inputSources).toEqual(["owner", "owner_draft"]);

    fireEvent.click(screen.getByRole("tab", { name: t("field.subgroup") }));
    await waitFor(() => expect(screen.queryByText(t("rss.previewBadge"))).not.toBeInTheDocument());
    expect(screen.getByText(t("anime.sources.selected"))).toBeInTheDocument();
  });

  it("owner 条目按服务端 verdict 隐藏已过滤项，显示被过滤只是呈现开关", async () => {
    const buildItem = (index: number, verdict: "accepted_pending_download" | "filtered", reasonCode: string) => ({
      snapshot_id: `release:release-${index}`,
      stable_seed_key: `stable-${index}`,
      seed: { title: `[G] 测试番 - 0${index}`, published_at: "2026-07-29T16:30:47Z", size_bytes: 1024, locator_display: "https://torrent.example.test/1.torrent" },
      torrent: { state: "not_applicable" as const },
      evaluation: { state: "evaluated" as const, verdict, reason_code: reasonCode, parsed_facts: { episode: index, special: false } },
      files: [],
      download_action: { allowed: false, kind: "none" as const },
      // No file rows means no structure to compare: undetermined.
      library_overlap: { verdict: "undetermined" as const, present_files: null, pending_files: null, reason_code: "no_rendered_targets" },
    });
    stubAuxiliaryFetch(ownerRssListView({
      groups: [{
        ...ownerGroup("selected", "selected"),
        items: [buildItem(1, "accepted_pending_download", "accepted"), buildItem(2, "filtered", "excluded_keyword")],
      }],
    }));
    const workspace = workspaceFixture();
    workspace.releases = [
      { id: "release-1", rss_source_id: "selected", stable_item_key: "stable-1", raw_facts: { title: "[G] 测试番 - 01" }, source_facts: { identity_kind: "guid", identity_value: "stable-1" }, parsed_facts: { episode: 1, group_name: "G", special: false }, verdict: "accepted_pending_download", reason_code: "accepted", version: 1 },
      { id: "release-2", rss_source_id: "selected", stable_item_key: "stable-2", raw_facts: { title: "[G] 测试番 - 02" }, source_facts: { identity_kind: "guid", identity_value: "stable-2" }, parsed_facts: { episode: 2, group_name: "G", special: false }, verdict: "filtered", reason_code: "excluded_keyword", version: 1 },
    ];
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rss" });
    await screen.findByRole("button", { name: t("rss.expandGroup", { name: "selected" }) });
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "selected" }) }));

    expect(screen.getByText("[G] 测试番 - 01")).toBeInTheDocument();
    expect(screen.queryByText("[G] 测试番 - 02")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("checkbox", { name: t("rss.showFiltered") }));
    expect(screen.getByText("[G] 测试番 - 02")).toBeInTheDocument();
    expect(screen.getByText("[G] 测试番 - 01")).toBeInTheDocument();
  });

  it("AniBT 标签过滤的种子显示具体原因与标签，同样受显示被过滤开关控制", async () => {
    stubAuxiliaryFetch(ownerRssListView({
      groups: [{
        ...ownerGroup("selected", "selected"),
        items: [{
          snapshot_id: "release:release-3",
          stable_seed_key: "stable-3",
          seed: { title: "[G] 测试番 - 03", published_at: "2026-07-29T16:30:47Z", size_bytes: 1024, locator_display: "https://torrent.example.test/3.torrent", anibt_tags: ["resolution:1080p", "language:CHT"] },
          torrent: { state: "not_applicable" as const },
          evaluation: { state: "evaluated" as const, verdict: "filtered" as const, reason_code: "anibt_tag_whitelist_unmet", reason_detail: "missing: language:CHS", parsed_facts: { episode: 3, special: false } },
          files: [],
          download_action: { allowed: false, kind: "none" as const },
          library_overlap: { verdict: "undetermined" as const, present_files: null, pending_files: null, reason_code: "no_rendered_targets" },
        }],
      }],
    }));
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged: () => {}, initialTab: "rss" });
    await screen.findByRole("button", { name: t("rss.expandGroup", { name: "selected" }) });
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "selected" }) }));
    expect(screen.queryByText("[G] 测试番 - 03")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("checkbox", { name: t("rss.showFiltered") }));
    expect(screen.getByText("[G] 测试番 - 03")).toBeInTheDocument();
    const reason = t("anibtTags.reasonMissing", { tags: "CHS" });
    expect(screen.getByText(t("verdict.filteredWithReason", { reason }))).toBeInTheDocument();
    // All tags are shown, none collapsed.
    const tags = screen.getByLabelText(t("tagFilters.seedRowAria"));
    expect(within(tags).getByText("1080p")).toBeInTheDocument();
    expect(within(tags).getByText("CHT")).toBeInTheDocument();
  });

  it("previewing an unsaved rules edit stays in preview mode", async () => {
    const calls = stubPreviewFetch();
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged: () => {}, initialTab: "rules" });
    editReleaseFilter("sample");
    fireEvent.click(screen.getByRole("button", { name: t("tabs.rules.preview") }));
    await screen.findByText(t("rss.previewBadge"));
    await waitFor(() => expect(calls.some(({ body }) => (body as { input_source?: string } | undefined)?.input_source === "owner_draft")).toBe(true));
    expect(screen.getByText(t("rss.previewBadge"))).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: t("rss.showFiltered") })).toHaveAttribute("aria-disabled", "true");
  });

  it("unsaved metadata edits render in the RSS tab without saving (owner_draft metadata_preview)", async () => {
    const calls = stubPreviewFetch();
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged: () => {}, initialTab: "metadata" });
    fireEvent.change(screen.getByRole("textbox", { name: t("forms.year") }), { target: { value: "2031" } });
    fireEvent.click(screen.getByRole("tab", { name: t("tabs.names.rss") }));
    await waitFor(() => expect(calls).toContainEqual({
      url: "/api/v1/anime/rss-list-view",
      body: {
        input_source: "owner_draft",
        managed_anime_id: "anime-1",
        metadata_preview: { bgm: null, tmdb: null, title_override: "测试番", media_kind_override: null, total_episodes: null, year: 2031, season: null },
        limit_per_group: 100,
      },
    }));
    expect(await screen.findByText(t("rss.previewNoticeMetadata"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("rss.exitPreview") })).not.toBeInTheDocument();
    // The preview does not save: the metadata tab still has unsaved changes.
    expect(hasUnsavedDot(tabNamed(t("tabs.names.metadata")))).toBe(true);
  });

  it("leaving RSS tab exits rules preview", async () => {
    stubPreviewFetch();
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged: () => {}, initialTab: "rules" });
    fireEvent.click(screen.getByRole("button", { name: t("tabs.rules.preview") }));
    await screen.findByText(t("rss.previewBadge"));
    fireEvent.click(screen.getByRole("tab", { name: t("tabs.names.metadata") }));
    fireEvent.click(screen.getByRole("tab", { name: "RSS" }));
    await waitFor(() => expect(screen.queryByText(t("rss.previewBadge"))).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: t("rss.refresh") })).toBeEnabled();
  });

  it("字幕组编辑不清空最近一次服务端订阅事实", async () => {
    const subscribedView = ownerRssListView({ groups: [ownerGroup("selected", "selected", true)] });
    stubAuxiliaryFetch(subscribedView);
    const workspace = workspaceFixture();
    workspace.subgroups.subgroups.entries = [
      { provider: "anibt", group_ref: "selected", display_name: "selected", selection_mode: "auto", manual_selected: null, effective_selected: true, score: 90 },
    ];
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rss" });
    await screen.findByRole("button", { name: /展开 selected/ });
    fireEvent.click(screen.getByRole("tab", { name: t("field.subgroup") }));
    expect(screen.getByText(t("anime.sources.selected"))).toBeInTheDocument();
    const policy = screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "selected" }) });
    fireEvent.click(within(policy).getByRole("radio", { name: t("forms.sources.policyAlways") }));
    await waitFor(() => expect(screen.getByText(t("anime.sources.selected"))).toBeInTheDocument());
  });
  it("owner 的订阅事实来自服务端生效选择，不被 owner 读模型的全选事实覆盖", async () => {
    // The owner-mode read model marks every existing source effective_selected=true; the subgroup
//   row's facts must come from the workspace's server-effective selection, otherwise unselected
//   groups would display as subscribed.
    stubAuxiliaryFetch(ownerRssListView({ groups: [ownerGroup("selected", "selected", true)] }));
    const workspace = workspaceFixture();
    workspace.subgroups.subgroups.entries = [
      { provider: "anibt", group_ref: "selected", display_name: "selected", selection_mode: "auto", manual_selected: null, effective_selected: false, score: 90 },
    ];
    workspace.subgroups.effective_selection = [
      {
        available: true, availability_generation: 1,
        id: "selection-selected",
        subscription_id: "subscription-1",
        provider: "anibt",
        group_ref: "selected",
        display_name: "selected",
        selection_mode: "auto",
        manual_selected: null,
        effective_selected: false,
        effective_score: 90,
        version: 1,
      },
    ];
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "subgroups" });
    expect(screen.getByText(t("anime.sources.unselected"))).toBeInTheDocument();
    expect(screen.queryByText(t("forms.sources.notPreviewed"))).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "RSS" }));
    await screen.findByRole("button", { name: t("rss.expandGroup", { name: "selected" }) });
    fireEvent.click(screen.getByRole("tab", { name: t("field.subgroup") }));
    expect(screen.getByText(t("anime.sources.unselected"))).toBeInTheDocument();
    expect(screen.queryByText(t("anime.sources.selected"))).not.toBeInTheDocument();
  });
});
describe("AnimeTabs 元数据标题/季度行编辑", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

  function stubMetadataPatchFetch(opts: { reevaluateFails?: boolean; patchFails?: boolean } = {}) {
    let metadataVersion = 1;
    const fetchMock = vi.fn<FetchStub>(async (input, init) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming")) {
        return new Response(
          JSON.stringify({ language_version: "1", catalog_version: "1", variables: [], functions: [] }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url.includes("/metadata/search")) {
        return new Response(
          JSON.stringify({
            candidates: [
              {
                provider: "tmdb",
                provider_work_id: "265010",
                title: "匹配到的番名",
                media_kind: "tv",
                details_status: "available",
                seasons: [{ season_number: 1, name: "第一季", episode_count: 12, air_date: "2024-01-01", is_special: false }],
                suggested_season: 1,
                year: 2024,
                score: 0.9,
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (init?.method === "PATCH" && url.includes("/anime/anime-1/metadata")) {
        if (opts.patchFails) {
          return new Response(JSON.stringify({ code: "INTERNAL", message: "boom" }), {
            status: 500,
            headers: { "Content-Type": "application/json" },
          });
        }
        metadataVersion += 1;
        return new Response(JSON.stringify({ managed_anime_id: "anime-1", bgm: null, tmdb: null, version: metadataVersion }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      // After a successful metadata save the frontend schedules the re-evaluation automatically.
      if (init?.method === "POST" && url.includes("/anime/anime-1/releases/reevaluate")) {
        if (opts.reevaluateFails) {
          return new Response(JSON.stringify({ code: "INTERNAL", message: "boom" }), {
            status: 500,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(
          JSON.stringify({ managed_anime_id: "anime-1", accepted: true, scheduled_count: 2 }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
        status: 400,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function metadataPatchBodies(fetchMock: ReturnType<typeof vi.fn<FetchStub>>) {
    return fetchMock.mock.calls
      .filter(([input, init]) => String(input).includes("/anime/anime-1/metadata") && init?.method === "PATCH")
      .map(([, init]) => JSON.parse(String(init?.body)) as Record<string, unknown>);
  }

  function reevaluateCalls(fetchMock: ReturnType<typeof vi.fn<FetchStub>>) {
    return fetchMock.mock.calls.filter(
      ([input, init]) => String(input).includes("/anime/anime-1/releases/reevaluate") && init?.method === "POST",
    );
  }

  function footerStatus() {
    return within(ownerFooter()).queryByRole("status");
  }

  const titleRegion = () => screen.getByRole("region", { name: t("tabs.metadata.title") });
  const titleInput = () => within(titleRegion()).getByRole("textbox", { name: t("tabs.metadata.title") });
  const titleEditButton = () => within(titleRegion()).getByRole("button", { name: t("tabs.metadata.editTitleAria") });
  function editTitle(value: string) {
    fireEvent.click(titleEditButton());
    fireEvent.change(titleInput(), { target: { value } });
    fireEvent.click(within(titleRegion()).getByRole("button", { name: t("tabs.metadata.saveTitle") }));
  }

  it("标题：只读显示，点编辑后修改；保存标题后由 footer 保存一次上送 title_override（去空白），不安排重评估", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: { work_id: "486109" },
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      version: 3,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    // By default the fixed title displays read-only (older data without title_override falls back to the title at subscribe time), no input rendered.
    expect(titleRegion()).toHaveTextContent("测试番");
    expect(within(titleRegion()).queryByRole("textbox")).not.toBeInTheDocument();
    expect(footerSave()).toBeDisabled();
    fireEvent.click(titleEditButton());
    expect(titleInput()).toHaveValue("测试番");
    fireEvent.change(titleInput(), { target: { value: "  新标题  " } });
    fireEvent.click(within(titleRegion()).getByRole("button", { name: t("tabs.metadata.saveTitle") }));
    // After saving the title it returns to the read-only display, waiting for the footer save.
    expect(within(titleRegion()).queryByRole("textbox")).not.toBeInTheDocument();
    expect(titleRegion()).toHaveTextContent("新标题");
    expect(footerStatus()).toHaveTextContent(t("act.unsavedCount", { n: 1 }));
    expect(hasUnsavedDot(tabNamed(t("tabs.names.metadata")))).toBe(true);
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.saved")));
    expect(toast.success).toHaveBeenCalledTimes(1);

    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toEqual({
      expected_version: 3,
      bgm: { work_id: "486109" },
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      title_override: "新标题",
      media_kind_override: null,
      total_episodes: null,
      year: null,
      season: null,
    });
    // The title never affects release evaluation: no re-evaluation scheduled.
    expect(reevaluateCalls(fetchMock)).toHaveLength(0);
    // After saving the baseline catches up with the edits: no unsaved changes remain.
    await waitFor(() => expect(footerSave()).toBeDisabled());
    expect(footerStatus()).not.toBeInTheDocument();
    expect(hasUnsavedDot(tabNamed(t("tabs.names.metadata")))).toBe(false);
  });

  it("标题：空标题不能保存并提示；取消恢复原标题，不产生未保存", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: null,
      title_override: "旧覆盖",
      version: 2,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    expect(titleRegion()).toHaveTextContent("旧覆盖");
    fireEvent.click(titleEditButton());
    fireEvent.change(titleInput(), { target: { value: "  " } });
    fireEvent.click(within(titleRegion()).getByRole("button", { name: t("tabs.metadata.saveTitle") }));
    expect(within(titleRegion()).getByRole("alert")).toHaveTextContent(t("tabs.metadata.titleRequired"));
    expect(titleInput()).toBeInTheDocument();
    expect(footerSave()).toBeDisabled();

    fireEvent.change(titleInput(), { target: { value: "改了又取消" } });
    fireEvent.click(within(titleRegion()).getByRole("button", { name: t("act.cancel") }));
    expect(within(titleRegion()).queryByRole("textbox")).not.toBeInTheDocument();
    expect(titleRegion()).toHaveTextContent("旧覆盖");
    expect(footerSave()).toBeDisabled();
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);
  });

  it("TMDB 匹配只回填绑定，不碰标题；标题与绑定由一次保存一起上送并安排重评估", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: null,
      title_override: "我的覆盖",
      version: 1,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });
    const tmdbRegion = screen.getByRole("region", { name: t("forms.tmdb") });
    fireEvent.click(within(tmdbRegion).getByRole("button", { name: t("forms.matchProvider", { provider: "TMDB" }) }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /匹配到的番名/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    // A match never backfills the title: the current fixed title stands.
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(titleRegion()).toHaveTextContent("我的覆盖");
    editTitle("新覆盖");
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.savedReevaluated", { n: 2 })));
    expect(toast.success).toHaveBeenCalledTimes(1);

    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toEqual({
      expected_version: 1,
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 1 },
      title_override: "新覆盖",
      // The unset type is filled once from the matched work's kind.
      media_kind_override: "tv",
      // Selecting a candidate pre-fills total episodes from the chosen season's count (S1 has 12).
      total_episodes: 12,
      // The match fills the candidate year into the form, persisted with the save.
      year: 2024,
      // Season unset: pre-fills once from the chosen TMDB season.
      season: 1,
    });
    expect(reevaluateCalls(fetchMock)).toHaveLength(1);
  });

  it("标题：没有固定标题的旧数据显示订阅时的标题，不算未保存；绑定保存时一并写入", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = { managed_anime_id: "anime-1", bgm: { work_id: "486109" }, tmdb: null, title_override: null, version: 5 };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });
    expect(titleRegion()).toHaveTextContent("测试番");
    expect(footerSave()).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.bangumi") }) }));
    clickFooterSave();
    await waitFor(() => expect(metadataPatchBodies(fetchMock)).toHaveLength(1));
    expect(metadataPatchBodies(fetchMock)[0]).toMatchObject({ title_override: "测试番" });
  });

  it("类型行：只有剧集与电影两项；选电影由 footer 保存并安排重评估", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: { work_id: "486109" },
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      title_override: null,
      media_kind_override: "tv",
      total_episodes: null,
      version: 3,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    const kindRegion = screen.getByRole("region", { name: t("forms.mediaKind") });
    expect(within(kindRegion).getAllByRole("radio")).toHaveLength(2);
    expect(within(kindRegion).getByRole("radio", { name: t("forms.tv") })).toHaveAttribute("aria-checked", "true");

    fireEvent.click(within(kindRegion).getByRole("radio", { name: t("forms.movie") }));
    expect(within(kindRegion).getByRole("radio", { name: t("forms.movie") })).toHaveAttribute("aria-checked", "true");
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);
    expect(footerStatus()).toHaveTextContent(t("act.unsavedCount", { n: 1 }));

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.savedReevaluated", { n: 2 })));

    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toEqual({
      expected_version: 3,
      bgm: { work_id: "486109" },
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      title_override: "测试番",
      media_kind_override: "movie",
      total_episodes: null,
      year: null,
      season: null,
    });
    expect(reevaluateCalls(fetchMock)).toHaveLength(1);
  });

  it("类型行：未设置且未关联 TMDB 时显示剧集", () => {
    stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: null,
      title_override: null,
      media_kind_override: null,
      total_episodes: null,
      version: 5,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    const kindRegion = screen.getByRole("region", { name: t("forms.mediaKind") });
    expect(within(kindRegion).getByRole("radio", { name: t("forms.tv") })).toHaveAttribute("aria-checked", "true");
    expect(footerSave()).toBeDisabled();
  });

  it("类型行：已设置电影时可改回剧集", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: null,
      title_override: null,
      media_kind_override: "movie",
      total_episodes: null,
      version: 5,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    const kindRegion = screen.getByRole("region", { name: t("forms.mediaKind") });
    expect(within(kindRegion).getByRole("radio", { name: t("forms.movie") })).toHaveAttribute("aria-checked", "true");

    fireEvent.click(within(kindRegion).getByRole("radio", { name: t("forms.tv") }));
    clickFooterSave();

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.savedReevaluated", { n: 2 })));
    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toEqual({
      expected_version: 5,
      bgm: null,
      tmdb: null,
      title_override: "测试番",
      media_kind_override: "tv",
      total_episodes: null,
      year: null,
      season: null,
    });
  });

  it("TMDB 匹配确认写入与已保存相同的绑定时不产生 dirty（匹配不再携带标题提示）", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 1 },
      // A bound TMDB tuple always comes with the type it backfilled.
      media_kind_override: "tv",
      // Equal to the candidate's S1 episodes/year/season: confirms that with an identical binding even the prefill produces no diff.
      total_episodes: 12,
      year: 2024,
      season: 1,
      version: 1,
    };
    const onDirtyChange = vi.fn();
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, onDirtyChange, initialTab: "metadata" });
    const tmdbRegion = screen.getByRole("region", { name: t("forms.tmdb") });
    fireEvent.click(within(tmdbRegion).getByRole("button", { name: t("forms.matchProvider", { provider: "TMDB" }) }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /匹配到的番名/ }));
    fireEvent.click(within(dialog).getByRole("checkbox", { name: t("forms.alsoSetTitle", { title: "匹配到的番名" }) }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    // Regression: the binding confirm writes equal to saved — no false dirty, and closing the dialog never misfires the discard confirmation.
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(footerStatus()).not.toBeInTheDocument();
    expect(footerSave()).toBeDisabled();
    expect(onDirtyChange).not.toHaveBeenCalledWith(true);
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);
  });

  it("标题候选：编辑时按来源顺序去重显示，隐藏现值；点击即写入并退出编辑，由 footer 保存上送", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: null,
      title_suggestions: [
        { source: "tmdb_cn", text: "候选甲" },
        { source: "tmdb_original", text: "Candidate B" },
        { source: "bgm_cn", text: "候选甲" },
        { source: "provider", text: "测试番" },
      ],
      version: 1,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    expect(screen.queryByRole("group", { name: t("tabs.metadata.suggestionsAria") })).not.toBeInTheDocument();
    fireEvent.click(titleEditButton());
    const badges = within(screen.getByRole("group", { name: t("tabs.metadata.suggestionsAria") })).getAllByRole("button");
    // The duplicate text keeps its first source; the provider title equals the current one and is hidden.
    expect(badges).toHaveLength(2);
    expect(badges[0]).toHaveTextContent(`${t("tabs.metadata.suggestionSource.tmdb_cn")}候选甲`);
    expect(badges[1]).toHaveTextContent(`${t("tabs.metadata.suggestionSource.tmdb_original")}Candidate B`);

    fireEvent.click(badges[1]!);
    expect(within(titleRegion()).queryByRole("textbox")).not.toBeInTheDocument();
    expect(titleRegion()).toHaveTextContent("Candidate B");
    expect(footerStatus()).toHaveTextContent(t("act.unsavedCount", { n: 1 }));

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.saved")));
    expect(metadataPatchBodies(fetchMock)[0]).toMatchObject({ title_override: "Candidate B" });
  });

  async function confirmTmdbMatch() {
    const tmdbRegion = screen.getByRole("region", { name: t("forms.tmdb") });
    fireEvent.click(within(tmdbRegion).getByRole("button", { name: t("forms.matchProvider", { provider: "TMDB" }) }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /匹配到的番名/ }));
    return dialog;
  }
  const alsoSetTitleName = t("forms.alsoSetTitle", { title: "匹配到的番名" });

  it("TMDB 匹配确认：标题仍是来源标题时勾选预选，绑定与标题由一次保存一起上送；取消勾选则标题不动", async () => {
    for (const checked of [true, false]) {
      const fetchMock = stubMetadataPatchFetch();
      const workspace = workspaceFixture();
      workspace.metadata = { managed_anime_id: "anime-1", bgm: null, tmdb: null, version: 1 };
      const { unmount } = renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

      const dialog = await confirmTmdbMatch();
      const box = within(dialog).getByRole("checkbox", { name: alsoSetTitleName });
      expect(box).toBeChecked();
      if (!checked) fireEvent.click(box);
      fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(titleRegion()).toHaveTextContent(checked ? "匹配到的番名" : "测试番");

      clickFooterSave();
      await waitFor(() => expect(metadataPatchBodies(fetchMock)).toHaveLength(1));
      expect(metadataPatchBodies(fetchMock)[0]).toMatchObject({
        tmdb: { work_id: "265010", media_kind: "tv", season: 1 },
        title_override: checked ? "匹配到的番名" : "测试番",
      });
      unmount();
    }
  });

  it("TMDB 匹配确认：标题已被改过时勾选不预选；候选名与现标题相同时不显示", async () => {
    stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = { managed_anime_id: "anime-1", bgm: null, tmdb: null, title_override: "我的覆盖", version: 1 };
    const { unmount } = renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });
    let dialog = await confirmTmdbMatch();
    expect(within(dialog).getByRole("checkbox", { name: alsoSetTitleName })).not.toBeChecked();
    unmount();

    const same = workspaceFixture();
    same.metadata = { managed_anime_id: "anime-1", bgm: null, tmdb: null, title_override: "匹配到的番名", version: 1 };
    renderTabs({ animeID: "anime-1", workspace: same, onChanged: () => {}, initialTab: "metadata" });
    dialog = await confirmTmdbMatch();
    expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("年份：TMDB 匹配只把年份填进表单，不落库；改后由 footer 保存上送编辑值", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = { managed_anime_id: "anime-1", bgm: null, tmdb: null, version: 1 };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });
    const yearRegion = screen.getByRole("region", { name: t("forms.year") });
    expect(within(yearRegion).getByRole("textbox")).toHaveValue("");

    const tmdbRegion = screen.getByRole("region", { name: t("forms.tmdb") });
    fireEvent.click(within(tmdbRegion).getByRole("button", { name: t("forms.matchProvider", { provider: "TMDB" }) }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /匹配到的番名/ }));
    // The title stays as it is: the pre-checked "also set the title" is switched off.
    fireEvent.click(within(dialog).getByRole("checkbox", { name: t("forms.alsoSetTitle", { title: "匹配到的番名" }) }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    // After the match: the input shows the candidate year; binding/type/year/season/total-episodes are five unsaved spots with no PATCH sent.
    await waitFor(() => expect(within(yearRegion).getByRole("textbox")).toHaveValue("2024"));
    expect(footerStatus()).toHaveTextContent(t("act.unsavedCount", { n: 5 }));
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);

    // The user edits the year: invalid input is not written back and reports on blur.
    const input = within(yearRegion).getByRole("textbox");
    fireEvent.change(input, { target: { value: "99" } });
    fireEvent.blur(input);
    expect(within(yearRegion).getByRole("alert")).toHaveTextContent(t("forms.yearInvalid"));
    fireEvent.change(input, { target: { value: "2023" } });
    expect(within(yearRegion).queryByRole("alert")).not.toBeInTheDocument();
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.savedReevaluated", { n: 2 })));
    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({ tmdb: { work_id: "265010", media_kind: "tv", season: 1 }, year: 2023 });
  });

  it("总集数：直接输入，非法输入失焦报错不写回；与年份一起由一次保存上送，类型针保留", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: null,
      media_kind_override: "movie",
      year: 2020,
      total_episodes: 6,
      version: 1,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });
    const yearRegion = screen.getByRole("region", { name: t("forms.year") });
    fireEvent.change(within(yearRegion).getByRole("textbox"), { target: { value: "2021" } });

    const totalRegion = screen.getByRole("region", { name: t("forms.totalEpisodes") });
    const totalInput = within(totalRegion).getByRole("textbox");
    expect(totalInput).toHaveValue("6");
    fireEvent.change(totalInput, { target: { value: "x" } });
    fireEvent.blur(totalInput);
    expect(within(totalRegion).getByRole("alert")).toHaveTextContent(t("forms.totalInvalid"));
    // Invalid input not written back: only the year counts as one unsaved spot.
    expect(footerStatus()).toHaveTextContent(t("act.unsavedCount", { n: 1 }));
    fireEvent.change(totalInput, { target: { value: "1" } });
    expect(within(totalRegion).queryByRole("alert")).not.toBeInTheDocument();
    expect(footerStatus()).toHaveTextContent(t("act.unsavedCount", { n: 2 }));
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.savedReevaluated", { n: 2 })));
    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({ media_kind_override: "movie", total_episodes: 1, year: 2021 });
  });

  it("总集数：清空上送 total_episodes: null", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = { managed_anime_id: "anime-1", bgm: null, tmdb: null, total_episodes: 12, version: 1 };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });
    const totalRegion = screen.getByRole("region", { name: t("forms.totalEpisodes") });
    fireEvent.change(within(totalRegion).getByRole("textbox"), { target: { value: "" } });
    expect(within(totalRegion).queryByRole("alert")).not.toBeInTheDocument();

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalled());
    expect(metadataPatchBodies(fetchMock)[0]).toMatchObject({ total_episodes: null });
  });

  it("绑定保存回传 title_override 现值（省略会被服务端当清空）", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: { work_id: "486109" },
      tmdb: null,
      title_override: "保留我",
      version: 4,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    expect(titleRegion()).toHaveTextContent("保留我");
    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.bangumi") }) }));
    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.savedReevaluated", { n: 2 })));

    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toEqual({ expected_version: 4, bgm: null, tmdb: null, title_override: "保留我", media_kind_override: null, total_episodes: null, year: null, season: null });
  });

  it("绑定保存成功后自动安排重评估（R4），重评估失败不拖累保存", async () => {
    const fetchMock = stubMetadataPatchFetch({ reevaluateFails: true });
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: { work_id: "486109" },
      tmdb: null,
      version: 3,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.bangumi") }) }));
    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(t("tabs.metadata.reevaluateFailed")));
    // The save itself succeeds: one success notice, unsaved count to zero, the footer reports no failure.
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.saved")));
    expect(reevaluateCalls(fetchMock)).toHaveLength(1);
    expect(metadataPatchBodies(fetchMock)).toHaveLength(1);
    await waitFor(() => expect(footerSave()).toBeDisabled());
    expect(within(ownerFooter()).queryByRole("alert")).not.toBeInTheDocument();
  });

  it("保存失败：footer 报出失败的部分，改动保持未保存", async () => {
    const fetchMock = stubMetadataPatchFetch({ patchFails: true });
    const workspace = workspaceFixture();
    workspace.metadata = { managed_anime_id: "anime-1", bgm: { work_id: "486109" }, tmdb: null, version: 3 };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.bangumi") }) }));
    clickFooterSave();
    await waitFor(() =>
      expect(within(ownerFooter()).getByRole("alert")).toHaveTextContent(t("act.partFailed", { part: t("tabs.names.metadata") })),
    );
    const { toast } = await import("sonner");
    expect(toast.success).not.toHaveBeenCalled();
    expect(reevaluateCalls(fetchMock)).toHaveLength(0);
    expect(footerSave()).toBeEnabled();
    expect(hasUnsavedDot(tabNamed(t("tabs.names.metadata")))).toBe(true);
  });

  it("季度行：季度独立于 TMDB 绑定，footer 保存上送季度并安排重评估，绑定原样回传", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: { work_id: "486109" },
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      title_override: "保留我",
      version: 3,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    const seasonRegion = screen.getByRole("region", { name: t("forms.season") });
    // The TMDB link shows the TMDB season number; the input is empty while the anime's season is unset.
    expect(screen.getByRole("link", { name: /265010/ })).toHaveTextContent(t("forms.seasonN", { n: "2" }));
    const seasonInput = within(seasonRegion).getByLabelText(t("forms.seasonAria"));
    expect(seasonInput).toHaveValue("");
    fireEvent.change(seasonInput, { target: { value: "3" } });
    expect(footerStatus()).toHaveTextContent(t("act.unsavedCount", { n: 1 }));
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);

    clickFooterSave();
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.metadata.savedReevaluated", { n: 2 })));

    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toEqual({
      expected_version: 3,
      bgm: { work_id: "486109" },
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      title_override: "保留我",
      media_kind_override: null,
      total_episodes: null,
      year: null,
      season: 3,
    });
    expect(within(seasonRegion).getByLabelText(t("forms.seasonAria"))).toHaveValue("3");
  });

  it("季度行：没有 TMDB 绑定也能保存季度", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = { managed_anime_id: "anime-1", bgm: null, tmdb: null, version: 2 };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    const seasonRegion = screen.getByRole("region", { name: t("forms.season") });
    fireEvent.change(within(seasonRegion).getByLabelText(t("forms.seasonAria")), { target: { value: "4" } });
    expect(footerStatus()).toHaveTextContent(t("act.unsavedCount", { n: 1 }));
    clickFooterSave();

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalled());
    expect(metadataPatchBodies(fetchMock)[0]).toMatchObject({ tmdb: null, season: 4 });
  });

  it("季度行：SP 按钮填入第 0 季；清空上送 null", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      season: 2,
      version: 2,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    const seasonRegion = screen.getByRole("region", { name: t("forms.season") });
    fireEvent.click(within(seasonRegion).getByRole("button", { name: "SP" }));
    expect(within(seasonRegion).getByLabelText(t("forms.seasonAria"))).toHaveValue("0");
    fireEvent.change(within(seasonRegion).getByLabelText(t("forms.seasonAria")), { target: { value: "" } });
    clickFooterSave();

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalled());
    const bodies = metadataPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({ tmdb: { work_id: "265010", media_kind: "tv", season: 2 }, season: null });
  });

  it("季度行：非法输入失焦内联报错，不写草稿也不发请求", async () => {
    const fetchMock = stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = { managed_anime_id: "anime-1", bgm: null, tmdb: null, season: 2, version: 2 };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    const seasonRegion = screen.getByRole("region", { name: t("forms.season") });
    const seasonInput = within(seasonRegion).getByLabelText(t("forms.seasonAria"));
    fireEvent.change(seasonInput, { target: { value: "x" } });
    fireEvent.blur(seasonInput);

    expect(within(seasonRegion).getByRole("alert")).toHaveTextContent(t("forms.seasonInvalid"));
    expect(footerSave()).toBeDisabled();
    expect(metadataPatchBodies(fetchMock)).toHaveLength(0);
    // After the fix the error disappears and the value writes into the draft.
    fireEvent.change(seasonInput, { target: { value: "5" } });
    expect(within(seasonRegion).queryByRole("alert")).not.toBeInTheDocument();
    expect(footerSave()).toBeEnabled();
  });

  it("季度行：电影没有季度，行降级为说明且无编辑入口", async () => {
    stubMetadataPatchFetch();
    const workspace = workspaceFixture();
    workspace.metadata = {
      managed_anime_id: "anime-1",
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "movie", season: 0 },
      version: 2,
    };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "metadata" });

    const seasonRegion = screen.getByRole("region", { name: t("forms.season") });
    expect(within(seasonRegion).getByText(t("forms.movieNoSeason"))).toBeInTheDocument();
    expect(within(seasonRegion).queryByRole("textbox")).not.toBeInTheDocument();
    expect(within(seasonRegion).queryByRole("button", { name: "SP" })).not.toBeInTheDocument();
  });
});

// Discovery interaction fixes: after discovery the list refreshes immediately without a false
// dirty, and a stopped subscription disables the discover button with a start-first note.
describe("AnimeTabs Subgroups discover 刷新", () => {
  type DiscoverStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

  function stubDiscoverFetch() {
    const fetchMock = vi.fn<DiscoverStub>(async (input, init) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming")) {
        return new Response(
          JSON.stringify({ language_version: "1", catalog_version: "1", variables: [], functions: [] }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (init?.method === "POST" && url.includes("/subgroups/discover")) {
        return new Response(
          JSON.stringify({
            managed_anime_id: "anime-1",
            outcome: "discovered",
            subgroups: {
              managed_anime_id: "anime-1",
              subgroups: {
                max_rss: 3,
                entries: [
                  {
                    provider: "anibt",
                    group_ref: "g-new",
                    display_name: "新组一号",
                    selection_mode: "auto",
                    manual_selected: null,
                    effective_selected: true,
                    score: 90,
                  },
                ],
              },
              effective_selection: [],
              rss_sources: [],
              version: 2,
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (init?.method === "PATCH" && url.includes("/anime/anime-1/subgroups")) {
        return new Response(JSON.stringify({
          managed_anime_id: "anime-1",
          subgroups: { max_rss: 3, entries: [] },
          effective_selection: [],
          rss_sources: [],
          version: 3,
        }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
        status: 400,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function discoverPatchBodies(fetchMock: ReturnType<typeof vi.fn<DiscoverStub>>) {
    return fetchMock.mock.calls
      .filter(([input, init]) => String(input).includes("/anime/anime-1/subgroups") && init?.method === "PATCH")
      .map(([, init]) => JSON.parse(String(init?.body)) as Record<string, unknown>);
  }

  it("discover 是即时动作：列表立即刷新且重置基线（无假 dirty）；之后的编辑以新版本保存并含新组", async () => {
    const fetchMock = stubDiscoverFetch();
    vi.clearAllMocks();
    const onChanged = vi.fn();
    const onDirtyChange = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(
      <QueryClientProvider client={client}>
        <AnimeTabs
          host={{ mode: "owner", animeID: "anime-1", workspace: workspaceFixture(), capability: workspaceCapability("active"), onChanged }}
          onDirtyChange={onDirtyChange}
          initialTab="subgroups"
        />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: t("config.discovery.refresh") }));

    // The list reflects newly discovered groups immediately (baseline replaced directly, no refetch wait).
    await waitFor(() => expect(screen.getByText("新组一号")).toBeInTheDocument());
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("config.discovery.found", { n: 1 })));
    expect(onChanged).toHaveBeenCalled();
    // Discovery results are already persisted server-side: they do not count as unsaved, the save button stays disabled and no discard confirmation pops.
    expect(footerSave()).toBeDisabled();
    expect(within(ownerFooter()).queryByRole("status")).not.toBeInTheDocument();
    expect(hasUnsavedDot(tabNamed(t("tabs.names.subgroups")))).toBe(false);
    expect(onDirtyChange).not.toHaveBeenCalledWith(true);
    expect(discoverPatchBodies(fetchMock)).toHaveLength(0);

    // Changing the new group's policy afterwards: the save CASes on the discover response's version and the sent entries include the new group.
    const policy = screen.getByRole("radiogroup", { name: t("forms.sources.policyAria", { name: "新组一号" }) });
    fireEvent.click(within(policy).getByRole("radio", { name: t("forms.sources.policyNever") }));
    clickFooterSave();
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("tabs.subgroups.saved")));
    const bodies = discoverPatchBodies(fetchMock);
    expect(bodies).toHaveLength(1);
    expect(bodies[0]!.expected_version).toBe(2);
    const entries = (bodies[0]!.subgroups as { entries: Array<{ group_ref: string }> }).entries;
    expect(entries.some((entry) => entry.group_ref === "g-new")).toBe(true);

    // After the refetch completes (workspace updated to the server snapshot including the new group) the false dirty is gone.
    const refreshed = workspaceFixture();
    refreshed.subgroups.version = 3;
    refreshed.subgroups.subgroups = {
      max_rss: 3,
      entries: [
        {
          provider: "anibt",
          group_ref: "g-new",
          display_name: "新组一号",
          selection_mode: "manual",
          manual_selected: false,
          effective_selected: true,
          score: 90,
        },
      ],
    };
    view.rerender(
      <QueryClientProvider client={client}>
        <AnimeTabs
          host={{ mode: "owner", animeID: "anime-1", workspace: refreshed, capability: workspaceCapability("active"), onChanged }}
          onDirtyChange={onDirtyChange}
          initialTab="subgroups"
        />
      </QueryClientProvider>,
    );
    await waitFor(() => expect(footerSave()).toBeDisabled());
    expect(hasUnsavedDot(tabNamed(t("tabs.names.subgroups")))).toBe(false);
  });

  it("stopped 订阅也允许刷新字幕组（一次性任务场景）", () => {
    stubDiscoverFetch();
    const workspace = workspaceFixture();
    workspace.subscription = { ...workspace.subscription, state: "stopped" };
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "subgroups" });

    const button = screen.getByRole("button", { name: t("config.discovery.refresh") }) as HTMLButtonElement;
    expect(button.disabled).toBe(false);
    expect(screen.queryByText("订阅已停止，先启动订阅再发现字幕组")).not.toBeInTheDocument();
    // The refresh action itself is executable: clicking enters the refreshing state (fires fetch).
    fireEvent.click(button);
    expect(screen.getByRole("button", { name: t("act.refreshing") })).toBeInTheDocument();
  });
});

// The tag filter card: after subscribing it shows after the title filter; the AniBT row only when
// the anime has an AniBT source, the title-parsing row always. The value is this anime's own
// complete copy, edited in place, with the save replacing the whole section.
describe("AnimeTabs 标签过滤卡片", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  function anibtWorkspace(): AnimeWorkspace {
    const workspace = workspaceFixture();
    workspace.rules = {
      ...workspace.rules,
      sections: {
        ...workspace.rules.sections,
        tag_filters: { mode: "replace", value: { anibt: { require: ["language:CHS"], exclude: [] }, title: { require: [], exclude: ["sub_lang:TC"] } } },
      },
      effective_tag_filters: {
        anibt: { require: ["language:CHS"], exclude: [] },
        title: { require: [], exclude: ["sub_lang:TC"] },
      },
      has_anibt_source: true,
    };
    return workspace;
  }

  function stubRulesFetch(defaults?: BusinessPresetView) {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/config/tag-filters/title-vocabulary")) {
        return Response.json({
          implementation: "reltitle@test",
          config_digest: "lexicon:sha256:test",
          dimensions: [
            { dimension: "resolution", values: ["2160p", "1080p", "720p"] },
            { dimension: "sub_lang", values: ["SC", "TC", "JPSC"] },
          ],
        });
      }
      if (url.includes("/template-catalogs/naming")) {
        return Response.json({ language_version: "1", catalog_version: "1", variables: [], functions: [] });
      }
      if (url.includes("/config/business-preset") && defaults) {
        return Response.json(defaults);
      }
      if (init?.method === "PATCH" && url.includes("/anime/anime-1/rules")) {
        return Response.json({ rules: anibtWorkspace().rules, reevaluation: { managed_anime_id: "anime-1", accepted: true, scheduled_count: 0 } });
      }
      return Response.json({ code: "IGNORED", message: "ignored" }, { status: 400 });
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function tagCard() {
    const card = screen.getByText(t("tagFilters.title")).closest("section");
    expect(card).not.toBeNull();
    return card!;
  }

  function patchBody(fetchMock: ReturnType<typeof stubRulesFetch>) {
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).includes("/anime/anime-1/rules") && init?.method === "PATCH");
    expect(call).toBeDefined();
    return JSON.parse(String(call![1]!.body)) as { sections: { tag_filters?: unknown; download_completion?: unknown } };
  }

  function clickSave() {
    const saves = screen.getAllByRole("button", { name: t("act.save") }) as HTMLButtonElement[];
    const enabled = saves.filter((button) => !button.disabled);
    expect(enabled).toHaveLength(1);
    fireEvent.click(enabled[0]!);
  }

  it("番剧没有 AniBT 来源时只显示标题解析行", () => {
    stubRulesFetch();
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged: () => {}, initialTab: "rules" });

    expect(screen.getByText(t("anime.rules.titleFilter"))).toBeInTheDocument();
    expect(within(tagCard()).getByText(t("titleTags.title"))).toBeInTheDocument();
    expect(screen.queryByText(t("anibtTags.title"))).not.toBeInTheDocument();
  });

  it("标题解析行：候选为本番出现过的标签并显示次数，从词表添加后保存", async () => {
    const fetchMock = stubRulesFetch();
    const workspace = anibtWorkspace();
    workspace.releases = [1, 2].map((n) => ({
      id: `release-${n}`, rss_source_id: "selected", stable_item_key: `stable-${n}`,
      raw_facts: { title: `[G] 测试番 - 0${n} [1080p][CHS]` },
      source_facts: { identity_kind: "guid" as const, identity_value: `stable-${n}`, title_tags: { schema: "reltitle-attrs-v1", implementation: "reltitle@test", tags: ["resolution:1080p", "sub_lang:SC"] } },
      parsed_facts: { episode: n, group_name: "G", special: false },
      verdict: "accepted_pending_download" as const, reason_code: "accepted", version: 1,
    }));
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rules" });

    const card = tagCard();
    const titleRow = (await within(card).findByText(t("titleTags.title"))).closest("section")!;
    expect(await within(titleRow).findAllByTestId("tag-row-entry")).toHaveLength(1);
    const sc = await within(titleRow).findByRole("button", { name: "SC" });
    expect(sc).toHaveAttribute("title", expect.stringContaining(t("tagFilters.count", { n: 2 })));
    // Candidates default to require; switching to exclude adds there; added tags can change role.
    fireEvent.click(sc);
    expect(within(titleRow).queryByRole("button", { name: "SC" })).not.toBeInTheDocument();
    fireEvent.click(within(titleRow).getByRole("radio", { name: t("anibtTags.role.exclude") }));
    fireEvent.click(within(titleRow).getByRole("button", { name: "1080p" }));
    fireEvent.click(within(titleRow).getByRole("button", { name: t("anibtTags.switchTo.require", { tag: titleTagFullLabel("sub_lang:TC") }) }));
    clickSave();

    await waitFor(() => expect(patchBody(fetchMock).sections.tag_filters).toEqual({
      mode: "replace",
      value: {
        anibt: { require: ["language:CHS"], exclude: [] },
        title: { require: ["sub_lang:SC", "sub_lang:TC"], exclude: ["resolution:1080p"] },
      },
    }));
  });

  it("直接编辑番剧自己的标签过滤，保存整段替换", async () => {
    const fetchMock = stubRulesFetch();
    renderTabs({ animeID: "anime-1", workspace: anibtWorkspace(), onChanged: () => {}, initialTab: "rules" });

    const card = tagCard();
    const [chs] = within(card).getAllByTestId("tag-row-entry");
    expect(chs).toHaveAttribute("data-role", "require");
    expect(within(card).queryByRole("radio", { name: t("config.useDefaultsConfirm") })).not.toBeInTheDocument();

    fireEvent.click(within(card).getByRole("button", { name: "1080p" }));
    await waitFor(() => expect(within(card).getAllByTestId("tag-row-entry")).toHaveLength(3));
    clickSave();

    await waitFor(() => expect(patchBody(fetchMock).sections.tag_filters).toEqual({
      mode: "replace",
      value: {
        anibt: { require: ["language:CHS", "resolution:1080p"], exclude: [] },
        title: { require: [], exclude: ["sub_lang:TC"] },
      },
    }));
  });

  it("「全部使用默认」把规则各段与标签过滤填成当前默认设置，用户保存后上送", async () => {
    const defaults = structuredClone(preset);
    defaults.tag_filters = { anibt: { require: [], exclude: ["codec:HEVC"] }, title: { require: [], exclude: [] } };
    const fetchMock = stubRulesFetch(defaults);
    renderTabs({ animeID: "anime-1", workspace: anibtWorkspace(), onChanged: () => {}, initialTab: "rules" });

    const card = tagCard();
    fireEvent.click(screen.getByRole("button", { name: t("config.useDefaults") }));
    await waitFor(() => expect(within(card).getAllByTestId("tag-row-entry").some((entry) => entry.textContent?.includes("HEVC"))).toBe(true));
    expect(within(card).getAllByTestId("tag-row-entry").some((entry) => entry.textContent?.includes("CHS"))).toBe(false);
    // Only filling the form: no PATCH of any kind yet.
    expect(fetchMock.mock.calls.some(([input, init]) => String(input).includes("/anime/anime-1/rules") && init?.method === "PATCH")).toBe(false);
    // Once equal to the default settings the button disables.
    await waitFor(() => expect(screen.getByRole("button", { name: t("config.useDefaults") })).toBeDisabled());
    clickSave();

    await waitFor(() => expect(patchBody(fetchMock).sections).toEqual(presetRuleSections(defaults)));
  });

  it("段卡「已修改」：与默认设置不同的段才标记，未保存的编辑也算，「全部使用默认」后消失", async () => {
    const workspace = anibtWorkspace();
    // Makes every other section equal to the defaults with only the tag filter differing.
    const defaults = structuredClone(preset);
    const sections = presetRuleSections(defaults);
    workspace.rules = { ...workspace.rules, sections: { ...sections, tag_filters: workspace.rules.sections.tag_filters } };
    stubRulesFetch(defaults);
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rules" });

    const card = tagCard();
    await waitFor(() => expect(within(card).getByText(t("config.modified"))).toHaveAttribute("title", t("config.modifiedTitle")));
    expect(screen.getAllByText(t("config.modified"))).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: t("config.useDefaults") }));
    await waitFor(() => expect(screen.queryByText(t("config.modified"))).not.toBeInTheDocument());

    // An unsaved edit marks this section immediately.
    fireEvent.change(screen.getByLabelText(t("settings.rules.titleNot")), { target: { value: "720p" } });
    const titleFilterCard = screen.getByText(t("anime.rules.titleFilter")).closest("section")!;
    expect(within(titleFilterCard).getByText(t("config.modified"))).toBeInTheDocument();
    expect(within(tagCard()).queryByText(t("config.modified"))).not.toBeInTheDocument();
  });

  function stubRulesAndStoredParser(defaults: BusinessPresetView, stored: "regex" | "reltitle") {
    const order: string[] = [];
    const bodies: { rules?: { sections: unknown }; parser?: { parser_kind: string; expected_version: number } } = {};
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/config/tag-filters/title-vocabulary")) {
        return Response.json({ implementation: "reltitle@test", config_digest: "lexicon:sha256:test", dimensions: [] });
      }
      if (url.includes("/template-catalogs/naming")) {
        return Response.json({ language_version: "1", catalog_version: "1", variables: [], functions: [] });
      }
      if (url.includes("/config/business-preset")) return Response.json(defaults);
      if (url.includes("/file-maintenance/parser-policy/anime/")) {
        if (init?.method === "PUT") {
          order.push("parser");
          bodies.parser = JSON.parse(String(init.body)) as { parser_kind: string; expected_version: number };
          return Response.json({ managed_anime_id: "anime-1", parser_kind: bodies.parser.parser_kind, source_default_version: 1, version: 2 });
        }
        return Response.json({ managed_anime_id: "anime-1", parser_kind: stored, source_default_version: 1, version: 1 });
      }
      if (url.includes("/parser-catalog")) {
        return Response.json([
          { id: "regex", implementation_version: "regex-v1", output_contract_version: "episode-evidence-v1", display_name: "Regex rules", capabilities: ["release_title"], config_version: "regex-config-v1" },
          { id: "reltitle", implementation_version: "reltitle@x", output_contract_version: "episode-evidence-v1", display_name: "reltitle", capabilities: ["release_title"], config_version: "reltitle-config-v1" },
        ]);
      }
      if (init?.method === "PATCH" && url.includes("/anime/anime-1/rules")) {
        order.push("rules");
        bodies.rules = JSON.parse(String(init.body)) as { sections: unknown };
        return Response.json({ rules: anibtWorkspace().rules, reevaluation: { managed_anime_id: "anime-1", accepted: true, scheduled_count: 0 } });
      }
      return Response.json({ code: "IGNORED", message: "ignored" }, { status: 400 });
    });
    vi.stubGlobal("fetch", fetchMock);
    return { order, bodies };
  }

  function parserCard() {
    return screen.getByText(t("tabs.sectionName.parser")).closest("section") as HTMLElement;
  }

  it("「全部使用默认」：规则与默认相同但解析器不同时可用，点击后解析器卡填成默认解析器", async () => {
    const workspace = anibtWorkspace();
    const defaults = structuredClone(preset);
    workspace.rules = { ...workspace.rules, sections: presetRuleSections(defaults) };
    const { order, bodies } = stubRulesAndStoredParser(defaults, "reltitle");
    renderTabs({ animeID: "anime-1", workspace, onChanged: () => {}, initialTab: "rules" });

    const parser = within(parserCard());
    await waitFor(() => expect(parser.getByRole("radio", { name: t("policy.parser.kindReltitle") })).toHaveAttribute("aria-checked", "true"));
    await waitFor(() => expect(parser.getByText(t("config.modified"))).toBeInTheDocument());
    const useDefaults = screen.getByRole("button", { name: t("config.useDefaults") });
    expect(useDefaults).toBeEnabled();

    fireEvent.click(useDefaults);
    await waitFor(() => expect(parser.getByRole("radio", { name: t("policy.parser.kindRegex") })).toHaveAttribute("aria-checked", "true"));
    await waitFor(() => expect(screen.queryByText(t("config.modified"))).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: t("config.useDefaults") })).toBeDisabled();
    // Only an unsaved choice: no writes of any kind yet.
    expect(order).toEqual([]);
    expect(hasUnsavedDot(tabNamed(t("tabs.names.rules")))).toBe(true);

    clickSave();
    await waitFor(() => expect(order).toEqual(["parser"]));
    expect(bodies.parser).toEqual({ expected_version: 1, parser_kind: "regex" });
  });

  it("「全部使用默认」同时重置规则与解析器，一次保存依次提交两者", async () => {
    const defaults = structuredClone(preset);
    const { order, bodies } = stubRulesAndStoredParser(defaults, "reltitle");
    renderTabs({ animeID: "anime-1", workspace: anibtWorkspace(), onChanged: () => {}, initialTab: "rules" });

    const parser = within(parserCard());
    await waitFor(() => expect(parser.getByRole("radio", { name: t("policy.parser.kindReltitle") })).toHaveAttribute("aria-checked", "true"));
    fireEvent.click(screen.getByRole("button", { name: t("config.useDefaults") }));
    await waitFor(() => expect(parser.getByRole("radio", { name: t("policy.parser.kindRegex") })).toHaveAttribute("aria-checked", "true"));
    await waitFor(() => expect(screen.queryByText(t("config.modified"))).not.toBeInTheDocument());

    clickSave();
    await waitFor(() => expect(order).toEqual(["rules", "parser"]));
    expect(bodies.rules?.sections).toEqual(presetRuleSections(defaults));
    expect(bodies.parser?.parser_kind).toBe("regex");
  });

  it("默认设置读取失败时段卡不显示「已修改」，也不报错", async () => {
    stubRulesFetch();
    renderTabs({ animeID: "anime-1", workspace: anibtWorkspace(), onChanged: () => {}, initialTab: "rules" });

    await within(tagCard()).findAllByTestId("tag-row-entry");
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByText(t("config.modified"))).not.toBeInTheDocument();
  });

  it("下载完成后只有全局设置：规则页没有这一项，保存也不上送", async () => {
    const fetchMock = stubRulesFetch();
    renderTabs({ animeID: "anime-1", workspace: anibtWorkspace(), onChanged: () => {}, initialTab: "rules" });

    expect(screen.queryByText(t("downloadCompletion.title"), { exact: true })).toBeNull();
    const card = tagCard();
    fireEvent.click(within(card).getByRole("button", { name: "1080p" }));
    clickSave();

    await waitFor(() => expect(patchBody(fetchMock).sections.tag_filters).toBeDefined());
    expect(patchBody(fetchMock).sections.download_completion).toBeUndefined();
  });
});

// The RSS tab's "scan library" opens the anime's files dialog and submits an alignment scan there; the workspace re-reads after the scan finishes.
describe("RSS 扫描媒体库", () => {
  it("opens the files dialog, submits one reconciliation scan and refreshes once the scan ends", async () => {
    const posts: { url: string; body?: unknown }[] = [];
    let filesReads = 0;
    const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    const scanView = (state: string) => ({ managed_anime_id: "anime-1", state, generation: 1, input_fingerprint: "f", progress: {}, capacity_stats: {}, version: 1 });
    const unavailable = { available: false, reason_code: "nothing_to_align" };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/anime/rss-list-view")) return jsonResponse(ownerRssListView());
      if (url.includes("/reconciliation/scans") && init?.method === "POST") {
        posts.push({ url, body: init.body ? JSON.parse(String(init.body)) : undefined });
        return jsonResponse(scanView("running"), 202);
      }
      if (url.includes("/anime/anime-1/files")) {
        filesReads += 1;
        return jsonResponse({
          managed_anime_id: "anime-1",
          root: "",
          scan: { state: filesReads > 1 ? "idle" : "running", generation: 1, scanned_at: "2026-10-21T01:00:00Z", rescan_required: false },
          aligning: false,
          scan_action: { available: true },
          align_all_action: unavailable,
          files: [],
        });
      }
      if (url.includes("/template-catalogs/naming")) {
        return jsonResponse({ language_version: "1", catalog_version: "1", variables: [], functions: [] });
      }
      return jsonResponse({ code: "IGNORED", message: "ignored" }, 400);
    }));
    const onChanged = vi.fn();
    renderTabs({ animeID: "anime-1", workspace: workspaceFixture(), onChanged, initialTab: "rss" });

    fireEvent.click(await screen.findByRole("button", { name: t("rss.scan") }));
    expect(await screen.findByRole("dialog", { name: t("tabs.files.dialogTitle", { title: workspaceFixture().managed_anime.title }) })).toBeInTheDocument();
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.body).toMatchObject({ managed_anime_id: "anime-1", trigger: "manual" });
    expect(posts[0]!.url).not.toContain("target-scan");
    await waitFor(() => expect(screen.getByText(t("tabs.files.empty"))).toBeInTheDocument(), { timeout: 4_000 });
    await waitFor(() => expect(onChanged).toHaveBeenCalled(), { timeout: 4_000 });
  });
});
