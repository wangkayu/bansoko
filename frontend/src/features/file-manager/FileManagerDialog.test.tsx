import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FileManagerEntryDetail, FileManagerListing, FileManagerMountList } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { EntryDetailView, FileManagerDialog } from "./FileManagerDialog";

const mounts: FileManagerMountList = {
  mounts: [
    { key: "root", kind: "root", label: "/", path: "/", available: true },
    { key: "download:qb-1", kind: "download", connection_id: "qb-1", label: "Home", path: "/downloads", available: false, unavailable_reason: "missing" },
    { key: "anime:anime-1", kind: "anime", managed_anime_id: "anime-1", label: "Show A", path: "/media/anime", available: true },
  ],
};

const rootPage: FileManagerListing = {
  path: "/",
  truncated: false,
  entries: [{ name: "media", path: "/media", kind: "directory", state: "detached", locked: false }],
};

const firstPage: FileManagerListing = {
  path: "/media/anime",
  truncated: false,
  next_cursor: "cursor-2",
  entries: [
    { name: "Season 01", path: "/media/anime/Season 01", kind: "directory", state: "in_progress", locked: true, modified_at: "2026-10-11T08:00:00Z" },
    { name: "ep01.mkv", path: "/media/anime/ep01.mkv", kind: "file", state: "managed", role: "video", size_bytes: 2048, locked: true, managed_anime_id: "anime-1" },
    { name: "ep01.zh.ass", path: "/media/anime/ep01.zh.ass", kind: "file", state: "managed", role: "subtitle", companion_of: "/media/anime/ep01.mkv", not_aligned: true, locked: true, managed_anime_id: "anime-1" },
  ],
};

const secondPage: FileManagerListing = {
  path: "/media/anime",
  truncated: false,
  entries: [{ name: "notes.txt", path: "/media/anime/notes.txt", kind: "file", state: "detached", size_bytes: 10, locked: false }],
};

const managedDetail: FileManagerEntryDetail = {
  entry: { name: "ep01.mkv", path: "/media/anime/ep01.mkv", kind: "file", state: "managed", role: "video", size_bytes: 2048, locked: true, managed_anime_id: "anime-1" },
  anime: { managed_anime_id: "anime-1", title: "Show A", subscription_state: "active", ended: false },
  acquisition_id: "acq-1",
  revision: "rev-1",
  in_progress: [],
  companions: [{ role: "subtitle", path: "/media/anime/ep01.zh.ass", present: true, not_aligned: true }],
  operations: [
    { operation: "scan", allowed: true },
    { operation: "align", allowed: true },
    { operation: "copy", allowed: true },
    { operation: "move", allowed: false, blocked_reason: "managed" },
    { operation: "rename", allowed: false, blocked_reason: "managed" },
    { operation: "delete", allowed: false, blocked_reason: "managed" },
    { operation: "release", allowed: true },
  ],
};

let fetchMock: ReturnType<typeof vi.fn>;
let entriesStatus: number;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => {
  entriesStatus = 200;
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/v1/file-manager/mounts") return json(mounts);
    if (url.pathname === "/api/v1/file-manager/entries") {
      if (entriesStatus !== 200) return json({ code: "FILE_MANAGER_PATH_DENIED", message: "denied" }, entriesStatus);
      if (url.searchParams.get("cursor") === "cursor-2") return json(secondPage);
      return json(url.searchParams.get("path") === "/" ? rootPage : firstPage);
    }
    if (url.pathname === "/api/v1/file-manager/entry" && url.searchParams.get("path") === "/media/anime/ep01.mkv") return json(managedDetail);
    return json({ code: "FILE_MANAGER_PATH_NOT_FOUND", message: "missing" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderDialog(props: Partial<Parameters<typeof FileManagerDialog>[0]> = {}) {
  const handlers = { onNavigate: vi.fn(), onOpenAnime: vi.fn(), onOpenRecord: vi.fn(), onClose: vi.fn() };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <FileManagerDialog open {...handlers} {...props} />
    </QueryClientProvider>,
  );
  return handlers;
}

function renderWithClient(node: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

function requested(): URL[] {
  return fetchMock.mock.calls.map(([input]) => new URL(String(input), "http://localhost"));
}

function quickEntries() {
  return within(screen.getByRole("region", { name: t("fileManager.locationsAria") }));
}

describe("FileManagerDialog", () => {
  it("opens at the filesystem root, lists the quick entries and jumps to one", async () => {
    const handlers = renderDialog();
    expect(await screen.findByText("media")).toBeInTheDocument();
    expect(requested().some((url) => url.pathname.endsWith("/entries") && url.searchParams.get("path") === "/")).toBe(true);

    // Unavailable quick entries are not clickable and state the reason.
    const download = quickEntries().getByRole("button", { name: new RegExp(t("fileManager.mount.downloadNamed", { name: "Home" })) });
    expect(download).toBeDisabled();
    expect(download.closest("li")!.textContent).toContain(t("fileManager.mountUnavailable.missing"));

    fireEvent.click(quickEntries().getByRole("button", { name: new RegExp(t("fileManager.mount.download")) }));
    expect(handlers.onNavigate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByText("media").closest("button")!);
    expect(handlers.onNavigate).toHaveBeenLastCalledWith({ path: "/media", file: undefined });

    fireEvent.click(quickEntries().getByRole("button", { name: "Show A" }));
    expect(handlers.onNavigate).toHaveBeenLastCalledWith({ path: "/media/anime", file: undefined });
  });

  it("goes to an absolute path from the path bar and refuses a relative one", async () => {
    const handlers = renderDialog();
    await screen.findByText("media");
    const input = screen.getByLabelText(t("fileManager.pathBar.label"));

    fireEvent.change(input, { target: { value: "/media//anime/" } });
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.pathBar.go") }));
    // Normalization removes repeated and trailing "/".
    expect(handlers.onNavigate).toHaveBeenLastCalledWith({ path: "/media/anime", file: undefined });

    fireEvent.change(input, { target: { value: "media/anime" } });
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.pathBar.go") }));
    expect(await screen.findByText(t("fileManager.pathBar.absolute"))).toBeInTheDocument();
    expect(handlers.onNavigate).toHaveBeenCalledTimes(1);
  });

  it("pages a folder, nests a subtitle under its video and navigates", async () => {
    const handlers = renderDialog({ path: "/media/anime" });
    const list = await screen.findByRole("list", { name: t("fileManager.listAria") });
    const videoItem = within(list).getByText("ep01.mkv").closest("li")!;
    // The subtitle renders inside the video entry it follows, flagged as not aligned.
    expect(within(videoItem).getByText("ep01.zh.ass")).toBeInTheDocument();
    expect(within(videoItem).getByText(t("fileManager.notAligned"))).toBeInTheDocument();
    expect(within(list).getByText("Season 01").closest("div")!.textContent).toContain(t("fileManager.state.in_progress"));
    expect(videoItem.textContent).toContain(t("fileManager.state.managed"));

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.loadMore") }));
    await waitFor(() => expect(list.textContent).toContain("notes.txt"));
    expect(list.textContent).toContain(t("fileManager.state.detached"));
    expect(requested().some((url) => url.pathname.endsWith("/entries") && url.searchParams.get("cursor") === "cursor-2" && url.searchParams.get("path") === "/media/anime")).toBe(true);
    expect(screen.queryByRole("button", { name: t("fileManager.loadMore") })).not.toBeInTheDocument();

    fireEvent.click(within(list).getByText("Season 01"));
    expect(handlers.onNavigate).toHaveBeenLastCalledWith({ path: "/media/anime/Season 01", file: undefined });
    fireEvent.click(within(list).getByText("ep01.mkv"));
    expect(handlers.onNavigate).toHaveBeenLastCalledWith({ path: "/media/anime", file: "/media/anime/ep01.mkv" });

    // The breadcrumb assembles the absolute path level by level from the root: go back to the root first.
    const breadcrumb = screen.getByRole("navigation", { name: t("fileManager.breadcrumbAria") });
    fireEvent.click(within(breadcrumb).getByRole("button", { name: t("fileManager.mount.root") }));
    expect(handlers.onNavigate).toHaveBeenLastCalledWith({ path: undefined, file: undefined });
  });

  it("marks locked entries and opens the work that manages them", async () => {
    const handlers = renderDialog({ path: "/media/anime" });
    const list = await screen.findByRole("list", { name: t("fileManager.listAria") });

    // The video and its subtitle are both managed by the same anime, each badge on its own row.
    const videoRow = within(list).getByText("ep01.mkv").closest("div")!;
    const locked = within(videoRow).getByRole("button", { name: t("fileManager.lockedBy", { title: "Show A" }) });
    fireEvent.click(locked);
    expect(handlers.onOpenAnime).toHaveBeenCalledWith("anime-1");
    expect(handlers.onNavigate).not.toHaveBeenCalled();

    // A folder only knows it is locked within the subtree and carries no anime name: show the lock only, no jump.
    const folderItem = within(list).getByText("Season 01").closest("li")!;
    expect(within(folderItem).getByText(t("fileManager.locked"))).toBeInTheDocument();
    expect(within(folderItem).queryByRole("button", { name: t("fileManager.locked") })).not.toBeInTheDocument();
  });

  it("shows the selected file's details, links and the allowed actions", async () => {
    const handlers = renderDialog({ path: "/media/anime", file: "/media/anime/ep01.mkv" });
    const detail = await screen.findByRole("region", { name: t("fileManager.detail.aria") });
    await within(detail).findByText("Show A");
    fireEvent.click(within(detail).getByRole("button", { name: "Show A" }));
    expect(handlers.onOpenAnime).toHaveBeenCalledWith("anime-1");
    fireEvent.click(within(detail).getByRole("button", { name: t("fileManager.detail.openRecord") }));
    expect(handlers.onOpenRecord).toHaveBeenCalledWith("acq-1");

    expect(within(detail).getByRole("button", { name: t("fileManager.operation.copy") })).toBeEnabled();
    expect(within(detail).getByRole("button", { name: t("fileManager.operation.move") })).toBeDisabled();

    fireEvent.click(within(detail).getByRole("button", { name: "ep01.zh.ass" }));
    expect(handlers.onNavigate).toHaveBeenLastCalledWith({ path: "/media/anime", file: "/media/anime/ep01.zh.ass" });
  });

  it("shows a typed error for a path the browser refuses", async () => {
    entriesStatus = 400;
    renderDialog({ path: "/etc" });
    expect(await screen.findByText(t("error.FILE_MANAGER_PATH_DENIED"))).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: t("fileManager.retry") }).length).toBeGreaterThan(0);
  });
});

describe("EntryDetailView", () => {
  it("explains a detached one-shot file and an in-progress owner", () => {
    const onOpenRecord = vi.fn();
    renderWithClient(
      <EntryDetailView
        detail={{
          entry: { name: "movie.zh.ass", path: "/media/Movie/movie.zh.ass", kind: "file", state: "detached", role: "subtitle", locked: false },
          detached_reason: "one_shot",
          revision: "rev-os",
          acquisition_id: "acq-os",
          in_progress: [],
          video: { role: "video", path: "/media/Movie/movie.mkv", present: false },
          companions: [],
          operations: [
            { operation: "copy", allowed: true },
            { operation: "move", allowed: true },
          ],
        }}
        onOpenAnime={vi.fn()}
        onOpenRecord={onOpenRecord}
        onSelect={vi.fn()}
      />,
    );
    expect(screen.getByText(t("fileManager.state.detached"))).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.detachedReason.one_shot"))).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.role.subtitle"))).toBeInTheDocument();
    // The video is no longer at its recorded path: show the name without a link.
    expect(screen.getByText("movie.mkv")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "movie.mkv" })).not.toBeInTheDocument();
    expect(screen.getByText(t("fileManager.detail.missing"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.detail.openRecord") }));
    expect(onOpenRecord).toHaveBeenCalledWith("acq-os");
  });

  it("shows the frozen episode evidence as status text", () => {
    renderWithClient(
      <EntryDetailView
        detail={{
          entry: { name: "x.mkv", path: "/media/x.mkv", kind: "file", state: "detached", role: "video", locked: false },
          revision: "r",
          in_progress: [],
          companions: [],
          operations: [],
          episode: { state: "invalid", failure_code: "episode_fractional", raw_value: "12.5", shape: "single" },
        }}
        onOpenAnime={vi.fn()}
        onOpenRecord={vi.fn()}
        onSelect={vi.fn()}
      />,
    );
    expect(screen.getByText(t("fileManager.detail.episode"))).toBeInTheDocument();
    expect(screen.getByText(`${t("reason.episode_fractional")} · 12.5`)).toBeInTheDocument();
    expect(screen.queryByText(/episode_fractional/)).not.toBeInTheDocument();
    expect(screen.queryByText(t("fileManager.episodeState.invalid"))).not.toBeInTheDocument();
  });

  it("shows a resolved episode without a reason", () => {
    renderWithClient(
      <EntryDetailView
        detail={{
          entry: { name: "y.mkv", path: "/media/y.mkv", kind: "file", state: "managed", role: "video", locked: true },
          revision: "r",
          in_progress: [],
          companions: [],
          operations: [],
          episode: { state: "resolved", raw_value: "05", shape: "single" },
        }}
        onOpenAnime={vi.fn()}
        onOpenRecord={vi.fn()}
        onSelect={vi.fn()}
      />,
    );
    expect(screen.getByText(`${t("fileManager.episodeState.resolved")} · 05`)).toBeInTheDocument();
  });

  it("lists unfinished processing", () => {
    renderWithClient(
      <EntryDetailView
        detail={{
          entry: { name: "ep02.mkv", path: "/media/anime/ep02.mkv", kind: "file", state: "in_progress", role: "video", locked: true },
          revision: "rev-2",
          in_progress: [{ kind: "alignment" }, { kind: "file_operation", acquisition_id: "acq-2" }, { kind: "user_operation" }],
          companions: [],
          operations: [
            { operation: "align", allowed: false, blocked_reason: "in_progress" },
            { operation: "copy", allowed: false, blocked_reason: "in_progress" },
          ],
        }}
        onOpenAnime={vi.fn()}
        onOpenRecord={vi.fn()}
        onSelect={vi.fn()}
      />,
    );
    const processing = screen.getByText(t("fileManager.detail.processing")).closest("div")!;
    expect(within(processing).getByText(t("fileManager.inProgressKind.alignment"))).toBeInTheDocument();
    expect(within(processing).getByText(t("fileManager.inProgressKind.file_operation"))).toBeInTheDocument();
    expect(within(processing).getByText(t("fileManager.inProgressKind.user_operation"))).toBeInTheDocument();
  });
});
