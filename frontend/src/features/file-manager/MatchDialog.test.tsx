import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FileManagerEntryDetail, FileManagerMatchPreview, FileManagerOperationView } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { EntryDetailView } from "./FileManagerDialog";

const source = "/media/anime/Show/extras/orig.mkv";

const animeItem = (id: string, title: string, state: "active" | "stopped" | "retired") =>
  ({ managed_anime: { id, title, version: 1 }, subscription: { id: `sub-${id}`, managed_anime_id: id, origin: "oneshot", state, version: 1 } }) as const;

function detailOf(overrides: Partial<FileManagerEntryDetail> = {}): FileManagerEntryDetail {
  return {
    entry: { name: "orig.mkv", path: source, kind: "file", state: "detached", role: "video", size_bytes: 10, locked: false },
    revision: "rev-1",
    in_progress: [],
    companions: [],
    operations: [
      { operation: "copy", allowed: true },
      { operation: "move", allowed: true },
      { operation: "rename", allowed: true },
      { operation: "match", allowed: true },
      { operation: "delete", allowed: true },
    ],
    match_default_anime: { managed_anime_id: "anime-1", title: "Show A", ended: false, subscription_state: "active" },
    ...overrides,
  };
}

type Call = { url: URL; method: string; headers: Headers; body?: Record<string, unknown> };
let calls: Call[];
let seasonsResponse: () => Response;
let previewResponses: Array<() => Response>;
let matchResponses: Response[];

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const target = "/media/anime/Show/Season 01/Show - E03.mkv";

function previewOf(overrides: Partial<FileManagerMatchPreview> = {}): FileManagerMatchPreview {
  return {
    revision: "rev-2",
    anime: { managed_anime_id: "anime-1", title: "Show A", ended: false },
    media_kind: "tv",
    season: 1,
    episode: 3,
    target_path: target,
    directories_to_create: ["/media/anime/Show/Season 01"],
    blockers: [],
    subtitles_left_behind: [],
    from_lineage: true,
    ...overrides,
  };
}

const accepted: FileManagerOperationView = {
  id: "op-1",
  operation: "match",
  state: "ready",
  path: source,
  created_at: "2026-10-13T08:00:00Z",
};

beforeEach(() => {
  calls = [];
  previewResponses = [];
  matchResponses = [];
  seasonsResponse = () =>
    json({
      seasons: [
        { season: 0, name: "Specials", episode_count: 2 },
        { season: 1, name: "Season 1", episode_count: 12 },
      ],
    });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, method, headers: new Headers(init?.headers), body });
      if (url.pathname === "/api/v1/anime") {
        return json([animeItem("anime-1", "Show A", "active"), animeItem("anime-2", "Show B", "stopped"), animeItem("anime-3", "Gone", "retired")]);
      }
      if (url.pathname.endsWith("/tmdb/seasons")) return seasonsResponse();
      if (url.pathname.includes("/tmdb/seasons/") && url.pathname.endsWith("/episodes")) {
        return json({
          season: 1,
          episodes: [
            { episode: 1, title: "One", air_date: "2024-01-07" },
            { episode: 3, title: "Three", air_date: "2024-01-21" },
          ],
        });
      }
      if (url.pathname === "/api/v1/file-manager/entry/match/preview") return (previewResponses.shift() ?? (() => json(previewOf())))();
      if (url.pathname === "/api/v1/file-manager/entry/match") return matchResponses.shift() ?? json(accepted, 202);
      return json({ code: "NOT_FOUND", message: "missing" }, 404);
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

const previews = () => calls.filter((call) => call.url.pathname === "/api/v1/file-manager/entry/match/preview");
const submits = () => calls.filter((call) => call.method === "POST" && call.url.pathname === "/api/v1/file-manager/entry/match");

async function openMatch() {
  fireEvent.click(screen.getByRole("button", { name: t("fileManager.operation.match") }));
  return screen.findByRole("alertdialog");
}

describe("match button", () => {
  it("follows the operations list and explains a refused match", async () => {
    renderDetail(detailOf({ operations: [{ operation: "match", allowed: false, blocked_reason: "managed" }] }));
    const button = screen.getByRole("button", { name: t("fileManager.operation.match") });
    expect(button).toBeDisabled();
    await userEvent.hover(button.parentElement!);
    expect(await screen.findByText(t("fileManager.operationBlock.managed"))).toBeInTheDocument();
  });

  it("is absent for a managed file", () => {
    renderDetail(detailOf({ entry: { name: "ep.mkv", path: "/media/anime/Show/ep.mkv", kind: "file", state: "managed", role: "video", locked: true }, operations: [{ operation: "align", allowed: true }] }));
    expect(screen.queryByRole("button", { name: t("fileManager.operation.match") })).not.toBeInTheDocument();
  });
});

describe("match dialog", () => {
  it("starts from the file's own anime, picks the season and episode from TMDB and previews without writing", async () => {
    renderDetail(detailOf());
    const dialog = await openMatch();
    // The default anime is chosen and its seasons are read (season 0 included).
    await waitFor(() => expect(calls.some((call) => call.url.pathname === "/api/v1/anime/anime-1/tmdb/seasons")).toBe(true));
    expect(within(dialog).getByRole("combobox", { name: t("fileManager.match.anime") })).toHaveTextContent("Show A");

    await userEvent.click(within(dialog).getByRole("combobox", { name: t("fileManager.match.season") }));
    expect(await screen.findByRole("option", { name: t("fileManager.match.specialsOption", { n: 0, count: 2 }) })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("option", { name: t("fileManager.match.seasonOption", { n: 1, count: 12 }) }));

    await userEvent.click(await within(dialog).findByRole("combobox", { name: t("fileManager.match.episode") }));
    await userEvent.click(await screen.findByRole("option", { name: /Three/ }));

    expect(await within(dialog).findByText(target)).toBeInTheDocument();
    expect(within(dialog).getByText("/media/anime/Show/Season 01")).toBeInTheDocument();
    expect(previews().at(-1)?.body).toEqual({ path: source, managed_anime_id: "anime-1", season: 1, episode: 3 });
    expect(submits()).toHaveLength(0);
  });

  it("falls back to manual season and episode when the anime has no TMDB binding", async () => {
    seasonsResponse = () => json({ code: "TMDB_BINDING_REQUIRED", message: "bind" }, 409);
    renderDetail(detailOf());
    const dialog = await openMatch();
    expect(await within(dialog).findByText(t("fileManager.match.tmdbRequired"))).toBeInTheDocument();

    fireEvent.change(within(dialog).getByRole("textbox", { name: t("fileManager.match.season") }), { target: { value: "0" } });
    fireEvent.change(within(dialog).getByRole("textbox", { name: t("fileManager.match.episode") }), { target: { value: "5" } });
    expect(await within(dialog).findByText(target)).toBeInTheDocument();
    expect(previews().at(-1)?.body).toMatchObject({ season: 0, episode: 5 });

    // Out-of-range input previews nothing.
    const before = previews().length;
    fireEvent.change(within(dialog).getByRole("textbox", { name: t("fileManager.match.episode") }), { target: { value: "0" } });
    expect(within(dialog).getByText(t("fileManager.match.episodeInvalid"))).toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 400));
    expect(previews()).toHaveLength(before);
    expect(within(dialog).getByRole("button", { name: t("fileManager.match.submit") })).toBeDisabled();
  });

  it("submits with the previewed revision and target and a fresh idempotency key", async () => {
    seasonsResponse = () => json({ code: "TMDB_BINDING_REQUIRED", message: "bind" }, 409);
    renderDetail(detailOf());
    const dialog = await openMatch();
    fireEvent.change(await within(dialog).findByRole("textbox", { name: t("fileManager.match.season") }), { target: { value: "1" } });
    fireEvent.change(within(dialog).getByRole("textbox", { name: t("fileManager.match.episode") }), { target: { value: "3" } });
    await within(dialog).findByText(target);
    const submit = within(dialog).getByRole("button", { name: t("fileManager.match.submit") });
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.click(submit);
    await waitFor(() => expect(submits()).toHaveLength(1));
    expect(submits()[0].body).toEqual({
      path: source,
      expected_revision: "rev-2",
      managed_anime_id: "anime-1",
      season: 1,
      episode: 3,
      expected_target: target,
      conflict: "block",
    });
    expect(submits()[0].headers.get("Idempotency-Key")).toMatch(/^.{8,128}$/);
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("offers cancel, pick another episode and add suffix for an occupied target, never overwrite", async () => {
    seasonsResponse = () => json({ code: "TMDB_BINDING_REQUIRED", message: "bind" }, 409);
    previewResponses.push(() => json(previewOf({ conflict: { occupied_by: "file", version_path: "/media/anime/Show/Season 01/Show - E03 (2).mkv" } })));
    renderDetail(detailOf());
    const dialog = await openMatch();
    fireEvent.change(await within(dialog).findByRole("textbox", { name: t("fileManager.match.season") }), { target: { value: "1" } });
    fireEvent.change(within(dialog).getByRole("textbox", { name: t("fileManager.match.episode") }), { target: { value: "3" } });

    expect(await within(dialog).findByText(t("fileManager.match.conflictTitle"))).toBeInTheDocument();
    expect(within(dialog).getByText(/\/media\/anime\/Show\/Season 01\/Show - E03 \(2\)\.mkv/)).toBeInTheDocument();
    const names = within(dialog)
      .getAllByRole("button")
      .map((button) => button.textContent);
    expect(names).toEqual(expect.arrayContaining([t("fileManager.conflict.cancel"), t("fileManager.match.changeEpisode"), t("fileManager.match.submitVersion")]));
    expect(names.join("|")).not.toMatch(/overwrite|覆盖/i);

    fireEvent.click(within(dialog).getByRole("button", { name: t("fileManager.match.submitVersion") }));
    await waitFor(() => expect(submits()).toHaveLength(1));
    expect(submits()[0].body).toMatchObject({ conflict: "version", expected_target: target });
  });

  it("shows blockers and cannot be submitted", async () => {
    seasonsResponse = () => json({ code: "TMDB_BINDING_REQUIRED", message: "bind" }, 409);
    previewResponses.push(() =>
      json(previewOf({ target_path: undefined, directories_to_create: [], blockers: [{ code: "facts_missing", facts: ["media.resolution"] }] })),
    );
    renderDetail(detailOf());
    const dialog = await openMatch();
    fireEvent.change(await within(dialog).findByRole("textbox", { name: t("fileManager.match.season") }), { target: { value: "1" } });
    fireEvent.change(within(dialog).getByRole("textbox", { name: t("fileManager.match.episode") }), { target: { value: "3" } });
    // The missing fact is named in plain words, not by its catalog path.
    expect(await within(dialog).findByText(t("fileManager.match.blocker.facts_missing", { facts: t("facts.media.resolution.label") }))).toBeInTheDocument();
    expect(within(dialog).queryByText(/file\.resolution/)).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t("fileManager.match.submit") })).toBeDisabled();
  });

  it("lists the subtitles that stay behind and the folders to create", async () => {
    seasonsResponse = () => json({ code: "TMDB_BINDING_REQUIRED", message: "bind" }, 409);
    previewResponses.push(() => json(previewOf({ subtitles_left_behind: ["/media/anime/Show/extras/orig.zh.ass"], from_lineage: false })));
    renderDetail(detailOf());
    const dialog = await openMatch();
    fireEvent.change(await within(dialog).findByRole("textbox", { name: t("fileManager.match.season") }), { target: { value: "1" } });
    fireEvent.change(within(dialog).getByRole("textbox", { name: t("fileManager.match.episode") }), { target: { value: "3" } });
    expect(await within(dialog).findByText(t("fileManager.match.subtitlesStay"))).toBeInTheDocument();
    expect(within(dialog).getByText("/media/anime/Show/extras/orig.zh.ass")).toBeInTheDocument();
    expect(within(dialog).getByText(t("fileManager.match.createFolders"))).toBeInTheDocument();
    expect(within(dialog).getByText(t("fileManager.match.withoutFile"))).toBeInTheDocument();
  });

  it("reads the preview again when the server says the target changed", async () => {
    seasonsResponse = () => json({ code: "TMDB_BINDING_REQUIRED", message: "bind" }, 409);
    matchResponses.push(json({ code: "FILE_MANAGER_MATCH_TARGET_CHANGED", message: "changed" }, 409));
    renderDetail(detailOf());
    const dialog = await openMatch();
    fireEvent.change(await within(dialog).findByRole("textbox", { name: t("fileManager.match.season") }), { target: { value: "1" } });
    fireEvent.change(within(dialog).getByRole("textbox", { name: t("fileManager.match.episode") }), { target: { value: "3" } });
    await within(dialog).findByText(target);
    const submit = within(dialog).getByRole("button", { name: t("fileManager.match.submit") });
    await waitFor(() => expect(submit).toBeEnabled());
    const before = previews().length;
    fireEvent.click(submit);
    expect(await within(dialog).findByText(t("error.FILE_MANAGER_MATCH_TARGET_CHANGED"))).toBeInTheDocument();
    await waitFor(() => expect(previews().length).toBeGreaterThan(before));
  });

  it("chooses another anime from the subscriptions that still take files", async () => {
    renderDetail(detailOf({ match_default_anime: undefined }));
    const dialog = await openMatch();
    await userEvent.click(within(dialog).getByRole("combobox", { name: t("fileManager.match.anime") }));
    expect(await screen.findByRole("option", { name: "Show A" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Show B" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Gone" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("option", { name: "Show B" }));
    await waitFor(() => expect(calls.some((call) => call.url.pathname === "/api/v1/anime/anime-2/tmdb/seasons")).toBe(true));
  });
});

describe("match deep link", () => {
  function renderLinked(detail: FileManagerEntryDetail, onClosed = vi.fn()) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <EntryDetailView detail={detail} operation="match" onOperationDialogClosed={onClosed} onOpenAnime={vi.fn()} onOpenRecord={vi.fn()} onSelect={vi.fn()} />
      </QueryClientProvider>,
    );
    return onClosed;
  }

  it("opens the match dialog of the linked file and reports its close", async () => {
    const onClosed = renderLinked(detailOf());
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("act.cancel") }));
    await waitFor(() => expect(onClosed).toHaveBeenCalled());
  });

  it("ignores the link when matching is not allowed", () => {
    renderLinked(detailOf({ operations: [{ operation: "match", allowed: false, blocked_reason: "managed" }] }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });
});
