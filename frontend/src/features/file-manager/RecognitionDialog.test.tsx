import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  FileManagerRecognitionPreview,
  FileManagerRecognitionResult,
  FileManagerRecognitionRow,
  NamingTemplateLibraryView,
  ParserCatalogEntry,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { toast } from "../../ui/toast";
import { RecognitionDialog } from "./RecognitionDialog";

const library: NamingTemplateLibraryView = {
  default_key: "standard",
  version: 3,
  templates: [
    {
      key: "standard",
      name: "标准",
      rules: {
        language: "js-naming-1",
        unknown: { directory_template: "Extras", video_filename_template: "${file.stem}" },
        tv: { directory_template: "${anime.title}", video_filename_template: "${file.stem}" },
        movie: { directory_template: "${anime.title}", video_filename_template: "${file.stem}" },
        subtitle_suffix_template: "",
      },
    },
  ],
};

const parsers: ParserCatalogEntry[] = [
  { id: "reltitle", display_name: "reltitle", implementation_version: "1", output_contract_version: "1", capabilities: ["file_name"], config_version: "1" },
];

const emptyRuntimeFacts = { media_probe: [], episode_titles: [] };

const renamed: FileManagerRecognitionRow[] = [
  {
    path: "/media/anime/ep01.mkv",
    name: "ep01.mkv",
    kind: "video",
    group: "ep01",
    revision: "rev-1",
    size_bytes: 1024 * 1024 * 250,
    has_placeholder: false,
    episode: { number: "1", offset: 0, source: "parsed" },
    target_path: "/media/anime/Show - 01.mkv",
    target_name: "Show - 01.mkv",
    disposition: "rename",
  },
  {
    path: "/media/anime/ep01.zh.ass",
    name: "ep01.zh.ass",
    kind: "subtitle",
    group: "ep01",
    revision: "rev-2",
    size_bytes: 4096,
    has_placeholder: false,
    target_path: "/media/anime/Show - 01.zh.ass",
    target_name: "Show - 01.zh.ass",
    disposition: "rename",
  },
];

const result: FileManagerRecognitionResult = { renamed: 2, moved: 0, operations: [] };

let previewBody: FileManagerRecognitionPreview;
let previewHandler: ((request: Record<string, unknown>) => Response | null) | null;
let submitStatus: number;
let submitBody: unknown;
let fetchMock: ReturnType<typeof vi.fn>;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function posts(pathname: string): Record<string, unknown>[] {
  return fetchMock.mock.calls
    .filter(([input]) => new URL(String(input), "http://localhost").pathname === pathname)
    .map(([, init]) => JSON.parse(String(init?.body)) as Record<string, unknown>);
}

beforeEach(() => {
  submitStatus = 202;
  submitBody = result;
  previewHandler = null;
  previewBody = {
    directory: "/media/anime",
    rows: renamed,
    unknown_route: "skip",
    plan_digest: "r2.ready.digest-1",
    conflicts: [],
    title_hint: "测试番剧",
    runtime_facts: emptyRuntimeFacts,
  };
  vi.spyOn(toast, "success").mockImplementation(() => "");
  vi.spyOn(toast, "warning").mockImplementation(() => "");
  vi.spyOn(toast, "error").mockImplementation(() => "");
  vi.spyOn(toast, "info").mockImplementation(() => "");
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/v1/config/naming-templates") return json(library);
    if (url.pathname === "/api/v1/template-catalogs/naming") {
      return json({ language: "js-naming-1", fields: [], functions: [], shorthands: [], samples: [] });
    }
    if (url.pathname === "/api/v1/parser-catalog") return json(parsers);
    if (url.pathname === "/api/v1/file-manager/recognition/preview" && init?.method === "POST") {
      const request = JSON.parse(String(init.body)) as Record<string, unknown>;
      if (previewHandler) {
        const custom = previewHandler(request);
        if (custom) return custom;
      }
      if (Array.isArray(request.overrides) && request.overrides.length > 0) {
        return json({
          ...previewBody,
          rows: [{ ...renamed[0], episode: { raw: "1", number: "8", offset: 0, source: "manual" } }, renamed[1]],
        });
      }
      return json(previewBody);
    }
    if (url.pathname === "/api/v1/file-manager/recognition" && init?.method === "POST") {
      if (submitStatus !== 202) return json({ code: "FILE_MANAGER_RECOGNITION_PLAN_CHANGED", message: "changed" }, submitStatus);
      return json(submitBody, 202);
    }
    return json({ code: "NOT_FOUND", message: "missing" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function renderDialog(
  paths = ["/media/anime/ep01.mkv", "/media/anime/ep01.zh.ass"],
  singleVideo = true,
  onFinished = vi.fn(),
  onOpenConnectionSettings?: (provider: "bgm" | "tmdb") => void,
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RecognitionDialog
        paths={paths}
        singleVideo={singleVideo}
        onOpenConnectionSettings={onOpenConnectionSettings}
        onClose={() => {}}
        onFinished={onFinished}
      />
    </QueryClientProvider>,
  );
  return onFinished;
}

describe("RecognitionDialog", () => {
  it("previews with concise filenames and sizes in the unified card, then submits the confirmed digest", async () => {
    const onFinished = renderDialog();
    expect(await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 })).toBeInTheDocument();
    // Source line shows basename + size instead of repeating the folder prefix.
    const sourceLine = screen.getByTitle("/media/anime/ep01.mkv");
    expect(sourceLine).toHaveTextContent("ep01.mkv");
    expect(sourceLine).toHaveTextContent("250.0 MB");

    // Bound subtitle renders as a format badge and expands with its name + size.
    const videoGroup = sourceLine.closest("li")!;
    const badges = screen.getByRole("list", { name: t("ui.subtitles.formats", { n: 1 }) });
    expect(videoGroup).toContainElement(badges);
    expect(within(badges).getByText("ASS")).toBeInTheDocument();
    expect(screen.queryByText("ep01.zh.ass")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("ui.subtitles.expand", { name: "ep01.mkv", n: 1 }) }));
    expect(screen.getByTitle("ep01.zh.ass")).toHaveTextContent("ep01.zh.ass");
    expect(screen.getByTitle("ep01.zh.ass")).toHaveTextContent("4.0 KB");
    expect(screen.getByText("→ /media/anime/Show - 01.zh.ass")).toBeInTheDocument();

    const [request] = posts("/api/v1/file-manager/recognition/preview");
    expect(request).toMatchObject({
      paths: ["/media/anime/ep01.mkv", "/media/anime/ep01.zh.ass"],
      parser: { id: "reltitle" },
      naming: library.templates[0].rules,
    });
    expect(request).not.toHaveProperty("load_runtime_facts");
    expect(request).not.toHaveProperty("refresh_media");

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.recognize.apply") }));
    expect(posts("/api/v1/file-manager/recognition")[0]).toMatchObject({
      expected_plan_digest: "r2.ready.digest-1",
      conflict: "block",
    });
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith(result, 0));
  });

  it("previews and submits sparse per-file numbering overrides", async () => {
    renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });
    fireEvent.click(screen.getByRole("button", { name: t("fileNumbering.title") }));
    const editor = await screen.findByRole("dialog", { name: t("fileNumbering.title") });
    fireEvent.change(within(editor).getByRole("textbox", { name: `${t("fileNumbering.episode")} /media/anime/ep01.mkv` }), {
      target: { value: "8" },
    });
    fireEvent.click(within(editor).getByRole("button", { name: t("fileNumbering.confirm", { n: 1 }) }));

    const overrides = [{ path: "/media/anime/ep01.mkv", episode: 8 }];
    await waitFor(() => expect(posts("/api/v1/file-manager/recognition/preview").at(-1)).toMatchObject({ overrides }), { timeout: 3000 });

    fireEvent.click(screen.getByRole("button", { name: t("fileNumbering.title") }));
    const reopened = await screen.findByRole("dialog", { name: t("fileNumbering.title") });
    const episodeInput = () => within(reopened).getByRole("textbox", { name: `${t("fileNumbering.episode")} /media/anime/ep01.mkv` });
    expect(episodeInput()).toHaveValue("8");
    fireEvent.click(within(reopened).getByRole("checkbox", { name: t("fileNumbering.checkRow", { path: "/media/anime/ep01.mkv" }) }));
    fireEvent.click(within(reopened).getByRole("button", { name: t("fileNumbering.restoreChecked") }));
    expect(episodeInput()).toHaveValue("1");
    fireEvent.click(within(reopened).getByRole("button", { name: t("act.cancel") }));

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.recognize.apply") }));
    expect(posts("/api/v1/file-manager/recognition")[0]).toMatchObject({ expected_plan_digest: "r2.ready.digest-1", overrides });
  });

  it("shows concise conflict text without verbose version paths and settles conflicts via dialog", async () => {
    previewBody = {
      ...previewBody,
      rows: [
        {
          ...renamed[0],
          conflict: { occupied_by: "file", version_path: "/media/anime/Show - 01 (2).mkv" },
        },
        renamed[1],
      ],
      conflicts: ["/media/anime/Show - 01.mkv"],
    };
    const onFinished = renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });
    expect(screen.getByText(t("fileManager.recognize.preview.conflict.file"))).toBeInTheDocument();
    expect(screen.queryByText(/Show - 01 \(2\)\.mkv/)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.recognize.apply") }));
    const choice = await screen.findByRole("alertdialog", { name: t("fileManager.conflictChoice.title") });
    expect(within(choice).getByText(t("fileManager.conflictChoice.overwriteWarning"))).toBeInTheDocument();
    fireEvent.click(within(choice).getByRole("button", { name: t("fileManager.conflictChoice.skip") }));
    expect(posts("/api/v1/file-manager/recognition")[0]).toMatchObject({ conflict: "skip" });
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith(result, 2));
  });

  it("never offers replace for a managed or in-progress target", async () => {
    previewBody = {
      ...previewBody,
      rows: [{ ...renamed[0], conflict: { occupied_by: "managed" } }, renamed[1]],
      conflicts: ["/media/anime/Show - 01.mkv"],
    };
    renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.recognize.apply") }));
    const choice = await screen.findByRole("alertdialog", { name: t("fileManager.conflictChoice.title") });
    expect(within(choice).queryByRole("button", { name: t("fileManager.conflictChoice.overwrite") })).not.toBeInTheDocument();
  });

  it("names with the typed title draft and offers the parser's candidate as a badge", async () => {
    renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });

    const [first] = posts("/api/v1/file-manager/recognition/preview");
    expect(first).not.toHaveProperty("match");

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /测试番剧/ }));
    const title = screen.getByRole("textbox", { name: t("field.title") });
    expect(title).toHaveValue("测试番剧");

    await waitFor(
      () => {
        const requests = posts("/api/v1/file-manager/recognition/preview") as {
          match?: { anime?: { title?: string } };
        }[];
        expect(requests.at(-1)?.match?.anime?.title).toBe("测试番剧");
      },
      { timeout: 3000 },
    );
  });

  it("keeps both file actions (numbering + permanent reload) in the toolbar above the rows", async () => {
    renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });
    const toolbar = screen.getByRole("toolbar", { name: t("fileManager.recognize.actions") });
    expect(within(toolbar).getByRole("button", { name: t("fileNumbering.title") })).toBeInTheDocument();
    expect(within(toolbar).getByRole("button", { name: t("fileManager.recognize.reload") })).toBeInTheDocument();
    const panel = screen.getByRole("region", { name: t("fileManager.recognize.configAria") });
    expect(within(panel).queryByRole("button", { name: t("fileManager.recognize.reload") })).not.toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: t("fileNumbering.title") })).not.toBeInTheDocument();
  });

  it("shows rose error-fallback placeholders, toasts once, suppresses failed keys on edits, and clears suppression on full reload", async () => {
    const openConnections = vi.fn();
    let recovered = false;
    previewHandler = (req) => {
      if (req.refresh_media === true) recovered = true;
      if (recovered) {
        return json({
          ...previewBody,
          plan_digest: "r2.ready.digest-2",
          rows: [
            {
              ...renamed[0],
              target_path: "/media/anime/Show S01E01 - 冒险的终点.mkv",
              target_name: "Show S01E01 - 冒险的终点.mkv",
              has_placeholder: false,
              disposition: "rename",
            },
            renamed[1],
          ],
          runtime_facts: {
            media_probe: [],
            episode_titles: [{ id: "t-1", provider: "tmdb", work_id: "209867", season: 1, state: "known", episodes: [1] }],
          },
        });
      }
      const cacheOnly = Array.isArray(req.episode_title_cache_only) && req.episode_title_cache_only.length > 0;
      return json({
        ...previewBody,
        plan_digest: "r2.blocked.digest-1",
        rows: [
          {
            ...renamed[0],
            target_path: "/media/anime/Show S01E01 - ⟨ep_title⟩.mkv",
            target_name: "Show S01E01 - ⟨ep_title⟩.mkv",
            has_placeholder: true,
            disposition: "error",
            blockers: [{ code: "runtime_facts_unavailable", facts: ["tmdb.ep_title"], diagnostic_ids: ["t-1"] }],
          },
          renamed[1],
        ],
        runtime_facts: {
          media_probe: [],
          episode_titles: [
            {
              id: "t-1",
              provider: "tmdb",
              work_id: "209867",
              season: 1,
              state: "unavailable",
              episodes: [1],
              reason: cacheOnly ? "cache_miss" : "unconfigured",
            },
          ],
        },
      });
    };

    renderDialog(undefined, true, vi.fn(), openConnections);

    // Rose placeholder token renders inside the target path and disables Apply.
    const token = await screen.findByText("⟨ep_title⟩", undefined, { timeout: 3000 });
    expect(token).toHaveAttribute("data-placeholder-token", "error");
    expect(screen.getByRole("button", { name: t("fileManager.recognize.apply") })).toBeDisabled();
    expect(toast.warning).toHaveBeenCalledTimes(1);

    // Inline summary link opens TMDB connection settings.
    const configLink = screen.getByRole("button", {
      name: t("fileManager.recognize.runtimeIssue.unconfiguredShort", { provider: "TMDB" }),
    });
    fireEvent.click(configLink);
    expect(openConnections).toHaveBeenCalledWith("tmdb");

    // Editing offset sends episode_title_cache_only for the failed key and does NOT repeat the toast.
    fireEvent.change(screen.getByRole("spinbutton", { name: t("fileManager.recognize.match.offset") }), { target: { value: "1" } });
    await waitFor(
      () => {
        const last = posts("/api/v1/file-manager/recognition/preview").at(-1);
        expect(last?.episode_title_cache_only).toEqual([{ provider: "tmdb", work_id: "209867", season: 1 }]);
      },
      { timeout: 3000 },
    );
    expect(toast.warning).toHaveBeenCalledTimes(1);

    // Clicking permanent Reload sends refresh_media: true, omits episode_title_cache_only, and unlocks Apply.
    const reloadButton = screen.getByRole("button", { name: t("fileManager.recognize.reload") });
    await waitFor(() => expect(reloadButton).toBeEnabled(), { timeout: 3000 });
    fireEvent.click(reloadButton);
    expect(await screen.findByText("/media/anime/Show S01E01 - 冒险的终点.mkv", undefined, { timeout: 3000 })).toBeInTheDocument();
    const reloadReq = posts("/api/v1/file-manager/recognition/preview").at(-1);
    expect(reloadReq?.refresh_media).toBe(true);
    expect(reloadReq).not.toHaveProperty("episode_title_cache_only");
    expect(screen.getByRole("button", { name: t("fileManager.recognize.apply") })).toBeEnabled();
    expect(toast.success).toHaveBeenCalledWith(t("fileManager.recognize.reloaded"));
  });

  it("omits bulky unknown-file alert cards while still showing skip disposition and blocking non-runtime errors", async () => {
    previewBody = {
      ...previewBody,
      plan_digest: "r2.blocked.digest-2",
      rows: [
        {
          path: "/media/anime/extras.mkv",
          name: "extras.mkv",
          kind: "video",
          group: "extras",
          revision: "rev-3",
          size_bytes: 512,
          has_placeholder: false,
          disposition: "error",
          blockers: [{ code: "facts_missing", facts: [] }],
        },
        {
          path: "/media/anime/readme.txt",
          name: "readme.txt",
          kind: "other",
          group: "readme",
          revision: "rev-4",
          size_bytes: 64,
          has_placeholder: false,
          disposition: "skip",
        },
      ],
    };
    renderDialog(["/media/anime/extras.mkv", "/media/anime/readme.txt"]);
    expect(
      await screen.findByText(t("fileManager.recognize.preview.blocker.facts_missing_unnamed"), undefined, { timeout: 3000 }),
    ).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.recognize.preview.disposition.skip"))).toBeInTheDocument();
    // No bulky unknown-file Alert card polluting the preview.
    expect(screen.queryByText(t("fileManager.recognize.unknownSkipText"))).not.toBeInTheDocument();
    const apply = screen.getByRole("button", { name: t("fileManager.recognize.apply") });
    expect(apply).toBeDisabled();
    expect(apply).toHaveAttribute("title", t("fileManager.recognize.applyDisabled.blockers", { n: 1 }));
  });

  it("shows directory heading and disables apply when all files are unchanged", async () => {
    previewBody = {
      ...previewBody,
      rows: [{ ...renamed[0], target_path: "/media/anime/ep01.mkv", target_name: "ep01.mkv", disposition: "unchanged" }],
    };
    renderDialog(["/media/anime/ep01.mkv"]);
    expect(await screen.findByText("/media/anime", undefined, { timeout: 3000 })).toBeInTheDocument();
    const apply = screen.getByRole("button", { name: t("fileManager.recognize.apply") });
    expect(apply).toBeDisabled();
    expect(apply).toHaveAttribute("title", t("fileManager.recognize.applyDisabled.nothing"));
  });
});
