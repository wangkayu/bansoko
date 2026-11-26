import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import { apiMetadataEpisodeLoader, resetEpisodeCache } from "../../lib/metadata/episodeLoader";
import type { MetadataCandidate, MetadataEpisode, MetadataEpisodeLoader } from "../../lib/metadata/types";
import { resetAuthedImageCache } from "../../lib/useAuthedImage";
import { MetadataEpisodeDialog } from "./MetadataEpisodeDialog";

const candidate: MetadataCandidate = { provider: "tmdb", workId: "265010", title: "剧集", detailsStatus: "available" };
const stillUrl = "/api/v1/images/cover/" + "ab".repeat(32);

function list() {
  return screen.getByRole("dialog", { name: t("forms.episodeListTitle") });
}

async function chooseLanguage(user: ReturnType<typeof userEvent.setup>, language: string) {
  await user.click(within(list()).getByRole("combobox", { name: t("forms.episodeLanguage") }));
  await user.click(await screen.findByRole("option", { name: language }));
}

describe("episode browsing", () => {
  afterEach(() => {
    resetAuthedImageCache();
    resetEpisodeCache();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("fetches a still only on opening the nested preview, using the selected language's overview", async () => {
    const user = userEvent.setup();
    const closeList = vi.fn();
    vi.stubGlobal("URL", class extends URL {
      static createObjectURL = () => "blob:episode-still";
      static revokeObjectURL = () => {};
    });
    let finishStill!: (response: Response) => void;
    const stillResponse = new Promise<Response>((resolve) => { finishStill = resolve; });
    const fetchMock = vi.fn(async (path: string) => {
      if (path === stillUrl) return stillResponse;
      const japanese = path.endsWith("?language=ja-JP");
      return Response.json({ season: 1, episodes: [
        { episode: 1, title: japanese ? "出発" : "出发", air_date: "2025-01-03", runtime_minutes: 24, still_url: stillUrl,
          overview: japanese ? "旅が始まる。" : "旅途开始。" },
        { episode: 2, title: "Notes", overview: "Notes only." },
        { episode: 3, title: "No details" },
      ] });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<MetadataEpisodeDialog candidate={candidate} loader={apiMetadataEpisodeLoader} onClose={closeList} />);

    expect(await within(list()).findByText("出发")).toBeInTheDocument();
    expect(within(list()).getByText("24m")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toMatch(/\?language=zh-CN$/);
    await chooseLanguage(user, "日本語");
    expect(await within(list()).findByText("出発")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1]?.[0]).toMatch(/\?language=ja-JP$/);
    expect(within(list()).queryByRole("img")).not.toBeInTheDocument();
    expect(within(list()).queryByRole("button", { name: t("forms.episodePreviewAction", { n: 3 }) })).not.toBeInTheDocument();

    const opener = within(list()).getByRole("button", { name: t("forms.episodePreviewAction", { n: 1 }) });
    await user.click(opener);
    const preview = within(await screen.findByRole("dialog", { name: /出発/ }));
    expect(preview.getByText("旅が始まる。")).toHaveAttribute("lang", "ja-JP");
    expect(preview.getByRole("status")).toHaveTextContent(t("forms.episodeStillLoading"));
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[2]?.[0]).toBe(stillUrl);
    finishStill(new Response("png", { headers: { "Content-Type": "image/png" } }));
    expect(await preview.findByRole("img")).toHaveAttribute("src", "blob:episode-still");
    fireEvent.error(preview.getByRole("img"));
    expect(preview.getByRole("status")).toHaveTextContent(t("forms.episodeStillFailed"));
    expect(preview.getByText("旅が始まる。")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /出発/ })).not.toBeInTheDocument());
    expect(list()).toBeInTheDocument();
    await waitFor(() => expect(opener).toHaveFocus());
    expect(closeList).not.toHaveBeenCalled();

    await user.click(within(list()).getByRole("button", { name: t("forms.episodePreviewAction", { n: 2 }) }));
    const notes = within(await screen.findByRole("dialog", { name: /Notes/ }));
    expect(notes.getByText("Notes only.")).toBeInTheDocument();
    expect(notes.queryByRole("img")).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(3);
    await user.click(notes.getByRole("button", { name: t("ui.close") }));
    expect(await screen.findByRole("dialog", { name: t("forms.episodeListTitle") })).toBeInTheDocument();
    expect(closeList).not.toHaveBeenCalled();
  });

  it("aborts obsolete language requests and ignores a late result even if the loader returns it", async () => {
    const user = userEvent.setup();
    let finishJapanese!: (episodes: MetadataEpisode[]) => void;
    const japanese = new Promise<MetadataEpisode[]>((resolve) => { finishJapanese = resolve; });
    const loader = vi.fn<MetadataEpisodeLoader>(async (_work, _season, options) => {
      if (options?.language === "ja-JP") return japanese;
      return [{ episode: 1, title: options?.language === "en-US" ? "Departure" : "出发" }];
    });
    render(<MetadataEpisodeDialog candidate={candidate} loader={loader} onClose={vi.fn()} />);
    await within(list()).findByText("出发");
    await chooseLanguage(user, "日本語");
    await chooseLanguage(user, "English");
    expect(await within(list()).findByText("Departure")).toBeInTheDocument();
    expect(loader.mock.calls.map((call) => call[2]?.language)).toEqual(["zh-CN", "ja-JP", "en-US"]);
    expect(loader.mock.calls[1]?.[2]?.signal?.aborted).toBe(true);
    await act(async () => finishJapanese([{ episode: 1, title: "出発" }]));
    expect(within(list()).queryByText("出発")).not.toBeInTheDocument();
    expect(within(list()).getByText("Departure")).toBeInTheDocument();
  });

  it("displays both BGM titles when different and filters by original title", async () => {
    const loader: MetadataEpisodeLoader = async () => [
      { episode: 1, title: "夜晚", originalTitle: "夜の話", duration: "23:40" },
      { episode: 2, title: "朝", originalTitle: "朝" },
      { episode: 3, originalTitle: "昼", duration: "约半小时" },
    ];
    render(<MetadataEpisodeDialog candidate={{ ...candidate, provider: "bgm" }} loader={loader} onClose={vi.fn()} />);
    expect(await within(list()).findByText("夜の話")).toBeInTheDocument();
    expect(within(list()).getByText("24m")).toBeInTheDocument();
    expect(within(list()).getByText("朝")).toBeInTheDocument();
    expect(within(list()).getByText("昼")).toBeInTheDocument();
    expect(within(list()).getByText("约半小时")).toBeInTheDocument();
    expect(within(list()).queryByRole("combobox")).not.toBeInTheDocument();
    fireEvent.change(within(list()).getByRole("textbox", { name: t("forms.episodeFilter") }), { target: { value: "夜の話" } });
    expect(within(list()).getByText("夜晚")).toBeInTheDocument();
    expect(within(list()).queryByText("朝")).not.toBeInTheDocument();
  });
});
