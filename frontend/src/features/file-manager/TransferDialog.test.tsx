import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FileManagerEntryDetail, FileManagerListing, FileManagerRoot } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { TransferDialog } from "./TransferDialog";

// Copy/move picks a destination folder inside the dialog, then submits each
// file on its own: revisions are read at start, every target is checked, and an
// occupied target is settled by the skip / version / replace / cancel choice
// and nothing is written before it; replace appears only for a plain file. The
// target must leave the source folder, and every target lands inside a library
// directory.

const roots: FileManagerRoot[] = [
  { id: "root-download", label: "Home", path: "/downloads", available: false, unavailable_reason: "missing" },
  { id: "root-media", label: "media", path: "/media", available: true },
  { id: "root-anime", label: "Show A", path: "/media/anime", available: true },
];

const sourceFolder: FileManagerListing = {
  path: "/media/anime",
  truncated: false,
  entries: [
    { name: "Season 01", path: "/media/anime/Season 01", kind: "directory", state: "detached", locked: false },
    { name: "notes.txt", path: "/media/anime/notes.txt", kind: "file", state: "detached", locked: false },
  ],
};

const emptyFolder: FileManagerListing = { path: "/media/anime/Season 01", truncated: false, entries: [] };

function detail(path: string, name: string, revision: string, locked = false): FileManagerEntryDetail {
  return {
    entry: { name, path, kind: "file", state: locked ? "managed" : "detached", size_bytes: 10, locked },
    revision,
    in_progress: [],
    companions: [],
    operations: [],
  };
}

let fetchMock: ReturnType<typeof vi.fn>;
let occupied: Set<string>;
let locked: Set<string>;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => {
  occupied = new Set();
  locked = new Set();
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/v1/file-manager/entries") {
      return json(url.searchParams.get("path") === "/media/anime" ? sourceFolder : emptyFolder);
    }
    if (url.pathname === "/api/v1/file-manager/entry") {
      const path = url.searchParams.get("path") ?? "";
      if (path === "/media/anime/notes.txt") return json(detail(path, "notes.txt", "rev-notes"));
      if (path === "/media/anime/poster.jpg") return json(detail(path, "poster.jpg", "rev-poster"));
      if (occupied.has(path)) return json(detail(path, path.slice(path.lastIndexOf("/") + 1), "rev-taken", locked.has(path)));
      return json({ code: "FILE_MANAGER_PATH_NOT_FOUND", message: "missing" }, 404);
    }
    if (url.pathname === "/api/v1/file-manager/operations" && init?.method === "POST") {
      return json({ id: "op-1", operation: "copy", state: "ready", path: "/media/anime/notes.txt" }, 202);
    }
    return json({ code: "NOT_FOUND", message: "missing" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => vi.unstubAllGlobals());

function renderDialog(mode: "copy" | "move" = "copy") {
  const onFinished = vi.fn();
  const onClose = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <TransferDialog
        mode={mode}
        sourceDir="/media/anime"
        paths={["/media/anime/notes.txt", "/media/anime/poster.jpg"]}
        roots={roots}
        onClose={onClose}
        onFinished={onFinished}
      />
    </QueryClientProvider>,
  );
  return { onFinished, onClose };
}

function submitted(): Record<string, unknown>[] {
  return fetchMock.mock.calls
    .filter(([input, init]) => new URL(String(input), "http://localhost").pathname === "/api/v1/file-manager/operations" && init?.method === "POST")
    .map(([, init]) => JSON.parse(String(init?.body)));
}

describe("TransferDialog", () => {
  it("starts at the source's own library directory and jumps to another by its button", async () => {
    renderDialog("copy");
    await screen.findByRole("button", { name: t("fileManager.transfer.startCopy") });
    // The pick starts in the library directory the selection lives in — which is also the source folder.
    expect(screen.getByText(t("fileManager.transfer.target", { path: "/media/anime" }))).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.transfer.sameFolder"))).toBeInTheDocument();

    // An unavailable library directory cannot be a target.
    expect(screen.getByRole("button", { name: "Home" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "media" }));
    expect(await screen.findByText(t("fileManager.transfer.target", { path: "/media" }))).toBeInTheDocument();
    // The breadcrumb starts at the library directory the pick now sits in.
    const nav = screen.getByRole("navigation", { name: t("fileManager.breadcrumbAria") });
    expect(within(nav).getByRole("button", { name: "media" })).toBeInTheDocument();
    expect(within(nav).queryByText("Show A")).not.toBeInTheDocument();
  });

  it("walks into a subfolder and copies each file there", async () => {
    const { onFinished } = renderDialog("copy");
    // The selection starts in the folder it lives in: the dialog says so and the start is closed.
    const start = await screen.findByRole("button", { name: t("fileManager.transfer.startCopy") });
    expect(start).toBeDisabled();
    expect(screen.getByText(t("fileManager.transfer.sameFolder"))).toBeInTheDocument();

    fireEvent.click(await screen.findByRole("button", { name: /Season 01/ }));
    expect(await screen.findByText(t("fileManager.transfer.target", { path: "/media/anime/Season 01" }))).toBeInTheDocument();
    expect(start).toBeEnabled();

    fireEvent.click(start);
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith({ submitted: 2, skipped: 0, failed: 0 }));
    const bodies = submitted();
    expect(bodies).toHaveLength(2);
    expect(bodies[0]).toMatchObject({
      operation: "copy",
      path: "/media/anime/notes.txt",
      expected_revision: "rev-notes",
      target: { path: "/media/anime/Season 01/notes.txt", conflict: "block" },
    });
  });

  it("checks every target first and lets skip leave the occupied one alone", async () => {
    occupied.add("/media/anime/Season 01/poster.jpg");
    const { onFinished } = renderDialog("move");
    await screen.findByRole("button", { name: t("fileManager.transfer.startMove") });
    fireEvent.click(await screen.findByRole("button", { name: /Season 01/ }));

    fireEvent.click(await screen.findByRole("button", { name: t("fileManager.transfer.startMove") }));
    // Nothing is written while the choice is open.
    const choice = await screen.findByRole("alertdialog", { name: t("fileManager.conflictChoice.title") });
    expect(within(choice).getByText("/media/anime/Season 01/poster.jpg")).toBeInTheDocument();
    expect(submitted()).toHaveLength(0);

    fireEvent.click(within(choice).getByRole("button", { name: t("fileManager.conflictChoice.skip") }));
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith({ submitted: 1, skipped: 1, failed: 0 }));
    expect(submitted()).toHaveLength(1);
    expect(submitted()[0]).toMatchObject({ operation: "move", path: "/media/anime/notes.txt" });
  });

  it("offers replace for a plain occupied target and submits it as overwrite", async () => {
    occupied.add("/media/anime/Season 01/poster.jpg");
    const { onFinished } = renderDialog("move");
    await screen.findByRole("button", { name: t("fileManager.transfer.startMove") });
    fireEvent.click(await screen.findByRole("button", { name: /Season 01/ }));
    fireEvent.click(await screen.findByRole("button", { name: t("fileManager.transfer.startMove") }));

    const choice = await screen.findByRole("alertdialog", { name: t("fileManager.conflictChoice.title") });
    expect(within(choice).getByText(t("fileManager.conflictChoice.overwriteWarning"))).toBeInTheDocument();
    fireEvent.click(within(choice).getByRole("button", { name: t("fileManager.conflictChoice.overwrite") }));
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith({ submitted: 2, skipped: 0, failed: 0 }));
    const bodies = submitted();
    expect(bodies.find((body) => (body.target as { path: string }).path === "/media/anime/Season 01/poster.jpg")).toMatchObject({
      target: { path: "/media/anime/Season 01/poster.jpg", conflict: "overwrite" },
    });
    expect(bodies.find((body) => (body.target as { path: string }).path === "/media/anime/Season 01/notes.txt")).toMatchObject({
      target: { path: "/media/anime/Season 01/notes.txt", conflict: "block" },
    });
  });

  it("never offers replace when an occupied target is managed", async () => {
    occupied.add("/media/anime/Season 01/poster.jpg");
    locked.add("/media/anime/Season 01/poster.jpg");
    renderDialog("move");
    await screen.findByRole("button", { name: t("fileManager.transfer.startMove") });
    fireEvent.click(await screen.findByRole("button", { name: /Season 01/ }));
    fireEvent.click(await screen.findByRole("button", { name: t("fileManager.transfer.startMove") }));

    const choice = await screen.findByRole("alertdialog", { name: t("fileManager.conflictChoice.title") });
    expect(within(choice).queryByRole("button", { name: t("fileManager.conflictChoice.overwrite") })).not.toBeInTheDocument();
    expect(within(choice).queryByText(t("fileManager.conflictChoice.overwriteWarning"))).not.toBeInTheDocument();
  });

  it("cancelling the choice changes nothing", async () => {
    occupied.add("/media/anime/Season 01/poster.jpg");
    const { onFinished, onClose } = renderDialog();
    await screen.findByRole("button", { name: t("fileManager.transfer.startCopy") });
    fireEvent.click(await screen.findByRole("button", { name: /Season 01/ }));
    fireEvent.click(await screen.findByRole("button", { name: t("fileManager.transfer.startCopy") }));

    const choice = await screen.findByRole("alertdialog", { name: t("fileManager.conflictChoice.title") });
    fireEvent.click(within(choice).getByRole("button", { name: t("fileManager.conflictChoice.cancel") }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(submitted()).toHaveLength(0);
    expect(onFinished).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });
});
