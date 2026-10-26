import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RSSListItem, RSSListGroup, RSSListView } from "../../lib/api/client";
import { createEmptyDraft, createEmptySourceDraft } from "../../lib/draft";
import { t } from "../../lib/i18n";
import { namingFactLabel, reasonLabel } from "../../lib/i18n/reasons";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { preset as businessPreset } from "../../testing/businessPreset";
import { OneShotDialog } from "./OneShotDialog";
import type { SearchSeedBrowse } from "./useOneShotSeeds";
const toastMock = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }));
vi.mock("sonner", () => ({ toast: toastMock }));

// The two-step "browse seeds" dialog: step one is a pure catalog (RSS facts only, local pagination,
//   a seed's archive fetched only on open), step two is config left / render right with in-place
//   submission. Stubs go through the real client's URLs, asserting:
//   - every fact shown comes from the server read model, never re-judged by the frontend; the catalog
//     read uses one_shot and is not limited by subgroup selection;
//   - each seed creates its own one-shot download, with retries reusing the same idempotency key;
//   - step-two config changes (custom preset included) only re-read the read model; submission sends
//     the edited draft and nothing writes back to the preset.

function seedItem(key: string, title: string, extra: Partial<RSSListItem> = {}): RSSListItem {
  return {
    snapshot_id: "snap-1",
    stable_seed_key: key,
    seed: { title, locator_display: `https://mikanani.invalid/${key}.torrent`, size_bytes: 1024 * 1024 * 700 },
    torrent: { state: "available" },
    evaluation: { state: "evaluated", verdict: "accepted_pending_download" },
    files: [
      {
        file_ref: `${key}-video`,
        ordinal: 0,
        source_path: `[组A] ${title}.mkv`,
        filename: `${title}.mkv`,
        media_kind: "video",
        included: true,
        rendered_target: `/media/测试番剧/${title}.mkv`,
        safety: "valid",
      },
      {
        file_ref: `${key}-readme`,
        ordinal: 1,
        source_path: "说明.txt",
        filename: "说明.txt",
        media_kind: "other",
        included: false,
        reason_code: "file_must_contain_not_matched",
        safety: "valid",
      },
    ],
    download_action: { allowed: true, kind: "one_shot" },
    // The write set is only the included subtitles/videos and files with a safe target: here 1 video, not held in the library.
    library_overlap: { verdict: "absent", present_files: 0, pending_files: 1 },
    ...extra,
  };
}

function seedGroup(overrides: Partial<RSSListGroup> = {}): RSSListGroup {
  return {
    scope: { provider: "anibt", provider_entry_ref: "entry-1", provider_group_ref: "g-1" },
    display_name: "组A",
    selection: { source: "configured", effective_selected: true, selection_mode: "auto", count_in_limit: true },
    offset: { value: 0, editable: true, source: "link" },
    feed: { state: "fresh", snapshot_id: "snap-1", snapshot_version: 3, content_hash: "hash-1" },
    items: [seedItem("seed-a", "[组A] 测试番剧 - 01 [1080p]")],
    ...overrides,
  };
}

function viewOf(groups: RSSListGroup[]): RSSListView {
  return {
    mode: "current_input",
    config_identity: "identity",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "p", evaluator: "e", renderer: "r", safety: "s", torrent_parser: "t" },
    generated_at: "2026-10-17T00:00:00Z",
    groups,
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
  };
}

function providerEntry(overrides: Partial<CatalogEntryView> = {}): CatalogEntryView {
  return {
    providerEntryRef: "entry-1",
    source: "anibt",
    title: "测试番剧",
    metadataMatch: { matched: true, bgmId: "bgm-42" },
    ...overrides,
  };
}

const customEntry: CatalogEntryView = {
  providerEntryRef: "",
  candidateId: "cand-1",
  source: "custom",
  title: "自定义番剧",
  metadataMatch: { matched: false },
};

type Download = { status: number; body?: unknown };
type Expand = { state: "available" | "unavailable"; error_code?: string };

/** The stub scenario: lists only the boundaries this dialog walks; unlisted paths fail outright so unexpected requests surface. */
type Scenario = {
  discovery?: { status: number; body?: unknown };
  view?: RSSListView;
  viewStatus?: number;
  /** The error body for a non-200 read (defaults to a generic INTERNAL). */
  viewErrorBody?: unknown;
  candidate?: unknown;
  customFetch?: { status: number; body?: unknown };
  download?: (stableSeedKey: string) => Download | "network";
  /** Single-group refresh (POST .../rss): returns the new snapshot identity; by default that group's refresh fails. */
  refresh?: { status: number; body?: unknown };
  /** Seed expansion: by default the fetch succeeds (the container already holds readable bytes). */
  expand?: (stableSeedKey: string) => Expand;
  /** Parser catalog: showing the parser section requires at least two parsers with release-title capability. */
  parsers?: { id: string; display_name: string; capabilities: string[] }[];
  /** Discovered subgroups; by default the anibt groups from the read model. */
  discoveryGroups?: { group_ref: string; display_name: string }[];
  /** The persisted seed list (GET .../rss): by default the fresh snap-1. */
  persisted?: (groupRef: string) => { status: number; body?: unknown };
  /** Answers the read model per read request (e.g. different episodes per the draft's parser). */
  viewFor?: (body: { draft?: { business?: { parser?: { kind?: string } } } }) => RSSListView;
  /** TMDB search results (GET /metadata/search). */
  metadataCandidates?: unknown[];
};

type Call = { url: string; body?: unknown; idempotencyKey: string | null };

let calls: Call[] = [];

function stubFetch(scenario: Scenario) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const headers = new Headers(init?.headers);
    const record = (body?: unknown) => calls.push({ url, body, idempotencyKey: headers.get("Idempotency-Key") });

    if (url.endsWith("/config/global-sections")) {
      record();
      return Response.json([]);
    }

    // Step two shows the parser section only when the instance offers multiple parsers; by default it answers "just one".
    if (url.endsWith("/parser-catalog")) {
      record();
      return Response.json(
        scenario.parsers ?? [{ id: "regex", display_name: "Regex", capabilities: ["release_title"] }],
      );
    }

    // A one-shot draft must first carry the Global business snapshot: the server fails closed for a
//     whole page on a draft without one (BUSINESS_SNAPSHOT_REQUIRED) and no seed is readable.
    if (url.endsWith("/config/business-preset")) {
      record();
      return Response.json(businessPreset);
    }

    if (url.includes("/groups:discover")) {
      record();
      const discovery = scenario.discovery ?? { status: 200 };
      if (discovery.status !== 200) {
        return Response.json(discovery.body ?? { code: "INTERNAL", message: "内部错误" }, { status: discovery.status });
      }
      return Response.json({
        provider: "anibt",
        provider_entry_ref: "entry-1",
        observed_at: "2026-10-17T00:00:00Z",
        source_version: "1",
        schema_version: "1",
        snapshot_version: 42,
        groups: (
          scenario.discoveryGroups ??
          (scenario.view ?? viewOf([seedGroup()])).groups
            .filter((group) => group.scope.provider === "anibt")
            .map((group) => ({ group_ref: group.scope.provider_group_ref, display_name: group.display_name }))
        ).map((group) => ({ provider: "anibt", ...group, feed_url: "https://example.invalid/feed", observed_at: "2026-10-17T00:00:00Z" })),
      });
    }

    // Entering a group first reads its persisted seed list (a GET, no network); a fresh one is used directly.
    const persistedRead = url.match(/\/entries\/entry-1\/rss\?group_ref=([^&]+)$/);
    if (persistedRead && (init?.method ?? "GET") === "GET") {
      record();
      const groupRef = decodeURIComponent(persistedRead[1]!);
      const persisted = scenario.persisted?.(groupRef) ?? { status: 200 };
      if (persisted.status !== 200) {
        return Response.json(persisted.body ?? { code: "INTERNAL", message: "内部错误" }, { status: persisted.status });
      }
      return Response.json(
        persisted.body ?? {
          provider: "anibt",
          provider_entry_ref: "entry-1",
          group_ref: groupRef,
          snapshot_id: "snap-1",
          snapshot_version: 3,
          freshness: "fresh",
          content_hash: "hash-1",
          seeds: [],
        },
      );
    }

    if (url.includes("/metadata/search")) {
      record();
      return Response.json({ provider: "tmdb", query: "q", candidates: scenario.metadataCandidates ?? [] });
    }

    if (url.endsWith("/entry-1/rss")) {
      record(JSON.parse(String(init?.body)));
      const refresh = scenario.refresh ?? { status: 200 };
      if (refresh.status !== 200) {
        return Response.json(refresh.body ?? { code: "PROVIDER_ERROR", message: "刷新失败" }, { status: refresh.status });
      }
      return Response.json({
        provider: "anibt",
        provider_entry_ref: "entry-1",
        group_ref: "g-1",
        snapshot_id: "snap-2",
        snapshot_version: 9,
        freshness: "fresh",
        content_hash: "hash-2",
        seeds: [],
      });
    }

    if (url.includes("/anime-config-candidates/")) {
      // A one-shot task's changes never write back to the preset: only candidate reads are allowed here.
      if ((init?.method ?? "GET") !== "GET") throw new Error(`unexpected candidate write ${init?.method} ${url}`);
      record();
      return Response.json(
        scenario.candidate ?? {
          id: "cand-1",
          origin: "custom",
          origin_entry_ref: null,
          title: "自定义番剧",
          title_override: null,
          instance_label: "自定义番剧",
          draft: { ...createEmptyDraft(), origin: "custom", title: "自定义番剧" },
          version: 7,
          current_revision: 3,
          activated_managed_anime_id: null,
          created_at: "2026-10-17T00:00:00Z",
          updated_at: "2026-10-17T00:00:00Z",
          state: "working",
        },
      );
    }

    if (url.endsWith("/catalog/custom/feed-snapshots:fetch")) {
      record(JSON.parse(String(init?.body)));
      const fetched = scenario.customFetch ?? { status: 200 };
      if (fetched.status !== 200) {
        return Response.json(fetched.body ?? { code: "PROVIDER_ERROR", message: "抓取失败" }, { status: fetched.status });
      }
      return Response.json({
        provider: "custom",
        provider_entry_ref: "custom-source",
        group_ref: "custom-group",
        snapshot_id: "custom-snap",
        snapshot_version: 2,
        content_hash: "custom-hash",
        freshness: "fresh",
        seeds: [],
      });
    }

    if (url.endsWith("/anime/rss-list-view")) {
      record(JSON.parse(String(init?.body)));
      if (scenario.viewStatus && scenario.viewStatus !== 200) {
        return Response.json(scenario.viewErrorBody ?? { code: "INTERNAL", message: "内部错误" }, { status: scenario.viewStatus });
      }
      const body = calls.at(-1)!.body as Parameters<NonNullable<Scenario["viewFor"]>>[0];
      return Response.json(scenario.viewFor?.(body) ?? scenario.view ?? viewOf([seedGroup()]));
    }

    const expand = url.match(/\/provider-feed-snapshots\/([^/]+)\/seeds\/([^/]+)\/expand$/);
    if (expand) {
      const stableSeedKey = decodeURIComponent(expand[2]!);
      record(JSON.parse(String(init?.body)));
      const outcome = scenario.expand?.(stableSeedKey) ?? { state: "available" };
      return Response.json({
        state: outcome.state,
        torrent:
          outcome.state === "available"
            ? { state: "available", manifest: { files: [] } }
            : { state: "unavailable", error_code: outcome.error_code ?? "torrent_fetch_failed" },
        ...(outcome.state === "available" ? {} : { error_code: outcome.error_code ?? "torrent_fetch_failed" }),
        cached: false,
      });
    }

    const download = url.match(/\/provider-feed-snapshots\/([^/]+)\/seeds\/([^/]+)\/download$/);
    if (download) {
      const stableSeedKey = decodeURIComponent(download[2]!);
      record(JSON.parse(String(init?.body)));
      const result = scenario.download?.(stableSeedKey) ?? { status: 200 };
      if (result === "network") throw new TypeError("Failed to fetch");
      if (result.status !== 200) {
        return Response.json(result.body ?? { code: "INTERNAL", message: "内部错误" }, { status: result.status });
      }
      return Response.json({
        one_shot_id: `os-${stableSeedKey}`,
        managed_anime_id: "m-1",
        subscription_id: "s-1",
        rss_source_id: "r-1",
        release_id: "rel-1",
        download_task_id: "dt-1",
        state: "running",
      });
    }

    throw new Error(`unexpected HTTP ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderDialog(entry: CatalogEntryView = providerEntry(), onOpenRecords?: () => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <OneShotDialog open entry={entry} onClose={() => {}} onOpenRecords={onOpenRecords} />
    </QueryClientProvider>,
  );
}

const SEED_A = "[组A] 测试番剧 - 01 [1080p]";
const SEED_A_TITLE = SEED_A;
const SEED_B = "[组A] 测试番剧 - 02 [1080p]";
const TARGET_A = `/media/测试番剧/${SEED_A}.mkv`;

const findSeed = (title: string) => screen.findByRole("checkbox", { name: title });
const button = (label: string) => screen.getByRole("button", { name: label });
const reads = () => calls.filter((call) => call.url.endsWith("/anime/rss-list-view"));
const downloads = () => calls.filter((call) => call.url.includes("/download"));

/** Step one lists the subgroups first (when there are two or more): a group's seeds appear only after entering it. */
async function enterGroup(name = "组A") {
  fireEvent.click(await screen.findByRole("button", { name: t("oneshot.openGroup", { name }) }));
}

/** A single subgroup: opening enters it directly. */
async function renderAndEnter(entry: CatalogEntryView = providerEntry(), onOpenRecords?: () => void) {
  const result = renderDialog(entry, onOpenRecords);
  await screen.findByRole("button", { name: t("oneshot.backToGroups") });
  return result;
}

/** The advanced section is folded by default: parser, file filters and naming need expanding first. */
function openAdvanced() {
  fireEvent.click(screen.getByRole("button", { name: new RegExp(`^${t("oneshot.advanced")}`) }));
}

const twoGroupView = () =>
  viewOf([
    seedGroup({ items: [seedItem("seed-a", SEED_A_TITLE, { seed: { title: SEED_A_TITLE, locator_display: "x", size_bytes: 1024 * 1024 * 700, published_at: "2026-10-19T07:12:57Z" } })] }),
    seedGroup({
      scope: { provider: "anibt", provider_entry_ref: "entry-1", provider_group_ref: "g-2" },
      display_name: "组B",
      items: [seedItem("seed-c", "[组B] 测试番剧 - 01 [720p]")],
    }),
  ]);

/** Checks a seed, enters step two, and waits for the first seed's render to appear. */
async function advanceToSettings(titles: string[]) {
  for (const title of titles) fireEvent.click(await findSeed(title));
  fireEvent.click(button(t("oneshot.nextWithCount", { n: titles.length })));
  await screen.findByRole("heading", { name: t("oneshot.configTitle") });
}

function twoSeedView() {
  return viewOf([seedGroup({ items: [seedItem("seed-a", SEED_A), seedItem("seed-b", SEED_B)] })]);
}

function pendingItem(key: string, title: string): RSSListItem {
  return seedItem(key, title, {
    torrent: { state: "pending" },
    evaluation: { state: "pending" } as RSSListItem["evaluation"],
    files: [],
    download_action: { allowed: false, kind: "none", reason_code: "seed_not_expanded" },
    library_overlap: { verdict: "undetermined" },
  });
}

beforeEach(() => {
  calls = [];
  for (const fn of Object.values(toastMock)) fn.mockReset();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("OneShotDialog 第一步：种子目录", () => {
  it("打开先只列字幕组：不取任何组的种子；进入一个组才取它的种子列表并按一次性浏览读取", async () => {
    stubFetch({ view: twoGroupView() });
    renderDialog();

    expect(await screen.findByRole("button", { name: t("oneshot.openGroup", { name: "组A" }) })).toBeInTheDocument();
    expect(screen.getByText(t("oneshot.groupsCount", { n: 2 }))).toBeInTheDocument();
    // Step one is the M size; only step two switches to the side-by-side L size.
    expect(screen.getByRole("dialog")).toHaveAttribute("data-tier", "M");
    expect(button(t("oneshot.nextWithCount", { n: 0 }))).toBeDisabled();
    expect(screen.getByText(t("oneshot.stepPosition", { n: 1 }), { exact: false })).toBeInTheDocument();
    await waitFor(() => {
      expect(calls.map((call) => call.url).sort()).toEqual(
        [
          "/api/v1/catalog/providers/anibt/entries/entry-1/groups:discover",
          "/api/v1/config/business-preset",
          "/api/v1/parser-catalog",
        ].sort(),
      );
    });

    await enterGroup();
    expect(await findSeed(SEED_A)).toBeInTheDocument();
    expect(calls.map((call) => call.url)).toContain("/api/v1/catalog/providers/anibt/entries/entry-1/rss?group_ref=g-1");
    expect(calls.some((call) => call.url.includes("group_ref=g-2"))).toBe(false);
    // One-shot browse is not limited by subgroup selection: the read request carries one_shot and only the entered groups.
    expect(reads()).toHaveLength(1);
    expect(reads()[0]!.body).toMatchObject({
      input_source: "current_input",
      one_shot: true,
      observation: { provider: "anibt", provider_entry_ref: "entry-1", snapshot_version: 42 },
      feed: [{ provider_group_ref: "g-1", snapshot_id: "snap-1", snapshot_version: 3, content_hash: "hash-1" }],
    });
  });

  it("持久化的种子列表已过期：只抓取进入的这一个组", async () => {
    stubFetch({
      persisted: () => ({
        status: 200,
        body: { provider: "anibt", provider_entry_ref: "entry-1", group_ref: "g-1", snapshot_id: "snap-1", snapshot_version: 3, freshness: "stale", content_hash: "hash-1", seeds: [] },
      }),
    });
    await renderAndEnter();
    await findSeed(SEED_A);
    const fetched = calls.filter((call) => call.url.endsWith("/entry-1/rss"));
    expect(fetched).toHaveLength(1);
    expect(fetched[0]!.body).toEqual({ group_ref: "g-1" });
    expect(reads().at(-1)!.body).toMatchObject({ feed: [{ snapshot_id: "snap-2", snapshot_version: 9 }] });
  });

  it("返回字幕组列表与切换组：已进入过的组不重复取，选择跨组保留", async () => {
    stubFetch({
      view: viewOf([
        seedGroup(),
        seedGroup({
          scope: { provider: "anibt", provider_entry_ref: "entry-1", provider_group_ref: "g-2" },
          display_name: "组B",
          feed: { state: "fresh", snapshot_id: "snap-1", snapshot_version: 3, content_hash: "hash-1" },
          items: [seedItem("seed-c", "[组B] 测试番剧 - 01 [720p]")],
        }),
      ]),
    });
    renderDialog();
    await enterGroup();
    fireEvent.click(await findSeed(SEED_A));
    expect(screen.queryByRole("checkbox", { name: "[组B] 测试番剧 - 01 [720p]" })).not.toBeInTheDocument();
    expect(calls.filter((call) => call.url.includes("group_ref=g-2"))).toHaveLength(0);

    fireEvent.click(button(t("oneshot.backToGroups")));
    // Entered groups show their seed count in the list.
    expect(await screen.findByText(t("oneshot.groupSeedCount", { n: 1 }))).toBeInTheDocument();
    await enterGroup("组B");
    fireEvent.click(await findSeed("[组B] 测试番剧 - 01 [720p]"));
    expect(screen.getByText(t("oneshot.selectedCount", { n: 2 }))).toBeInTheDocument();
    expect(reads().at(-1)!.body).toMatchObject({
      feed: [{ provider_group_ref: "g-1" }, { provider_group_ref: "g-2" }],
    });

    const readsBefore = reads().length;
    fireEvent.click(button(t("oneshot.backToGroups")));
    await enterGroup("组A");
    expect(await findSeed(SEED_A)).toBeChecked();
    expect(calls.filter((call) => call.url.includes("group_ref=g-1"))).toHaveLength(1);
    expect(reads().length).toBe(readsBefore);
  });

  it("目录只有 RSS 事实：不显示过滤结论、目标路径与库内重合", async () => {
    stubFetch({
      view: viewOf([
        seedGroup({
          items: [
            seedItem("seed-a", SEED_A),
            seedItem("seed-b", SEED_B, { evaluation: { state: "evaluated", verdict: "filtered", reason_code: "filtered_by_rules" } }),
          ],
        }),
      ]),
    });
    await renderAndEnter();
    await findSeed(SEED_B);
    const dialog = within(screen.getByRole("dialog"));
    expect(dialog.queryByText(t("verdict.filteredWithReason", { reason: reasonLabel("filtered_by_rules")! }))).not.toBeInTheDocument();
    expect(dialog.queryByText(TARGET_A)).not.toBeInTheDocument();
    expect(dialog.queryByText(t("oneshot.libraryAbsent"))).not.toBeInTheDocument();
    // Size and release time are RSS facts and display as usual.
    expect(dialog.getAllByText("700.0 MB", { exact: false }).length).toBeGreaterThan(0);
  });

  it("多选：计数正确，筛选只影响可见行不影响已选", async () => {
    stubFetch({ view: viewOf([seedGroup({ items: [seedItem("seed-a", SEED_A), seedItem("seed-b", SEED_B), seedItem("seed-c", "[组A] 别的番 - 01 [720p]")] })]) });
    await renderAndEnter();

    fireEvent.click(await findSeed(SEED_A));
    fireEvent.click(await findSeed("[组A] 别的番 - 01 [720p]"));
    expect(screen.getByText(t("oneshot.selectedCount", { n: 2 }))).toBeInTheDocument();

    fireEvent.change(screen.getByRole("textbox", { name: t("oneshot.seedFilter") }), { target: { value: "别的番" } });
    expect(screen.queryByRole("checkbox", { name: SEED_A })).not.toBeInTheDocument();
    expect(screen.getByText(t("oneshot.selectedCount", { n: 2 }))).toBeInTheDocument();

    fireEvent.change(screen.getByRole("textbox", { name: t("oneshot.seedFilter") }), { target: { value: "不存在" } });
    expect(screen.getByText(t("oneshot.noMatch"))).toBeInTheDocument();
  });

  it("组内一次列出全部种子：没有分页，也不再请求", async () => {
    const total = 45;
    const items = Array.from({ length: total }, (_, index) => seedItem(`seed-${index}`, `[组A] 测试番剧 - ${String(index + 1).padStart(2, "0")}`));
    stubFetch({ view: viewOf([seedGroup({ items })]) });
    await renderAndEnter();

    expect(await findSeed("[组A] 测试番剧 - 01")).toBeInTheDocument();
    expect(screen.getAllByRole("checkbox")).toHaveLength(total);
    expect(screen.getByRole("checkbox", { name: `[组A] 测试番剧 - ${total}` })).toBeInTheDocument();
    expect(screen.getByText(t("oneshot.groupSeedCount", { n: total }))).toBeInTheDocument();
    expect(reads()).toHaveLength(1);
  });

  it("点开未展开的种子：只取一次种子文件，随后重读并列出文件", async () => {
    let expanded = false;
    const scenario: Scenario = {
      get view() {
        return viewOf([seedGroup({ items: [expanded ? seedItem("seed-a", SEED_A) : pendingItem("seed-a", SEED_A)] })]);
      },
      expand: () => {
        expanded = true;
        return { state: "available" };
      },
    };
    stubFetch(scenario);
    await renderAndEnter();
    await findSeed(SEED_A);
    const readsBefore = reads().length;

    fireEvent.click(button(t("oneshot.showFiles", { title: SEED_A })));
    expect(await screen.findByText(`[组A] ${SEED_A}.mkv`, { exact: false })).toBeInTheDocument();
    expect(calls.filter((call) => call.url.includes("/expand"))).toHaveLength(1);
    expect(reads().length).toBe(readsBefore + 1);
    expect(reads().at(-1)!.body).toMatchObject({ one_shot: true });

    // Collapse then expand again: the files are already in the read model and are not fetched twice.
    fireEvent.click(button(t("oneshot.showFiles", { title: SEED_A })));
    fireEvent.click(button(t("oneshot.showFiles", { title: SEED_A })));
    expect(calls.filter((call) => call.url.includes("/expand"))).toHaveLength(1);
  });

  it("取种子文件失败：行内说明并可重试", async () => {
    stubFetch({
      view: viewOf([seedGroup({ items: [pendingItem("seed-a", SEED_A)] })]),
      expand: () => ({ state: "unavailable", error_code: "torrent_fetch_failed" }),
    });
    await renderAndEnter();
    await findSeed(SEED_A);
    fireEvent.click(button(t("oneshot.showFiles", { title: SEED_A })));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    fireEvent.click(button(t("act.retry")));
    await waitFor(() => expect(calls.filter((call) => call.url.includes("/expand"))).toHaveLength(2));
  });

  it("行内没有下载动作：只靠勾选加「下一步」进入第二步，只带勾选的种子", async () => {
    stubFetch({ view: twoSeedView() });
    await renderAndEnter();
    await findSeed(SEED_A);
    expect(screen.queryByRole("button", { name: t("rss.downloadAria", { title: SEED_A }) })).not.toBeInTheDocument();
    fireEvent.click(await findSeed(SEED_B));
    fireEvent.click(button(t("oneshot.nextWithCount", { n: 1 })));

    await screen.findByRole("heading", { name: t("oneshot.configTitle") });
    expect(screen.getByText(t("oneshot.stepPosition", { n: 2 }), { exact: false })).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toHaveAttribute("data-tier", "L");
    expect(await screen.findByText(`/media/测试番剧/${SEED_B}.mkv`)).toBeInTheDocument();
    expect(screen.queryByText(TARGET_A)).not.toBeInTheDocument();
    expect(button(t("oneshot.create", { n: 1 }))).toBeInTheDocument();
  });

  it("只有一个字幕组时直接进入；返回仍回到列表，列表上写种子数与最新时间", async () => {
    stubFetch({
      view: viewOf([
        seedGroup({
          items: [
            seedItem("seed-a", SEED_A, { seed: { title: SEED_A, locator_display: "x", size_bytes: 1024 * 1024 * 700, published_at: "2026-10-02T10:00:00Z" } }),
            seedItem("seed-b", SEED_B, { seed: { title: SEED_B, locator_display: "x", size_bytes: 1024 * 1024 * 700, published_at: "2026-10-19T07:12:57Z" } }),
          ],
        }),
      ]),
    });
    renderDialog();
    expect(await findSeed(SEED_A)).toBeInTheDocument();
    expect(calls.filter((call) => call.url.includes("group_ref=g-1"))).toHaveLength(1);
    // Seed meta facts join with " · ", not the CJK enumeration mark.
    const meta = screen.getAllByText(/700\.0 MB/)[0]!;
    expect(meta.textContent).toContain(" · ");
    expect(meta.textContent).not.toContain("、");

    fireEvent.click(button(t("oneshot.backToGroups")));
    const latest = new Date("2026-10-19T07:12:57Z").toLocaleDateString("zh-CN", { month: "numeric", day: "numeric", ...(new Date().getFullYear() === 2026 ? {} : { year: "numeric" }) });
    expect(
      await screen.findByText(`${t("oneshot.groupSeedCount", { n: 2 })} · ${t("oneshot.groupLatest", { date: latest })}`),
    ).toBeInTheDocument();
    // After going back it stays on the list and never auto-enters again.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.getByRole("button", { name: t("oneshot.openGroup", { name: "组A" }) })).toBeInTheDocument();
  });

  it("不可下载的种子：勾选框与下载都禁用并说明原因，全选跳过它", async () => {
    stubFetch({
      view: viewOf([
        seedGroup({
          items: [
            seedItem("seed-a", SEED_A),
            seedItem("seed-b", SEED_B, { download_action: { allowed: false, kind: "none", reason_code: "published_at_missing" } }),
          ],
        }),
      ]),
    });
    await renderAndEnter();

    expect(await findSeed(SEED_B)).toHaveAttribute("aria-disabled", "true");
    expect(screen.getAllByText(reasonLabel("published_at_missing")!).length).toBeGreaterThan(0);
    fireEvent.click(button(t("oneshot.selectAll")));
    expect(screen.getByText(t("oneshot.selectedCount", { n: 1 }))).toBeInTheDocument();
  });

  it("空态：没有字幕组、组里没有种子各有说明", async () => {
    stubFetch({ view: viewOf([]) });
    const { unmount } = renderDialog();
    expect(await screen.findByText(t("oneshot.noGroups"))).toBeInTheDocument();
    unmount();

    stubFetch({ view: viewOf([seedGroup({ items: [] })]) });
    await renderAndEnter();
    expect(await screen.findByText(t("oneshot.groupEmpty"))).toBeInTheDocument();
  });

  it("读取失败：显示错误与重试；重试成功后恢复字幕组列表", async () => {
    stubFetch({ discovery: { status: 500 } });
    renderDialog();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    const retry = button(t("act.retry"));
    stubFetch({});
    fireEvent.click(retry);
    // After the retry a single subgroup remains: entered directly.
    expect(await findSeed(SEED_A)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("进入组时取种子列表失败：在该组行内说明，可再次进入", async () => {
    stubFetch({ persisted: () => ({ status: 500 }), refresh: { status: 502 } });
    renderDialog();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    fireEvent.click(button(t("oneshot.backToGroups")));
    expect(await screen.findByRole("button", { name: t("oneshot.openGroup", { name: "组A" }) })).toBeEnabled();
  });

  it("读取结果形状异常时给出失败态，不渲染半截列表也不抛错", async () => {
    stubFetch({ view: { groups: "bad" } as unknown as RSSListView, discoveryGroups: [{ group_ref: "g-1", display_name: "组A" }] });
    renderDialog();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("分组过期：该组可单独刷新，刷新后清空该组已选", async () => {
    stubFetch({ view: viewOf([seedGroup({ feed: { state: "refresh_required" } })]), refresh: { status: 200 } });
    await renderAndEnter();
    fireEvent.click(await findSeed(SEED_A));
    expect(screen.getByText(t("oneshot.selectedCount", { n: 1 }))).toBeInTheDocument();

    fireEvent.click(button(t("oneshot.refreshSeedsAction")));
    await waitFor(() => {
      expect(screen.getByText(t("oneshot.selectedCount", { n: 0 }))).toBeInTheDocument();
    });
    expect(calls.some((call) => call.url.endsWith("/entry-1/rss"))).toBe(true);
    expect(reads().at(-1)!.body).toMatchObject({
      one_shot: true,
      feed: [{ snapshot_id: "snap-2", snapshot_version: 9, content_hash: "hash-2" }],
    });
  });
});

describe("OneShotDialog 第二步：配置、渲染与提交", () => {
  it("左侧是从全局配置载入的任务配置，右侧按服务端渲染给出目标路径、过滤原因与库内重合", async () => {
    stubFetch({});
    await renderAndEnter();
    await advanceToSettings([SEED_A]);

    expect(screen.getByRole("textbox", { name: t("field.title") })).toHaveValue("测试番剧");
    expect(screen.getByText("bgm-42")).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: t("oneshot.offset") })).toHaveValue(0);

    expect(await screen.findByText(TARGET_A)).toBeInTheDocument();
    expect(screen.getByText(reasonLabel("file_must_contain_not_matched")!)).toBeInTheDocument();
    expect(screen.getByText([t("ui.fileUnits.videos", { n: 1 }), t("ui.fileUnits.others", { n: 1 })].join(" · "))).toBeInTheDocument();
    expect(screen.getByText(t("oneshot.libraryAbsent"))).toBeInTheDocument();
  });

  it("外挂字幕是视频行上的格式徽章：按文件顺序，同名未保留的带删除线与原因，摘要按视频单元计数", async () => {
    const video = `[组A] ${SEED_A}.mkv`;
    const item = seedItem("seed-a", SEED_A);
    item.files = [
      item.files[0]!,
      { file_ref: "sub-sc", ordinal: 2, source_path: `[组A] ${SEED_A}.sc.ass`, filename: "sc.ass", size_bytes: 2048, media_kind: "subtitle", language: "zh-Hans", included: true, rendered_target: `/media/测试番剧/${SEED_A}.zh-Hans.ass`, bound_video_source_path: video, safety: "valid" },
      { file_ref: "sub-dup", ordinal: 3, source_path: `[组A] ${SEED_A}.chs.ass`, filename: "chs.ass", size_bytes: 2048, media_kind: "subtitle", language: "zh-Hans", included: false, reason_code: "target_duplicate", bound_video_source_path: video, safety: "valid" },
      { file_ref: "sub-bare", ordinal: 4, source_path: `[组A] ${SEED_A}.srt`, filename: "bare.srt", size_bytes: 1024, media_kind: "subtitle", included: true, rendered_target: `/media/测试番剧/${SEED_A}.srt`, bound_video_source_path: video, safety: "valid" },
      item.files[1]!,
    ];
    stubFetch({ view: viewOf([seedGroup({ items: [item] })]) });
    await renderAndEnter();
    await advanceToSettings([SEED_A]);

    const videoRow = (await screen.findByText(TARGET_A)).closest("li") as HTMLElement;
    expect(screen.queryByText(`[组A] ${SEED_A}.sc.ass`)).toBeNull();
    const badges = Array.from(videoRow.querySelectorAll<HTMLElement>("[data-subtitle-badge]"));
    expect(badges.map((badge) => badge.dataset.subtitleBadge)).toEqual(["placed", "duplicate", "placed"]);
    expect(badges.map((badge) => badge.textContent?.slice(0, 3))).toEqual(["ASS", "ASS", "SRT"]);
    expect(badges[1]!.querySelector("s")).toHaveTextContent("ASS");
    expect(badges[1]).toHaveTextContent(t("ui.subtitles.duplicate"));
    expect(screen.getByText([
      t("ui.fileUnits.videos", { n: 1 }),
      t("ui.fileUnits.subtitlesPartial", { placed: 2, total: 3 }),
      t("ui.fileUnits.others", { n: 1 }),
    ].join(" · "))).toBeInTheDocument();
    // An unbound file still gets its own row.
    expect(screen.getByText("说明.txt").closest("li")).not.toBe(videoRow);

    const toggle = within(videoRow).getByRole("button", { name: t("ui.subtitles.expand", { name: video, n: 3 }) });
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(within(videoRow).getByText(`→ /media/测试番剧/${SEED_A}.zh-Hans.ass`)).toBeInTheDocument();
    const duplicate = within(videoRow).getByText(`[组A] ${SEED_A}.chs.ass`).closest("li") as HTMLElement;
    expect(duplicate).toHaveTextContent(t("ui.subtitles.duplicate"));
    expect(duplicate).not.toHaveTextContent("→");
    const bare = within(videoRow).getByText(`[组A] ${SEED_A}.srt`).closest("li") as HTMLElement;
    expect(bare).toHaveTextContent(t("ui.subtitles.noLanguage"));
  });

  it("改配置只重读读模型；提交编辑后的冻结草稿，提交后不能回到第一步，通知带执行记录入口", async () => {
    const onOpenRecords = vi.fn();
    stubFetch({});
    await renderAndEnter(providerEntry(), onOpenRecords);
    await advanceToSettings([SEED_A]);
    await screen.findByText(TARGET_A);
    const discoversBefore = calls.filter((call) => call.url.includes("groups:discover")).length;
    const readsBefore = reads().length;

    fireEvent.change(screen.getByRole("textbox", { name: t("field.title") }), { target: { value: "改过的名字" } });
    await waitFor(() => expect(reads().length).toBeGreaterThan(readsBefore));
    expect(reads().at(-1)!.body).toMatchObject({ input_source: "current_input", one_shot: true, draft: { title: "改过的名字" } });
    expect(calls.filter((call) => call.url.includes("groups:discover")).length).toBe(discoversBefore);
    expect(calls.some((call) => call.url.includes("/expand"))).toBe(false);

    expect(button(t("oneshot.back"))).toBeInTheDocument();
    fireEvent.click(button(t("oneshot.create", { n: 1 })));
    await waitFor(() => expect(screen.getByText(t("oneshot.resultOk"))).toBeInTheDocument());

    const body = downloads()[0]!.body as { draft: { title: string; business_digest: string }; input_source: string };
    expect(body.input_source).toBe("current_input");
    expect(body.draft.title).toBe("改过的名字");
    expect(body.draft.business_digest).toMatch(/^sha256:[a-f0-9]{64}$/);

    // After submission there is no return to the catalog.
    expect(screen.queryByRole("button", { name: t("oneshot.back") })).not.toBeInTheDocument();
    expect(toastMock.success).toHaveBeenCalledWith(
      t("oneshot.createdAll", { n: 1 }),
      expect.objectContaining({ action: expect.objectContaining({ label: t("oneshot.viewRecords") }) }),
    );
    const [, options] = toastMock.success.mock.calls[0]! as [unknown, { action: { onClick: () => void } }];
    options.action.onClick();
    expect(onOpenRecords).toHaveBeenCalledTimes(1);
  });

  it("「恢复默认」只在本地复位命名与偏移，之后只有一次读模型重读", async () => {
    stubFetch({});
    await renderAndEnter();
    await advanceToSettings([SEED_A]);
    await screen.findByText(TARGET_A);
    const baselineName = businessPreset.snapshot.sections.naming_rules;
    // Naming sits in the default-folded advanced section; the restore-defaults note is in the "?" beside the button.
    expect(screen.queryByRole("textbox", { name: t("naming.directory") })).not.toBeInTheDocument();
    expect(button(t("oneshot.restoreDefaultsHelpLabel"))).toBeInTheDocument();
    openAdvanced();
    await screen.findByRole("textbox", { name: t("naming.directory") });

    fireEvent.change(screen.getByRole("textbox", { name: t("naming.directory") }), { target: { value: "改过的目录" } });
    fireEvent.change(screen.getByRole("spinbutton", { name: t("oneshot.offset") }), { target: { value: "3" } });
    expect(screen.getByRole("textbox", { name: t("naming.directory") })).toHaveValue("改过的目录");

    const before = calls.length;
    fireEvent.click(button(t("oneshot.restoreDefaults")));
    expect(screen.getByRole("textbox", { name: t("naming.directory") })).toHaveValue(baselineName.tv.directory_template);
    expect(screen.getByRole("spinbutton", { name: t("oneshot.offset") })).toHaveValue(0);
    await waitFor(() => expect(calls.length).toBeGreaterThan(before));
    expect(calls.slice(before).every((call) => call.url.endsWith("/anime/rss-list-view"))).toBe(true);
  });

  it("每个种子各建一个一次性下载，各自的幂等键不同；请求只带快照身份与草稿", async () => {
    stubFetch({ view: twoSeedView() });
    await renderAndEnter();
    await advanceToSettings([SEED_A, SEED_B]);
    fireEvent.click(button(t("oneshot.create", { n: 2 })));

    await waitFor(() => expect(screen.getAllByText(t("oneshot.resultOk"))).toHaveLength(2));
    expect(downloads()).toHaveLength(2);
    expect(new Set(downloads().map((call) => call.idempotencyKey)).size).toBe(2);
    expect(downloads()[0]!.url).toBe("/api/v1/provider-feed-snapshots/snap-1/seeds/seed-a/download");
    expect(downloads()[0]!.body).toMatchObject({
      provider: "anibt",
      provider_entry_ref: "entry-1",
      provider_group_ref: "g-1",
      snapshot_version: 3,
      content_hash: "hash-1",
      input_source: "current_input",
    });
    expect(downloads()[0]!.body).not.toHaveProperty("episode_offset");
    expect(button(t("oneshot.done"))).toBeInTheDocument();
  });

  it("网络失败后重试：只重发失败的种子，沿用同一个幂等键", async () => {
    let failSecond = true;
    stubFetch({
      view: twoSeedView(),
      download: (stableSeedKey) => (stableSeedKey === "seed-b" && failSecond ? "network" : { status: 200 }),
    });
    await renderAndEnter();
    await advanceToSettings([SEED_A, SEED_B]);
    fireEvent.click(button(t("oneshot.create", { n: 2 })));

    expect(await screen.findByText(t("oneshot.resultFailed"))).toBeInTheDocument();
    expect(screen.getAllByText(t("oneshot.resultOk"))).toHaveLength(1);
    expect(toastMock.warning).toHaveBeenCalled();

    failSecond = false;
    fireEvent.click(button(t("oneshot.retryFailed", { n: 1 })));
    await waitFor(() => expect(screen.getAllByText(t("oneshot.resultOk"))).toHaveLength(2));
    const sent = downloads();
    const seedB = sent.filter((call) => call.url.includes("/seeds/seed-b/download"));
    expect(seedB.length).toBeGreaterThanOrEqual(2);
    expect(sent.at(-1)!.url).toContain("/seeds/seed-b/download");
    expect(new Set(seedB.map((call) => call.idempotencyKey)).size).toBe(1);
  });

  it("偏移参与预览：改偏移后按新偏移重读，下载请求带偏移", async () => {
    stubFetch({});
    await renderAndEnter();
    await advanceToSettings([SEED_A]);
    const readsBefore = reads().length;
    fireEvent.change(screen.getByRole("spinbutton", { name: t("oneshot.offset") }), { target: { value: "2" } });
    await waitFor(() => {
      expect(reads().length).toBeGreaterThan(readsBefore);
      expect(reads().at(-1)!.body).toMatchObject({ episode_offset: 2, one_shot: true });
    });
    fireEvent.click(button(t("oneshot.create", { n: 1 })));
    await waitFor(() => expect(screen.getByText(t("oneshot.resultOk"))).toBeInTheDocument());
    expect(downloads()[0]!.body).toMatchObject({ episode_offset: 2 });
  });

  it("偏移非法：给出说明且不能提交", async () => {
    stubFetch({});
    await renderAndEnter();
    await advanceToSettings([SEED_A]);
    fireEvent.change(screen.getByRole("spinbutton", { name: t("oneshot.offset") }), { target: { value: "2000" } });
    expect(screen.getByText(t("rss.offsetInvalid"))).toBeInTheDocument();
    expect(button(t("oneshot.create", { n: 1 }))).toBeDisabled();
  });
  it("预览优先：常显标题、Bangumi/TMDB 与偏移，解析器、文件筛选、命名收在默认折叠的「高级」里", async () => {
    stubFetch({});
    await renderAndEnter();
    await advanceToSettings([SEED_A]);
    await screen.findByText(TARGET_A);

    expect(screen.getByRole("textbox", { name: t("field.title") })).toBeInTheDocument();
    expect(button(t("forms.matchProvider", { provider: t("forms.providerTmdb") }))).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: t("oneshot.offset") })).toBeInTheDocument();
    const advanced = screen.getByRole("button", { name: new RegExp(`^${t("oneshot.advanced")}`) });
    expect(advanced).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("textbox", { name: t("settings.rules.fileMust") })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: t("naming.directory") })).not.toBeInTheDocument();
    expect(screen.queryByText(t("oneshot.advancedChanged"))).not.toBeInTheDocument();

    openAdvanced();
    expect(advanced).toHaveAttribute("aria-expanded", "true");
    expect(await screen.findByRole("textbox", { name: t("naming.directory") })).toBeInTheDocument();
    expect(screen.getByText(t("settings.rules.fileMust"))).toBeInTheDocument();
  });

  it("空值：摘要写出目标与缺的值，预览里的空值是警告色，点它跳到对应的输入", async () => {
    stubFetch({
      view: viewOf([
        seedGroup({
          items: [
            seedItem("seed-a", SEED_A, {
              files: [
                {
                  file_ref: "seed-a-video",
                  ordinal: 0,
                  source_path: `[组A] ${SEED_A}.mkv`,
                  filename: `${SEED_A}.mkv`,
                  included: true,
                  rendered_target: "/library/tv/测试番剧/测试番剧 E01.mkv",
                  empty_facts: ["anime.year", "ep.display"],
                  safety: "valid",
                },
              ],
            }),
          ],
        }),
      ]),
    });
    await renderAndEnter();
    await advanceToSettings([SEED_A]);
    await screen.findByText("/library/tv/测试番剧/测试番剧 E01.mkv");

    // The phone summary (hidden on desktop): seed count → the target shorthand, with each missing value as its own button.
    const year = namingFactLabel("anime.year");
    const episode = namingFactLabel("ep.display");
    expect(button(t("oneshot.summary", { n: 1, target: "…/测试番剧 E01.mkv" }))).toBeInTheDocument();
    const summaryMissing = button(t("oneshot.missingFacts", { facts: `${year}、${episode}` }));
    expect(summaryMissing).toHaveClass("tw:text-warning");

    // Every empty in the preview is tappable: the year jumps to the year input.
    const yearLink = button(year);
    expect(yearLink.closest("p")).toHaveClass("tw:text-warning");
    fireEvent.click(yearLink);
    const yearInput = within(screen.getByRole("region", { name: t("forms.year") })).getByRole("textbox");
    await waitFor(() => expect(yearInput).toHaveFocus());

    // A value the template references but the form lacks jumps to naming: expand the advanced section first.
    fireEvent.click(button(episode));
    expect(screen.getByRole("button", { name: new RegExp(`^${t("oneshot.advanced")}`) })).toHaveAttribute("aria-expanded", "true");
    await waitFor(() => expect(screen.getByRole("textbox", { name: t("naming.directory") })).toHaveFocus());

    // The summary's missing-value button goes to the directly editable field (the year).
    fireEvent.click(summaryMissing);
    await waitFor(() => expect(yearInput).toHaveFocus());
  });
});

describe("OneShotDialog 解析器与元数据", () => {
  const twoParsers = [
    { id: "regex", display_name: "Regex", capabilities: ["release_title"] },
    { id: "reltitle", display_name: "reltitle", capabilities: ["release_title"] },
  ];
  /** An expanded seed carries episodes per the parser: regex reads 1, reltitle reads 7. */
  const episodeByParser = (body: Parameters<NonNullable<Scenario["viewFor"]>>[0]) =>
    viewOf([
      seedGroup({
        items: [
          seedItem("seed-a", SEED_A, {
            evaluation: { state: "evaluated", verdict: "accepted_pending_download", parsed_facts: { episode: body.draft?.business?.parser?.kind === "reltitle" ? 7 : 1, special: false } },
          }),
        ],
      }),
    ]);

  it("第一步只浏览：种子行不显示解析结果，也没有解析器选择", async () => {
    stubFetch({ parsers: twoParsers, viewFor: episodeByParser });
    await renderAndEnter();
    await findSeed(SEED_A);
    expect(screen.queryByText(t("rss.episode", { n: 1 }), { exact: false })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: t("policy.parser.kindLabel") })).not.toBeInTheDocument();
  });

  it("第二步切换解析器：按它重读预览，随这次任务提交，不写回任何设置", async () => {
    stubFetch({ parsers: twoParsers, viewFor: episodeByParser });
    await renderAndEnter();
    await advanceToSettings([SEED_A]);
    expect(await screen.findByText(t("rss.episode", { n: 1 }), { exact: false })).toBeInTheDocument();
    const readsBefore = reads().length;
    // The parser lives only in step two's advanced section.
    expect(screen.queryByRole("combobox", { name: t("policy.parser.kindLabel") })).not.toBeInTheDocument();
    openAdvanced();

    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("policy.parser.kindLabel") }));
    await user.click(await screen.findByRole("option", { name: t("policy.parser.kindReltitle") }));

    await waitFor(() => expect(reads().length).toBeGreaterThan(readsBefore));
    await waitFor(() => expect(reads().at(-1)!.body).toMatchObject({ draft: { business: { parser: { kind: "reltitle" } } } }));
    expect(await screen.findByText(t("rss.episode", { n: 7 }), { exact: false })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: t("policy.parser.kindLabel") })).toHaveTextContent(t("policy.parser.kindReltitle"));
    // Even folded, the advanced section shows that something changed.
    expect(screen.getByText(t("oneshot.advancedChanged"))).toBeInTheDocument();
    fireEvent.click(button(t("oneshot.create", { n: 1 })));
    await waitFor(() => expect(screen.getByText(t("oneshot.resultOk"))).toBeInTheDocument());
    const body = downloads()[0]!.body as { draft: { business: { parser: { kind: string } }; business_digest: string } };
    expect(body.draft.business.parser.kind).toBe("reltitle");
    expect(body.draft.business_digest).toMatch(/^sha256:[a-f0-9]{64}$/);
    // Only reads, expansion and submission: no request writes parser policy or global settings.
    expect(calls.some((call) => /parser-polic|\/config\/global|business-preset:/.test(call.url) && !call.url.endsWith("/config/business-preset"))).toBe(false);
  });

  it("「恢复默认」把解析器也复位为任务开始时的", async () => {
    stubFetch({ parsers: twoParsers, viewFor: episodeByParser });
    await renderAndEnter();
    await advanceToSettings([SEED_A]);
    openAdvanced();
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("policy.parser.kindLabel") }));
    await user.click(await screen.findByRole("option", { name: t("policy.parser.kindReltitle") }));
    await waitFor(() => expect(reads().at(-1)!.body).toMatchObject({ draft: { business: { parser: { kind: "reltitle" } } } }));

    fireEvent.click(button(t("oneshot.restoreDefaults")));
    await waitFor(() => expect(reads().at(-1)!.body).toMatchObject({ draft: { business: { parser: { kind: "regex", provenance: "global", source_owner: "global" } } } }));
  });

  it("只有一个解析器时不显示选择", async () => {
    stubFetch({});
    await renderAndEnter();
    await findSeed(SEED_A);
    expect(screen.queryByRole("combobox", { name: t("policy.parser.kindLabel") })).not.toBeInTheDocument();
  });

  it("匹配 TMDB 把关联、季度、总集数与年份填进这次任务的草稿（只填空的），提交时带上", async () => {
    stubFetch({
      metadataCandidates: [
        {
          provider: "tmdb",
          provider_work_id: "777",
          title: "测试番剧",
          media_kind: "tv",
          details_status: "available",
          seasons: [{ season_number: 1, name: "第一季", episode_count: 12, air_date: "2024-04-01", is_special: false }],
          suggested_season: 1,
          year: 2024,
          score: 1,
        },
      ],
    });
    await renderAndEnter();
    await advanceToSettings([SEED_A]);

    fireEvent.click(button(t("forms.matchProvider", { provider: t("forms.providerTmdb") })));
    const match = await screen.findByRole("dialog", { name: t("forms.matchTitle", { provider: "TMDB" }) });
    fireEvent.click(within(match).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(match).findByRole("option", { name: /测试番剧/ }));
    fireEvent.click(within(match).getByRole("button", { name: t("forms.confirmMatch") }));

    await waitFor(() =>
      expect(reads().at(-1)!.body).toMatchObject({
        draft: { metadata: { tmdb: { work_id: "777", media_kind: "tv", season: 1 }, total_episodes: 12, year: 2024, bgm: { work_id: "bgm-42" } } },
      }),
    );
    expect(within(screen.getByRole("region", { name: t("forms.year") })).getByRole("textbox")).toHaveValue("2024");
    expect(screen.getByRole("region", { name: t("forms.totalEpisodes") })).toHaveTextContent(t("forms.episodesCount", { n: 12 }));

    fireEvent.click(button(t("oneshot.create", { n: 1 })));
    await waitFor(() => expect(screen.getByText(t("oneshot.resultOk"))).toBeInTheDocument());
    expect(downloads()[0]!.body).toMatchObject({ draft: { metadata: { tmdb: { work_id: "777" }, year: 2024, total_episodes: 12 } } });
  });
});

describe("OneShotDialog 自定义预设", () => {
  const customView = () =>
    viewOf([
      seedGroup({
        scope: { provider: "custom", provider_entry_ref: "custom-source", provider_group_ref: "custom-group" },
        feed: { state: "fresh", snapshot_id: "custom-snap", snapshot_version: 2, content_hash: "custom-hash" },
        items: [seedItem("seed-a", SEED_A)],
      }),
    ]);
  const customCandidate = {
    id: "cand-1",
    origin: "custom",
    origin_entry_ref: null,
    title: "自定义番剧",
    title_override: null,
    instance_label: "自定义番剧",
    draft: {
      ...createEmptyDraft(),
      origin: "custom",
      title: "自定义番剧",
      rss_sources: [{ ...createEmptySourceDraft(), locator: "https://example.invalid/feed", group_name: "组A", episode_offset: 4 }],
    },
    version: 7,
    current_revision: 3,
    activated_managed_anime_id: null,
    created_at: "2026-10-17T00:00:00Z",
    updated_at: "2026-10-17T00:00:00Z",
    state: "working",
  };

  it("没有种子快照：给出显式获取入口；获取后按一次性浏览读这次任务的草稿", async () => {
    stubFetch({ view: customView(), candidate: customCandidate });
    renderDialog(customEntry);

    expect(await screen.findByText(t("oneshot.loadSeedsHint"))).toBeInTheDocument();
    expect(reads()).toHaveLength(0);
    fireEvent.click(button(t("oneshot.loadSeeds")));
    expect(await findSeed(SEED_A)).toBeInTheDocument();
    // A custom source has a single seed list: no subgroup layer.
    expect(screen.queryByRole("button", { name: t("oneshot.backToGroups") })).not.toBeInTheDocument();
    expect(screen.queryByText(t("oneshot.groupsCount", { n: 1 }))).not.toBeInTheDocument();
    expect(calls.find((call) => call.url.endsWith("feed-snapshots:fetch"))!.body).toEqual({
      candidate_id: "cand-1",
      revision: 3,
      expected_version: 7,
    });
    expect(reads().at(-1)!.body).toMatchObject({
      input_source: "current_input",
      one_shot: true,
      episode_offset: 4,
      draft: { title: "自定义番剧" },
      feed: [{ provider: "custom", snapshot_id: "custom-snap", snapshot_version: 2, content_hash: "custom-hash" }],
    });
  });

  it("第二步可编辑预设载入的配置；提交按当前输入，不写回预设", async () => {
    stubFetch({ view: customView(), candidate: customCandidate });
    renderDialog(customEntry);
    await screen.findByText(t("oneshot.loadSeedsHint"));
    fireEvent.click(button(t("oneshot.loadSeeds")));
    await advanceToSettings([SEED_A]);

    expect(screen.getByText(t("oneshot.customConfigNote"))).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: t("oneshot.offset") })).toHaveValue(4);
    const title = screen.getByRole("textbox", { name: t("field.title") });
    expect(title).not.toHaveAttribute("readonly");
    const readsBefore = reads().length;
    fireEvent.change(title, { target: { value: "这次的名字" } });
    await waitFor(() => expect(reads().length).toBeGreaterThan(readsBefore));
    expect(reads().at(-1)!.body).toMatchObject({ one_shot: true, draft: { title: "这次的名字" } });

    fireEvent.click(button(t("oneshot.create", { n: 1 })));
    await waitFor(() => expect(screen.getByText(t("oneshot.resultOk"))).toBeInTheDocument());
    const body = calls.find((call) => call.url.endsWith("/seeds/seed-a/download"))!.body as Record<string, unknown>;
    expect(body).toMatchObject({
      provider: "custom",
      provider_entry_ref: "custom-source",
      provider_group_ref: "custom-group",
      snapshot_version: 2,
      content_hash: "custom-hash",
      input_source: "current_input",
      episode_offset: 4,
      draft: { title: "这次的名字" },
    });
    expect(body).not.toHaveProperty("candidate_id");
    // The candidate is only ever read: no request writes back to the preset (the stub fails write requests outright).
    expect(calls.filter((call) => call.url.includes("/anime-config-candidates/")).length).toBeGreaterThan(0);
  });
});

describe("OneShotDialog 搜索快照来源", () => {
  // Search fixtures use placeholder names only (never real releases, groups or refs).
  const SEARCH_SCOPE_REF = "search-scope:deadbeefcafe0011";
  const SEARCH_QUERY_REF = "search-query:0011deadbeefcafe";
  const SEARCH_CONTENT_HASH = "search-hash-1";
  const SEARCH_SNAPSHOT_ID = "search-snap-1";

  /** The read model's projection of one search snapshot: a single group whose server-side display name is the query_ref placeholder. */
  const searchGroupView = (items: RSSListItem[] = [
    seedItem("seed-a", SEED_A, { snapshot_id: SEARCH_SNAPSHOT_ID }),
    seedItem("seed-b", SEED_B, { snapshot_id: SEARCH_SNAPSHOT_ID }),
  ]) =>
    viewOf([
      seedGroup({
        scope: { provider: "anibt", provider_entry_ref: SEARCH_SCOPE_REF, provider_group_ref: SEARCH_QUERY_REF },
        display_name: SEARCH_QUERY_REF,
        feed: { state: "fresh", snapshot_id: SEARCH_SNAPSHOT_ID, snapshot_version: 1, content_hash: SEARCH_CONTENT_HASH },
        items,
      }),
    ]);

  const searchBrowse: SearchSeedBrowse = {
    provider: "anibt",
    query: "搜索关键词甲",
    snapshot: {
      provider: "anibt",
      scope_ref: SEARCH_SCOPE_REF,
      query_ref: SEARCH_QUERY_REF,
      query: "搜索关键词甲",
      snapshot_id: SEARCH_SNAPSHOT_ID,
      snapshot_version: 1,
      content_hash: SEARCH_CONTENT_HASH,
      freshness: "fresh",
      total_seeds: 2,
      window: 20,
      retention_seconds: 86400,
      seeds: [],
    },
    preselectSeedKey: "seed-a",
    titleHint: "示例作品",
  };

  function renderSearchDialog(search: SearchSeedBrowse = searchBrowse, onOpenRecords?: () => void) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <QueryClientProvider client={client}>
        <OneShotDialog open search={search} onClose={() => {}} onOpenRecords={onOpenRecords} />
      </QueryClientProvider>,
    );
  }

  it("打开即读：单一列表没有字幕组层，读请求携带搜索身份与 origin=oneshot 草稿，点击的种子已预选", async () => {
    stubFetch({ view: searchGroupView() });
    renderSearchDialog();

    expect(await findSeed(SEED_A)).toBeChecked();
    expect(await findSeed(SEED_B)).not.toBeChecked();
    // A search source has a single seed list: no subgroup layer.
    expect(screen.queryByRole("button", { name: t("oneshot.backToGroups") })).not.toBeInTheDocument();
    // The header renders the keyword the user searched plus the site, never the placeholder query_ref.
    expect(screen.getAllByText("搜索关键词甲").length).toBeGreaterThan(0);
    expect(screen.getByText(/AniBT/)).toBeInTheDocument();
    expect(screen.queryByText(new RegExp(SEARCH_QUERY_REF))).not.toBeInTheDocument();

    expect(reads()).toHaveLength(1);
    expect(reads()[0]!.body).toMatchObject({
      input_source: "current_input",
      one_shot: true,
      feed: [
        {
          provider: "anibt",
          provider_entry_ref: SEARCH_SCOPE_REF,
          provider_group_ref: SEARCH_QUERY_REF,
          snapshot_id: SEARCH_SNAPSHOT_ID,
          snapshot_version: 1,
          content_hash: SEARCH_CONTENT_HASH,
        },
      ],
      draft: { origin: "oneshot", title: "示例作品", instance_label: "示例作品", rss_sources: [] },
    });
  });

  it("点开的种子自动展开：打开即取它的种子文件并显示文件列表", async () => {
    let expanded = false;
    stubFetch({
      get view() {
        return searchGroupView([
          expanded
            ? seedItem("seed-a", SEED_A, { snapshot_id: SEARCH_SNAPSHOT_ID })
            : { ...pendingItem("seed-a", SEED_A), snapshot_id: SEARCH_SNAPSHOT_ID },
          seedItem("seed-b", SEED_B, { snapshot_id: SEARCH_SNAPSHOT_ID }),
        ]);
      },
      expand: () => {
        expanded = true;
        return { state: "available" };
      },
    });
    renderSearchDialog();

    // No manual row click: the click that opened the dialog is the expansion request, and only
    // the clicked seed is fetched.
    expect(await screen.findByText(`[组A] ${SEED_A}.mkv`, { exact: false })).toBeInTheDocument();
    const expansions = calls.filter((call) => call.url.includes("/expand"));
    expect(expansions).toHaveLength(1);
    expect(expansions[0]!.url).toContain(`/provider-feed-snapshots/${SEARCH_SNAPSHOT_ID}/seeds/seed-a/expand`);
    expect(reads().at(-1)!.body).toMatchObject({ one_shot: true });
  });

  it("第二步：标题编辑同步 instance_label，提交按快照身份与编辑后的草稿", async () => {
    stubFetch({ view: searchGroupView() });
    renderSearchDialog();
    await findSeed(SEED_A);
    // The clicked seed was preselected: straight to step two.
    fireEvent.click(button(t("oneshot.nextWithCount", { n: 1 })));
    await screen.findByRole("heading", { name: t("oneshot.configTitle") });

    const title = screen.getByRole("textbox", { name: t("field.title") });
    expect(title).toHaveValue("示例作品");
    const readsBefore = reads().length;
    fireEvent.change(title, { target: { value: "改名作品" } });
    await waitFor(() => expect(reads().length).toBeGreaterThan(readsBefore));
    expect(reads().at(-1)!.body).toMatchObject({ one_shot: true, draft: { title: "改名作品", instance_label: "改名作品" } });

    fireEvent.click(button(t("oneshot.create", { n: 1 })));
    await waitFor(() => expect(screen.getByText(t("oneshot.resultOk"))).toBeInTheDocument());
    const call = calls.find((entry) => entry.url.includes("/seeds/seed-a/download"))!;
    expect(call.url).toContain(`/provider-feed-snapshots/${SEARCH_SNAPSHOT_ID}/seeds/seed-a/download`);
    expect(call.body).toMatchObject({
      provider: "anibt",
      provider_entry_ref: SEARCH_SCOPE_REF,
      provider_group_ref: SEARCH_QUERY_REF,
      snapshot_version: 1,
      content_hash: SEARCH_CONTENT_HASH,
      input_source: "current_input",
      draft: { origin: "oneshot", title: "改名作品", instance_label: "改名作品", rss_sources: [] },
    });
  });

  it("读取返回 409：提示重新搜索，不提供重试，也不静默重发", async () => {
    stubFetch({ viewStatus: 409, viewErrorBody: { code: "FEED_SNAPSHOT_REFRESH_REQUIRED", message: "expired" } });
    renderSearchDialog();

    expect(await screen.findByText(t("oneshot.searchStale"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("act.retry") })).not.toBeInTheDocument();
    expect(reads()).toHaveLength(1);
  });

  it("提交时快照已过期：行内提示重新搜索，不静默重发", async () => {
    stubFetch({
      view: searchGroupView(),
      download: () => ({ status: 409, body: { code: "FEED_SNAPSHOT_REFRESH_REQUIRED", message: "expired" } }),
    });
    renderSearchDialog();
    await findSeed(SEED_A);
    fireEvent.click(button(t("oneshot.nextWithCount", { n: 1 })));
    await screen.findByRole("heading", { name: t("oneshot.configTitle") });

    fireEvent.click(button(t("oneshot.create", { n: 1 })));
    await waitFor(() => expect(downloads()).toHaveLength(1));
    expect(await screen.findByText(t("oneshot.resultFailed"))).toBeInTheDocument();
    expect(screen.getByText(t("oneshot.searchStale"))).toBeInTheDocument();
    expect(downloads()).toHaveLength(1);
  });
});
