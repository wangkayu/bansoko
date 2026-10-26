import { describe, expect, it } from "vitest";
import type { QualityProfile } from "../../lib/api/client";
import {
  COMPARE_ITEMS,
  addEntry,
  addItem,
  comboValues,
  moveEntry,
  moveItem,
  parseEntry,
  removeEntry,
  removeItem,
  setKey,
  suggestions,
  unusedItems,
} from "./qualityProfile";

const profile: QualityProfile = {
  version: "profile-v1",
  items: [
    { key: "subgroup.score", ranks: [] },
    { key: "anibt.language", ranks: [["CHS", "CHT"], ["CHS"]] },
    { key: "release.resolution", ranks: [["1080p"], ["720p"]] },
  ],
};

describe("quality profile model", () => {
  it("matches the backend registry order and namespaces", () => {
    expect(COMPARE_ITEMS.map((item) => item.key)).toEqual([
      "subgroup.score", "release.lang", "release.resolution", "release.revision",
      "anibt.resolution", "anibt.language", "anibt.subtitle", "anibt.format",
      "rt.resolution", "rt.sub_lang", "rt.sub_flag", "rt.source", "rt.codec",
    ]);
    expect(COMPARE_ITEMS.filter((item) => item.kind === "multi").map((item) => item.key)).toEqual(["anibt.language", "rt.sub_lang", "rt.sub_flag"]);
  });

  it("orders items and keeps each item once", () => {
    expect(moveItem(profile, 2, 0).items.map((item) => item.key)).toEqual(["release.resolution", "subgroup.score", "anibt.language"]);
    expect(moveItem(profile, 0, 5)).toEqual(profile);
    expect(removeItem(profile, 0).items.map((item) => item.key)).toEqual(["anibt.language", "release.resolution"]);
    expect(addItem(profile, "rt.codec").items.at(-1)).toEqual({ key: "rt.codec", ranks: [] });
    expect(addItem(profile, "subgroup.score")).toBe(profile);
    expect(unusedItems(profile).map((item) => item.key)).not.toContain("anibt.language");
  });

  it("orders and removes values of one item", () => {
    expect(moveEntry(profile, "release.resolution", 1, 0).items[2].ranks).toEqual([["720p"], ["1080p"]]);
    expect(removeEntry(profile, "release.resolution", 0).items[2].ranks).toEqual([["720p"]]);
    expect(profile.items[2].ranks).toEqual([["1080p"], ["720p"]]);
  });

  it("treats combinations as order- and case-insensitive sets", () => {
    expect(setKey(["CHT", "chs"])).toBe(setKey(["CHS", "CHT"]));
    expect(parseEntry(" CHS + CHT ", "anibt.language")).toEqual(["CHS", "CHT"]);
    expect(parseEntry("CHS+CHT", "release.lang")).toEqual(["CHS+CHT"]);
  });

  it("adds a wider combination before narrower entries and rejects a duplicate set", () => {
    const wider = addEntry(profile, "anibt.language", ["JPN", "CHS", "CHT"]);
    expect(wider.problem).toBeUndefined();
    expect(wider.profile?.items[1].ranks).toEqual([["CHS", "CHT", "JPN"], ["CHS", "CHT"], ["CHS"]]);
    expect(addEntry(profile, "anibt.language", ["cht", "chs"]).problem).toBe("duplicate");
    expect(addEntry(profile, "anibt.language", ["chs"]).problem).toBe("duplicate");
    expect(addEntry(profile, "anibt.language", ["CHT"]).profile?.items[1].ranks.at(-1)).toEqual(["CHT"]);
    expect(addEntry(profile, "anibt.language", ["CHS", "chs"]).problem).toBe("invalid");
    expect(addEntry(profile, "anibt.language", ["A", "B", "C", "D", "E", "F", "G", "H", "I"]).problem).toBe("tooWide");
  });

  it("allows combinations only on multi-valued items", () => {
    expect(addEntry(profile, "release.resolution", parseEntry("1080p+720p", "release.resolution")).problem).toBe("combination");
    expect(addEntry(profile, "release.resolution", ["1080P"]).problem).toBe("duplicate");
    expect(addEntry(profile, "release.resolution", ["2160p"]).profile?.items[2].ranks).toEqual([["1080p"], ["720p"], ["2160p"]]);
    expect(addEntry(profile, "subgroup.score", ["x"]).problem).toBe("invalid");
    expect(addEntry(profile, "release.resolution", [" "]).problem).toBe("invalid");
  });

  it("offers suggestions not yet listed and combination values from both", () => {
    expect(suggestions([["CHS"], ["CHS", "CHT"]], ["chs", "CHT", "JPN"])).toEqual(["CHT", "JPN"]);
    expect(comboValues([["CHS", "ENG"]], ["chs", "CHT"])).toEqual(["chs", "CHT", "ENG"]);
  });
});
