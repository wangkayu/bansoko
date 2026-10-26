// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { QualityProfile } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { renderWithQuery } from "../../testing/renderWithQuery";
import { AnimeTabNavigationContext } from "../anime-tabs/tabNavigation";
import { QualityCompareEditor } from "./QualityCompareEditor";
import { compareItemName } from "./compareCopy";

const initial: QualityProfile = {
  version: "profile-v1",
  items: [
    { key: "subgroup.score", ranks: [] },
    { key: "anibt.language", ranks: [["CHS"]] },
    { key: "rt.codec", ranks: [["HEVC"], ["AVC"]] },
  ],
};

let vocabularyURLs: string[];
let latest: QualityProfile;

beforeEach(() => {
  vocabularyURLs = [];
  latest = initial;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    vocabularyURLs.push(String(input));
    return new Response(JSON.stringify({ items: [{ key: "anibt.language", values: ["CHS", "CHT", "JPN"] }, { key: "rt.codec", values: ["AV1", "AVC", "HEVC"] }] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }));
});

afterEach(() => vi.unstubAllGlobals());

function Harness({ mode = "single_copy", animeID }: { mode?: "all" | "per_group" | "single_copy"; animeID?: string }) {
  const [value, setValue] = useState(initial);
  return (
    <QualityCompareEditor
      value={value}
      admissionMode={mode}
      managedAnimeID={animeID}
      onChange={(next) => { latest = next; setValue(next); }}
    />
  );
}

function itemOrder(): string[] {
  const list = screen.getByRole("list", { name: t("qualityCompare.listAria") });
  return within(list).getAllByRole("listitem").filter((item) => item.parentElement === list).map((item) => item.textContent ?? "");
}

function entries(key: Parameters<typeof compareItemName>[0]): string[] {
  const list = screen.getByRole("list", { name: t("qualityCompare.ranksAria", { item: compareItemName(key) }) });
  return within(list).getAllByRole("listitem").map((item) => item.textContent ?? "");
}

describe("QualityCompareEditor", () => {
  it("orders items with the arrow buttons and labels each item with its source", () => {
    renderWithQuery(<Harness />);
    expect(itemOrder()[0]).toContain(t("qualityCompare.item.subgroup_score"));
    expect(screen.getByRole("button", { name: t("qualityCompare.moveUp", { item: compareItemName("subgroup.score") }) })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.moveDown", { item: compareItemName("subgroup.score") }) }));
    expect(latest.items.map((item) => item.key)).toEqual(["anibt.language", "subgroup.score", "rt.codec"]);
    expect(itemOrder()[0]).toContain(t("qualityCompare.namespace.anibt"));
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.entryEarlier", { value: "AVC" }) }));
    expect(entries("rt.codec")).toEqual(["1AVC", "2HEVC", t("qualityCompare.others")]);
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.removeItem", { item: compareItemName("rt.codec") }) }));
    expect(latest.items.map((item) => item.key)).toEqual(["anibt.language", "subgroup.score"]);
  });

  it("marks rt items as reltitle values and offers new items grouped by source", async () => {
    renderWithQuery(<Harness />);
    const rtItem = itemOrder().find((text) => text.includes(t("qualityCompare.item.rt_codec")));
    expect(rtItem).toContain(t("qualityCompare.rtSource"));
    expect(screen.getAllByText(t("qualityCompare.rtSource"))).toHaveLength(1);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: t("qualityCompare.addItem") }));
    const menu = await screen.findByRole("menu");
    const offered = within(menu).getAllByRole("menuitem").map((item) => item.textContent);
    expect(offered).toContain(compareItemName("release.lang"));
    expect(offered).toContain(compareItemName("rt.sub_lang"));
    expect(offered).not.toContain(compareItemName("anibt.language"));
    await user.click(within(menu).getByRole("menuitem", { name: compareItemName("rt.sub_lang") }));
    expect(latest.items.at(-1)).toEqual({ key: "rt.sub_lang", ranks: [] });
    expect(screen.getAllByText(t("qualityCompare.rtSource"))).toHaveLength(2);
    expect(screen.getByText(t("qualityCompare.empty"))).toBeInTheDocument();
  });

  it("explains the subtitle type item when it is added", async () => {
    renderWithQuery(<Harness />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: t("qualityCompare.addItem") }));
    const menu = await screen.findByRole("menu");
    await user.click(within(menu).getByRole("menuitem", { name: compareItemName("rt.sub_flag") }));
    expect(latest.items.at(-1)).toEqual({ key: "rt.sub_flag", ranks: [] });
    expect(screen.getByText(new RegExp(t("qualityCompare.subFlagHint")))).toBeInTheDocument();
  });

  it("adds a combination ahead of narrower entries and rejects the same set again", async () => {
    renderWithQuery(<Harness animeID="anime-1" />);
    await waitFor(() => expect(screen.getByRole("checkbox", { name: t("qualityCompare.comboValueAria", { value: "CHT" }) })).toBeInTheDocument());
    expect(vocabularyURLs.some((url) => url.includes("compare-vocabulary?managed_anime_id=anime-1"))).toBe(true);
    const comboAdd = screen.getByRole("button", { name: t("qualityCompare.comboAdd") });
    fireEvent.click(screen.getByRole("checkbox", { name: t("qualityCompare.comboValueAria", { value: "CHT" }) }));
    expect(comboAdd).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: t("qualityCompare.comboValueAria", { value: "CHS" }) }));
    fireEvent.click(comboAdd);
    expect(latest.items[1].ranks).toEqual([["CHS", "CHT"], ["CHS"]]);
    expect(entries("anibt.language")).toEqual(["1CHS+CHT", "2CHS", t("qualityCompare.others")]);

    const name = compareItemName("anibt.language");
    fireEvent.change(screen.getByLabelText(t("qualityCompare.valueAria", { item: name })), { target: { value: "chs+cht" } });
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.addValueAria", { item: name }) }));
    expect(screen.getByRole("alert")).toHaveTextContent(t("qualityCompare.problem.duplicate"));
    expect(latest.items[1].ranks).toHaveLength(2);

    fireEvent.change(screen.getByLabelText(t("qualityCompare.valueAria", { item: name })), { target: { value: "JPN" } });
    expect(screen.queryByRole("alert")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.addValueAria", { item: name }) }));
    expect(latest.items[1].ranks).toEqual([["CHS", "CHT"], ["CHS"], ["JPN"]]);
  });

  it("refuses combinations on single-valued items", async () => {
    renderWithQuery(<Harness />);
    const name = compareItemName("rt.codec");
    // Only the multi-valued item offers the combination builder.
    expect(await screen.findAllByRole("group", { name: t("qualityCompare.comboLabel") })).toHaveLength(1);
    fireEvent.change(screen.getByLabelText(t("qualityCompare.valueAria", { item: name })), { target: { value: "AV1+HEVC" } });
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.addValueAria", { item: name }) }));
    expect(screen.getByRole("alert")).toHaveTextContent(t("qualityCompare.problem.combination"));
    expect(latest).toBe(initial);
  });

  it("links the group score item to the anime's groups tab only inside the anime tabs", () => {
    const navigate = vi.fn();
    const { unmount } = renderWithQuery(
      <AnimeTabNavigationContext.Provider value={navigate}>
        <Harness mode="per_group" />
      </AnimeTabNavigationContext.Provider>,
    );
    expect(screen.getByText(t("qualityCompare.scorePerGroup"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.scoreLink") }));
    expect(navigate).toHaveBeenCalledWith("subgroups");
    unmount();
    renderWithQuery(<Harness mode="all" />);
    expect(screen.queryByRole("button", { name: t("qualityCompare.scoreLink") })).toBeNull();
    expect(screen.getByText(t("qualityCompare.allModeNote"))).toBeInTheDocument();
  });
});
