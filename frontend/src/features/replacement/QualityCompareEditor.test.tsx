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
  version: "profile-v2",
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
    return new Response(
      JSON.stringify({
        items: [
          { key: "anibt.language", values: ["CHS", "CHT", "JPN"] },
          { key: "rt.codec", values: ["AV1", "AVC", "HEVC"] },
        ],
      }),
      {
        status: 200,
        headers: { "Content-Type": "application/json" },
      },
    );
  }));
});

afterEach(() => vi.unstubAllGlobals());

function Harness({
  mode = "single_copy",
  animeID,
  start = initial,
}: {
  mode?: "all" | "per_group" | "single_copy";
  animeID?: string;
  start?: QualityProfile;
}) {
  const [value, setValue] = useState(start);
  return (
    <QualityCompareEditor
      value={value}
      admissionMode={mode}
      managedAnimeID={animeID}
      onChange={(next) => {
        latest = next;
        setValue(next);
      }}
    />
  );
}

function itemOrder(): string[] {
  const list = screen.getByRole("list", { name: t("qualityCompare.listAria") });
  return within(list)
    .getAllByRole("listitem")
    .filter((item) => item.parentElement === list)
    .map((item) => item.textContent ?? "");
}

function expandItem(key: Parameters<typeof compareItemName>[0]) {
  fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.expandItem", { item: compareItemName(key) }) }));
}

function entries(key: Parameters<typeof compareItemName>[0]): string[] {
  const list = screen.getByRole("list", { name: t("qualityCompare.ranksAria", { item: compareItemName(key) }) });
  return within(list)
    .getAllByRole("listitem")
    .map((item) => item.textContent ?? "");
}

describe("QualityCompareEditor", () => {
  it("starts collapsed with inline summaries, supports expand/collapse all, and reorders items and entries", () => {
    renderWithQuery(<Harness />);
    expect(itemOrder()[0]).toContain(t("qualityCompare.item.subgroup_score"));
    expect(itemOrder()[2]).toContain("HEVC");
    expect(itemOrder()[2]).toContain("AVC");
    // Collapsed by default: rank editor list is hidden until expanded.
    expect(screen.queryByRole("list", { name: t("qualityCompare.ranksAria", { item: compareItemName("rt.codec") }) })).toBeNull();

    expect(screen.getByRole("button", { name: t("qualityCompare.moveUp", { item: compareItemName("subgroup.score") }) })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.moveDown", { item: compareItemName("subgroup.score") }) }));
    expect(latest.items.map((item) => item.key)).toEqual(["anibt.language", "subgroup.score", "rt.codec"]);
    expect(itemOrder()[0]).toContain(t("qualityCompare.namespace.anibt"));

    expandItem("rt.codec");
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.entryEarlier", { value: "AVC" }) }));
    expect(entries("rt.codec")).toEqual(["1AVC", "2HEVC", t("qualityCompare.others")]);

    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.expandAll") }));
    expect(screen.getByRole("list", { name: t("qualityCompare.ranksAria", { item: compareItemName("anibt.language") }) })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.collapseAll") }));
    expect(screen.queryByRole("list", { name: t("qualityCompare.ranksAria", { item: compareItemName("rt.codec") }) })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.removeItem", { item: compareItemName("rt.codec") }) }));
    expect(latest.items.map((item) => item.key)).toEqual(["anibt.language", "subgroup.score"]);
  });

  it("restricts dragging to dedicated handles and isolates dimension drag from entry drag", () => {
    renderWithQuery(<Harness />);
    expandItem("anibt.language");

    const languageCard = document.querySelector<HTMLLIElement>('[data-compare-item="anibt.language"]');
    const codecCard = document.querySelector<HTMLLIElement>('[data-compare-item="rt.codec"]');
    if (!languageCard || !codecCard) throw new Error("expected cards not found");

    // Card itself is not draggable; only its handle is.
    expect(languageCard).not.toHaveAttribute("draggable", "true");
    const dimHandle = languageCard.querySelector<HTMLSpanElement>("[data-dim-drag-handle]");
    expect(dimHandle).toHaveAttribute("draggable", "true");

    const dataTransfer = { effectAllowed: "", setDragImage: vi.fn() };
    fireEvent.dragStart(dimHandle!, { dataTransfer });
    fireEvent.dragOver(codecCard, { clientY: 100 });
    fireEvent.drop(codecCard, { clientY: 100 });
    expect(latest.items.map((item) => item.key)).toEqual(["subgroup.score", "rt.codec", "anibt.language"]);
    fireEvent.dragEnd(dimHandle!);

    // Inner entry handle exists and is draggable without triggering card drag.
    const entryHandle = languageCard.querySelector<HTMLSpanElement>("[data-entry-handle]");
    expect(entryHandle).toHaveAttribute("draggable", "true");
  });

  it("adds items via the grouped popover and auto-expands the added item", async () => {
    renderWithQuery(<Harness />);
    const rtItem = itemOrder().find((text) => text.includes(t("qualityCompare.item.rt_codec")));
    expect(rtItem).toContain(t("qualityCompare.namespace.rt"));
    expect(rtItem).not.toContain("取值来自");
    const anibtItem = itemOrder().find((text) => text.includes(t("qualityCompare.namespace.anibt")));
    expect(anibtItem).toContain(t("qualityCompare.item.anibt_language"));

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: t("qualityCompare.addItem") }));
    const anibtGroup = await screen.findByRole("group", { name: t("qualityCompare.namespace.anibt") });
    expect(within(anibtGroup).getByText(t("qualityCompare.item.anibt_subtitle"))).toBeInTheDocument();
    expect(t("qualityCompare.item.anibt_language")).not.toBe(t("qualityCompare.item.anibt_subtitle"));

    const rtGroup = await screen.findByRole("group", { name: t("qualityCompare.namespace.rt") });
    expect(within(rtGroup).getByText(t("qualityCompare.item.rt_revision"))).toBeInTheDocument();
    expect(within(rtGroup).getByText(t("qualityCompare.item.rt_sub_lang"))).toBeInTheDocument();
    expect(screen.queryByText(t("qualityCompare.itemDesc.anibt_language"))).not.toBeInTheDocument();

    await user.click(within(rtGroup).getByText(t("qualityCompare.item.rt_sub_lang")));
    expect(latest.items.at(-1)).toEqual({ key: "rt.sub_lang", ranks: [] });
    // Newly added item is auto-expanded.
    expect(screen.getByText(t("qualityCompare.empty"))).toBeInTheDocument();
  });

  it("explains subtitle type and content rating when added and supports custom input", async () => {
    renderWithQuery(<Harness />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: t("qualityCompare.addItem") }));
    const rtGroup = await screen.findByRole("group", { name: t("qualityCompare.namespace.rt") });
    await user.click(within(rtGroup).getByText(t("qualityCompare.item.rt_sub_flag")));
    expect(latest.items.at(-1)).toEqual({ key: "rt.sub_flag", ranks: [] });
    expect(screen.getByText(new RegExp(t("qualityCompare.subFlagHint").slice(0, 12)))).toBeInTheDocument();
    // "unknown" is offered as a clickable candidate chip so explicit external subs can be ordered last.
    await user.click(screen.getByRole("button", { name: /^unknown\b/ }));
    expect(latest.items.at(-1)?.ranks).toEqual([["unknown"]]);

    await user.click(screen.getByRole("button", { name: t("qualityCompare.addItem") }));
    const fileGroup = await screen.findByRole("group", { name: t("qualityCompare.namespace.file") });
    await user.click(within(fileGroup).getByText(t("qualityCompare.item.file_size")));
    expect(latest.items.at(-1)).toEqual({ key: "file.size", ranks: [["larger"]] });
    const sizeGroup = screen.getByRole("radiogroup", { name: t("qualityCompare.sizeAria") });
    await user.click(within(sizeGroup).getByRole("radio", { name: t("qualityCompare.sizeSmaller") }));
    expect(latest.items.at(-1)).toEqual({ key: "file.size", ranks: [["smaller"]] });

    await user.click(screen.getByRole("button", { name: t("qualityCompare.addItem") }));
    const rtGroup2 = await screen.findByRole("group", { name: t("qualityCompare.namespace.rt") });
    await user.click(within(rtGroup2).getByText(t("qualityCompare.item.rt_content_rating")));
    expect(latest.items.at(-1)).toEqual({ key: "rt.content_rating", ranks: [] });
    expect(screen.getByText(new RegExp(t("qualityCompare.contentRatingHint")))).toBeInTheDocument();

    const input = screen.getByLabelText(t("qualityCompare.valueAria", { item: compareItemName("rt.content_rating") }));
    await user.type(input, "UNCENSORED");
    await user.click(screen.getByRole("button", { name: t("qualityCompare.addValueAria", { item: compareItemName("rt.content_rating") }) }));
    expect(latest.items.at(-1)?.ranks).toEqual([["UNCENSORED"]]);
  });

  it("supports one-click single chip add, overflow toggle, and combination builder with auto-prepend", async () => {
    renderWithQuery(<Harness animeID="anime-1" />);
    expandItem("anibt.language");

    await waitFor(() => expect(vocabularyURLs.some((url) => url.includes("compare-vocabulary?managed_anime_id=anime-1"))).toBe(true));

    // Common presets render as direct chips without <datalist>; overflow languages are folded.
    expect(document.querySelector("datalist")).toBeNull();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /^CHT\b/ }));
    expect(latest.items[1].ranks).toEqual([["CHS"], ["CHT"]]);

    // Overflow languages expand on demand.
    const moreBtn = screen.getByRole("button", { name: /^\+/ });
    await user.click(moreBtn);
    expect(screen.getByRole("button", { name: /^UK\b/ })).toBeInTheDocument();

    // Switch to combination mode and build CHS+CHT (auto-prepended before narrower entries).
    const modeGroup = screen.getByRole("radiogroup", { name: t("qualityCompare.picker.modeAria", { item: compareItemName("anibt.language") }) });
    await user.click(within(modeGroup).getByRole("radio", { name: t("qualityCompare.picker.modeCombo") }));

    const comboAdd = screen.getByRole("button", { name: t("qualityCompare.comboAdd") });
    expect(comboAdd).toBeDisabled();
    await user.click(screen.getByRole("button", { name: /^CHT\b/, pressed: false }));
    expect(comboAdd).toBeDisabled();
    await user.click(screen.getByRole("button", { name: /^CHS\b/, pressed: false }));
    expect(comboAdd).not.toBeDisabled();
    await user.click(comboAdd);

    expect(latest.items[1].ranks).toEqual([["CHS", "CHT"], ["CHS"], ["CHT"]]);
    expect(screen.getByRole("status")).toHaveTextContent(t("qualityCompare.picker.insertedAt", { n: 1 }));

    // Duplicate combination via text input is rejected.
    const name = compareItemName("anibt.language");
    fireEvent.change(screen.getByLabelText(t("qualityCompare.valueAria", { item: name })), { target: { value: "chs+cht" } });
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.addValueAria", { item: name }) }));
    expect(screen.getByRole("alert")).toHaveTextContent(t("qualityCompare.problem.duplicate"));
    expect(latest.items[1].ranks).toHaveLength(3);

    // Adding a single value via text input clears error and appends.
    fireEvent.change(screen.getByLabelText(t("qualityCompare.valueAria", { item: name })), { target: { value: "JPN" } });
    expect(screen.queryByRole("alert")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.addValueAria", { item: name }) }));
    expect(latest.items[1].ranks).toEqual([["CHS", "CHT"], ["CHS"], ["CHT"], ["JPN"]]);
  });

  it("offers AniBT presets as clickable chips when no value has been seen", async () => {
    renderWithQuery(<Harness start={{ version: "profile-v2", items: [{ key: "anibt.resolution", ranks: [] }] }} />);
    expandItem("anibt.resolution");
    await waitFor(() => expect(vocabularyURLs.length).toBeGreaterThan(0));
    expect(document.querySelector("datalist")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "1080p" }));
    expect(latest.items[0].ranks).toEqual([["1080p"]]);
    expect(screen.getByRole("button", { name: "1080p" })).toBeDisabled();
  });

  it("refuses combinations on single-valued items", async () => {
    renderWithQuery(<Harness />);
    expandItem("rt.codec");
    const name = compareItemName("rt.codec");
    expect(screen.queryByRole("radiogroup", { name: t("qualityCompare.picker.modeAria", { item: name }) })).toBeNull();
    fireEvent.change(screen.getByLabelText(t("qualityCompare.valueAria", { item: name })), { target: { value: "AV1+HEVC" } });
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.addValueAria", { item: name }) }));
    expect(screen.getByRole("alert")).toHaveTextContent(t("qualityCompare.problem.combination"));
    expect(latest).toBe(initial);
  });

  it("shows inactive badge in per_group mode and links group score to the anime groups tab", () => {
    const navigate = vi.fn();
    const { unmount } = renderWithQuery(
      <AnimeTabNavigationContext.Provider value={navigate}>
        <Harness mode="per_group" />
      </AnimeTabNavigationContext.Provider>,
    );
    // Visible right away on the collapsed summary row.
    expect(screen.getByText(t("qualityCompare.summary.scoreInactive"))).toBeInTheDocument();
    expandItem("subgroup.score");
    expect(screen.getByText(t("qualityCompare.scorePerGroup"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.scoreLink") }));
    expect(navigate).toHaveBeenCalledWith("subgroups");
    unmount();
    renderWithQuery(<Harness mode="all" />);
    expect(screen.queryByRole("button", { name: t("qualityCompare.scoreLink") })).toBeNull();
    expect(screen.getByText(t("qualityCompare.allModeNote"))).toBeInTheDocument();
  });
});
