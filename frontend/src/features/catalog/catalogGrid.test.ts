import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { WALL_ROW_CLASS, WALL_ROW_ITEM_CLASS, WALL_ROW_TRACK_CLASS } from "./wallRowClasses";

// A subscribed card is a poster filling a 2:3 box (`.catalog-pw`), and the wall's layout must keep it at poster size.
const css = readFileSync(fileURLToPath(new URL("../../index.css", import.meta.url)), "utf8");

function rule(selector: string): string[] {
  const pattern = new RegExp(`(?:^|[,{}\\s])${selector.replace(".", "\\.")}\\s*\\{([^}]*)\\}`, "gm");
  return [...css.matchAll(pattern)].map((match) => match[1]);
}

describe("已订阅海报墙网格", () => {
  it("卡片是 2:3 满铺海报", () => {
    const base = rule(".catalog-pw").find((body) => body.includes("aspect-ratio"));
    expect(base).toContain("aspect-ratio: 2 / 3");
  });

  // The subscribed wall is one row per day: rows scroll horizontally, never wrap, and cards stay at
//   poster width rather than being stretched into ~600x900 single-column posters.
  it("墙的每一行横向滚动、不换行，卡宽是固定的海报尺寸", () => {
    const track = WALL_ROW_TRACK_CLASS.split(" ");
    expect(track).toContain("tw:flex");
    expect(track).toContain("tw:overflow-x-auto");
    expect(track).not.toContain("tw:flex-wrap");
    expect(track).toContain("tw:snap-x");
    const item = WALL_ROW_ITEM_CLASS.split(" ");
    expect(item).toContain("tw:flex-none");
    expect(item).toContain("tw:w-(--wall-card-w)");
    const widths = [...WALL_ROW_CLASS.matchAll(/\[--wall-card-w:([\d.]+)rem\]/g)].map((match) => Number(match[1]));
    expect(widths).toHaveLength(3);
    expect(Math.min(...widths)).toBeGreaterThanOrEqual(9);
    expect(Math.max(...widths)).toBeLessThanOrEqual(14);
  });

  it("浏览卡信息条不挡整卡点击区，只有浏览小图标把指针要回来", () => {
    const overlay = rule(".browse-card-overlay").find((body) => body.includes("position: absolute"));
    expect(overlay).toContain("pointer-events: none");
    expect(rule(".browse-card-browse").some((body) => body.includes("pointer-events: auto"))).toBe(true);
  });

  // Once the grid narrows a card the info strip's share of the card face grows and the whole strip
//   sits above `.catalog-pw-open`. Were the strip to receive pointer events it would cover the
//   poster's click area and no click anywhere on the card could open the config (this is exactly how
//   a real e2e went red). After the redesign the strip is a pure display layer: the whole card has a
//   single open target, and nothing in the strip wrestles the pointer back.
  it("信息条是纯展示层，不阻挡海报点击区", () => {
    const overlay = rule(".catalog-pw-overlay").find((body) => body.includes("position: absolute"));
    expect(overlay).toBeDefined();
    expect(overlay).toContain("pointer-events: none");
    for (const selector of [".catalog-pw-title-text", ".catalog-pw-meta", ".catalog-pw-progress-text", ".catalog-pw-day"]) {
      expect(rule(selector).some((body) => body.includes("pointer-events: auto")), selector).toBe(false);
    }
  });

  // At two phone columns the card face is about 173px wide: the title takes at most two lines, the summary line truncates to one, and the strip never eats the poster.
  it("信息条紧凑：标题最多两行，摘要行单行截断", () => {
    expect(rule(".catalog-pw-title-text").some((body) => body.includes("-webkit-line-clamp: 2"))).toBe(true);
    expect(rule(".catalog-pw-progress-text").some((body) => body.includes("white-space: nowrap") && body.includes("text-overflow: ellipsis"))).toBe(true);
  });

  it("⋯ 菜单在卡面 hover、focus-within、菜单打开时显现；无 hover 的触屏常驻", () => {
    expect(css).toMatch(/\.catalog-pw:hover \.catalog-pw-more[^{]*\{[^}]*opacity: 1/);
    expect(css).toMatch(/\.catalog-pw:focus-within \.catalog-pw-more[^{]*\{[^}]*opacity: 1/);
    // Browse cards reuse the same trigger and visibility rules.
    expect(css).toMatch(/\.browse-card:hover \.catalog-pw-more[^{]*\{[^}]*opacity: 1/);
    expect(css).toMatch(/\.browse-card:focus-within \.catalog-pw-more[^{]*\{[^}]*opacity: 1/);
    // The popup lives in a portal; once open the pointer and focus both leave the card, and the trigger stays visible via data-popup-open.
    expect(css).toMatch(/\.catalog-pw-more\[data-popup-open\][^{]*\{[^}]*opacity: 1/);
    expect(css).toMatch(/@media \(hover: none\), \(pointer: coarse\) \{ \.catalog-pw-more \{ opacity: 1; \} \}/);
  });

  // The click area spans the card, is clipped by overflow:hidden with the info strip pressed over its bottom, so its own focus ring stays invisible.
  it("整卡点击区的键盘焦点画在卡片外沿", () => {
    expect(css).toMatch(/\.catalog-pw:has\(> \.catalog-pw-open:focus-visible\)\s*\{[^}]*outline: 2px solid/);
    expect(css).toMatch(/\.browse-card:has\(> \.browse-card-open:focus-visible\)\s*\{[^}]*outline: 2px solid/);
  });

  // The RSS tab's grid/flex children default to min-width:auto, letting long titles and paths push
//   the container into a horizontal scrollbar; these rules pin down what the fix relies on.
  it("RSS 页签网格列可收缩，长文本与徽章行折行", () => {
    for (const selector of [".rss-tab-stack", ".seed-group-list", ".seed-list", ".seed-row", ".seed-file-row", ".seed-group-panel", ".chain-files", ".chain-file"]) {
      expect(rule(selector).some((body) => body.includes("grid-template-columns: minmax(0, 1fr)")), selector).toBe(true);
    }
    expect(rule(".chain-file-badges").some((body) => body.includes("flex-wrap: wrap"))).toBe(true);
    for (const selector of [".chain-file-path-pending", ".chain-file-path-landed", ".seed-row-meta"]) {
      expect(rule(selector).some((body) => body.includes("overflow-wrap: anywhere")), selector).toBe(true);
    }
  });
});
