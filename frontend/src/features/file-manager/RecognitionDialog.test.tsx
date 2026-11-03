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
import { RecognitionDialog } from "./RecognitionDialog";

// The recognition window: the browse layer's selected files go in, the server's
// rendered plan comes back, and 应用 submits that exact plan (its digest and
// conflict choice included). Everything here is one operation's own
// configuration — the template library is only read.

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

const renamed: FileManagerRecognitionRow[] = [
  {
    path: "/media/anime/ep01.mkv",
    name: "ep01.mkv",
    kind: "video",
    group: "ep01",
    revision: "rev-1",
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
    target_path: "/media/anime/Show - 01.zh.ass",
    target_name: "Show - 01.zh.ass",
    disposition: "rename",
  },
];

const result: FileManagerRecognitionResult = { renamed: 2, moved: 0, operations: [] };

let previewBody: FileManagerRecognitionPreview;
let submitStatus: number;
let submitBody: unknown;
let fetchMock: ReturnType<typeof vi.fn>;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function posts(pathname: string): unknown[] {
  return fetchMock.mock.calls
    .filter(([input]) => new URL(String(input), "http://localhost").pathname === pathname)
    .map(([, init]) => JSON.parse(String(init?.body)));
}

beforeEach(() => {
  submitStatus = 202;
  submitBody = result;
  // The parser's read of the first file's name rides on the preview as the
  // title candidate the badges offer.
  previewBody = { directory: "/media/anime", rows: renamed, unknown_route: "skip", plan_digest: "digest-1", conflicts: [], title_hint: "测试番剧" };
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/v1/config/naming-templates") return json(library);
    if (url.pathname === "/api/v1/template-catalogs/naming") {
      return json({ language: "js-naming-1", fields: [], functions: [], shorthands: [], samples: [], vars_keys: [], vars_loaded_at: null });
    }
    if (url.pathname === "/api/v1/parser-catalog") return json(parsers);
    if (url.pathname === "/api/v1/file-manager/recognition/preview" && init?.method === "POST") return json(previewBody);
    if (url.pathname === "/api/v1/file-manager/recognition" && init?.method === "POST") {
      if (submitStatus !== 202) return json({ code: "FILE_MANAGER_RECOGNITION_PLAN_CHANGED", message: "changed" }, submitStatus);
      return json(submitBody, 202);
    }
    return json({ code: "NOT_FOUND", message: "missing" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => vi.unstubAllGlobals());

function renderDialog(paths = ["/media/anime/ep01.mkv", "/media/anime/ep01.zh.ass"], singleVideo = true, onFinished = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RecognitionDialog paths={paths} singleVideo={singleVideo} onClose={() => {}} onFinished={onFinished} />
    </QueryClientProvider>,
  );
  return onFinished;
}

describe("RecognitionDialog", () => {
  it("previews the plan the library template seeds, then submits that plan with its digest", async () => {
    const onFinished = renderDialog();
    // The rows arrive after the library seeds the template and the debounce settles.
    expect(await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 })).toBeInTheDocument();
    expect(screen.getByText("/media/anime/ep01.mkv")).toBeInTheDocument();
    // The plan is grouped like the one-shot preview: the subtitle is a badge on
    // its video's row and expands beneath it, not a separate rename beside it.
    const videoGroup = screen.getByText("/media/anime/ep01.mkv").closest("li")!;
    const badges = screen.getByRole("list", { name: t("ui.subtitles.formats", { n: 1 }) });
    expect(videoGroup).toContainElement(badges);
    expect(within(badges).getByText("ASS")).toBeInTheDocument();
    // Collapsed by default: the subtitle's own source and target only follow an expand.
    expect(screen.queryByText("/media/anime/ep01.zh.ass")).not.toBeInTheDocument();
    expect(screen.queryByText("/media/anime/Show - 01.zh.ass")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("ui.subtitles.expand", { name: "/media/anime/ep01.mkv", n: 1 }) }));
    expect(screen.getByText("/media/anime/ep01.zh.ass")).toBeInTheDocument();
    expect(screen.getByText("→ /media/anime/Show - 01.zh.ass")).toBeInTheDocument();
    // The recognition preview carries no language facts, so it shows no language line.
    expect(screen.queryByText(t("ui.subtitles.noLanguage"))).not.toBeInTheDocument();
    expect(screen.getByText(t("fileManager.recognize.willProcess", { n: 2 }))).toBeInTheDocument();

    const [request] = posts("/api/v1/file-manager/recognition/preview") as Record<string, unknown>[];
    expect(request).toMatchObject({
      paths: ["/media/anime/ep01.mkv", "/media/anime/ep01.zh.ass"],
      parser: { id: "reltitle" },
      // The operation renders the same complete naming rules one-shot renders.
      naming: library.templates[0].rules,
    });
    expect(request).not.toHaveProperty("video_template");
    expect(request).not.toHaveProperty("subtitle_template");

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.recognize.apply") }));
    expect(posts("/api/v1/file-manager/recognition")[0]).toMatchObject({
      expected_plan_digest: "digest-1",
      conflict: "block",
    });
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith(result, 0));
  });

  it("asks how to settle occupied targets and submits the skip choice", async () => {
    previewBody = { ...previewBody, conflicts: ["/media/anime/Show - 01.mkv"] };
    const onFinished = renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.recognize.apply") }));
    const choice = await screen.findByRole("alertdialog", { name: t("fileManager.conflictChoice.title") });
    expect(within(choice).getByText("/media/anime/Show - 01.mkv")).toBeInTheDocument();

    // Nothing is submitted until the choice is made: cancel leaves the plan alone.
    expect(posts("/api/v1/file-manager/recognition")).toHaveLength(0);
    fireEvent.click(within(choice).getByRole("button", { name: t("fileManager.conflictChoice.skip") }));
    expect(posts("/api/v1/file-manager/recognition")[0]).toMatchObject({ conflict: "skip" });
    // The skipped video's subtitle follows it, so both files are reported back for the toast.
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith(result, 2));
  });

  it("offers replace for a plain file at an occupied target", async () => {
    previewBody = {
      ...previewBody,
      rows: [{ ...renamed[0], conflict: { occupied_by: "file" } }, renamed[1]],
      conflicts: ["/media/anime/Show - 01.mkv"],
    };
    const onFinished = renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });

    fireEvent.click(screen.getByRole("button", { name: t("fileManager.recognize.apply") }));
    const choice = await screen.findByRole("alertdialog", { name: t("fileManager.conflictChoice.title") });
    expect(within(choice).getByText(t("fileManager.conflictChoice.overwriteWarning"))).toBeInTheDocument();

    fireEvent.click(within(choice).getByRole("button", { name: t("fileManager.conflictChoice.overwrite") }));
    expect(posts("/api/v1/file-manager/recognition")[0]).toMatchObject({ conflict: "overwrite" });
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith(result, 0));
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
    expect(within(choice).queryByText(t("fileManager.conflictChoice.overwriteWarning"))).not.toBeInTheDocument();
  });

  it("names with the typed title draft and offers the parser's candidate as a badge", async () => {
    renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });

    // No match while the title is empty: the render is a pure parse of the names.
    const [first] = posts("/api/v1/file-manager/recognition/preview") as Record<string, unknown>[];
    expect(first).not.toHaveProperty("match");
    // Total episodes play no part in a rename, so the row is not offered here.
    expect(screen.queryByText(t("forms.totalEpisodes"))).not.toBeInTheDocument();

    // The parser's read of the first file's name is offered like one-shot's work names.
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /测试番剧/ }));
    const title = screen.getByRole("textbox", { name: t("field.title") });
    expect(title).toHaveValue("测试番剧");

    await waitFor(
      () => {
        const requests = posts("/api/v1/file-manager/recognition/preview") as {
          match?: { anime?: { title?: string }; managed_anime_id?: string };
        }[];
        expect(requests.at(-1)?.match?.anime?.title).toBe("测试番剧");
        expect(requests.at(-1)?.match?.managed_anime_id).toBeUndefined();
      },
      { timeout: 3000 },
    );

    // An edited title is the draft's, and the next render reads it.
    fireEvent.change(title, { target: { value: "作品乙" } });
    await waitFor(
      () => {
        const requests = posts("/api/v1/file-manager/recognition/preview") as { match?: { anime?: { title?: string } } }[];
        expect(requests.at(-1)?.match?.anime?.title).toBe("作品乙");
      },
      { timeout: 3000 },
    );
  });

  it("sends a bare episode adjustment without any title", async () => {
    renderDialog();
    await screen.findByText("/media/anime/Show - 01.mkv", undefined, { timeout: 3000 });

    // Episode and offset are plain fields of the settings column, no match needed.
    fireEvent.change(screen.getByRole("spinbutton", { name: t("fileManager.recognize.match.episode") }), { target: { value: "3" } });
    fireEvent.change(screen.getByRole("spinbutton", { name: t("fileManager.recognize.match.offset") }), { target: { value: "1" } });
    await waitFor(
      () => {
        const requests = posts("/api/v1/file-manager/recognition/preview") as {
          match?: { episode?: number; offset?: number; anime?: unknown };
        }[];
        expect(requests.at(-1)?.match).toMatchObject({ episode: 3, offset: 1 });
        expect(requests.at(-1)?.match?.anime).toBeUndefined();
      },
      { timeout: 3000 },
    );
  });

  it("shows blockers and the unknown-file policy without allowing a submit", async () => {
    previewBody = {
      ...previewBody,
      rows: [
        {
          path: "/media/anime/extras.mkv",
          name: "extras.mkv",
          kind: "video",
          group: "extras",
          revision: "rev-3",
          disposition: "error",
          blockers: [{ code: "facts_missing", facts: [] }],
        },
        { path: "/media/anime/readme.txt", name: "readme.txt", kind: "other", group: "readme", revision: "rev-4", disposition: "skip" },
      ],
    };
    renderDialog(["/media/anime/extras.mkv", "/media/anime/readme.txt"]);
    expect(
      await screen.findByText(t("fileManager.recognize.preview.blocker.facts_missing_unnamed"), undefined, { timeout: 3000 }),
    ).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.recognize.unknownNote", { n: 1 }))).toBeInTheDocument();
    expect(screen.getByText(t("fileManager.recognize.unknownSkipText"))).toBeInTheDocument();
    // A blocked 应用 names why it cannot run instead of disabling silently.
    const apply = screen.getByRole("button", { name: t("fileManager.recognize.apply") });
    expect(apply).toBeDisabled();
    expect(apply).toHaveAttribute("title", t("fileManager.recognize.applyDisabled.blockers", { n: 1 }));
  });

  it("names an empty plan as the reason nothing can be applied, and shows the selected folder", async () => {
    previewBody = {
      ...previewBody,
      rows: [{ ...renamed[0], target_path: "/media/anime/ep01.mkv", target_name: "ep01.mkv", disposition: "unchanged" }],
    };
    renderDialog(["/media/anime/ep01.mkv"]);
    expect(await screen.findByText(t("fileManager.recognize.preview.folder", { path: "/media/anime" }), undefined, { timeout: 3000 })).toBeInTheDocument();
    const apply = screen.getByRole("button", { name: t("fileManager.recognize.apply") });
    expect(apply).toBeDisabled();
    expect(apply).toHaveAttribute("title", t("fileManager.recognize.applyDisabled.nothing"));
  });
});
