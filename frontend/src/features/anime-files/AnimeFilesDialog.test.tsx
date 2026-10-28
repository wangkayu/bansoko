import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AnimeFile, AnimeFilesView } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { reasonLabel } from "../../lib/i18n/reasons";
import { AnimeFilesDialog } from "./AnimeFilesDialog";

const toastMock = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() }));
vi.mock("../../ui/toast", () => ({ toast: toastMock }));

const notApplicable = (reason: string) => ({ available: false, reason_code: reason });
const available = { available: true };

function fileRow(overrides: Partial<AnimeFile>): AnimeFile {
  return {
    placement_id: "p-1",
    release_id: "r-1",
    media_kind: "tv",
    episodes: ["1"],
    path: "/library/Show/EP 01.mkv",
    relative_path: "Show/EP 01.mkv",
    size_bytes: 1024,
    status: "aligned",
    actions: { align: notApplicable("nothing_to_align"), rerun: notApplicable("file_not_missing"), release: notApplicable("file_not_missing"), recover: notApplicable("nothing_to_recover") },
    companions: [],
    ...overrides,
  };
}

function filesView(files: AnimeFile[], overrides: Partial<AnimeFilesView> = {}): AnimeFilesView {
  return {
    managed_anime_id: "anime-1",
    root: "/library",
    scan: { state: "idle", generation: 3, scanned_at: "2026-10-21T01:00:00Z", rescan_required: false },
    aligning: false,
    scan_action: available,
    align_all_action: files.some((f) => f.actions.align.available) ? available : notApplicable("nothing_to_align"),
    files,
    ...overrides,
  };
}

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

function renderFiles(props: { readOnly?: boolean; scanOnOpen?: boolean; onChanged?: () => void; onClose?: () => void } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AnimeFilesDialog
        animeID="anime-1"
        title="测试番"
        readOnly={props.readOnly ?? false}
        scanOnOpen={props.scanOnOpen}
        onChanged={props.onChanged ?? (() => {})}
        onClose={props.onClose ?? (() => {})}
      />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("AnimeFilesDialog", () => {
  it("opens as its own dialog titled with the anime", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(filesView([fileRow({})]))));
    const onClose = vi.fn();
    renderFiles({ onClose });
    const dialog = await screen.findByRole("dialog", { name: t("tabs.files.dialogTitle", { title: "测试番" }) });
    expect(await within(dialog).findByRole("list", { name: t("tabs.files.title") })).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: t("ui.close") }));
    expect(onClose).toHaveBeenCalled();
  });

  it("submits one scan on open when asked and refreshes once the scan ends", async () => {
    const posts: unknown[] = [];
    let reads = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "POST") {
        posts.push(init.body ? JSON.parse(String(init.body)) : undefined);
        return jsonResponse({ managed_anime_id: "anime-1", state: "running", generation: 1, input_fingerprint: "f", progress: {}, capacity_stats: {}, version: 1 }, 202);
      }
      if (url.includes("/anime/anime-1/files")) {
        reads += 1;
        return jsonResponse(filesView([], { scan: { state: reads > 1 ? "idle" : "running", generation: 1, scanned_at: "2026-10-21T01:00:00Z", rescan_required: false } }));
      }
      return jsonResponse({});
    }));
    const onChanged = vi.fn();
    renderFiles({ scanOnOpen: true, onChanged });
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toMatchObject({ managed_anime_id: "anime-1", trigger: "manual" });
    await waitFor(() => expect(screen.getByText(t("tabs.files.empty"))).toBeInTheDocument(), { timeout: 4_000 });
    await waitFor(() => expect(onChanged).toHaveBeenCalled(), { timeout: 4_000 });
  });

  it("rescans once an alignment finishes even though it was never seen in flight", async () => {
    // The alignment started and settled between two polls: every read already
    // reports aligning false. Only the server's rescan_required fact says the
    // rows still describe pre-move paths.
    const scans: unknown[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "POST" && url.includes("/reconciliation/scans")) {
        scans.push(JSON.parse(String(init.body)));
        return jsonResponse({ managed_anime_id: "anime-1", state: "completed", generation: 4, input_fingerprint: "f", progress: {}, capacity_stats: {}, version: 1 }, 202);
      }
      if (url.includes("/anime/anime-1/files")) {
        // Generation 3 needs the rescan until the new scan lands; generation 4
        // is the scan submitted below, so it must not ask for another one.
        const generation = scans.length === 0 ? 3 : 4;
        return jsonResponse(filesView([fileRow({})], { scan: { state: scans.length === 0 ? "idle" : "running", generation, scanned_at: "2026-10-21T01:00:00Z", rescan_required: scans.length === 0 } }));
      }
      return jsonResponse({});
    }));
    renderFiles();
    await waitFor(() => expect(scans).toHaveLength(1), { timeout: 4_000 });
    expect(scans[0]).toMatchObject({ managed_anime_id: "anime-1", trigger: "manual" });
    // Waiting out a poll cycle proves the flag is consumed per generation: the
    // post-submit reads all carry generation 4 and no second scan is posted.
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(scans).toHaveLength(1);
  });

  it("groups files by subtitle group with alignment as the focus", async () => {
    const misaligned = fileRow({
      placement_id: "p-2",
      group_name: "GroupA",
      relative_path: "Show/EP 02.mkv",
      status: "misaligned",
      target_relative_path: "Show/Season 01/Show - E02.mkv",
      actions: { align: available, rerun: notApplicable("file_not_missing"), release: notApplicable("file_not_missing"), recover: notApplicable("nothing_to_recover") },
      companions: [{ id: "c-1", media_kind: "subtitle", path: "/library/Show/EP 02.ass", relative_path: "Show/EP 02.ass", size_bytes: 10, status: "misaligned", target_relative_path: "Show/Season 01/Show - E02.ass" }],
    });
    const missing = fileRow({ placement_id: "p-3", group_name: "GroupA", relative_path: "Show/EP 03.mkv", status: "missing" });
    const aligned = fileRow({ group_name: "GroupA" });
    const ungrouped = fileRow({ placement_id: "p-4", relative_path: "Other/EP 09.mkv" });
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(filesView([aligned, ungrouped, misaligned, missing]))));
    renderFiles();

    const list = await screen.findByRole("list", { name: t("tabs.files.title") });
    const groupA = within(list).getByRole("list", { name: "GroupA" });
    const other = within(list).getByRole("list", { name: t("tabs.files.otherGroup") });
    expect(within(other).getByText("EP 09.mkv")).toBeInTheDocument();
    // Group header: file count, awaiting-alignment count, problem count; "other" sorts last.
    expect(
      within(list).getByText([t("tabs.files.groupFiles", { n: 3 }), t("tabs.files.groupPending", { n: 1 }), t("tabs.files.groupProblems", { n: 1 })].join(" · ")),
    ).toBeInTheDocument();
    const groupNames = within(list).getAllByRole("list").map((el) => el.getAttribute("aria-label")).filter(Boolean);
    expect(groupNames).toEqual(["GroupA", t("tabs.files.otherGroup")]);
    // Within a group: problems first, awaiting alignment next, aligned last.
    const rows = Array.from(groupA.querySelectorAll(":scope > li")).map((el) => el.getAttribute("data-placement-id"));
    expect(rows).toEqual(["p-3", "p-2", "p-1"]);
    // File name primary, directory secondary; awaiting alignment shows the new name and the directory change.
    expect(within(groupA).getByText("EP 01.mkv")).toBeInTheDocument();
    expect(within(groupA).getByText("→ Show - E02.mkv")).toBeInTheDocument();
    expect(within(groupA).getByText("→ Show - E02.ass")).toBeInTheDocument();
    expect(within(groupA).getAllByText("Show/Season 01")).toHaveLength(2);
    // Season and episode are not shown.
    expect(within(list).queryByText(/E02$/)).toBeNull();
    expect(screen.getByText(t("tabs.files.root", { path: "/library" }))).toBeInTheDocument();
    // An aligned row has no actions; an awaiting-alignment row has only Align.
    expect(within(list).getAllByRole("button", { name: t("tabs.files.align") })).toHaveLength(1);
    expect(within(list).queryByRole("button", { name: t("tabs.files.rerun") })).toBeNull();
    expect(screen.getByRole("button", { name: t("tabs.files.alignAll") })).toBeEnabled();

    // The filter only changes the display.
    fireEvent.click(screen.getByRole("radio", { name: new RegExp(t("tabs.files.filter.problem")) }));
    expect(within(screen.getByRole("list", { name: t("tabs.files.title") })).queryByText("EP 01.mkv")).toBeNull();
    expect(screen.getByText("EP 03.mkv")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: new RegExp(t("tabs.files.filter.pending")) }));
    expect(screen.queryByText("EP 03.mkv")).toBeNull();
    expect(screen.getByText("→ Show - E02.mkv")).toBeInTheDocument();
  });

  it("shows 未扫描 before any scan", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(filesView([fileRow({ status: "unscanned" })], { scan: { state: "idle", generation: 0, rescan_required: false } }))));
    renderFiles();
    expect(await screen.findAllByText(t("tabs.files.unscanned"))).not.toHaveLength(0);
  });

  it("aligns one row with the read generation, and reports a refusal through the reason catalog", async () => {
    const posts: { url: string; body: unknown }[] = [];
    let denied = false;
    const misaligned = fileRow({ status: "misaligned", target_relative_path: "Show/Season 01/Show - E01.mkv", actions: { align: available, rerun: notApplicable("file_not_missing"), release: notApplicable("file_not_missing"), recover: notApplicable("nothing_to_recover") } });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/files/align") && init?.method === "POST") {
        posts.push({ url, body: JSON.parse(String(init.body)) });
        if (denied) return jsonResponse({ code: "ANIME_FILES_ALIGN_UNAVAILABLE", message: "x", details: [{ code: "scan_changed", message: "x" }] }, 409);
        return jsonResponse({ generation: 3, admitted: 1 }, 202);
      }
      return jsonResponse(filesView([misaligned]));
    }));
    renderFiles();

    const list = await screen.findByRole("list", { name: t("tabs.files.title") });
    fireEvent.click(within(list).getByRole("button", { name: t("tabs.files.align") }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.body).toEqual({ generation: 3, placement_ids: ["p-1"] });
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith(t("tabs.files.alignAdmitted", { n: 1 })));

    denied = true;
    fireEvent.click(screen.getByRole("button", { name: t("tabs.files.alignAll") }));
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1]!.body).toEqual({ generation: 3 });
    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith(t("tabs.files.alignBlocked", { reason: reasonLabel("scan_changed") ?? "" })));
  });

  it("re-runs a missing file through release retry and shows the denial reason", async () => {
    const missing = fileRow({ status: "missing", actions: { align: notApplicable("nothing_to_align"), rerun: available, release: available, recover: notApplicable("nothing_to_recover") } });
    const posts: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "POST") {
        posts.push(url);
        return jsonResponse({ code: "RELEASE_NOT_RETRYABLE", message: "x", details: [{ code: "source_release_unavailable", message: "x" }] }, 409);
      }
      return jsonResponse(filesView([missing]));
    }));
    renderFiles();

    const list = await screen.findByRole("list", { name: t("tabs.files.title") });
    expect(within(list).getByRole("button", { name: t("policy.fileEffect.releasePlacement") })).toBeEnabled();
    fireEvent.click(within(list).getByRole("button", { name: t("tabs.files.rerun") }));
    await waitFor(() => expect(posts).toEqual([expect.stringContaining("/releases/r-1/retry")]));
    await waitFor(() =>
      expect(toastMock.error).toHaveBeenCalledWith(t("rss.releaseRetry.blocked", { reason: reasonLabel("source_release_unavailable") ?? "" })),
    );
  });

  it("disables an unavailable action with its reason and hides actions when read-only", async () => {
    const missing = fileRow({ status: "changed", actions: { align: notApplicable("nothing_to_align"), rerun: notApplicable("owner_retired"), release: notApplicable("owner_retired"), recover: notApplicable("nothing_to_recover") } });
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(filesView([missing]))));
    const first = renderFiles();
    const rerun = await screen.findByRole("button", { name: t("tabs.files.rerun") });
    expect(rerun).toBeDisabled();
    expect(rerun).toHaveAttribute("title", reasonLabel("owner_retired"));
    first.unmount();

    renderFiles({ readOnly: true });
    await screen.findByRole("list", { name: t("tabs.files.title") });
    expect(screen.queryByRole("button", { name: t("tabs.files.rerun") })).toBeNull();
    expect(screen.queryByRole("button", { name: t("tabs.files.scan") })).toBeNull();
  });

  it("shows the recovered target inline as a library-relative hint", async () => {
    const missing = fileRow({
      status: "missing",
      recovery: { generation: 3, target_path: "/library/Show/Season 01/Show - E01.mkv", target_relative_path: "Show/Season 01/Show - E01.mkv", size_bytes: 4096, placement_version: 7, fingerprint: "meta:size=4096", companions: 0 },
      actions: { align: notApplicable("nothing_to_align"), rerun: available, release: available, recover: available },
    });
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(filesView([missing]))));
    renderFiles();

    const list = await screen.findByRole("list", { name: t("tabs.files.title") });
    // The row says what the scan found and where, using the relative path the
    // server computed: the browser never assembles an absolute path itself.
    expect(
      within(list).getByText(t("policy.fileEffect.recoverFound", { path: "Show/Season 01/Show - E01.mkv" })),
    ).toBeInTheDocument();
    expect(within(list).queryByText(/\/library\//)).toBeNull();
  });

  it("recovers with the full frozen token, closes only on success and rescans", async () => {
    const recoverPosts: { url: string; body: unknown }[] = [];
    const scanPosts: unknown[] = [];
    const missing = fileRow({
      status: "missing",
      recovery: { generation: 3, target_path: "/library/Show/Season 01/Show - E01.mkv", target_relative_path: "Show/Season 01/Show - E01.mkv", size_bytes: 4096, placement_version: 7, fingerprint: "meta:size=4096", companions: 1 },
      actions: { align: notApplicable("nothing_to_align"), rerun: available, release: available, recover: available },
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/files/recover") && init?.method === "POST") {
        recoverPosts.push({ url, body: JSON.parse(String(init.body)) });
        return jsonResponse({ placement_id: "p-1", generation: 3, target_path: "/library/Show/Season 01/Show - E01.mkv", replay: false, companions: 1 }, 200);
      }
      // The real client POSTs /reconciliation/scans (no per-anime path suffix).
      if (url.endsWith("/reconciliation/scans") && init?.method === "POST") {
        scanPosts.push(JSON.parse(String(init.body)));
        return jsonResponse({ state: "running", generation: 4 }, 202);
      }
      return jsonResponse(filesView([missing]));
    }));
    renderFiles();

    const list = await screen.findByRole("list", { name: t("tabs.files.title") });
    fireEvent.click(within(list).getByRole("button", { name: t("policy.fileEffect.recover") }));
    await screen.findByRole("dialog");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: t("policy.fileEffect.recover") }));
    await waitFor(() => expect(recoverPosts).toHaveLength(1));
    expect(recoverPosts[0]!.body).toEqual({
      placement_id: "p-1",
      generation: 3,
      target_path: "/library/Show/Season 01/Show - E01.mkv",
      fingerprint: "meta:size=4096",
      placement_version: 7,
    });
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith(t("policy.fileEffect.recovered")));
    // The confirm dialog (recovery target line) closes after the success;
    // only the outer files dialog remains.
    await waitFor(() => expect(screen.queryByText(t("policy.fileEffect.recoverTarget", { path: "/library/Show/Season 01/Show - E01.mkv" }))).toBeNull());
    // The success path triggers exactly one full rescan for the anime, and the
    // row is released (no stuck recover busy).
    await waitFor(() => expect(scanPosts).toHaveLength(1));
    expect(scanPosts[0]).toMatchObject({ managed_anime_id: "anime-1", trigger: "manual" });
    expect(within(list).getByRole("button", { name: t("policy.fileEffect.recover") })).toBeEnabled();
  });

  it("keeps the recover dialog open with the typed reason on a refusal", async () => {
    const recoverPosts: number[] = [];
    const scanPosts: unknown[] = [];
    const missing = fileRow({
      status: "missing",
      recovery: { generation: 3, target_path: "/library/Show/Season 01/Show - E01.mkv", target_relative_path: "Show/Season 01/Show - E01.mkv", size_bytes: 4096, placement_version: 7, fingerprint: "meta:size=4096", companions: 0 },
      actions: { align: notApplicable("nothing_to_align"), rerun: available, release: available, recover: available },
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/files/recover") && init?.method === "POST") {
        recoverPosts.push(1);
        return jsonResponse({ code: "ANIME_FILES_RECOVER_REFUSED", message: "x", details: [{ code: "target_changed", message: "x" }] }, 409);
      }
      if (url.endsWith("/reconciliation/scans") && init?.method === "POST") {
        scanPosts.push(JSON.parse(String(init.body)));
        return jsonResponse({ state: "running", generation: 4 }, 202);
      }
      return jsonResponse(filesView([missing]));
    }));
    renderFiles();

    const list = await screen.findByRole("list", { name: t("tabs.files.title") });
    fireEvent.click(within(list).getByRole("button", { name: t("policy.fileEffect.recover") }));
    await screen.findByRole("dialog");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: t("policy.fileEffect.recover") }));
    await waitFor(() => expect(recoverPosts).toHaveLength(1));
    expect(await screen.findByRole("dialog")).toHaveTextContent(reasonLabel("target_changed") ?? "");
    expect(toastMock.success).not.toHaveBeenCalled();
    // A refusal never triggers the follow-up scan.
    expect(scanPosts).toHaveLength(0);
  });

  it("shows the server-projected relative target in the recover dialog", async () => {
    const missing = fileRow({
      status: "missing",
      recovery: { generation: 3, target_path: "/library/Show/Season 01/Show - E01.mkv", target_relative_path: "Show/Season 01/Show - E01.mkv", size_bytes: 4096, placement_version: 7, fingerprint: "meta:size=4096", companions: 0 },
      actions: { align: notApplicable("nothing_to_align"), rerun: available, release: available, recover: available },
    });
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(filesView([missing]))));
    renderFiles();

    const list = await screen.findByRole("list", { name: t("tabs.files.title") });
    fireEvent.click(within(list).getByRole("button", { name: t("policy.fileEffect.recover") }));
    const dialog = await screen.findByRole("dialog");
    // The contract projects the target relative to the library root; the
    // dialog must render that projection and never compose/reveal an absolute
    // path of its own.
    expect(within(dialog).getByText(t("policy.fileEffect.recoverTarget", { path: "Show/Season 01/Show - E01.mkv" }))).toBeTruthy();
    expect(within(dialog).queryByText(/\/library\//)).toBeNull();
  });

  it("clears a previous refusal reason when the dialog is reopened", async () => {
    const recoverPosts: number[] = [];
    let refused = true;
    const missing = fileRow({
      status: "missing",
      recovery: { generation: 3, target_path: "/library/Show/Season 01/Show - E01.mkv", target_relative_path: "Show/Season 01/Show - E01.mkv", size_bytes: 4096, placement_version: 7, fingerprint: "meta:size=4096", companions: 0 },
      actions: { align: notApplicable("nothing_to_align"), rerun: available, release: available, recover: available },
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/files/recover") && init?.method === "POST") {
        recoverPosts.push(1);
        if (refused) {
          return jsonResponse({ code: "ANIME_FILES_RECOVER_REFUSED", message: "x", details: [{ code: "target_changed", message: "x" }] }, 409);
        }
        return jsonResponse({ placement_id: "p-1", generation: 3, target_path: "/library/Show/Season 01/Show - E01.mkv", replay: false, companions: 0 }, 200);
      }
      if (url.endsWith("/reconciliation/scans") && init?.method === "POST") {
        return jsonResponse({ state: "running", generation: 4 }, 202);
      }
      return jsonResponse(filesView([missing]));
    }));
    renderFiles();

    const list = await screen.findByRole("list", { name: t("tabs.files.title") });
    // First attempt: refused, the reason stays inline.
    fireEvent.click(within(list).getByRole("button", { name: t("policy.fileEffect.recover") }));
    await screen.findByRole("dialog");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: t("policy.fileEffect.recover") }));
    await waitFor(() => expect(recoverPosts).toHaveLength(1));
    expect(await screen.findByRole("dialog")).toHaveTextContent(reasonLabel("target_changed") ?? "");

    // Close and reopen: the stale refusal must not greet the user again. The
    // files dialog stays mounted, so the recover confirm is the innermost one.
    const recoverDialog = () => {
      const dialogs = screen.getAllByRole("dialog");
      return dialogs[dialogs.length - 1]!;
    };
    fireEvent.click(within(recoverDialog()).getByRole("button", { name: t("act.cancel") }));
    await waitFor(() =>
      expect(screen.queryByText(t("policy.fileEffect.recoverConfirmBody"))).toBeNull(),
    );
    fireEvent.click(within(list).getByRole("button", { name: t("policy.fileEffect.recover") }));
    await waitFor(() =>
      expect(screen.queryByText(t("policy.fileEffect.recoverConfirmBody"))).not.toBeNull(),
    );
    expect(recoverDialog()).not.toHaveTextContent(reasonLabel("target_changed") ?? "");

    // And a subsequent success works from a clean slate.
    refused = false;
    fireEvent.click(within(recoverDialog()).getByRole("button", { name: t("policy.fileEffect.recover") }));
    await waitFor(() => expect(recoverPosts).toHaveLength(2));
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith(t("policy.fileEffect.recovered")));
  });
});
