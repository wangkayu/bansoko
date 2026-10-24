import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FileManagerEntryDetail, FileManagerOperationView } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { EntryDetailView } from "./FileManagerDialog";

// Targets are always absolute: the file manager is limited only by absolute-path rules (absolute
//   path, not the original file itself, an existing parent directory) — there is no "location root"
//   layer anymore.
const source = "/media/anime/Show/ep01.mkv";

const operations = (allowed: boolean, reason?: "managed") =>
  (["scan", "align", "copy", "move", "rename", "delete", "release"] as const).map((operation) => ({
    operation,
    allowed: operation === "scan" || operation === "align" || operation === "copy" || operation === "release" ? true : allowed,
    ...(operation === "move" || operation === "rename" || operation === "delete" ? (allowed ? {} : { blocked_reason: reason }) : {}),
  }));

function detailOf(overrides: Partial<FileManagerEntryDetail> = {}): FileManagerEntryDetail {
  return {
    entry: { name: "ep01.mkv", path: source, kind: "file", state: "detached", role: "video", size_bytes: 10, locked: false },
    anime: { managed_anime_id: "anime-1", title: "Show A", subscription_state: "active", ended: false },
    revision: "rev-1",
    in_progress: [],
    companions: [],
    operations: operations(true),
    ...overrides,
  };
}

type Call = { url: URL; method: string; headers: Headers; body?: Record<string, unknown> };
let calls: Call[];
/** Pre-existing paths: the target check (reading details) reports them as taken, any other path as absent. */
let occupied: Set<string>;
let postResponses: Response[];

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const accepted: FileManagerOperationView = {
  id: "op-1",
  operation: "move",
  state: "ready",
  path: source,
  created_at: "2026-10-12T08:00:00Z",
};

beforeEach(() => {
  calls = [];
  occupied = new Set();
  postResponses = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, method, headers: new Headers(init?.headers), body });
      if (url.pathname === "/api/v1/file-manager/entry" && method === "GET") {
        const path = url.searchParams.get("path") ?? "";
        return occupied.has(path)
          ? json(detailOf({ entry: { name: path, path, kind: "file", state: "detached", locked: false } }))
          : json({ code: "FILE_MANAGER_PATH_NOT_FOUND", message: "missing" }, 404);
      }
      if (method === "POST") {
        const next = postResponses.shift();
        if (next) return next;
        if (url.pathname.endsWith("/entry/release")) return json({ managed_anime_id: "anime-1", released_placements: 1 });
        if (url.pathname.endsWith("/entry/align")) return json({ managed_anime_id: "anime-1" }, 202);
        if (url.pathname.endsWith("/reconciliation/scans")) return json({}, 202);
        return json(accepted, 202);
      }
      return json({ code: "FILE_MANAGER_PATH_NOT_FOUND", message: "missing" }, 404);
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderDetail(detail: FileManagerEntryDetail) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <EntryDetailView detail={detail} onOpenAnime={vi.fn()} onOpenRecord={vi.fn()} onSelect={vi.fn()} />
    </QueryClientProvider>,
  );
}

const posts = () => calls.filter((call) => call.method === "POST");
const opButton = (name: string) => screen.getByRole("button", { name });
const opLabel = (op: string) => t(`fileManager.operation.${op}` as "fileManager.operation.move");

async function openDialog(name: string) {
  fireEvent.click(opButton(name));
  return screen.findByRole("alertdialog");
}

function setInput(dialog: HTMLElement, label: string, value: string) {
  fireEvent.change(within(dialog).getByRole("textbox", { name: new RegExp(label) }), { target: { value } });
}

describe("operation buttons", () => {
  it("follow the operations list and explain a blocked one in a tooltip", async () => {
    renderDetail(detailOf({ entry: { name: "ep01.mkv", path: source, kind: "file", state: "managed", role: "video", locked: true }, operations: operations(false, "managed") }));
    const section = screen.getByRole("region", { name: t("fileManager.operationsHeading") });
    const names = within(section).getAllByRole("button").map((button) => button.textContent);
    expect(names).toEqual(["scan", "align", "copy", "move", "rename", "delete", "release"].map(opLabel));
    expect(opButton(opLabel("copy"))).toBeEnabled();
    expect(opButton(opLabel("move"))).toBeDisabled();
    expect(opButton(opLabel("delete"))).toBeDisabled();

    await userEvent.hover(opButton(opLabel("move")).parentElement!);
    expect(await screen.findByText(t("fileManager.operationBlock.managed"))).toBeInTheDocument();
  });

  it("explains a file whose alignment is switched off", async () => {
    renderDetail(detailOf({ operations: [{ operation: "align", allowed: false, blocked_reason: "alignment_disabled" }] }));
    expect(opButton(opLabel("align"))).toBeDisabled();
    await userEvent.hover(opButton(opLabel("align")).parentElement!);
    expect(await screen.findByText(t("fileManager.operationBlock.alignment_disabled"))).toBeInTheDocument();
  });

  it("scans the anime without a confirmation", async () => {
    renderDetail(detailOf());
    fireEvent.click(opButton(opLabel("scan")));
    await waitFor(() => expect(posts().some((call) => call.url.pathname === "/api/v1/reconciliation/scans" && call.body?.managed_anime_id === "anime-1")).toBe(true));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });
});

describe("move, rename and copy", () => {
  it("submits a move to a free absolute target with the revision and an idempotency key", async () => {
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("move"));
    setInput(dialog, t("fileManager.dialog.targetPath"), "/media/anime/Show/Season 01/ep01.mkv");
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.move") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    const call = posts()[0];
    expect(call.url.pathname).toBe("/api/v1/file-manager/operations");
    expect(call.body).toEqual({
      operation: "move",
      path: source,
      expected_revision: "rev-1",
      target: { path: "/media/anime/Show/Season 01/ep01.mkv", conflict: "block" },
    });
    expect(call.headers.get("Idempotency-Key")).toMatch(/^.{8,128}$/);
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("refuses a relative target and a target equal to the source", async () => {
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("move"));
    const submit = within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.move") });
    setInput(dialog, t("fileManager.dialog.targetPath"), "Show/ep01.mkv");
    expect(submit).toBeDisabled();
    expect(within(dialog).getByText(t("fileManager.dialog.targetAbsolute"))).toBeInTheDocument();
    setInput(dialog, t("fileManager.dialog.targetPath"), source);
    expect(submit).toBeDisabled();
    expect(within(dialog).getByText(t("fileManager.dialog.unchanged"))).toBeInTheDocument();
    // Repeated "/" differ only in spelling; after normalization it is the same location as the source file.
    setInput(dialog, t("fileManager.dialog.targetPath"), "/media/anime/Show//ep01.mkv");
    expect(submit).toBeDisabled();
    expect(posts()).toHaveLength(0);
  });

  it("offers cancel, add suffix and rename for an occupied target, never overwrite", async () => {
    occupied.add("/media/anime/Show/taken.mkv");
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("move"));
    setInput(dialog, t("fileManager.dialog.targetPath"), "/media/anime/Show/taken.mkv");
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.move") }));

    expect(await within(dialog).findByText(t("fileManager.conflict.title"))).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    expect(within(dialog).getByRole("button", { name: t("fileManager.conflict.cancel") })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t("fileManager.conflict.rename") })).toBeInTheDocument();
    expect(within(dialog).getAllByRole("button").map((button) => button.textContent)).not.toContain(expect.stringMatching(/overwrite|覆盖/i));

    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.conflict.version") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body?.target).toEqual({ path: "/media/anime/Show/taken.mkv", conflict: "version" });
  });

  it("goes back to editing on rename and checks the new target again", async () => {
    occupied.add("/media/anime/Show/taken.mkv");
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("move"));
    setInput(dialog, t("fileManager.dialog.targetPath"), "/media/anime/Show/taken.mkv");
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.move") }));
    fireEvent.click(await within(dialog).findByRole("button", { name: t("fileManager.conflict.rename") }));
    expect(within(dialog).queryByText(t("fileManager.conflict.title"))).not.toBeInTheDocument();
    expect(within(dialog).getByRole("textbox", { name: new RegExp(t("fileManager.dialog.targetPath")) })).toHaveFocus();

    setInput(dialog, t("fileManager.dialog.targetPath"), "/media/anime/Show/free.mkv");
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.move") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body?.target).toEqual({ path: "/media/anime/Show/free.mkv", conflict: "block" });
  });

  it("shows the choice when the server reports the target as taken after the check", async () => {
    postResponses.push(json({ code: "FILE_MANAGER_TARGET_EXISTS", message: "exists" }, 409));
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("move"));
    setInput(dialog, t("fileManager.dialog.targetPath"), "/media/anime/Show/race.mkv");
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.move") }));
    expect(await within(dialog).findByText(t("fileManager.conflict.title"))).toBeInTheDocument();
    // The choice replaces a plain error: the same sentence is not shown twice.
    expect(within(dialog).getAllByText(t("error.FILE_MANAGER_TARGET_EXISTS"))).toHaveLength(1);
    expect(within(dialog).getByRole("button", { name: t("fileManager.conflict.version") })).toBeInTheDocument();
  });

  it("renames in place: only the file name changes", async () => {
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("rename"));
    const input = within(dialog).getByRole("textbox", { name: new RegExp(t("fileManager.dialog.newName")) });
    expect(input).toHaveValue("ep01.mkv");
    // Unchanged name cannot be submitted; a slash is refused.
    expect(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.rename") })).toBeDisabled();
    fireEvent.change(input, { target: { value: "a/b.mkv" } });
    expect(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.rename") })).toBeDisabled();
    fireEvent.change(input, { target: { value: "ep01-final.mkv" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.rename") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toMatchObject({
      operation: "rename",
      path: source,
      target: { path: "/media/anime/Show/ep01-final.mkv", conflict: "block" },
    });
    expect(calls.some((call) => call.method === "GET" && call.url.searchParams.get("path") === "/media/anime/Show/ep01-final.mkv")).toBe(true);
  });

  it("copies to an absolute target the user fills in", async () => {
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("copy"));
    setInput(dialog, t("fileManager.dialog.targetPath"), "/media/anime/Show/ep01 (copy).mkv");
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.copy") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toEqual({
      operation: "copy",
      path: source,
      expected_revision: "rev-1",
      target: { path: "/media/anime/Show/ep01 (copy).mkv", conflict: "block" },
    });
  });
});

describe("delete", () => {
  it("confirms in two lines and submits without a target", async () => {
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("delete"));
    expect(within(dialog).getByText(t("fileManager.dialog.line1.delete"))).toBeInTheDocument();
    expect(within(dialog).getByText(t("fileManager.dialog.line2.delete"))).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.delete") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toEqual({ operation: "delete", path: source, expected_revision: "rev-1" });
  });

  it("reuses the idempotency key after a server error and renews it after a rejection", async () => {
    postResponses.push(json({ code: "INTERNAL", message: "boom" }, 500), json({ code: "FILE_MANAGER_ENTRY_CHANGED", message: "changed" }, 409));
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("delete"));
    const submit = () => fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.delete") }));

    submit();
    await waitFor(() => expect(posts()).toHaveLength(1));
    await within(dialog).findByText(t("error.INTERNAL"));
    submit();
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect(posts()[1].headers.get("Idempotency-Key")).toBe(posts()[0].headers.get("Idempotency-Key"));

    await within(dialog).findByText(t("error.FILE_MANAGER_ENTRY_CHANGED"));
    submit();
    await waitFor(() => expect(posts()).toHaveLength(3));
    expect(posts()[2].headers.get("Idempotency-Key")).not.toBe(posts()[1].headers.get("Idempotency-Key"));
  });
});

describe("release", () => {
  it("confirms, warns about re-acquisition and sends no move target by default", async () => {
    renderDetail(detailOf({ entry: { name: "ep01.mkv", path: source, kind: "file", state: "managed", role: "video", locked: true } }));
    const dialog = await openDialog(opLabel("release"));
    expect(within(dialog).getByText(t("fileManager.dialog.line1.release"))).toBeInTheDocument();
    expect(within(dialog).getByText(t("fileManager.dialog.releaseReacquire"))).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.release") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url.pathname).toBe("/api/v1/file-manager/entry/release");
    expect(posts()[0].body).toEqual({ path: source, expected_revision: "rev-1" });
  });

  it("does not warn when the subscription has ended", async () => {
    renderDetail(detailOf({ anime: { managed_anime_id: "anime-1", title: "Show A", subscription_state: "stopped", ended: true } }));
    const dialog = await openDialog(opLabel("release"));
    expect(within(dialog).queryByText(t("fileManager.dialog.releaseReacquire"))).not.toBeInTheDocument();
  });

  it("releases and moves in one step", async () => {
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("release"));
    fireEvent.click(within(dialog).getByRole("checkbox", { name: t("fileManager.dialog.moveAlso") }));
    setInput(dialog, t("fileManager.dialog.moveTo"), "/media/moved/ep01.mkv");
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.releaseAndMove") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toEqual({
      path: source,
      expected_revision: "rev-1",
      move_to: { path: "/media/moved/ep01.mkv", conflict: "block" },
    });
  });

  it("offers the conflict choice for an occupied move target", async () => {
    occupied.add("/media/moved/ep01.mkv");
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("release"));
    fireEvent.click(within(dialog).getByRole("checkbox", { name: t("fileManager.dialog.moveAlso") }));
    setInput(dialog, t("fileManager.dialog.moveTo"), "/media/moved/ep01.mkv");
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.releaseAndMove") }));
    fireEvent.click(await within(dialog).findByRole("button", { name: t("fileManager.conflict.version") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body?.move_to).toEqual({ path: "/media/moved/ep01.mkv", conflict: "version" });
  });
});

describe("align", () => {
  it("confirms and submits the revision", async () => {
    renderDetail(detailOf({ entry: { name: "ep01.mkv", path: source, kind: "file", state: "managed", role: "video", locked: true } }));
    const dialog = await openDialog(opLabel("align"));
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.align") }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].url.pathname).toBe("/api/v1/file-manager/entry/align");
    expect(posts()[0].body).toEqual({ path: source, expected_revision: "rev-1" });
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("shows the typed error when alignment is switched off", async () => {
    postResponses.push(json({ code: "FILE_MANAGER_ALIGNMENT_DISABLED", message: "off" }, 409));
    renderDetail(detailOf());
    const dialog = await openDialog(opLabel("align"));
    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.dialog.submit.align") }));
    expect(await within(dialog).findByText(t("error.FILE_MANAGER_ALIGNMENT_DISABLED"))).toBeInTheDocument();
  });
});

describe("last operation", () => {
  const base = { id: "op-9", operation: "move" as const, path: source, created_at: "2026-10-12T08:00:00Z" };

  it("shows the failure reason of a blocked operation", () => {
    renderDetail(detailOf({ last_operation: { ...base, state: "blocked", failure: "target_occupied" } }));
    const section = screen.getByRole("region", { name: t("fileManager.lastOperation.label") });
    expect(within(section).getByText(t("fileManager.operation.move"))).toBeInTheDocument();
    expect(within(section).getByText(t("fileManager.operationState.blocked"))).toBeInTheDocument();
    expect(within(section).getByText(t("fileManager.operationFailure.target_occupied"))).toBeInTheDocument();
  });

  it("shows a failed operation and the result of a completed one", () => {
    renderDetail(detailOf({ last_operation: { ...base, state: "failed", failure: "execute_failed" } }));
    expect(screen.getByText(t("fileManager.operationState.failed"))).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.operationFailure.execute_failed"))).toBeInTheDocument();
  });

  it("shows where a completed operation put the file", () => {
    renderDetail(detailOf({ last_operation: { ...base, state: "completed", result_path: "/media/anime/Show/ep01 (1).mkv", completed_at: "2026-10-12T08:01:00Z" } }));
    expect(screen.getByText(t("fileManager.operationState.completed"))).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.lastOperation.result", { path: "/media/anime/Show/ep01 (1).mkv" }))).toBeInTheDocument();
  });
});
