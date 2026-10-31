import { describe, expect, it } from "vitest";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { draftFromEntry } from "./entryDraft";

// 目录条目 → 草稿：备注（instance_label）只来自用户已经写下的备注，不再默认复制标题。
// 真实作品名 + 构造条目（不取自任何 RSS 采集结果）。

function entry(overrides: Partial<CatalogEntryView> = {}): CatalogEntryView {
  return {
    providerEntryRef: "anibt:slam-dunk",
    source: "anibt",
    title: "灌篮高手",
    metadataMatch: { matched: false },
    ...overrides,
  };
}

describe("draftFromEntry 的订阅备注", () => {
  it("provider 条目没有备注时留空，不复制标题", () => {
    expect(draftFromEntry(entry()).instance_label).toBeNull();
  });

  it("已经写下的备注原样带入草稿", () => {
    expect(draftFromEntry(entry({ instanceLabel: "收藏版" })).instance_label).toBe("收藏版");
  });

  it("自定义候选的必填备注照常带入", () => {
    const custom = draftFromEntry(
      entry({ providerEntryRef: "custom:1", source: "custom", instanceLabel: "特别篇", rssUrl: "https://example.test/rss" }),
    );
    expect(custom.instance_label).toBe("特别篇");
  });
});
