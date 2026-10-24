import { describe, expect, it } from "vitest";
import { t } from "./i18n";
import { disambiguateGroupNames, groupKey } from "./groupLabels";

describe("disambiguateGroupNames", () => {
  it("名称唯一时原样显示，不出现任何标识", () => {
    const labels = disambiguateGroupNames([
      { provider: "anibt", ref: "ref-1", name: "北宇治字幕组" },
      { provider: "anibt", ref: "ref-2", name: "示例字幕组" },
    ]);
    expect(labels.get(groupKey("anibt", "ref-1"))).toBe("北宇治字幕组");
    expect(labels.get(groupKey("anibt", "ref-2"))).toBe("示例字幕组");
  });

  it("同一站点下同名字幕组用短标识区分", () => {
    const labels = disambiguateGroupNames([
      { provider: "anibt", ref: "1234567890", name: "同名组" },
      { provider: "anibt", ref: "abcdefghij", name: "同名组" },
    ]);
    expect(labels.get(groupKey("anibt", "1234567890"))).toBe(
      t("anime.sources.groupNameWithTag", { name: "同名组", tag: t("anime.sources.groupShortId", { id: "567890" }) }),
    );
    expect(labels.get(groupKey("anibt", "abcdefghij"))).toBe(
      t("anime.sources.groupNameWithTag", { name: "同名组", tag: t("anime.sources.groupShortId", { id: "efghij" }) }),
    );
  });

  it("不同站点的同名字幕组用站点名区分", () => {
    const labels = disambiguateGroupNames([
      { provider: "anibt", ref: "a", name: "同名组" },
      { provider: "mikan", ref: "b", name: "同名组" },
    ]);
    expect(labels.get(groupKey("anibt", "a"))).toBe(t("anime.sources.groupNameWithTag", { name: "同名组", tag: "AniBT" }));
    expect(labels.get(groupKey("mikan", "b"))).toBe(t("anime.sources.groupNameWithTag", { name: "同名组", tag: "Mikan" }));
  });
});
