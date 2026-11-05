import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FileManagerRoot } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { RootsDialog } from "./RootsDialog";

// The roots dialog adds a library directory either by typing its absolute path
// or by walking the folder tree. The tree only reads subdirectory names outside
// the added roots, picking one fills the same draft, and Add remains the single
// write; removing a root never deletes anything.

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() } }));

const roots: FileManagerRoot[] = [{ id: "root-1", label: "media", path: "/media", available: true }];

let fetchMock: ReturnType<typeof vi.fn>;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => {
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/v1/file-manager/roots" && init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as { path: string };
      return json({ id: "root-2", label: body.path.slice(body.path.lastIndexOf("/") + 1), path: body.path, available: true }, 201);
    }
    if (url.pathname === "/api/v1/file-manager/roots") return json({ roots });
    if (url.pathname.startsWith("/api/v1/file-manager/roots/") && init?.method === "DELETE") {
      return new Response(null, { status: 204 });
    }
    if (url.pathname === "/api/v1/file-manager/root-picker/entries") {
      const path = url.searchParams.get("path");
      if (path === "/") {
        return json({
          path: "/",
          truncated: false,
          next_cursor: url.searchParams.get("cursor") ? undefined : "page-2",
          entries: url.searchParams.get("cursor")
            ? [{ name: "srv", path: "/srv" }]
            : [
                { name: "data", path: "/data" },
                { name: "media", path: "/media" },
              ],
        });
      }
      if (path === "/data") {
        return json({
          path: "/data",
          truncated: true,
          entries: [
            { name: "anime", path: "/data/anime" },
            { name: "downloads", path: "/data/downloads" },
          ],
        });
      }
      return json({ path, truncated: false, entries: [] });
    }
    return json({ code: "NOT_FOUND", message: "missing" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => vi.unstubAllGlobals());

function renderDialog() {
  const onClose = vi.fn();
  const onRemoved = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RootsDialog roots={roots} activeRootID="root-1" onRemoved={onRemoved} onClose={onClose} />
    </QueryClientProvider>,
  );
  return { onClose, onRemoved };
}

function pickerCalls(): string[] {
  return fetchMock.mock.calls
    .filter(([input]) => new URL(String(input), "http://localhost").pathname === "/api/v1/file-manager/root-picker/entries")
    .map(([input]) => new URL(String(input), "http://localhost").searchParams.get("path") ?? "");
}

function addCalls(): { path: string }[] {
  return fetchMock.mock.calls
    .filter(
      ([input, init]) =>
        new URL(String(input), "http://localhost").pathname === "/api/v1/file-manager/roots" && init?.method === "POST",
    )
    .map(([, init]) => JSON.parse(String(init?.body)) as { path: string });
}

describe("RootsDialog", () => {
  it("adds a root from the typed path and removes one without touching files", async () => {
    const { onRemoved } = renderDialog();
    const input = screen.getByLabelText(t("fileManager.roots.pathLabel"));
    fireEvent.change(input, { target: { value: "/data/anime/" } });
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.roots.add") }));

    await waitFor(() => expect(addCalls()).toEqual([{ path: "/data/anime" }]));
    expect(pickerCalls()).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.roots.remove") }));
    await waitFor(() => expect(onRemoved).toHaveBeenCalledWith("/media"));
    const deletes = fetchMock.mock.calls.filter(
      ([input, init]) =>
        new URL(String(input), "http://localhost").pathname === "/api/v1/file-manager/roots/root-1" && init?.method === "DELETE",
    );
    expect(deletes).toHaveLength(1);
  });

  it("walks the folder tree, pages, and fills the draft from a pick", async () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.roots.browse") }));

    // The tree starts at the filesystem root when the draft is empty.
    await screen.findByText("/");
    expect(pickerCalls()).toEqual(["/"]);
    const picker = screen.getByRole("region", { name: t("fileManager.roots.browseAria") });
    await within(picker).findByText("data");
    expect(within(picker).getByText("media")).toBeTruthy();

    // A second page is loaded on demand.
    fireEvent.click(within(picker).getByRole("button", { name: t("fileManager.loadMore") }));
    await waitFor(() => expect(pickerCalls()).toEqual(["/", "/"]));
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("cursor=page-2"))).toBe(true);

    // Opening a folder lists its subfolders and says when the read was cut short.
    fireEvent.click(within(picker).getByRole("button", { name: "data" }));
    await within(picker).findByText("anime");
    expect(pickerCalls()).toContain("/data");
    expect(within(picker).getByText(t("fileManager.roots.browseTruncated"))).toBeTruthy();

    // Up goes back one level.
    fireEvent.click(within(picker).getByRole("button", { name: t("fileManager.roots.up") }));
    await waitFor(() => expect(pickerCalls()).toContain("/"));

    // Picking a folder closes the tree and fills the same input the manual
    // path uses; adding stays the one write.
    fireEvent.click(within(picker).getByRole("button", { name: "data" }));
    await within(picker).findByText("downloads");
    fireEvent.click(within(picker).getAllByRole("button", { name: t("fileManager.roots.select") })[0]!);
    await waitFor(() => expect(screen.queryByRole("region", { name: t("fileManager.roots.browseAria") })).toBeNull());
    expect((screen.getByLabelText(t("fileManager.roots.pathLabel")) as HTMLInputElement).value).toBe("/data/anime");

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.roots.add") }));
    await waitFor(() => expect(addCalls()).toEqual([{ path: "/data/anime" }]));
  });

  it("selects the folder currently open in the tree", async () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.roots.browse") }));
    const picker = screen.getByRole("region", { name: t("fileManager.roots.browseAria") });
    await within(picker).findByText("data");

    fireEvent.click(within(picker).getByRole("button", { name: "data" }));
    await within(picker).findByText("anime");
    fireEvent.click(within(picker).getByRole("button", { name: t("fileManager.roots.selectCurrent") }));
    await waitFor(() => expect(screen.queryByRole("region", { name: t("fileManager.roots.browseAria") })).toBeNull());
    expect((screen.getByLabelText(t("fileManager.roots.pathLabel")) as HTMLInputElement).value).toBe("/data");
  });

  it("shows the real failure instead of an empty folder", async () => {
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname === "/api/v1/file-manager/roots") return json({ roots });
      if (url.pathname === "/api/v1/file-manager/root-picker/entries") {
        return json({ code: "FILE_MANAGER_PATH_UNREADABLE", message: "denied" }, 409);
      }
      return json({ code: "NOT_FOUND", message: "missing" }, 404);
    });
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: t("fileManager.roots.browse") }));
    const picker = screen.getByRole("region", { name: t("fileManager.roots.browseAria") });
    await within(picker).findByRole("alert");
    expect(within(picker).queryByText(t("fileManager.roots.browseEmpty"))).toBeNull();
    expect(within(picker).queryByText(t("fileManager.roots.browseLoading"))).toBeNull();
  });
});
