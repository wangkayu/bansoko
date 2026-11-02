import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FileManagerEntryDetail, FileManagerListing, FileManagerRoot, FileManagerRootList } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { FileManagerDialog } from "./FileManagerDialog";

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() }),
}));

// The browse layer: browsing happens in absolute paths inside the owner-added
// library directories (roots), while the rows themselves are read-only — files
// pick up a checkbox, locked files open their managed detail, and every write
// is a dialog asked for from the selection bar.

const downloadRoot: FileManagerRoot = { id: "root-download", label: "Home", path: "/downloads", available: false, unavailable_reason: "missing" };
const mediaRoot: FileManagerRoot = { id: "root-media", label: "media", path: "/media", available: true };
const showARoot: FileManagerRoot = { id: "root-anime", label: "Show A", path: "/media/anime", available: true };

const mediaPage: FileManagerListing = {
  path: "/media",
  truncated: false,
  entries: [{ name: "anime", path: "/media/anime", kind: "directory", state: "detached", locked: false }],
};

const firstPage: FileManagerListing = {
  path: "/media/anime",
  truncated: false,
  next_cursor: "cursor-2",
  entries: [
    { name: "Season 01", path: "/media/anime/Season 01", kind: "directory", state: "in_progress", locked: true, modified_at: "2026-10-11T08:00:00Z" },
    { name: "ep01.mkv", path: "/media/anime/ep01.mkv", kind: "file", state: "managed", role: "video", size_bytes: 2048, locked: true, managed_anime_id: "anime-1" },
    { name: "ep01.zh.ass", path: "/media/anime/ep01.zh.ass", kind: "file", state: "managed", role: "subtitle", companion_of: "/media/anime/ep01.mkv", locked: true, managed_anime_id: "anime-1" },
  ],
};

const secondPage: FileManagerListing = {
  path: "/media/anime",
  truncated: false,
  entries: [
    { name: "notes.txt", path: "/media/anime/notes.txt", kind: "file", state: "detached", size_bytes: 10, locked: false },
    { name: "poster.jpg", path: "/media/anime/poster.jpg", kind: "file", state: "detached", size_bytes: 30, locked: false },
    // A plain file is classified by its extension, not by owner records.
    { name: "raw-02.mkv", path: "/media/anime/raw-02.mkv", kind: "file", file_kind: "video", state: "detached", size_bytes: 40, locked: false },
  ],
};

const managedDetail: FileManagerEntryDetail = {
  entry: { name: "ep01.mkv", path: "/media/anime/ep01.mkv", kind: "file", state: "managed", role: "video", size_bytes: 2048, locked: true, managed_anime_id: "anime-1" },
  anime: { managed_anime_id: "anime-1", title: "Show A", subscription_state: "active", ended: false },
  acquisition_id: "acq-1",
  revision: "rev-1",
  in_progress: [],
  companions: [{ role: "subtitle", path: "/media/anime/ep01.zh.ass", present: true }],
  operations: [
    { operation: "release", allowed: true },
    { operation: "copy", allowed: true },
  ],
};

const notesDetail: FileManagerEntryDetail = {
  entry: { name: "notes.txt", path: "/media/anime/notes.txt", kind: "file", state: "detached", size_bytes: 10, locked: false },
  revision: "rev-notes",
  in_progress: [],
  companions: [],
  operations: [],
};

let fetchMock: ReturnType<typeof vi.fn>;
let rootsBody: FileManagerRootList;
let entriesFailure: { status: number; code: string } | null;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => {
  rootsBody = { roots: [downloadRoot, mediaRoot, showARoot] };
  entriesFailure = null;
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/v1/file-manager/roots") {
      if (init?.method === "POST") {
        const request = JSON.parse(String(init?.body)) as { path: string };
        const root: FileManagerRoot = {
          id: `root-${request.path}`,
          label: request.path.slice(request.path.lastIndexOf("/") + 1),
          path: request.path,
          available: true,
        };
        rootsBody = { roots: [...rootsBody.roots, root] };
        return json(root, 201);
      }
      return json(rootsBody);
    }
    if (url.pathname.startsWith("/api/v1/file-manager/roots/") && init?.method === "DELETE") {
      const id = decodeURIComponent(url.pathname.slice("/api/v1/file-manager/roots/".length));
      const removed = rootsBody.roots.some((root) => root.id === id);
      rootsBody = { roots: rootsBody.roots.filter((root) => root.id !== id) };
      return removed ? new Response(null, { status: 204 }) : json({ code: "FILE_MANAGER_ROOT_NOT_FOUND", message: "missing" }, 404);
    }
    if (url.pathname === "/api/v1/file-manager/entries") {
      if (entriesFailure) return json({ code: entriesFailure.code, message: "denied" }, entriesFailure.status);
      if (url.searchParams.get("cursor") === "cursor-2") return json(secondPage);
      return json(url.searchParams.get("path") === "/media" ? mediaPage : firstPage);
    }
    if (url.pathname === "/api/v1/file-manager/entry") {
      const path = url.searchParams.get("path");
      if (path === "/media/anime/ep01.mkv") return json(managedDetail);
      if (path === "/media/anime/notes.txt") return json(notesDetail);
    }
    if (url.pathname === "/api/v1/file-manager/operations" && init?.method === "POST") {
      return json({ id: "op-1", operation: "delete", state: "ready", path: "/media/anime/notes.txt" });
    }
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

function requested(): URL[] {
  return fetchMock.mock.calls.map(([input]) => new URL(String(input), "http://localhost"));
}

async function loadFolder(props: Partial<Parameters<typeof FileManagerDialog>[0]> = { path: "/media/anime" }) {
  const handlers = renderDialog(props);
  const list = await screen.findByRole("list", { name: t("fileManager.listAria") });
  fireEvent.click(screen.getByRole("button", { name: t("fileManager.loadMore") }));
  await waitFor(() => expect(list.textContent).toContain("notes.txt"));
  return { list, handlers };
}

function fileCheckbox(name: string) {
  return screen.getByRole("checkbox", { name: t("fileManager.browse.selectFile", { name }) });
}

describe("FileManagerDialog", () => {
  it("opens at the first available library directory and jumps from the root selector", async () => {
    const handlers = renderDialog();
    expect(await screen.findByText("anime")).toBeInTheDocument();
    // The first available root is the opener; unavailable ones never get a folder read.
    expect(requested().some((url) => url.pathname.endsWith("/entries") && url.searchParams.get("path") === "/media")).toBe(true);
    expect(requested().some((url) => url.pathname.endsWith("/entries") && url.searchParams.get("path") === "/downloads")).toBe(false);

    fireEvent.click(screen.getByText("anime").closest("button")!);
    expect(handlers.onNavigate).toHaveBeenLastCalledWith("/media/anime");

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: t("fileManager.roots.selector") }));
    const menu = await screen.findByRole("menu");
    // An unavailable root is disabled in place and states the reason.
    const download = within(menu).getByRole("menuitem", { name: /Home/ });
    expect(download).toHaveAttribute("aria-disabled", "true");
    expect(download.textContent).toContain(t("fileManager.roots.unavailable.missing"));

    await user.click(within(menu).getByRole("menuitem", { name: /Show A/ }));
    expect(handlers.onNavigate).toHaveBeenLastCalledWith("/media/anime");
  });

  it("goes to an absolute path from the path bar and refuses a relative one", async () => {
    const handlers = renderDialog();
    await screen.findByText("anime");
    // The path field stays behind its button; the root selector and breadcrumb carry navigation.
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.pathBar.toggle") }));
    const input = screen.getByLabelText(t("fileManager.pathBar.label"));

    fireEvent.change(input, { target: { value: "/media//anime/" } });
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.pathBar.go") }));
    // Normalization removes repeated and trailing "/"; the path itself is navigated, not guessed.
    expect(handlers.onNavigate).toHaveBeenLastCalledWith("/media/anime");

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.pathBar.toggle") }));
    fireEvent.change(screen.getByLabelText(t("fileManager.pathBar.label")), { target: { value: "media/anime" } });
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.pathBar.go") }));
    expect(await screen.findByText(t("fileManager.pathBar.absolute"))).toBeInTheDocument();
    expect(handlers.onNavigate).toHaveBeenCalledTimes(1);
  });

  it("lists flat rows with folders first, pages, and navigates up the breadcrumb", async () => {
    const { list, handlers } = await loadFolder();
    const items = within(list).getAllByRole("listitem");
    // Folders come first; a subtitle is a sibling row of its video, not nested under it.
    const flat = items.map((item) => item.textContent ?? "");
    expect(flat.findIndex((text) => text.includes("Season 01"))).toBe(0);
    expect(flat.findIndex((text) => text.includes("notes.txt"))).toBeGreaterThan(0);
    const videoItem = within(list).getByText("ep01.mkv").closest("li")!;
    expect(within(videoItem).queryByText("ep01.zh.ass")).not.toBeInTheDocument();
    expect(within(list).getByText("ep01.zh.ass").closest("li")).not.toBe(videoItem);

    expect(requested().some((url) => url.pathname.endsWith("/entries") && url.searchParams.get("cursor") === "cursor-2" && url.searchParams.get("path") === "/media/anime")).toBe(true);
    expect(screen.queryByRole("button", { name: t("fileManager.loadMore") })).not.toBeInTheDocument();

    fireEvent.click(within(list).getByText("Season 01"));
    expect(handlers.onNavigate).toHaveBeenLastCalledWith("/media/anime/Season 01");

    // A library directory has nothing above it inside the breadcrumb, so its only crumb is the root itself.
    const breadcrumb = screen.getByRole("navigation", { name: t("fileManager.breadcrumbAria") });
    expect(within(breadcrumb).getByText("Show A")).toHaveAttribute("aria-current", "location");
    expect(within(breadcrumb).queryByRole("button")).not.toBeInTheDocument();
  });

  it("walks up the breadcrumb from a subfolder to its library directory", async () => {
    const handlers = renderDialog({ path: "/media/anime/Season 01" });
    await screen.findByRole("list", { name: t("fileManager.listAria") });
    const breadcrumb = screen.getByRole("navigation", { name: t("fileManager.breadcrumbAria") });
    expect(within(breadcrumb).getByText("Season 01")).toHaveAttribute("aria-current", "location");
    fireEvent.click(within(breadcrumb).getByRole("button", { name: "Show A" }));
    expect(handlers.onNavigate).toHaveBeenLastCalledWith("/media/anime");
  });

  it("opens the read-only managed detail for a locked file and its anime", async () => {
    const handlers = renderDialog({ path: "/media/anime" });
    const list = await screen.findByRole("list", { name: t("fileManager.listAria") });
    // Locked files carry no checkbox: clicking the grayed row opens the detail.
    expect(within(within(list).getByText("ep01.mkv").closest("li")!).queryByRole("checkbox")).not.toBeInTheDocument();
    fireEvent.click(within(list).getByText("ep01.mkv"));
    const detail = await screen.findByRole("dialog", { name: "ep01.mkv" });
    expect(within(detail).getByText(t("fileManager.managedDialog.unbind"))).toBeInTheDocument();
    fireEvent.click(within(detail).getByRole("button", { name: "Show A" }));
    expect(handlers.onOpenAnime).toHaveBeenCalledWith("anime-1");
  });

  it("selects files, gates rename to a single file, and opens the rename dialog", async () => {
    await loadFolder();
    fireEvent.click(fileCheckbox("notes.txt"));
    fireEvent.click(fileCheckbox("poster.jpg"));
    expect(screen.getByText(t("fileManager.selection.count", { n: 2 }))).toBeInTheDocument();

    const rename = screen.getByRole("button", { name: t("fileManager.selection.rename") });
    expect(rename).toBeDisabled();
    expect(rename.closest("span")!.title).toBe(t("fileManager.selection.renameSingleHint"));

    fireEvent.click(fileCheckbox("poster.jpg"));
    expect(screen.getByText(t("fileManager.selection.count", { n: 1 }))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("fileManager.selection.rename") })).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.selection.rename") }));
    // Blocking short forms open as alertdialog (the base Dialog's blocking role).
    const dialog = await screen.findByRole("alertdialog", { name: t("fileManager.renameDialog.title") });
    expect(within(dialog).getByText("notes.txt")).toBeInTheDocument();
    const input = within(dialog).getByLabelText(t("fileManager.renameDialog.newName"));
    expect(input).toHaveValue("notes.txt");
    // The field opens on the current name; that is not a validation error until the owner edits it.
    expect(within(dialog).queryByText(t("fileManager.renameDialog.nameSame"))).not.toBeInTheDocument();
    fireEvent.change(input, { target: { value: "poster.jpg" } });
    expect(within(dialog).getByText(t("fileManager.renameDialog.nameTaken"))).toBeInTheDocument();
  });

  it("switching the filter empties the selection; hide-managed drops locked rows", async () => {
    await loadFolder();
    fireEvent.click(fileCheckbox("notes.txt"));
    expect(screen.getByText(t("fileManager.selection.count", { n: 1 }))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("radio", { name: t("fileManager.browse.filter.video") }));
    await waitFor(() => expect(screen.queryByText(t("fileManager.selection.count", { n: 1 }))).not.toBeInTheDocument());
    expect(screen.queryByRole("checkbox", { name: t("fileManager.browse.selectFile", { name: "notes.txt" }) })).not.toBeInTheDocument();
    // A detached video is filtered as a video by its extension, not left in "other".
    expect(screen.getByText("raw-02.mkv")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("radio", { name: t("fileManager.browse.filter.all") }));
    fireEvent.click(screen.getByRole("switch", { name: t("fileManager.browse.hideManaged") }));
    await waitFor(() => expect(screen.queryByText("ep01.mkv")).not.toBeInTheDocument());
    expect(screen.getByText("notes.txt")).toBeInTheDocument();
    expect(screen.getByText("poster.jpg")).toBeInTheDocument();

    // Images are a browse-only bucket: a plain .jpg is reachable, other files are not.
    fireEvent.click(screen.getByRole("radio", { name: t("fileManager.browse.filter.image") }));
    expect(screen.getByText("poster.jpg")).toBeInTheDocument();
    expect(screen.queryByText("notes.txt")).not.toBeInTheDocument();
  });

  it("deletes a selection per file: revision read, submitted with it, toast and clear", async () => {
    await loadFolder();
    fireEvent.click(fileCheckbox("notes.txt"));
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.selection.delete") }));
    const dialog = await screen.findByRole("alertdialog", { name: t("fileManager.deleteDialog.title") });
    expect(within(dialog).getByText("notes.txt")).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.deleteDialog.submit") }));
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("fileManager.batch.done", { n: 1 })));
    const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(posts).toHaveLength(1);
    const body = JSON.parse(String(posts[0][1].body));
    expect(body).toMatchObject({ operation: "delete", path: "/media/anime/notes.txt", expected_revision: "rev-notes" });
    // The finished batch closes the dialog and empties the selection.
    await waitFor(() => expect(screen.queryByRole("alertdialog", { name: t("fileManager.deleteDialog.title") })).not.toBeInTheDocument());
    expect(screen.queryByText(t("fileManager.selection.count", { n: 1 }))).not.toBeInTheDocument();
  });

  it("shows a typed error for a path the browser refuses", async () => {
    entriesFailure = { status: 400, code: "FILE_MANAGER_PATH_DENIED" };
    renderDialog({ path: "/etc" });
    expect(await screen.findByText(t("error.FILE_MANAGER_PATH_DENIED"))).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: t("fileManager.retry") }).length).toBeGreaterThan(0);
  });

  it("shows the empty state while no library directory exists, and adds one from there", async () => {
    rootsBody = { roots: [] };
    renderDialog();
    expect(await screen.findByText(t("fileManager.noRoots.title"))).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.noRoots.body"))).toBeInTheDocument();
    // Nothing is browsed yet: there is no authorized directory to list.
    expect(requested().some((url) => url.pathname.endsWith("/entries"))).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.noRoots.add") }));
    const dialog = await screen.findByRole("dialog", { name: t("fileManager.roots.title") });
    fireEvent.change(within(dialog).getByLabelText(t("fileManager.roots.pathLabel")), { target: { value: "/srv/library" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.roots.add") }));
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("fileManager.roots.added", { path: "/srv/library" })));
    const posts = fetchMock.mock.calls.filter(([input, init]) => new URL(String(input), "http://localhost").pathname === "/api/v1/file-manager/roots" && init?.method === "POST");
    expect(posts).toHaveLength(1);
    expect(JSON.parse(String(posts[0][1].body))).toEqual({ path: "/srv/library" });
  });

  it("offers to add the parent directory when the path lies outside every library directory", async () => {
    entriesFailure = { status: 403, code: "FILE_MANAGER_PATH_OUTSIDE_ROOTS" };
    renderDialog({ path: "/srv/library" });
    expect(await screen.findByText(t("fileManager.outside.title"))).toBeInTheDocument();
    // Outside-roots is a state, not a load failure: the retry button stays away.
    expect(screen.queryByRole("button", { name: t("fileManager.retry") })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.outside.add") }));
    const dialog = await screen.findByRole("dialog", { name: t("fileManager.roots.title") });
    expect(within(dialog).getByLabelText(t("fileManager.roots.pathLabel"))).toHaveValue("/srv");
  });

  it("manages library directories: removing the browsed one switches to the next", async () => {
    const handlers = renderDialog({ path: "/media/anime" });
    await screen.findByRole("list", { name: t("fileManager.listAria") });

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: t("fileManager.roots.selector") }));
    const menu = await screen.findByRole("menu");
    await user.click(within(menu).getByRole("menuitem", { name: t("fileManager.roots.manage") }));

    const dialog = await screen.findByRole("dialog", { name: t("fileManager.roots.title") });
    // Only the root being browsed carries the current badge — not a nested parent also holding the path.
    const showARow = within(dialog).getByText("Show A").closest("li")!;
    expect(within(showARow).getByText(t("fileManager.roots.current"))).toBeInTheDocument();
    const mediaRow = within(dialog).getByText("media", { exact: true }).closest("li")!;
    expect(within(mediaRow).queryByText(t("fileManager.roots.current"))).not.toBeInTheDocument();

    fireEvent.click(within(showARow).getByRole("button", { name: t("fileManager.roots.remove") }));
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("fileManager.roots.removed", { path: "/media/anime" })));
    expect(fetchMock.mock.calls.some(([input, init]) => init?.method === "DELETE" && new URL(String(input), "http://localhost").pathname === "/api/v1/file-manager/roots/root-anime")).toBe(true);
    // Removing the browsed root lands on the next usable one.
    expect(handlers.onNavigate).toHaveBeenCalledWith("/media");
  });
});
