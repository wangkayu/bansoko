import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getBusinessPreset, type AnimeConfigCandidate, type AnimeDraft, type AnimeWorkspace, type SubscribeResult } from "../../lib/api/client";
import { CandidateVersionConflict, type CandidateStore } from "../../lib/candidate/port";
import { getCandidateStore } from "../../lib/candidate/serverCandidateStore";
import type { CatalogProvider } from "../../lib/providers/port";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { draftFromEntry } from "../catalog/entryDraft";
import { AnimeConfigDialog } from "./AnimeConfigDialog";
import { RecordLinkContext } from "../../shell/recordLinkContext";
import { t } from "../../lib/i18n";
import { presetRuleSections } from "./businessDraft";
import { preset } from "../../testing/businessPreset";

// A new draft starts from the Global business preset; every test gets a
// complete one instead of adding a route to each fetch stub.
// The dialog has two "close" buttons, as in production: the title-bar X
// (aria-label) and the footer button (text). These tests drive the footer one.
function footerCloseButton(): HTMLElement {
  const button = screen.getAllByRole("button", { name: t("act.close") }).find((candidate) => !candidate.hasAttribute("aria-label"));
  if (!button) throw new Error("footer 关闭 button not found");
  return button;
}

vi.mock("../../lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/api/client")>()),
  getBusinessPreset: vi.fn(async () => (await import("../../testing/businessPreset")).preset),
}));

vi.mock("../../lib/candidate/serverCandidateStore", () => ({
  getCandidateStore: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

const entry: CatalogEntryView = {
  providerEntryRef: "custom:anime-1",
  source: "custom",
  title: "测试番",
  metadataMatch: { matched: true },
  subscriptionState: "active",
  managedAnimeRef: "anime-1",
  metadataHold: { held: false, missing: [] },
};

const provider = {
  key: "custom",
  label: t("grid.tabCustom"),
  isMock: false,
  preview: vi.fn().mockResolvedValue({
    metadata_hold: { held: false, missing: [] },
    sources: [],
    releases: [],
    files: [],
  }),
} as unknown as CatalogProvider;

function workspaceFixture(): AnimeWorkspace {
  return {
    managed_anime: { id: "anime-1", title: "测试番", title_override: null, version: 1 },
    placement_handoffs: [],
    subscription: {
      id: "subscription-1",
      managed_anime_id: "anime-1",
      origin: "custom",
      origin_entry_ref: null,
      instance_label: "测试实例",
      state: "active",
      lifecycle_state: "active",
      lifecycle_epoch: 1,
      version: 1,
      new_releases_only: false,
    },
    metadata: {
      managed_anime_id: "anime-1",
      bgm: { work_id: "bgm-1" },
      tmdb: { work_id: "tmdb-1", media_kind: "tv", season: 1 },
      version: 1,
    },
    metadata_hold: { held: false, missing: [] },
    subgroups: {
      managed_anime_id: "anime-1",
      subgroups: { max_rss: 1, entries: [] },
      effective_selection: [],
      rss_sources: [],
      version: 1,
    },
    rules: {
      managed_anime_id: "anime-1",
      sections: presetRuleSections(preset),
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
    rss_sources: [{
      id: "source-1",
      subscription_id: "subscription-1",
      source_kind: "custom_rss",
      locator_display: "[redacted]",
      group_name: "测试字幕组",
      title: "测试番",
      episode_offset: 0,
      version: 1,
    }],
    rss_source_runtimes: [{ rss_source_id: "source-1", health: "ready", version: 1 }],
    releases: [{
      id: "release-1",
      rss_source_id: "source-1",
      stable_item_key: "release-key-1",
      raw_facts: { title: "[测试字幕组] 测试番 - 01" },
      source_facts: { identity_kind: "guid", identity_value: "release-key-1" },
      parsed_facts: { episode: 1, special: false },
      verdict: "submitted",
      reason_code: "submitted",
      download_task_id: "task-1",
      acquisition_id: "acquisition-1",
      version: 1,
    }],
    download_tasks: [{
      id: "task-1",
      release_id: "release-1",
      frozen_input: { release_stable_item_key: "release-key-1", source_locator_display: "[redacted]" },
      frozen_policy: {
          rules_version: 1,
          metadata_version: 1,
          effective_sections: {},
          section_versions: {},
          section_schemas: {},
          effective_digest: "sha256:" + "0".repeat(64),
          config_identity: { identity_kind: "instance_materialized", effective_digest: "sha256:" + "0".repeat(64) },
        },
      state: "downloading",
      idempotency_key: "download-1",
      version: 1,
    }],
    artifacts: [],
    file_operations: [],
    logs: [],
    episode_progress: null,
  };
}

function completedWorkspace(): AnimeWorkspace {
  const workspace = workspaceFixture();
  workspace.download_tasks[0] = { ...workspace.download_tasks[0]!, state: "completed" };
  workspace.artifacts = [{
    id: "artifact-1",
    download_task_id: "task-1",
    files: [],
    fingerprints: [],
    state: "verified",
    version: 1,
  }];
  workspace.file_operations = [{
    id: "operation-1",
    artifact_id: "artifact-1",
    artifact_file_ref: "file-1",
    operation: "hardlink",
    source_path: "/downloads/test.mkv",
    target_path: "/library/test.mkv",
    source_fingerprint: { algorithm: "sha256", value: "a".repeat(64), size_bytes: 1 },
    target_fingerprint: { algorithm: "sha256", value: "a".repeat(64), size_bytes: 1 },
    collision_policy: "versioned",
    frozen_render_snapshot: { renderer_version: "1", directory: "library", filename: "test.mkv" },
    frozen_config_snapshot: { rules_version: 1, safety_version: "1" },
    state: "completed",
    idempotency_key: "operation-1",
    version: 1,
  }];
  return workspace;
}

/** An owner-mode RSSListView fixture: one source group plus one entry linked to a workspace Release. */
function ownerRssListView(seedTitle = "[测试字幕组] 测试番 - 01"): unknown {
  return {
    mode: "owner",
    config_identity: "owner:anime-1@1",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "p", evaluator: "e", renderer: "r", safety: "s", torrent_parser: "t" },
    generated_at: "2026-09-24T00:00:00Z",
    groups: [
      {
        scope: { provider: "custom", provider_entry_ref: "", provider_group_ref: "测试字幕组", rss_source_id: "source-1" },
        display_name: "测试字幕组",
        selection: { source: "configured" as const, effective_selected: true, selection_mode: "auto" },
        offset: { value: 0, editable: false },
        feed: { state: "fresh" },
        items: [
          {
            snapshot_id: "release:release-1",
            stable_seed_key: "release-key-1",
            seed: { title: seedTitle, published_at: "2026-07-21T09:00:00Z", size_bytes: 1024, locator_display: "[redacted]" },
            torrent: { state: "not_applicable" },
            evaluation: { state: "evaluated", verdict: "submitted", reason_code: "submitted" },
            files: [],
            download_action: { allowed: false, kind: "none" },
          },
        ],
      },
    ],
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
  };
}

// The execution-records entries (an RSS row's processing, the footer's records) are driven by the opener the app shell provides.
const openRecordSpy = vi.fn();

function renderDialog(responses: AnimeWorkspace[], staleTime = 0, entryOverride?: CatalogEntryView) {
  let requestCount = 0;
  let viewCount = 0;
  const rssViews = [ownerRssListView(), ownerRssListView("[测试字幕组] 测试番 - 02")];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    // With forceMount the rules tab's naming catalog/preview requests take the error branch, keeping the workspace polling counts undisturbed.
    if (url.includes("/template-catalogs/naming") || url.includes("/template-previews/naming")) {
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
        status: 400,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.includes("/file-maintenance/file-effect/anime/")) {
      return new Response(JSON.stringify({ unclassified_route: "skip", version: 1 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.includes("/parser-policy/anime/")) {
      return new Response(JSON.stringify({
        managed_anime_id: "anime-dialog",
        parser_kind: "regex",
        source_default_version: 1,
        version: 1,
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    if (url.includes("/rss-source-runtimes/") && url.endsWith("/retry")) {
      return new Response(JSON.stringify({ owner_type: "rss_source_runtime", owner_id: "source-1", accepted: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.includes("/rss-list-view")) {
      // The owner read model and the workspace count separately: every re-read advances a new
      // result, proving the panel follows the most recent server read.
      const body = rssViews[Math.min(viewCount, rssViews.length - 1)]!;
      viewCount += 1;
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (!url.includes("/anime/anime-1/workspace")) {
      return new Response(JSON.stringify({}), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    const body = responses[Math.min(requestCount, responses.length - 1)]!;
    requestCount += 1;
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }));

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 120_000, staleTime } },
  });
  const dialog = (open: boolean) => (
    <QueryClientProvider client={client}>
      <RecordLinkContext.Provider value={openRecordSpy}>
        <AnimeConfigDialog
          open={open}
          onOpenChange={() => {}}
          provider={provider}
          entry={entryOverride ?? entry}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab="rss"
        />
      </RecordLinkContext.Provider>
    </QueryClientProvider>
  );
  const view = render(dialog(true));
  return {
    requestCount: () => requestCount,
    setOpen: (open: boolean) => view.rerender(dialog(open)),
  };
}


async function flushQuery(): Promise<void> {
  await act(async () => {
    for (let index = 0; index < 5; index += 1) {
      await Promise.resolve();
      await vi.advanceTimersByTimeAsync(0);
    }
  });
}

describe("AnimeConfigDialog subscribed workspace polling", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  // A subscribed entry's four-tab dialog opens on RSS (the result/action face) by default; a draft keeps metadata.
  it("已订阅弹窗传 initialTab=rss 时默认落 RSS tab", async () => {
    renderDialog([workspaceFixture()]);
    await flushQuery();
    await flushQuery();

    expect(screen.getByRole("tab", { name: "RSS" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: t("tabs.names.metadata") })).toHaveAttribute("aria-selected", "false");
    // The RSS content is visible right away, no tab click needed first.
    expect(screen.getByRole("button", { name: t("rss.expandGroup", { name: "测试字幕组" }) })).toBeInTheDocument();
  });

  it("updates the RSS read model in one open session and stops at the stable owner state", async () => {
    const { requestCount } = renderDialog([workspaceFixture(), completedWorkspace()]);
    await flushQuery();
    await flushQuery();

    // The panel renders the server read model; it must re-read once after the workspace re-reads, and its result advances with it.
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "测试字幕组" }) }));
    expect(screen.getByText("[测试字幕组] 测试番 - 01")).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    await flushQuery();
    expect(requestCount()).toBe(2);
    await vi.waitFor(() => expect(screen.getByText("[测试字幕组] 测试番 - 02")).toBeInTheDocument());

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(requestCount()).toBe(2);
  });

  it("restarts polling from fresh cached data after close and reopen", async () => {
    const { requestCount, setOpen } = renderDialog([workspaceFixture()], 60_000);
    await flushQuery();
    expect(requestCount()).toBe(1);

    await act(async () => setOpen(false));
    await flushQuery();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    await act(async () => setOpen(true));
    await flushQuery();
    expect(requestCount()).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(requestCount()).toBe(2);
  });

  it("stops polling when the 30 second session deadline is reached", async () => {
    const { requestCount } = renderDialog([workspaceFixture()]);
    await flushQuery();

    for (let elapsed = 0; elapsed < 30_000; elapsed += 1_000) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_000);
      });
    }
    const countAtDeadline = requestCount();
    expect(countAtDeadline).toBeGreaterThan(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(requestCount()).toBe(countAtDeadline);
  });

  // After the polling deadline, a retry action resets the deadline through onChanged and the polling window reopens.
  it("reopens the polling window when a retry is submitted after the deadline", async () => {
    const failedRuntime = workspaceFixture();
    failedRuntime.rss_source_runtimes = [
      { rss_source_id: "source-1", health: "failed", reason_code: "network_error", failed_work_item_id: "wi-1", version: 1 },
    ];
    const { requestCount } = renderDialog([failedRuntime]);
    await flushQuery();

    for (let elapsed = 0; elapsed < 31_000; elapsed += 1_000) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_000);
      });
    }
    const countAtDeadline = requestCount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_000);
    });
    expect(requestCount()).toBe(countAtDeadline);

    fireEvent.click(screen.getByRole("button", { name: t("rss.retryRefresh") }));
    await flushQuery();
    // Submit the retry; onChanged re-reads once immediately
    const countAfterRetry = requestCount();
    expect(countAfterRetry).toBeGreaterThan(countAtDeadline);

    // The deadline has been reset: polling keeps advancing
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(requestCount()).toBeGreaterThan(countAfterRetry);
  });
});

// Hidden per the subscription's current retention and cleanup version (the restore checklist lives with the dialog's hideRetention).
describe("AnimeConfigDialog 已订阅的订阅设置", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  // The single-save model: the rules tab no longer mounts "edit subscription settings" (the
  // second save path); unknown episodes come straight from FileEffectPolicySection and saving
  // goes through the footer's single save.
  it("规则 tab 不渲染编辑订阅设置，未识别集数照常显示且无保留与清理", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming") || url.includes("/template-previews/naming")) {
        return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/file-maintenance/file-effect/anime/")) {
        return new Response(JSON.stringify({ unclassified_route: "skip", version: 1 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/anime/anime-1/workspace")) {
        return new Response(JSON.stringify(workspaceFixture()), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={entry}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab="rules"
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    expect(screen.queryByRole("region", { name: t("anime.revision.aria") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("config.revision.edit") })).not.toBeInTheDocument();

    // The unknown-episodes policy is read separately, after the workspace.
    await flushQuery();
    expect(screen.getByRole("combobox", { name: t("file.unclassifiedTitle") })).toBeInTheDocument();
    expect(screen.queryByText(t("config.snapshot.legend"))).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: t("retention.admissionTitle") })).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: t("qualityCompare.title") })).not.toBeInTheDocument();

    // The footer has one save: disabled while the rules tab has no changes.
    expect(screen.getByRole("button", { name: t("act.save") })).toBeDisabled();
  });
});

// Closing the dialog after subscribing gives unsaved owner edits the same discard confirmation as before subscribing.
describe("AnimeConfigDialog subscribed discard confirmation", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function renderSubscribedDialog(onOpenChange: (open: boolean) => void) {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming") || url.includes("/template-previews/naming")) {
        return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify(workspaceFixture()), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={onOpenChange}
          provider={provider}
          entry={entry}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab="metadata"
        />
      </QueryClientProvider>,
    );
  }

  it("无未保存编辑时直接关闭，不弹确认", async () => {
    const onOpenChange = vi.fn();
    renderSubscribedDialog(onOpenChange);
    await flushQuery();

    fireEvent.click(footerCloseButton());
    expect(screen.queryByText(t("common.discardEditsTitle"))).not.toBeInTheDocument();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("有未保存编辑时关闭先确认；取消保留编辑，确认丢弃后关闭", async () => {
    const onOpenChange = vi.fn();
    renderSubscribedDialog(onOpenChange);
    await flushQuery();

    const saveButton = screen.getByRole("button", { name: t("act.save") });
    expect(saveButton).toBeDisabled();
    expect(document.querySelector("[data-unsaved-dot]")).toBeNull();

    // Clearing the BGM binding → owner edits go dirty: the footer count, the single save enabled, the metadata tab marked.
    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.bangumi") }) }));
    expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("act.save") })).toBeEnabled();
    expect(screen.getByRole("tab", { name: t("tabs.names.metadata") }).querySelector("[data-unsaved-dot]")).not.toBeNull();

    fireEvent.click(footerCloseButton());
    expect(screen.getByText(t("common.discardEditsTitle"))).toBeInTheDocument();
    expect(onOpenChange).not.toHaveBeenCalled();

    // Cancel: the edits stay and the dialog remains
    fireEvent.click(screen.getByRole("button", { name: t("act.cancel") }));
    expect(screen.queryByText(t("common.discardEditsTitle"))).not.toBeInTheDocument();
    expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();

    fireEvent.click(footerCloseButton());
    fireEvent.click(screen.getByRole("button", { name: t("common.discardChanges") }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});

// --- Draft mode: candidate loading terms ----------------------------------------------

const draftEntry: CatalogEntryView = {
  providerEntryRef: "custom:candidate-1",
  source: "custom",
  title: "预设番",
  metadataMatch: { matched: true },
  metadataHold: { held: false, missing: [] },
  candidateId: "candidate-1",
  instanceLabel: "预设番",
  rssUrl: "https://feeds.example.com/rss.xml",
  groupName: "测试字幕组",
};

const draftEntryWithoutCandidate: CatalogEntryView = {
  ...draftEntry,
  candidateId: undefined,
};

function candidateFixture(overrides: Partial<AnimeConfigCandidate>): AnimeConfigCandidate {
  const draft = draftFromEntry(draftEntry);
  return {
    id: "candidate-1",
    origin: "custom",
    origin_entry_ref: null,
    title: draft.title,
    title_override: null,
    instance_label: draft.instance_label ?? null,
    draft,
    version: 1,
    activated_managed_anime_id: null,
    created_at: "2026-07-28T00:00:00Z",
    updated_at: "2026-07-28T00:00:00Z",
    status: "working",
    draft_digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
    current_revision: 1,
    last_activity: "2026-07-28T00:00:00Z",
    retention_until: "2026-08-27T00:00:00Z",
    ...overrides,
  } as AnimeConfigCandidate;
}

function candidateStoreMock(
  list: CandidateStore["list"],
  get: CandidateStore["get"] = async (id) => (await list()).find((candidate) => candidate.id === id) ?? null,
): CandidateStore {
  return {
    list,
    get,
    save: async () => {
      throw new Error("unexpected save");
    },
    patch: async () => {
      throw new Error("unexpected patch");
    },
    subscribe: async () => {
      throw new Error("unexpected subscribe");
    },
    delete: async () => {
      throw new Error("unexpected delete");
    },
  };
}

function renderDraftDialog(entry: CatalogEntryView = draftEntryWithoutCandidate) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AnimeConfigDialog
        open
        onOpenChange={() => {}}
        provider={provider}
        entry={entry}
        sourceLabel={t("grid.tabCustom")}
        onSubscribed={() => {}}
      />
    </QueryClientProvider>,
  );
}

describe("AnimeConfigDialog draft candidate loading", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("loads the exact working candidate from entry.candidateId", async () => {
    const exact = candidateFixture({
      version: 3,
      draft: { ...draftFromEntry(draftEntry), title: "未激活预设" },
    });
    const other = candidateFixture({
      id: "candidate-other",
      version: 7,
      activated_managed_anime_id: "anime-1",
      status: "dormant",
      draft: { ...draftFromEntry(draftEntry), title: "另一条历史候选" },
    });
    const list = vi.fn(async () => [exact, other]);
    const get = vi.fn(async (id: string) => id === exact.id ? exact : other);
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(list, get));

    renderDraftDialog(draftEntry);
    await flushQuery();

    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    expect(within(titleRegion).getByText("未激活预设")).toBeInTheDocument();
    expect(within(titleRegion).queryByText("另一条历史候选")).not.toBeInTheDocument();
    expect(screen.getByText(t("config.footerSaved"))).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith("candidate-1");
    expect(list).not.toHaveBeenCalled();
  });

  it("shows an actionable error for an exact dormant candidate without falling back", async () => {
    const dormant = candidateFixture({
      version: 7,
      activated_managed_anime_id: "anime-1",
      status: "dormant",
      draft: { ...draftFromEntry(draftEntry), title: "已激活预设" },
    });
    const list = vi.fn(async () => [
      candidateFixture({ version: 3, draft: { ...draftFromEntry(draftEntry), title: "另一条 working 候选" } }),
    ]);
    const get = vi.fn(async () => dormant);
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(list, get));

    renderDraftDialog(draftEntry);
    await flushQuery();

    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    expect(within(titleRegion).getByText("已激活预设")).toBeInTheDocument();
    expect(within(titleRegion).queryByText("另一条 working 候选")).not.toBeInTheDocument();
    expect(screen.getByText(t("config.draftNotEditable"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("config.refreshReselect") })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("anime.subscribe") })).toBeDisabled();
  });

  it("版本冲突时重新加载最新 working 草稿并清掉本地冲突", async () => {
    const initial = candidateFixture({ version: 1, draft: { ...draftFromEntry(draftEntry), title: "服务端旧配置" } });
    const latest = candidateFixture({ version: 2, draft: { ...draftFromEntry(draftEntry), title: "服务端最新配置" } });
    const list = vi.fn(async () => [initial]);
    const get = vi.fn()
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(latest);
    const patch = vi.fn(async () => {
      throw new CandidateVersionConflict(latest.version);
    });
    vi.mocked(getCandidateStore).mockReturnValue({
      ...candidateStoreMock(list, get),
      patch,
    });

    renderDraftDialog(draftEntry);
    await flushQuery();
    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    fireEvent.change(within(titleRegion).getByLabelText(t("tabs.metadata.title")), { target: { value: "本地未保存修改" } });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.saveTitle") }));
    fireEvent.click(screen.getByRole("button", { name: t("config.saveButton") }));
    await flushQuery();

    expect(patch).toHaveBeenCalledWith(initial.id, expect.objectContaining({ title: "本地未保存修改" }), initial.version);
    expect(screen.getByText(t("config.conflictOther"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("config.reloadLatest") }));
    await flushQuery();

    expect(get).toHaveBeenCalledWith(initial.id);
    expect(within(screen.getByRole("region", { name: t("tabs.metadata.title") })).getByText("服务端最新配置")).toBeInTheDocument();
    expect(screen.getByText(t("config.footerSaved"))).toBeInTheDocument();
    expect(screen.queryByText("配置已被其他窗口修改，请选择重新加载或使用最新版本覆盖保存。")).not.toBeInTheDocument();
  });

  it("版本冲突时覆盖保存使用服务端 fresh version 且不合并字段", async () => {
    const initial = candidateFixture({ version: 1, draft: { ...draftFromEntry(draftEntry), title: "服务端旧配置" } });
    const updated = candidateFixture({ version: 3, draft: { ...draftFromEntry(draftEntry), title: "本地覆盖配置" } });
    const list = vi.fn(async () => [initial]);
    const patch = vi.fn()
      .mockRejectedValueOnce(new CandidateVersionConflict(2))
      .mockResolvedValueOnce(updated);
    vi.mocked(getCandidateStore).mockReturnValue({
      ...candidateStoreMock(list),
      patch,
    });

    renderDraftDialog(draftEntry);
    await flushQuery();
    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    fireEvent.change(within(titleRegion).getByLabelText(t("tabs.metadata.title")), { target: { value: "本地覆盖配置" } });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.saveTitle") }));
    fireEvent.click(screen.getByRole("button", { name: t("config.saveButton") }));
    await flushQuery();

    fireEvent.click(screen.getByRole("button", { name: t("config.overwrite") }));
    await flushQuery();

    expect(patch).toHaveBeenNthCalledWith(1, initial.id, expect.objectContaining({ title: "本地覆盖配置" }), 1);
    expect(patch).toHaveBeenNthCalledWith(2, initial.id, expect.objectContaining({ title: "本地覆盖配置" }), 2);
    expect(within(screen.getByRole("region", { name: t("tabs.metadata.title") })).getByText("本地覆盖配置")).toBeInTheDocument();
    expect(screen.getByText(t("config.footerSaved"))).toBeInTheDocument();
    expect(screen.queryByText(t("config.overwrite"))).not.toBeInTheDocument();
  });
});

// The rules tab's inherited effective values before subscribing must come from the server's
// global sections (the same cache key as the settings page), never frontend hard-coded defaults —
// otherwise the inherited values the user sees would drift from the real global ones.
describe("AnimeConfigDialog draft rules copied from the preset", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("新草稿以服务端 Global 业务预设初始化规则，而非前端硬编码默认", async () => {
    // The draft's rule sections come from the one complete business preset
    // the dialog loads for a new draft; there is no per-section inherit.
    const { preset } = await import("../../testing/businessPreset");
    const custom = structuredClone(preset);
    custom.snapshot.sections.parser = { episode_patterns: ["全局正则"] };
    custom.snapshot.sections.release_rules = { must_contain: "", not_in: "全局排除词" };
    custom.snapshot.sections.naming_rules = {
      ...custom.snapshot.sections.naming_rules,
      tv: { directory_template: "/全局目录", video_filename_template: "{title} 全局" },
    };
    vi.mocked(getBusinessPreset).mockResolvedValueOnce(custom);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming") || url.includes("/template-previews/naming")) {
        return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/config/global-sections")) {
        // Deliberately different current Global values: the draft must show
        // the preset it was initialized from, not these.
        return new Response(JSON.stringify([
          { section_name: "parser", schema_version: "1", value: { episode_patterns: ["当前正则"] }, version: 2 },
          { section_name: "release_rules", schema_version: "go-re2-v1", value: { must_contain: "", not_in: "当前排除词" }, version: 2 },
          { section_name: "file_rules", schema_version: "go-re2-v1", value: { must_contain: "", not_in: "" }, version: 2 },
          { section_name: "naming_rules", schema_version: "1", value: { language: "js-naming-1", unknown: { directory_template: "", video_filename_template: "" }, tv: { directory_template: "/当前目录", video_filename_template: "{title} 当前" }, movie: { directory_template: "/当前目录", video_filename_template: "{title} 当前" }, subtitle_suffix_template: "" }, version: 2 },
          { section_name: "file_effect", schema_version: "1", value: { operation: "copy", collision_policy: "fail" }, version: 2 },
        ]), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response("{}", {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }));
    const list = vi.fn(async () => []);
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(list));

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={draftEntryWithoutCandidate}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab="rules"
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    // Hard-coded defaults: episode_patterns=[] and naming template "" — the assertions below
    // hold only when the values come from the server's business preset.
    expect(screen.getByText("全局正则")).toBeInTheDocument();
    expect(screen.getByDisplayValue("全局排除词")).toBeInTheDocument();
    expect(screen.getByDisplayValue("/全局目录")).toBeInTheDocument();
    expect(screen.getByDisplayValue("{title} 全局")).toBeInTheDocument();
    expect(screen.queryByText("当前正则")).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue("当前排除词")).not.toBeInTheDocument();
  });

  it("读取 Global 业务预设与候选列表并行，而不是串行等待", async () => {
    // First open of an unsubscribed provider card needs both. The preset request
    // must already be in flight while the candidate list is still pending.
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(
      String(input).includes("/config/global-sections") ? "[]" : "{}",
      { status: 200, headers: { "Content-Type": "application/json" } },
    )));
    let releaseList: (value: AnimeConfigCandidate[]) => void = () => {};
    const list = vi.fn(() => new Promise<AnimeConfigCandidate[]>((resolve) => { releaseList = resolve; }));
    vi.mocked(getBusinessPreset).mockClear();
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(list));
    const providerEntry: CatalogEntryView = {
      providerEntryRef: "mikan:entry-1",
      source: "mikan",
      title: "测试番",
      metadataMatch: { matched: true },
      metadataHold: { held: false, missing: [] },
    };

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={{ key: "mikan", label: "Mikan", isMock: false } as CatalogProvider}
          entry={providerEntry}
          sourceLabel="Mikan"
          onSubscribed={() => {}}
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    expect(list).toHaveBeenCalledTimes(1);
    expect(getBusinessPreset).toHaveBeenCalledTimes(1);

    releaseList([]);
    await flushQuery();
    expect(getBusinessPreset).toHaveBeenCalledTimes(1);
  });

  // Hidden per the subscription's current retention and cleanup version (the restore checklist lives with the dialog's hideRetention).
  it("规则 tab 的订阅设置不显示保留与清理，未识别集数照常显示", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(
      String(input).includes("/config/global-sections") ? "[]" : "{}",
      { status: 200, headers: { "Content-Type": "application/json" } },
    )));
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(vi.fn(async () => [])));

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={draftEntryWithoutCandidate}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab="rules"
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    const section = screen.getByRole("region", { name: t("anime.revision.aria") });
    expect(screen.queryByRole("region", { name: t("config.retentionAria") })).not.toBeInTheDocument();
    // "Use defaults everywhere" is no longer a stray button inside the subscription-settings area: it sits at the top of the rules tab.
    expect(within(section).queryByRole("button", { name: t("config.useDefaults") })).not.toBeInTheDocument();
    // A new draft with use-defaults-everywhere: the top button is present but disabled when nothing is customizable left to reset.
    expect(screen.getByRole("button", { name: t("config.useDefaults") })).toBeDisabled();
    expect(within(section).getByRole("combobox", { name: t("file.unclassifiedTitle") })).toBeInTheDocument();
    expect(within(section).queryByText(t("config.snapshot.legend"))).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: t("retention.admissionTitle") })).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: t("qualityCompare.title") })).not.toBeInTheDocument();
  });

  it("草稿规则段卡「已修改」：新草稿不标记，未保存的编辑标记该段，「全部使用默认」后消失", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(
      String(input).includes("/config/global-sections") ? "[]" : "{}",
      { status: 200, headers: { "Content-Type": "application/json" } },
    )));
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(vi.fn(async () => [])));

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={draftEntryWithoutCandidate}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab="rules"
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    expect(screen.getByText(t("anime.rules.titleFilter"))).toBeInTheDocument();
    expect(screen.queryByText(t("config.modified"))).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(t("settings.rules.titleNot")), { target: { value: "720p" } });
    const card = screen.getByText(t("anime.rules.titleFilter")).closest("section")!;
    expect(within(card).getByText(t("config.modified"))).toHaveAttribute("title", t("config.modifiedTitle"));
    expect(screen.getAllByText(t("config.modified"))).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: t("config.useDefaults") }));
    await flushQuery();
    fireEvent.click(screen.getByRole("button", { name: t("config.useDefaultsConfirm") }));
    await flushQuery();
    expect(screen.queryByText(t("config.modified"))).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("config.useDefaults") })).toBeDisabled();
  });
});


// Config dialog polish: the header has no "waiting for metadata" pill; the pre-subscription
// metadata tab offers the match button; the subgroups tab shows only subscription facts (no
// refresh/seeds); the RSS tab has an empty state plus a refresh button.
describe("AnimeConfigDialog DraftBody 打磨", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function stubDraftFetch() {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming") || url.includes("/template-previews/naming")) {
        return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.includes("/metadata/search")) {
        return new Response(
          JSON.stringify({
            candidates: [
              { provider: "tmdb", provider_work_id: "265010", title: "匹配到的番名", media_kind: "tv", details_status: "available", seasons: [{ season_number: 1, name: "第一季", episode_count: 12, air_date: "2024-01-01", is_special: false }], suggested_season: 1, score: 0.9 },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    }));
  }

  function renderDraft(initialTab: string = "metadata", onOpenChange: (open: boolean) => void = () => {}) {
    stubDraftFetch();
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(async () => []));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={onOpenChange}
          provider={provider}
          entry={draftEntryWithoutCandidate}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab={initialTab}
        />
      </QueryClientProvider>,
    );
  }

  it("未订阅未匹配时头部不显示「等待匹配元数据」pill", async () => {
    renderDraft();
    await flushQuery();
    expect(screen.queryByText(t("verdict.waiting_metadata"))).not.toBeInTheDocument();
  });

  it("订阅前元数据 tab 提供匹配 BGM/TMDB 按钮，弹窗搜索后回填草稿", async () => {
    renderDraft();
    await flushQuery();

    fireEvent.click(screen.getByRole("button", { name: t("forms.matchProvider", { provider: "TMDB" }) }));
    const dialog = screen.getAllByRole("dialog").at(-1)!;
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    await vi.waitFor(() => expect(within(dialog).getAllByRole("option").length).toBeGreaterThan(0));
    fireEvent.click(within(dialog).getAllByRole("option")[0]!);
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    // Draft backfilled: the TMDB binding row shows a work_id (the title area shows the same binding)
    await vi.waitFor(() =>
      expect(screen.getAllByText(/265010/).length).toBeGreaterThanOrEqual(2),
    );
  });

  it("草稿标题默认是站点标题、只读显示；编辑后空标题被拒绝，保存标题写入草稿，取消不写", async () => {
    renderDraft();
    await flushQuery();

    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    expect(within(titleRegion).getByText("预设番")).toBeInTheDocument();
    expect(within(titleRegion).queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByText(t("config.footerUnsaved"))).toBeInTheDocument();
    const editButton = () => within(titleRegion).getByRole("button", { name: t("tabs.metadata.editTitleAria") });
    const saveButton = () => within(titleRegion).getByRole("button", { name: t("tabs.metadata.saveTitle") });
    const input = () => within(titleRegion).getByLabelText(t("tabs.metadata.title"));

    // An empty title: an inline error, still editing, the draft unchanged (no unsaved count).
    fireEvent.click(editButton());
    expect(input()).toHaveValue("预设番");
    fireEvent.change(input(), { target: { value: "   " } });
    fireEvent.click(saveButton());
    expect(within(titleRegion).getByRole("alert")).toHaveTextContent(t("tabs.metadata.titleRequired"));
    expect(input()).toHaveValue("   ");
    expect(screen.queryByText(t("act.unsavedCount", { n: 1 }))).not.toBeInTheDocument();

    // Cancel: back to the original title, the draft unchanged.
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("act.cancel") }));
    expect(within(titleRegion).getByText("预设番")).toBeInTheDocument();
    expect(within(titleRegion).queryByRole("alert")).not.toBeInTheDocument();
    expect(document.querySelector("[data-unsaved-dot]")).toBeNull();

    // Saving the title writes the draft: back to the read-only display, a footer count, the metadata tab marked unsaved.
    fireEvent.click(editButton());
    fireEvent.change(input(), { target: { value: "改名番" } });
    fireEvent.click(saveButton());
    expect(within(titleRegion).queryByRole("textbox")).not.toBeInTheDocument();
    expect(within(titleRegion).getByText("改名番")).toBeInTheDocument();
    expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: t("tabs.names.metadata") }).querySelector("[data-unsaved-dot]")).not.toBeNull();
  });

  // The draft mode's discard confirmation was previously locked only by owner-mode tests; the draft-side contract is added after the row-editing rework.
  it("草稿有未保存修改时关闭先弹丢弃确认，确认后才关闭", async () => {
    const onOpenChange = vi.fn();
    renderDraft("metadata", onOpenChange);
    await flushQuery();

    // Typing a title writes the draft directly → dirty.
    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    fireEvent.change(within(titleRegion).getByLabelText(t("tabs.metadata.title")), { target: { value: "改名番" } });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.saveTitle") }));
    await vi.waitFor(() => expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument());

    // Closing shows the confirmation first instead of closing outright.
    fireEvent.click(footerCloseButton());
    expect(screen.getByText(t("common.discardEditsTitle"))).toBeInTheDocument();
    expect(onOpenChange).not.toHaveBeenCalled();

    // Cancel: the draft is kept and the dialog remains.
    fireEvent.click(screen.getByRole("button", { name: t("act.cancel") }));
    expect(screen.queryByText(t("common.discardEditsTitle"))).not.toBeInTheDocument();
    expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();

    // The dialog truly closes only after the discard is confirmed.
    fireEvent.click(footerCloseButton());
    fireEvent.click(screen.getByRole("button", { name: t("common.discardChanges") }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("字幕组 tab 保留刷新按钮但无种子/待预选列表，展示订阅情况", async () => {
    renderDraft("subgroups");
    await flushQuery();

    // The proactive fetch button is kept (a provider entry; draftEntry is custom so it does not
    // show) — custom entries show no refresh subgroups
    expect(screen.queryByRole("button", { name: t("config.discovery.refresh") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "查看种子" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("待预选字幕组")).not.toBeInTheDocument();
    // Custom no longer shows the meaningless subscription-count entry.
    expect(screen.queryByRole("button", { name: t("forms.sources.maxButton") })).not.toBeInTheDocument();
  });

  it("provider 条目字幕组 tab 显示刷新字幕组按钮", async () => {
    renderDraft("subgroups");
    await flushQuery();
    const providerEntry: CatalogEntryView = {
      providerEntryRef: "mikan:entry-1",
      source: "mikan",
      title: "测试番",
      metadataMatch: { matched: true },
      metadataHold: { held: false, missing: [] },
    };
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={{ key: "mikan", label: "Mikan", isMock: false } as CatalogProvider}
          entry={providerEntry}
          sourceLabel="Mikan"
          onSubscribed={() => {}}
          initialTab="subgroups"
        />
      </QueryClientProvider>,
    );
    await flushQuery();
    expect(screen.getByRole("button", { name: t("config.discovery.refresh") })).toBeInTheDocument();
  });

  it("已保存的 custom 预设打开 RSS tab 时与 provider 条目一样自动抓取快照", async () => {
    // Every custom card in the catalog carries a candidate (saving a preset creates a
    // revision), so the "save first" state is unreachable through real entries: opening the RSS
    // tab should take the same auto-backfill path as a provider entry.
    let customFetches = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/anime/rss-list-view")) {
        return Response.json({
          mode: "current_input",
          config_identity: "draft:test",
          metadata_hold: { held: false, missing: [] },
          versions: { parser: "p", evaluator: "e", renderer: "r", safety: "s", torrent_parser: "t" },
          generated_at: "2026-10-21T00:00:00Z",
          groups: [{
            scope: { provider: "custom", provider_entry_ref: "custom-source:abc", provider_group_ref: "custom-group:abc" },
            display_name: "测试字幕组",
            score: 0,
            selection: { source: "configured", effective_selected: true, selection_mode: "auto" },
            offset: { value: 0, editable: false },
            feed: { state: "snapshot_required" },
            items: [],
          }],
        });
      }
      if (url.includes("/catalog/custom/feed-snapshots:fetch")) {
        customFetches += 1;
        return Response.json({
          provider: "custom",
          provider_entry_ref: "custom-source:abc",
          group_ref: "custom-group:abc",
          observed_at: "2026-10-21T00:00:00Z",
          snapshot_id: "snap-c",
          snapshot_version: 1,
          content_hash: "sha256:" + "a".repeat(64),
          freshness: "fresh",
          seeds: [],
        });
      }
      if (url.includes("/template-catalogs/naming") || url.includes("/template-previews/naming")) {
        return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      }
      void init;
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(async () => [candidateFixture({})]));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={draftEntry}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab="rss"
        />
      </QueryClientProvider>,
    );
    await flushQuery();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    await flushQuery();

    expect(customFetches).toBe(1);
    expect(screen.queryByText(t("rss.customBenchSaveFirst"))).not.toBeInTheDocument();
  });

  it("未保存的自定义草稿 RSS tab 显示测试台与先保存提示，不报错", async () => {
    renderDraft("rss");
    await flushQuery();
    // Advance the 500ms debounce so the preview completes (otherwise the button stays in its "refreshing..." pending state).
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    await flushQuery();

    // An unsaved custom draft: the test bench still displays, only noting that seeds of that source can be read after saving.
    expect(screen.queryByText(/failed typed validation|预览失败/)).not.toBeInTheDocument();
    expect(screen.getByText(t("rss.customBenchSaveFirst"))).toBeInTheDocument();
  });
});

// Draft-save regression: dirty must clear immediately after the candidate saves (a live-caught
// regression where savedDraftRef being a ref kept dirty forever).
//
// One-shot downloads have no top-level entry: they are triggered by each seed row's download
// icon in the unified RSS workspace, with the admission reason given by the server's
// download_action.
describe("AnimeConfigDialog 草稿保存与 RSS 预览", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  // A custom-source draft has no provider seed structure: the naming template endpoint returns
  // 400 per contract and the other reads fall back to empty objects; this regression relies only
  // on the candidate save channel.
  function stubCustomDraftFetch() {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/template-catalogs/naming") || url.includes("/template-previews/naming")) {
        return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    }));
  }

  // No quick subscribe on the card face: a new entry opened from a catalog card has no candidate
  // yet, the subscribe button is available immediately, and clicking it first creates the
  // candidate from the current content, then activates with the new candidate's id / version /
  // revision.
  it("没有候选的干净草稿可直接订阅：先创建候选再激活它", async () => {
    stubCustomDraftFetch();
    const created = candidateFixture({ id: "candidate-new", version: 4, current_revision: 2 });
    const saveMock = vi.fn(async () => created);
    const subscribeMock = vi.fn(async () => ({
      managed_anime: { id: "anime-new" },
      subscription: { state: "pending", version: 1 },
    }) as unknown as SubscribeResult);
    const onSubscribed = vi.fn();
    vi.mocked(getCandidateStore).mockReturnValue({
      ...candidateStoreMock(async () => []),
      save: saveMock,
      subscribe: subscribeMock,
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={draftEntryWithoutCandidate}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={onSubscribed}
          initialTab="rss"
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    expect(screen.getByText(t("config.footerUnsaved"))).toBeInTheDocument();
    const subscribe = screen.getByRole("button", { name: t("anime.subscribe") });
    expect(subscribe).toBeEnabled();
    fireEvent.click(subscribe);
    await flushQuery();

    expect(saveMock).toHaveBeenCalledTimes(1);
    expect(subscribeMock).toHaveBeenCalledTimes(1);
    expect(subscribeMock).toHaveBeenCalledWith("candidate-new", 4, expect.any(String), { revision: 2 });
    expect(onSubscribed).toHaveBeenCalledTimes(1);
  });

  it("没有候选的干净草稿也能单独保存", async () => {
    stubCustomDraftFetch();
    const saveMock = vi.fn(async (draft: AnimeDraft) => candidateFixture({ draft, version: 1 }));
    vi.mocked(getCandidateStore).mockReturnValue({
      ...candidateStoreMock(async () => []),
      save: saveMock,
    });
    renderDraftDialog();
    await flushQuery();
    fireEvent.click(screen.getByRole("button", { name: t("config.saveButton") }));
    await flushQuery();
    expect(saveMock).toHaveBeenCalledTimes(1);
    expect(screen.getByText(t("config.footerSaved"))).toBeInTheDocument();
  });

  it("保存草稿后 dirty 立即清掉：已保存 v1 出现，订阅按钮放开（webtest 回归）", async () => {
    stubCustomDraftFetch();
    const saveMock = vi.fn(async (draft: AnimeDraft) => candidateFixture({ draft, version: 1 }));
    vi.mocked(getCandidateStore).mockReturnValue({
      ...candidateStoreMock(async () => []),
      save: saveMock,
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={draftEntryWithoutCandidate}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
          initialTab="rss"
        />
      </QueryClientProvider>,
    );
    await flushQuery();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600);
    });
    await flushQuery();

    // Typing a title makes the draft dirty.
    fireEvent.click(screen.getByRole("tab", { name: t("tabs.names.metadata") }));
    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    fireEvent.change(within(titleRegion).getByLabelText(t("tabs.metadata.title")), { target: { value: "改名番" } });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.saveTitle") }));
    expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("config.saveButton") }));
    await flushQuery();

    // A live-caught regression: savedDraftRef was a ref and the dirty useMemo depended only on
    // [draft], so after saving the draft reference never changed → dirty stuck true forever and
    // the subscribe button permanently disabled.
    expect(saveMock).toHaveBeenCalledTimes(1);
    expect(screen.getByText(t("config.footerSaved"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("anime.subscribe") })).toBeEnabled();

    // After saving the RSS preview tab still opens (the download action is judged per seed row's
    // download_action; the dialog header no longer has a one-shot entry).
    fireEvent.click(screen.getByRole("tab", { name: t("tabs.names.rssPreview") }));
    expect(screen.getByRole("tab", { name: t("tabs.names.rssPreview") })).toHaveAttribute("aria-selected", "true");
    await flushQuery();
    // Saved: the test bench fetches source snapshots from the saved preset revision and no longer asks to save first.
    expect(screen.queryByText(t("rss.customBenchSaveFirst"))).not.toBeInTheDocument();
  });
});

// Title-area facts (from the card redesign): the original title, the Bangumi / TMDB links and
// the anime-page link moved from the poster card into the config dialog's title area.
describe("AnimeConfigDialog 标题区事实", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("显示原名、Bangumi / TMDB 外链与番剧页外链", () => {
    renderDialog([workspaceFixture()], 0, {
      ...entry,
      originalTitle: "テスト番",
      sourceUrl: "https://mikanani.me/Home/Bangumi/3701",
      metadataMatch: { matched: true, bgmId: "123", tmdbWorkId: "456", tmdbMediaKind: "tv", tmdbSeason: 1 },
    });
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("テスト番")).toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "123" })).toHaveAttribute("href", "https://bgm.tv/subject/123");
    expect(within(dialog).getByRole("link", { name: /456/ })).toHaveAttribute("href", "https://www.themoviedb.org/tv/456");
    expect(within(dialog).getByRole("link", { name: new RegExp(t("grid.card.sourcePage")) })).toHaveAttribute(
      "href",
      "https://mikanani.me/Home/Bangumi/3701",
    );
  });

  it("挂起缺失的关联在标题区用短形（未关联 Bangumi）", () => {
    renderDialog([workspaceFixture()], 0, { ...entry, metadataMatch: { matched: false }, metadataHold: { held: true, missing: ["bgm"] } });
    expect(within(screen.getByRole("dialog")).getByText(t("anime.metadata.missing", { names: "Bangumi" }))).toBeInTheDocument();
  });
});

// The title area's metadata follows the current source: a draft reads the in-memory draft's
// bindings, an owner reads the workspace, and neither stays on the catalog entry frozen at
// open.
describe("AnimeConfigDialog 标题区元数据同步", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("草稿匹配 TMDB 后标题区立即去掉未关联提示并显示已匹配", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/metadata/search")) {
        return Response.json({
          provider: "tmdb",
          query: "q",
          candidates: [{
            provider: "tmdb",
            provider_work_id: "777",
            title: "测试番剧",
            media_kind: "tv",
            details_status: "available",
            seasons: [{ season_number: 1, name: "第一季", episode_count: 12, air_date: "2024-04-01", is_special: false }],
            suggested_season: 1,
            year: 2024,
            score: 1,
          }],
        });
      }
      return new Response(url.includes("/config/global-sections") ? "[]" : "{}", {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }));
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(vi.fn(async () => [])));
    const providerEntry: CatalogEntryView = {
      providerEntryRef: "mikan:entry-1",
      source: "mikan",
      title: "测试番",
      metadataMatch: { matched: false, bgmId: "bgm-42" },
    };
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={{ key: "mikan", label: "Mikan", isMock: false } as CatalogProvider}
          entry={providerEntry}
          sourceLabel="Mikan"
          onSubscribed={() => {}}
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    const dialog = () => screen.getAllByRole("dialog")[0]!;
    const missingTmdb = t("anime.metadata.missing", { names: "TMDB" });
    expect(within(dialog()).getByText(missingTmdb)).toBeInTheDocument();
    expect(within(dialog()).queryByText(t("config.metadataMatched"))).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("forms.matchProvider", { provider: t("forms.providerTmdb") }) }));
    await flushQuery();
    const match = screen.getByRole("dialog", { name: t("forms.matchTitle", { provider: "TMDB" }) });
    fireEvent.click(within(match).getByRole("button", { name: t("forms.search") }));
    await flushQuery();
    fireEvent.click(within(match).getByRole("option", { name: /测试番剧/ }));
    fireEvent.click(within(match).getByRole("button", { name: t("forms.confirmMatch") }));
    await flushQuery();

    expect(within(dialog()).queryByText(missingTmdb)).not.toBeInTheDocument();
    expect(within(dialog()).getByText(t("config.metadataMatched"))).toBeInTheDocument();
    const headFacts = dialog().querySelector<HTMLElement>(".anime-config-head-facts")!;
    expect(within(headFacts).getByRole("link", { name: /777/ })).toHaveAttribute("href", "https://www.themoviedb.org/tv/777");
  });

  it("custom 预设草稿已绑定 BGM/TMDB 时标题区显示关联", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));
    const customEntry: CatalogEntryView = { ...draftEntry, metadataMatch: { matched: false } };
    const bound = candidateFixture({
      draft: {
        ...draftFromEntry(customEntry),
        metadata: {
          bgm: { work_id: "bgm-9" },
          tmdb: { work_id: "777", media_kind: "tv", season: 2 },
          total_episodes: null,
          year: null,
          season: null,
        },
      },
    });
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(async () => [bound]));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={customEntry}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    const dialog = screen.getAllByRole("dialog")[0]!;
    const headFacts = dialog.querySelector<HTMLElement>(".anime-config-head-facts");
    if (!headFacts) throw new Error("header facts not found");
    expect(within(headFacts).getByRole("link", { name: "bgm-9" })).toHaveAttribute("href", "https://bgm.tv/subject/bgm-9");
    expect(within(headFacts).getByRole("link", { name: /777/ })).toHaveAttribute("href", "https://www.themoviedb.org/tv/777");
  });

  it("custom 预设未绑定关联时与 provider 目录同样显示未关联", async () => {
    // An empty preset does not hide the badge row: no binding means "Bangumi and TMDB not
    // linked", the same terms as a provider browse entry (subscribing does not depend on the
    // links; a missing link is only a fact).
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));
    const customEntry: CatalogEntryView = { ...draftEntry, metadataMatch: { matched: false } };
    vi.mocked(getCandidateStore).mockReturnValue(candidateStoreMock(async () => []));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <AnimeConfigDialog
          open
          onOpenChange={() => {}}
          provider={provider}
          entry={customEntry}
          sourceLabel={t("grid.tabCustom")}
          onSubscribed={() => {}}
        />
      </QueryClientProvider>,
    );
    await flushQuery();

    const dialog = screen.getAllByRole("dialog")[0]!;
    const headFacts = dialog.querySelector<HTMLElement>(".anime-config-head-facts");
    if (!headFacts) throw new Error("header facts not found");
    expect(within(headFacts).getByText(t("anime.metadata.missing", { names: "Bangumi、TMDB" }))).toBeInTheDocument();
  });

  it("owner 标题区以 workspace 为准：冻结条目仍缺 TMDB，workspace 已关联则不再提示", async () => {
    const workspace = workspaceFixture();
    workspace.metadata = { ...workspace.metadata, tmdb: { work_id: "456", media_kind: "tv", season: 1 } };
    workspace.metadata_hold = { held: false, missing: [] };
    renderDialog([workspace], 0, { ...entry, metadataMatch: { matched: false }, metadataHold: { held: true, missing: ["tmdb"] } });
    const dialog = () => screen.getAllByRole("dialog")[0]!;
    expect(within(dialog()).getByText(t("anime.metadata.missing", { names: "TMDB" }))).toBeInTheDocument();
    await flushQuery();

    expect(within(dialog()).queryByText(t("anime.metadata.missing", { names: "TMDB" }))).not.toBeInTheDocument();
    const headFacts = dialog().querySelector<HTMLElement>(".anime-config-head-facts")!;
    expect(within(headFacts).getByRole("link", { name: /456/ })).toHaveAttribute("href", "https://www.themoviedb.org/tv/456");
  });
});

// The header summary row (the overview restored): an owner's four cells come from the injected
// workspace read model; a draft shows "not subscribed" and a cell without data shows "—" instead
// of disappearing.
describe("AnimeConfigDialog 头部摘要行", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("owner 模式信息行只有种子数与最近更新，状态只在标题胶囊", async () => {
    const workspace = workspaceFixture();
    // Gives the release a publish time, locking the "last updated" cell to the newest seed's time fact.
    workspace.releases[0] = {
      ...workspace.releases[0]!,
      raw_facts: { title: "[测试字幕组] 测试番 - 01", published_at: "2026-08-20T10:00:00Z" },
    };
    renderDialog([workspace]);
    await flushQuery();

    const summary = screen.getByRole("status", { name: t("config.summary.aria") });
    expect(within(summary).queryByText(t("anime.state.active"))).not.toBeInTheDocument();
    expect(within(summary).getByText(t("config.summary.seeds"))).toBeInTheDocument();
    expect(within(summary).getByText("1")).toBeInTheDocument();
    expect(within(summary).getByText(t("config.summary.latest"))).toBeInTheDocument();
    // formatDateTime outputs localized time; the test only locks that it is non-placeholder, i.e. from the read model.
    expect(within(summary).queryByText("—")).not.toBeInTheDocument();
  });

  it("未开始的订阅显示「未开始」与开始订阅；没有刷新入口，开始后转为运行中", async () => {
    const pending = workspaceFixture();
    pending.subscription = { ...pending.subscription, state: "pending" };
    renderDialog([pending, workspaceFixture()], 0, { ...entry, subscriptionState: "pending" });
    await flushQuery();
    await flushQuery();

    expect(screen.getAllByText(t("anime.state.pending")).length).toBeGreaterThan(0);
    expect(screen.getAllByText(t("anime.pending.banner")).length).toBe(1);
    // The state is expressed by the title chip and the banner; the summary row no longer repeats "subscription state: not started".
    const summary = screen.getByRole("status", { name: t("config.summary.aria") });
    expect(within(summary).queryByText(t("config.summary.state"))).not.toBeInTheDocument();
    expect(within(summary).queryByText(t("anime.state.pending"))).not.toBeInTheDocument();
    // A not-yet-started subscription (custom sources alike) is a test bench: no refresh entry.
    expect(screen.queryByRole("button", { name: new RegExp(t("rss.refresh")) })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("anime.start") }));
    await flushQuery();
    await flushQuery();

    const fetchMock = vi.mocked(globalThis.fetch);
    const patch = fetchMock.mock.calls.find(
      ([input, init]) => init?.method === "PATCH" && String(input).endsWith("/anime/anime-1/subscription"),
    );
    expect(patch).toBeDefined();
    expect(JSON.parse(String(patch![1]?.body))).toEqual({ expected_version: 1, state: "active" });
    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith(t("anime.started"));
    expect(screen.queryByRole("button", { name: t("anime.start") })).not.toBeInTheDocument();
    expect(screen.queryByText(t("anime.pending.banner"))).not.toBeInTheDocument();
  });

  it("订阅中缺 TMDB 关联：信息行换成长形警告行，修复动作打开 TMDB 匹配", async () => {
    const workspace = workspaceFixture();
    workspace.metadata_hold = { held: true, missing: ["tmdb"] };
    renderDialog([workspace]);
    await flushQuery();

    expect(screen.queryByRole("status", { name: t("config.summary.aria") })).not.toBeInTheDocument();
    expect(screen.getByText(t("anime.metadata.paused", { names: "TMDB" }))).toBeInTheDocument();
    // With links missing, nothing anywhere claims "matched".
    expect(screen.queryByText(/已匹配/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("anime.metadata.link", { name: "TMDB" }) }));
    await flushQuery();
    // Goes through the metadata tab's same match dialog, stacked above the config dialog.
    expect(screen.getByRole("dialog", { name: t("forms.matchTitle", { provider: "TMDB" }) })).toBeInTheDocument();
  });

  it("未开始且缺关联：警告行写短形，不说“已暂停”", async () => {
    const workspace = workspaceFixture();
    workspace.subscription = { ...workspace.subscription, state: "pending" };
    workspace.metadata_hold = { held: true, missing: ["bgm", "tmdb"] };
    renderDialog([workspace], 0, { ...entry, subscriptionState: "pending" });
    await flushQuery();

    expect(screen.getAllByText(t("anime.metadata.missing", { names: "Bangumi、TMDB" })).length).toBeGreaterThan(0);
    expect(screen.queryByText(t("anime.metadata.paused", { names: "Bangumi、TMDB" }))).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("anime.metadata.link", { name: "Bangumi" }) })).toBeInTheDocument();
  });

  it("draft 模式显示「未订阅」，无数据源格子显示「—」", async () => {
    renderDraftDialog();
    await flushQuery();

    const summary = screen.getByRole("status", { name: t("config.summary.aria") });
    expect(within(summary).getByText(t("anime.state.draft"))).toBeInTheDocument();
    // A draft judges no metadata on the info row; with no read model, seeds-collected/last-updated must show placeholders.
    expect(within(summary).getAllByText("—")).toHaveLength(2);
  });
});

// The retired owner's read-only workspace contract: the same workspace read model, with retired
// only switching off mutations. History (releases/downloads/verification/imports/processing/
// logs) stays readable, while config saves, refresh, retry, release and rules preview render
// nowhere.
describe("AnimeConfigDialog retired 只读工作区", () => {
  const retiredEntry: CatalogEntryView = {
    ...entry,
    subscriptionState: "stopped",
    lifecycleState: "retired",
    lifecycleEpoch: 2,
  };

  function retiredWorkspace(): AnimeWorkspace {
    const workspace = workspaceFixture();
    workspace.subscription = { ...workspace.subscription, state: "stopped", lifecycle_state: "retired", lifecycle_epoch: 2 };
    workspace.rss_source_runtimes = [{
      rss_source_id: "source-1",
      health: "failed",
      reason_code: "feed_fetch_failed",
      failed_work_item_id: "work-rss-1",
      version: 2,
    }];
    return workspace;
  }

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("RSS tab 保留完整 owner 链与处理过程（执行记录）入口，不渲染任何 mutation 按钮", async () => {
    renderDialog([retiredWorkspace()], 0, retiredEntry);
    await flushQuery();
    await flushQuery();

    // The read-only banner and the owner chain (the history facts) are visible.
    expect(screen.getByText(t("anime.ended.banner"))).toBeInTheDocument();
    expect(screen.getByText(new RegExp(t("rss.endedNotice")))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("rss.expandGroup", { name: "测试字幕组" }) })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "测试字幕组" }) }));
    fireEvent.click(screen.getByRole("button", { name: t("records.openFromRelease") }));
    expect(openRecordSpy).toHaveBeenCalledWith({ acquisitionID: "acquisition-1" });
    fireEvent.click(screen.getByRole("button", { name: t("records.title") }));
    expect(openRecordSpy).toHaveBeenCalledWith({ managedAnimeID: "anime-1" });

    // Every mutation entry: refresh/scan/retry/release.
    expect(screen.queryByRole("button", { name: new RegExp(t("rss.refresh")) })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("rss.scan")) })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("rss.retryRelease") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("rss.retryRefresh")) })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("rss.adjustOffset", { name: "测试字幕组" })) })).not.toBeInTheDocument();
  });

  it("元数据 tab 展示生效值但没有编辑/保存入口", async () => {
    renderDialog([retiredWorkspace()], 0, retiredEntry);
    await flushQuery();

    fireEvent.click(screen.getByRole("tab", { name: t("tabs.names.metadata") }));
    // The title shows the effective value read-only, no input rendered.
    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    expect(within(titleRegion).getByText("测试番")).toBeInTheDocument();
    expect(within(titleRegion).queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^保存/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("forms.matchProvider", { provider: "Bangumi" })) })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("forms.clearAria", { label: t("forms.bangumi") })) })).not.toBeInTheDocument();
  });

  it("字幕组与规则 tab 只读，不渲染保存/发现/预览入口", async () => {
    renderDialog([retiredWorkspace()], 0, retiredEntry);
    await flushQuery();

    fireEvent.click(screen.getByRole("tab", { name: t("field.subgroup") }));
    expect(screen.getByText(t("tabs.subgroupsReadOnly"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("config.discovery.refresh")) })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: t("tabs.names.rules") }));
    expect(screen.getByText(t("tabs.rulesReadOnly"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("tabs.rules.preview")) })).not.toBeInTheDocument();
  });
});
