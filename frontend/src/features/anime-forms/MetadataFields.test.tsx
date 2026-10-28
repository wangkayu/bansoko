import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AnimeMetadataDraft } from "../../lib/api/client";
import type { MetadataCandidate, MetadataProvider, MetadataSearcher } from "../../lib/metadata/types";
import { MetadataFields } from "./MetadataFields";
import { t } from "../../lib/i18n";
import { setSessionTokenProvider } from "../../lib/api/client";
import { resetAuthedImageCache } from "../../lib/useAuthedImage";

// MetadataFields: the resting state is only the binding fact rows (external link + clear);
// matching goes through the controlled MetadataMatchDialog (title search → candidates → TMDB
// media type/season editing → confirm backfill).

// Base UI Select: the popup is portaled outside the dialog, so options are
// looked up on screen after opening the combobox with a real pointer sequence.
async function openSeason(dialog: HTMLElement) {
  await userEvent.setup().click(within(dialog).getByRole("combobox", { name: t("forms.applySeason") }));
}
async function pickSeason(dialog: HTMLElement, optionName: string | RegExp) {
  await openSeason(dialog);
  await userEvent.setup().click(await screen.findByRole("option", { name: optionName }));
}

function Controlled({
  searcher,
  initial = { bgm: null, tmdb: null },
  onChange,
  title = "沉默魔女的秘密",
  matchQueryCandidates,
  onMatchTitle,
}: {
  searcher: MetadataSearcher;
  initial?: AnimeMetadataDraft;
  onChange?: (next: AnimeMetadataDraft) => void;
  title?: string;
  matchQueryCandidates?: string[];
  onMatchTitle?: (title: string) => void;
}) {
  const [value, setValue] = useState<AnimeMetadataDraft>(initial);
  const [matchProvider, setMatchProvider] = useState<MetadataProvider>();
  return (
    <>
      <button onClick={() => setMatchProvider("bgm")}>{t("forms.matchProvider", { provider: "Bangumi" })}</button>
      <button onClick={() => setMatchProvider("tmdb")}>匹配 TMDB</button>
      <MetadataFields
        idPrefix="test"
        title={title}
        matchQueryCandidates={matchQueryCandidates}
        onMatchTitle={onMatchTitle}
        value={value}
        onChange={(next) => {
          setValue(next);
          onChange?.(next);
        }}
        searcher={searcher}
        matchProvider={matchProvider}
        onMatchProviderChange={setMatchProvider}
      />
    </>
  );
}

const bgmCandidate: MetadataCandidate = {
  provider: "bgm",
  workId: "486109",
  title: "沉默魔女的秘密",
  detailsStatus: "not_applicable",
};
const tmdbCandidate: MetadataCandidate = {
  provider: "tmdb",
  workId: "265010",
  title: "星际邮差艾拉",
  mediaKind: "tv",
  detailsStatus: "available",
  seasons: [
    { seasonNumber: 2, name: "第二季", episodeCount: 12, airDate: "2024-01-01", isSpecial: false },
    { seasonNumber: 12, name: "第十二季", episodeCount: 10, airDate: "2025-01-01", isSpecial: false },
  ],
  suggestedSeason: 2,
};

function searcherReturning(candidates: MetadataCandidate[]): MetadataSearcher {
  return { isMock: false, search: vi.fn().mockResolvedValue(candidates) };
}

function clickMatch(provider: "bgm" | "tmdb") {
  const label = provider === "bgm" ? t("forms.matchProvider", { provider: "Bangumi" }) : t("forms.matchProvider", { provider: "TMDB" });
  // Controlled/SeasonHarness keeps one outer entry; the MetadataFields binding rows also have
  // buttons of the same name, so tests must click the outer entry explicitly.
  fireEvent.click(screen.getAllByRole("button", { name: label })[0]!);
}

describe("MetadataFields 绑定事实行", () => {
  it("常态只显示未匹配事实与清除，不再有内联搜索区", () => {
    render(<Controlled searcher={searcherReturning([])} />);

    expect(screen.getAllByText(t("grid.card.unmatched"))).toHaveLength(2);
    expect(screen.queryByRole("button", { name: t("forms.search") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "搜索匹配" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重新匹配" })).not.toBeInTheDocument();
  });

  it("已绑定条目显示 ID 与外链，可清除", () => {
    const onChange = vi.fn();
    render(
      <MetadataFields
        idPrefix="test"
        title="t"
        value={{ bgm: { work_id: "486109" }, tmdb: { work_id: "265010", media_kind: "tv", season: 2 } }}
        onChange={onChange}
        searcher={searcherReturning([])}
      />,
    );
    expect(screen.getByRole("link", { name: /486109/ })).toHaveAttribute(
      "href",
      "https://bgm.tv/subject/486109",
    );
    expect(screen.getByRole("link", { name: /265010/ })).toHaveTextContent(t("forms.seasonN", { n: "2" }));
    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.bangumi") }) }));
    expect(onChange).toHaveBeenCalledWith({
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
    });
  });
});

describe("MetadataMatchDialog", () => {
  it("BGM：搜索候选并确认后回填 work_id", async () => {
    const searcher = searcherReturning([bgmCandidate]);
    const onChange = vi.fn();
    render(<Controlled searcher={searcher} onChange={onChange} />);

    clickMatch("bgm");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    await waitFor(() => expect(searcher.search).toHaveBeenCalledWith("bgm", "沉默魔女的秘密"));

    fireEvent.click(await within(dialog).findByRole("option", { name: /沉默魔女的秘密/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(onChange).toHaveBeenCalledWith({ bgm: { work_id: "486109" }, tmdb: null });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("link", { name: /486109/ })).toBeInTheDocument();
  });

  describe("候选海报", () => {
    beforeEach(() => {
      URL.createObjectURL = vi.fn(() => "blob:candidate-poster");
      URL.revokeObjectURL = vi.fn();
      setSessionTokenProvider(() => "t-1");
    });
    afterEach(() => {
      resetAuthedImageCache();
      vi.unstubAllGlobals();
      setSessionTokenProvider(() => "");
    });

    it("只经会话 fetch 加载同源代理路径，第三方 URL 显示占位且不请求", async () => {
      const fetchMock = vi.fn().mockResolvedValue(new Response("png", { status: 200, headers: { "Content-Type": "image/png" } }));
      vi.stubGlobal("fetch", fetchMock);
      const proxied: MetadataCandidate = { ...tmdbCandidate, workId: "1", title: "代理海报", posterUrl: "/api/v1/images/cover/" + "ab".repeat(32) };
      const foreign: MetadataCandidate = { ...tmdbCandidate, workId: "2", title: "外链海报", posterUrl: "https://image.tmdb.org/t/p/w342/p.jpg" };
      render(<Controlled searcher={searcherReturning([proxied, foreign])} />);

      clickMatch("tmdb");
      const dialog = await screen.findByRole("dialog");
      fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
      const proxiedOption = await within(dialog).findByRole("option", { name: /代理海报/ });
      const foreignOption = await within(dialog).findByRole("option", { name: /外链海报/ });

      await waitFor(() => expect(proxiedOption.querySelector("img")?.getAttribute("src")).toBe("blob:candidate-poster"));
      expect(foreignOption.querySelector("img")).toBeNull();
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect((fetchMock.mock.calls[0] as [string])[0]).toBe("/api/v1/images/cover/" + "ab".repeat(32));
    });
  });

  it("TMDB：只渲染真实季度并将自定义入口分组", async () => {
    render(<Controlled searcher={searcherReturning([tmdbCandidate])} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));

    await openSeason(dialog);
    expect(await screen.findByRole("option", { name: new RegExp(t("forms.seasonOption", { n: 2, name: "第二季", count: 12 })) })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: new RegExp(t("forms.seasonOption", { n: 12, name: "第十二季", count: 10 })) })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: t("grid.tabCustom") })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "S1" })).not.toBeInTheDocument();
    // Real seasons and the custom entry are shown in separate groups.
    expect(screen.getByText(t("forms.realSeasons"))).toBeInTheDocument();
    expect(screen.getAllByText(t("grid.tabCustom")).length).toBeGreaterThanOrEqual(2);
  });

  it("TMDB：候选推荐季度带入 season 编辑器，确认写入完整绑定", async () => {
    const searcher = searcherReturning([tmdbCandidate]);
    const onChange = vi.fn();
    render(<Controlled searcher={searcher} onChange={onChange} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));

    // The inferred result (suggestedSeason=2) already fills the dropdown.
    expect(within(dialog).getByRole("combobox", { name: t("forms.applySeason") })).toHaveTextContent(t("forms.seasonOption", { n: 2, name: "第二季", count: 12 }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(onChange).toHaveBeenCalledWith({
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      // An empty total-episodes pre-fills from the chosen season (S2, 12 episodes).
      total_episodes: 12,
      // While the season is unset it pre-fills from the chosen TMDB season.
      season: 2,
    });
  });

  it("TMDB：已有总集数不被自动填充覆盖", async () => {
    const onChange = vi.fn();
    render(<Controlled searcher={searcherReturning([tmdbCandidate])} initial={{ bgm: null, tmdb: null, total_episodes: 24 }} onChange={onChange} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(onChange).toHaveBeenCalledWith({
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      total_episodes: 24,
      season: 2,
    });
  });

  it("TMDB：自定义季度可输入第 0 季", async () => {
    const onChange = vi.fn();
    render(<Controlled searcher={searcherReturning([tmdbCandidate])} onChange={onChange} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));
    await pickSeason(dialog, t("grid.tabCustom"));
    fireEvent.change(within(dialog).getByLabelText(t("forms.customSeasonAria")), { target: { value: "0" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(onChange).toHaveBeenCalledWith({
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 0 },
      season: 0,
    });
  });

  it("TMDB：自定义季非法输入报错并禁用确认", async () => {
    render(<Controlled searcher={searcherReturning([tmdbCandidate])} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));
    await pickSeason(dialog, t("grid.tabCustom"));
    fireEvent.change(within(dialog).getByLabelText(t("forms.customSeasonAria")), { target: { value: "abc" } });

    expect(within(dialog).getByRole("alert")).toHaveTextContent(t("forms.seasonInvalid"));
    expect(within(dialog).getByRole("button", { name: t("forms.confirmMatch") })).toBeDisabled();
  });

  it("TMDB：movie 无季度（season 0 上送，season 编辑器隐藏）", async () => {
    const movie: MetadataCandidate = { ...tmdbCandidate, mediaKind: "movie", suggestedSeason: undefined };
    const onChange = vi.fn();
    render(<Controlled searcher={searcherReturning([movie])} onChange={onChange} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));

    expect(within(dialog).queryByRole("combobox", { name: t("forms.applySeason") })).not.toBeInTheDocument();
    expect(within(dialog).getByText("电影没有季度。")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(onChange).toHaveBeenCalledWith({
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "movie", season: 0 },
    });
  });

  it.each([
    ["empty", t("forms.noSeasonInfo")],
    ["error", t("forms.seasonInfoFailed")],
  ] as const)("TMDB：details 状态 %s 显示明确状态并允许自定义", async (detailsStatus, message) => {
    const candidate: MetadataCandidate = {
      ...tmdbCandidate,
      detailsStatus,
      seasons: detailsStatus === "empty" ? [] : undefined,
      suggestedSeason: undefined,
    };
    render(<Controlled searcher={searcherReturning([candidate])} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));

    expect(within(dialog).getByText(new RegExp(message))).toBeInTheDocument();
    await pickSeason(dialog, t("grid.tabCustom"));
    fireEvent.change(within(dialog).getByLabelText(t("forms.customSeasonAria")), { target: { value: "9" } });
    expect(within(dialog).getByRole("button", { name: t("forms.confirmMatch") })).not.toBeDisabled();
  });

  it("空结果显示空态", async () => {
    render(<Controlled searcher={{ isMock: false, search: vi.fn().mockResolvedValue([]) }} />);

    clickMatch("bgm");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    await within(dialog).findByText(t("forms.noCandidates"));
    expect(within(dialog).getByRole("button", { name: t("forms.confirmMatch") })).toBeDisabled();
  });

  it("搜索失败显示错误", async () => {
    render(<Controlled searcher={{ isMock: false, search: vi.fn().mockRejectedValue(new Error("网络错误")) }} />);

    clickMatch("bgm");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    await within(dialog).findByRole("alert");
    expect(within(dialog).getByRole("alert")).toHaveTextContent(t("forms.queryFailed"));
  });
});

// The season row (rendered with showSeason): the season is the anime's own field (value.season),
// independent of the TMDB binding. Unset shows "not set"; edit → number input + SP + save/cancel,
// saving back into the form value; an empty input = clearing to unset. Confirming a match
// pre-fills the chosen TMDB season only while the season is unset.
describe("MetadataFields 季度行", () => {
  function SeasonHarness({
    initial = { bgm: null, tmdb: null },
    candidates = [],
    onChange,
    directEdit = false,
    readOnly = false,
  }: {
    initial?: AnimeMetadataDraft;
    candidates?: MetadataCandidate[];
    onChange?: (next: AnimeMetadataDraft) => void;
    directEdit?: boolean;
    readOnly?: boolean;
  }) {
    const [value, setValue] = useState<AnimeMetadataDraft>(initial);
    const [matchProvider, setMatchProvider] = useState<MetadataProvider>();
    return (
      <>
        <button onClick={() => setMatchProvider("tmdb")}>匹配 TMDB</button>
        <MetadataFields
          idPrefix="test"
          title="沉默魔女的秘密"
          showSeason
          directEdit={directEdit}
          readOnly={readOnly}
          value={value}
          onChange={(next) => {
            setValue(next);
            onChange?.(next);
          }}
          searcher={searcherReturning(candidates)}
          matchProvider={matchProvider}
          onMatchProviderChange={setMatchProvider}
        />
      </>
    );
  }

  function seasonRegion() {
    return screen.getByRole("region", { name: t("forms.season") });
  }

  it("未设置时显示「未设置」，不伪显示第 1 季", () => {
    render(<SeasonHarness />);
    expect(seasonRegion()).toHaveTextContent(t("forms.seasonUnset"));
  });

  it("不传 showSeason 时不渲染季度行", () => {
    render(<Controlled searcher={searcherReturning([])} />);
    expect(screen.queryByRole("region", { name: t("forms.season") })).not.toBeInTheDocument();
  });

  it("没有 TMDB 绑定也能保存季度，写回表单值", async () => {
    const onChange = vi.fn();
    render(<SeasonHarness onChange={onChange} />);

    const region = seasonRegion();
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editSeasonAria") }));
    fireEvent.change(within(region).getByLabelText(t("forms.seasonAria")), { target: { value: "3" } });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveSeason") }));

    expect(onChange).toHaveBeenLastCalledWith({ bgm: null, tmdb: null, season: 3 });
    await waitFor(() => expect(region).toHaveTextContent(t("forms.seasonN", { n: 3 })));
  });

  it("季度与 TMDB 季号互不改写：编辑季度不动绑定，链接仍显示 TMDB 季号", async () => {
    const onChange = vi.fn();
    render(
      <SeasonHarness
        initial={{ bgm: null, tmdb: { work_id: "265010", media_kind: "tv", season: 1 }, season: 2 }}
        onChange={onChange}
      />,
    );
    expect(seasonRegion()).toHaveTextContent(t("forms.seasonN", { n: 2 }));
    expect(screen.getByRole("link", { name: /265010/ })).toHaveTextContent(t("forms.seasonN", { n: "1" }));

    const region = seasonRegion();
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editSeasonAria") }));
    fireEvent.change(within(region).getByLabelText(t("forms.seasonAria")), { target: { value: "0" } });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveSeason") }));
    expect(onChange).toHaveBeenLastCalledWith({
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 1 },
      season: 0,
    });
    await waitFor(() => expect(region).toHaveTextContent(t("forms.seasonSpecials")));
  });

  it("空输入清除为未设置", async () => {
    const onChange = vi.fn();
    render(<SeasonHarness initial={{ bgm: null, tmdb: null, season: 2 }} onChange={onChange} />);

    const region = seasonRegion();
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editSeasonAria") }));
    fireEvent.change(within(region).getByLabelText(t("forms.seasonAria")), { target: { value: "" } });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveSeason") }));

    expect(onChange).toHaveBeenLastCalledWith({ bgm: null, tmdb: null, season: null });
    await waitFor(() => expect(region).toHaveTextContent(t("forms.seasonUnset")));
  });

  it("非法输入内联报错且不写回", () => {
    const onChange = vi.fn();
    render(<SeasonHarness initial={{ bgm: null, tmdb: null, season: 2 }} onChange={onChange} />);

    const region = seasonRegion();
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editSeasonAria") }));
    fireEvent.change(within(region).getByLabelText(t("forms.seasonAria")), { target: { value: "100" } });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveSeason") }));

    expect(within(region).getByRole("alert")).toHaveTextContent(t("forms.seasonInvalid"));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("直接编辑：输入立即写回，清空写回 null", () => {
    const onChange = vi.fn();
    render(<SeasonHarness directEdit onChange={onChange} />);
    const input = within(seasonRegion()).getByLabelText(t("forms.seasonAria"));

    fireEvent.change(input, { target: { value: "4" } });
    expect(onChange).toHaveBeenLastCalledWith({ bgm: null, tmdb: null, season: 4 });
    fireEvent.change(input, { target: { value: "" } });
    expect(onChange).toHaveBeenLastCalledWith({ bgm: null, tmdb: null, season: null });
  });

  it("只读工作区展示「未设置」", () => {
    render(<SeasonHarness readOnly />);
    expect(seasonRegion()).toHaveTextContent(t("forms.seasonUnset"));
    expect(within(seasonRegion()).queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("电影没有季度：行降级为说明且无编辑入口", () => {
    render(<SeasonHarness initial={{ bgm: null, tmdb: { work_id: "265010", media_kind: "movie", season: 0 } }} />);

    const region = seasonRegion();
    expect(region).toHaveTextContent(t("forms.movieNoSeason"));
    expect(within(region).queryByRole("button", { name: t("forms.editSeasonAria") })).not.toBeInTheDocument();
  });

  async function confirmTmdb() {
    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));
    return dialog;
  }

  it("匹配在季度未设置时预填所选 TMDB 季度", async () => {
    const onChange = vi.fn();
    render(<SeasonHarness candidates={[tmdbCandidate]} onChange={onChange} />);
    const dialog = await confirmTmdb();
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ season: 2, tmdb: { work_id: "265010", media_kind: "tv", season: 2 } }),
    );
    await waitFor(() => expect(seasonRegion()).toHaveTextContent(t("forms.seasonN", { n: 2 })));
  });

  it("已有季度时匹配不覆盖，季度作为弹窗 TMDB 季号的初始值", async () => {
    const onChange = vi.fn();
    render(<SeasonHarness candidates={[tmdbCandidate]} initial={{ bgm: null, tmdb: null, season: 12 }} onChange={onChange} />);
    const dialog = await confirmTmdb();
    expect(within(dialog).getByRole("combobox", { name: t("forms.applySeason") })).toHaveTextContent(
      t("forms.seasonOption", { n: 12, name: "第十二季", count: 10 }),
    );
    await pickSeason(dialog, new RegExp(t("forms.seasonOption", { n: 2, name: "第二季", count: 12 })));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ season: 12, tmdb: { work_id: "265010", media_kind: "tv", season: 2 } }),
    );
  });
});

describe("MetadataMatchDialog 检索词候选与作品名回传", () => {
  const groupName = () => t("forms.queryCandidatesAria");

  it("不传候选：初始搜索词是当前标题，没有候选标签", async () => {
    render(<Controlled searcher={searcherReturning([])} />);
    clickMatch("bgm");
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("textbox", { name: t("forms.titleQuery") })).toHaveValue("沉默魔女的秘密");
    expect(within(dialog).queryByRole("group", { name: groupName() })).not.toBeInTheDocument();
  });

  it("标题为空：第一个候选作初始搜索词，其余渲染为标签，点击替换搜索词并搜索", async () => {
    const searcher = searcherReturning([]);
    render(<Controlled searcher={searcher} title="" matchQueryCandidates={["作品乙", "作品甲", "关键词"]} />);
    clickMatch("bgm");
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByRole("textbox", { name: t("forms.titleQuery") });
    expect(input).toHaveValue("作品乙");
    const chips = within(within(dialog).getByRole("group", { name: groupName() })).getAllByRole("button");
    expect(chips.map((chip) => chip.textContent)).toEqual(["作品甲", "关键词"]);

    fireEvent.click(chips[0]!);
    await waitFor(() => expect(searcher.search).toHaveBeenCalledWith("bgm", "作品甲"));
    expect(input).toHaveValue("作品甲");
  });

  it("标题非空：以标题为初始搜索词，候选标签仍可用", async () => {
    render(<Controlled searcher={searcherReturning([])} matchQueryCandidates={["候选一"]} />);
    clickMatch("bgm");
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("textbox", { name: t("forms.titleQuery") })).toHaveValue("沉默魔女的秘密");
    expect(within(within(dialog).getByRole("group", { name: groupName() })).getAllByRole("button")).toHaveLength(1);
  });

  it("确认匹配把作品名传出：本地化名优先，退回原名；手动 TMDB ID 不传", async () => {
    const onMatchTitle = vi.fn();
    const searcher = searcherReturning([{ ...bgmCandidate, title: "", originalTitle: "Original Name" }]);
    render(<Controlled searcher={searcher} onMatchTitle={onMatchTitle} />);
    clickMatch("bgm");
    let dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /Original Name/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));
    expect(onMatchTitle).toHaveBeenLastCalledWith("Original Name");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    onMatchTitle.mockClear();
    clickMatch("tmdb");
    dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(t("forms.tmdbIdAria")), { target: { value: "259972" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.useTmdbId") }));
    await pickSeason(dialog, t("grid.tabCustom"));
    fireEvent.change(within(dialog).getByLabelText(t("forms.customSeasonAria")), { target: { value: "3" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));
    expect(onMatchTitle).not.toHaveBeenCalled();
  });
});

describe("MetadataMatchDialog 按 ID 填写", () => {
  it("TMDB ID 直接确认时默认 TV 第 1 季", async () => {
    const onChange = vi.fn();
    render(<Controlled searcher={searcherReturning([])} onChange={onChange} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(t("forms.tmdbIdAria")), { target: { value: "259972" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.useTmdbId") }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(onChange).toHaveBeenLastCalledWith({
      bgm: null,
      tmdb: { work_id: "259972", media_kind: "tv", season: 1 },
      season: 1,
    });
  });

  it("TMDB ID 不联网核对也能保存完整绑定", async () => {
    const searcher = searcherReturning([]);
    const onChange = vi.fn();
    render(<Controlled searcher={searcher} onChange={onChange} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(t("forms.tmdbIdAria")), { target: { value: "259972" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.useTmdbId") }));

    expect(within(dialog).getByText(t("forms.manualTmdbNote"))).toBeInTheDocument();
    await pickSeason(dialog, t("grid.tabCustom"));
    fireEvent.change(within(dialog).getByLabelText(t("forms.customSeasonAria")), { target: { value: "3" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));

    expect(searcher.search).not.toHaveBeenCalled();
    expect(onChange).toHaveBeenLastCalledWith({
      bgm: null,
      tmdb: { work_id: "259972", media_kind: "tv", season: 3 },
      season: 3,
    });
  });

  it("非法 ID 内联报错且不选中", async () => {
    render(<Controlled searcher={searcherReturning([])} />);

    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(t("forms.tmdbIdAria")), { target: { value: "0123" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.useTmdbId") }));

    expect(within(dialog).getByRole("alert")).toHaveTextContent(t("forms.tmdbIdInvalid"));
    expect(within(dialog).getByRole("button", { name: t("forms.confirmMatch") })).toBeDisabled();
  });

  it("Bangumi 不提供按 ID 填写", async () => {
    render(<Controlled searcher={searcherReturning([])} />);
    clickMatch("bgm");
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByLabelText(t("forms.tmdbIdAria"))).not.toBeInTheDocument();
  });
});

// The total-episodes row (rendered when onSaveTotalEpisodes exists): resting read-only shows "N
// episodes / unknown", edit → number input + save/cancel; an empty input = clearing to NULL (back
// to unknown). The save semantics are injected by the caller (owner=PATCH, draft=write the
// draft). 0 is a valid value.
describe("MetadataFields 总集数行", () => {
  function TotalEpisodesHarness({
    totalEpisodes,
    onSaveTotalEpisodes,
  }: {
    totalEpisodes: number | null;
    onSaveTotalEpisodes: (total: number | null) => Promise<boolean>;
  }) {
    const [value, setValue] = useState<AnimeMetadataDraft>({ bgm: null, tmdb: null, total_episodes: totalEpisodes });
    return (
      <MetadataFields
        idPrefix="test"
        title="沉默魔女的秘密"
        value={value}
        onChange={() => {}}
        searcher={searcherReturning([])}
        onSaveTotalEpisodes={async (total) => {
          const ok = await onSaveTotalEpisodes(total);
          if (ok) setValue({ ...value, total_episodes: total });
          return ok;
        }}
      />
    );
  }

  it("有值时展示「N 集」，无值时展示「未知」", () => {
    const { unmount } = render(<TotalEpisodesHarness totalEpisodes={12} onSaveTotalEpisodes={vi.fn()} />);
    expect(screen.getByRole("region", { name: t("forms.totalEpisodes") })).toHaveTextContent(t("forms.episodesCount", { n: "12" }));
    unmount();

    render(<TotalEpisodesHarness totalEpisodes={null} onSaveTotalEpisodes={vi.fn()} />);
    expect(screen.getByRole("region", { name: t("forms.totalEpisodes") })).toHaveTextContent(t("settings.delivery.state.other"));
  });

  it("不传 onSaveTotalEpisodes 时不渲染总集数行", () => {
    render(
      <MetadataFields
        idPrefix="test"
        value={{ bgm: null, tmdb: null }}
        onChange={() => {}}
        searcher={searcherReturning([])}
      />,
    );
    expect(screen.queryByRole("region", { name: t("forms.totalEpisodes") })).not.toBeInTheDocument();
  });

  it("编辑保存调用 onSaveTotalEpisodes，成功后退出编辑态展示新值", async () => {
    const onSaveTotalEpisodes = vi.fn().mockResolvedValue(true);
    render(<TotalEpisodesHarness totalEpisodes={12} onSaveTotalEpisodes={onSaveTotalEpisodes} />);

    const region = screen.getByRole("region", { name: t("forms.totalEpisodes") });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editTotalAria") }));
    fireEvent.change(within(region).getByLabelText(t("forms.totalEpisodes")), { target: { value: "24" } });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveTotal") }));

    await waitFor(() => expect(onSaveTotalEpisodes).toHaveBeenCalledWith(24));
    await waitFor(() => expect(region).toHaveTextContent(t("forms.episodesCount", { n: "24" })));
    expect(within(region).queryByLabelText(t("forms.totalEpisodes"))).not.toBeInTheDocument();
  });

  it("保存未成功（返回 false）停留编辑态", async () => {
    const onSaveTotalEpisodes = vi.fn().mockResolvedValue(false);
    render(<TotalEpisodesHarness totalEpisodes={12} onSaveTotalEpisodes={onSaveTotalEpisodes} />);

    const region = screen.getByRole("region", { name: t("forms.totalEpisodes") });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editTotalAria") }));
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveTotal") }));

    await waitFor(() => expect(onSaveTotalEpisodes).toHaveBeenCalledWith(12));
    expect(within(region).getByLabelText(t("forms.totalEpisodes"))).toBeInTheDocument();
  });

  it("非法输入内联报错且不调用 onSaveTotalEpisodes", () => {
    const onSaveTotalEpisodes = vi.fn().mockResolvedValue(true);
    render(<TotalEpisodesHarness totalEpisodes={12} onSaveTotalEpisodes={onSaveTotalEpisodes} />);

    const region = screen.getByRole("region", { name: t("forms.totalEpisodes") });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editTotalAria") }));
    fireEvent.change(within(region).getByLabelText(t("forms.totalEpisodes")), { target: { value: "-1" } });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveTotal") }));

    expect(within(region).getByRole("alert")).toHaveTextContent(t("forms.totalInvalid"));
    expect(onSaveTotalEpisodes).not.toHaveBeenCalled();
  });

  it("空输入清除为 NULL：调用 onSaveTotalEpisodes(null)", async () => {
    const onSaveTotalEpisodes = vi.fn().mockResolvedValue(true);
    render(<TotalEpisodesHarness totalEpisodes={12} onSaveTotalEpisodes={onSaveTotalEpisodes} />);

    const region = screen.getByRole("region", { name: t("forms.totalEpisodes") });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editTotalAria") }));
    fireEvent.change(within(region).getByLabelText(t("forms.totalEpisodes")), { target: { value: "" } });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveTotal") }));

    await waitFor(() => expect(onSaveTotalEpisodes).toHaveBeenCalledWith(null));
    await waitFor(() => expect(region).toHaveTextContent(t("settings.delivery.state.other")));
  });

  it("0 是合法值：保存后展示「0 集」", async () => {
    const onSaveTotalEpisodes = vi.fn().mockResolvedValue(true);
    render(<TotalEpisodesHarness totalEpisodes={null} onSaveTotalEpisodes={onSaveTotalEpisodes} />);

    const region = screen.getByRole("region", { name: t("forms.totalEpisodes") });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.editTotalAria") }));
    fireEvent.change(within(region).getByLabelText(t("forms.totalEpisodes")), { target: { value: "0" } });
    fireEvent.click(within(region).getByRole("button", { name: t("forms.saveTotal") }));

    await waitFor(() => expect(onSaveTotalEpisodes).toHaveBeenCalledWith(0));
    await waitFor(() => expect(region).toHaveTextContent(t("forms.episodesCount", { n: "0" })));
  });
});

describe("MetadataFields 年份行", () => {
  function YearHarness({
    searcher,
    initial = { bgm: null, tmdb: null },
    onChange,
    readOnly = false,
  }: {
    searcher: MetadataSearcher;
    initial?: AnimeMetadataDraft;
    onChange: (next: AnimeMetadataDraft) => void;
    readOnly?: boolean;
  }) {
    const [value, setValue] = useState<AnimeMetadataDraft>(initial);
    const [matchProvider, setMatchProvider] = useState<MetadataProvider>();
    return (
      <>
        <button onClick={() => setMatchProvider("tmdb")}>匹配 TMDB</button>
        <MetadataFields
          idPrefix="year"
          title="星际邮差艾拉"
          showYear
          readOnly={readOnly}
          value={value}
          onChange={(next) => {
            setValue(next);
            onChange(next);
          }}
          searcher={searcher}
          matchProvider={matchProvider}
          onMatchProviderChange={setMatchProvider}
        />
      </>
    );
  }

  it("不传 showYear 时不渲染年份行", () => {
    render(<Controlled searcher={searcherReturning([])} />);
    expect(screen.queryByRole("region", { name: t("forms.year") })).not.toBeInTheDocument();
  });

  it("输入写回表单值；清空写回 null；不合法输入不写回并在失焦时提示", () => {
    const onChange = vi.fn();
    render(<YearHarness searcher={searcherReturning([])} onChange={onChange} />);
    const region = screen.getByRole("region", { name: t("forms.year") });
    const input = within(region).getByRole("textbox");

    fireEvent.change(input, { target: { value: "2019" } });
    expect(onChange).toHaveBeenLastCalledWith({ bgm: null, tmdb: null, year: 2019 });
    fireEvent.change(input, { target: { value: "" } });
    expect(onChange).toHaveBeenLastCalledWith({ bgm: null, tmdb: null, year: null });

    onChange.mockClear();
    fireEvent.change(input, { target: { value: "3000" } });
    fireEvent.blur(input);
    expect(onChange).not.toHaveBeenCalled();
    expect(within(region).getByRole("alert")).toHaveTextContent(t("forms.yearInvalid"));
  });

  async function confirmTmdbMatch() {
    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));
  }

  it("TMDB 匹配在年份为空时填入候选年份，之后可手动改", async () => {
    const onChange = vi.fn();
    render(<YearHarness searcher={searcherReturning([{ ...tmdbCandidate, year: 2024 }])} onChange={onChange} />);
    await confirmTmdbMatch();

    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ year: 2024 }));
    const region = screen.getByRole("region", { name: t("forms.year") });
    expect(within(region).getByRole("textbox")).toHaveValue("2024");
    fireEvent.change(within(region).getByRole("textbox"), { target: { value: "2025" } });
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ year: 2025 }));
  });

  it("已有年份时匹配不覆盖（与服务端回填同一条 fill-if-empty 规则）", async () => {
    const onChange = vi.fn();
    render(
      <YearHarness
        searcher={searcherReturning([{ ...tmdbCandidate, year: 2024 }])}
        initial={{ bgm: null, tmdb: null, year: 2010 }}
        onChange={onChange}
      />,
    );
    await confirmTmdbMatch();
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ year: 2010, tmdb: expect.objectContaining({ work_id: tmdbCandidate.workId }) }));
  });

  it("候选列表显示 TMDB 年份", async () => {
    render(<YearHarness searcher={searcherReturning([{ ...tmdbCandidate, year: 2024 }])} onChange={vi.fn()} />);
    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    expect(await within(dialog).findByRole("option", { name: /2024/ })).toBeInTheDocument();
  });

  it("候选没有年份时保留现值", async () => {
    const onChange = vi.fn();
    render(
      <YearHarness searcher={searcherReturning([tmdbCandidate])} initial={{ bgm: null, tmdb: null, year: 2010 }} onChange={onChange} />,
    );
    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ year: 2010 }));
  });

  it("只读时只展示值", () => {
    render(<YearHarness searcher={searcherReturning([])} initial={{ bgm: null, tmdb: null, year: 2018 }} onChange={vi.fn()} readOnly />);
    const region = screen.getByRole("region", { name: t("forms.year") });
    expect(region).toHaveTextContent("2018");
    expect(within(region).queryByRole("textbox")).not.toBeInTheDocument();
  });
});

describe("MetadataFields 播出形式", () => {
  it("不渲染播出形式行，BGM 匹配只写入关联", async () => {
    const onChange = vi.fn();
    render(<Controlled searcher={searcherReturning([bgmCandidate])} onChange={onChange} />);
    expect(screen.queryByRole("region", { name: "播出形式" })).not.toBeInTheDocument();
    clickMatch("bgm");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /沉默魔女的秘密/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));
    expect(onChange).toHaveBeenLastCalledWith({ bgm: { work_id: "486109" }, tmdb: null });
  });
});

describe("MetadataFields TMDB 绑定关联值", () => {
  const bound: AnimeMetadataDraft = {
    bgm: null,
    tmdb: { work_id: "111", media_kind: "tv", season: 1 },
    season: 1,
    year: 2020,
    total_episodes: 24,
  };

  async function confirmCandidate(candidate: MetadataCandidate, onChange: (next: AnimeMetadataDraft) => void, initial: AnimeMetadataDraft) {
    render(<Controlled searcher={searcherReturning([candidate])} initial={initial} onChange={onChange} />);
    clickMatch("tmdb");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: new RegExp(candidate.title) }));
    // The dialog seeds the TMDB season from the anime's own season; choose the candidate's season 2 explicitly.
    if (candidate.seasons?.length) await pickSeason(dialog, new RegExp(t("forms.seasonOption", { n: 2, name: "第二季", count: 12 })));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));
  }

  it("清除 TMDB 绑定同时重置季度、年份和总集数", () => {
    const onChange = vi.fn();
    render(<Controlled searcher={searcherReturning([])} initial={{ ...bound, bgm: { work_id: "9" } }} onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.tmdb") }) }));
    expect(onChange).toHaveBeenLastCalledWith({
      bgm: { work_id: "9" },
      tmdb: null,
      season: null,
      year: null,
      total_episodes: null,
    });
  });

  it("替换已有绑定（tv）用新候选覆盖季度、年份和总集数", async () => {
    const onChange = vi.fn();
    await confirmCandidate({ ...tmdbCandidate, year: 2024 }, onChange, bound);
    expect(onChange).toHaveBeenLastCalledWith({
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      season: 2,
      year: 2024,
      total_episodes: 12,
    });
  });

  it("替换已有绑定时候选缺年份或集数则置空", async () => {
    const onChange = vi.fn();
    await confirmCandidate({ ...tmdbCandidate, seasons: [] }, onChange, { ...bound, season: 2 });
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ season: 2, year: null, total_episodes: null }));
  });

  it("替换已有绑定（movie）季度与总集数置空，年份取候选", async () => {
    const onChange = vi.fn();
    const movie: MetadataCandidate = { provider: "tmdb", workId: "777", title: "剧场版候选", mediaKind: "movie", detailsStatus: "available", year: 2023 };
    await confirmCandidate(movie, onChange, bound);
    expect(onChange).toHaveBeenLastCalledWith({
      bgm: null,
      tmdb: { work_id: "777", media_kind: "movie", season: 0 },
      season: null,
      year: 2023,
      total_episodes: null,
    });
  });

  it("首次绑定保留此前手填的值，只填空缺", async () => {
    const onChange = vi.fn();
    await confirmCandidate({ ...tmdbCandidate, year: 2024 }, onChange, { bgm: null, tmdb: null, season: 5, year: 2010 });
    expect(onChange).toHaveBeenLastCalledWith({
      bgm: null,
      tmdb: { work_id: "265010", media_kind: "tv", season: 2 },
      season: 5,
      year: 2010,
      total_episodes: 12,
    });
  });
});
