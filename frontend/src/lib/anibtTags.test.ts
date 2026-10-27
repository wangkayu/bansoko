import { describe, expect, it } from "vitest";
import { ANIBT_TAG_DIMENSIONS, ANIBT_TAG_PRESETS, MAX_TAGS_PER_LIST, addRowTag, anibtTagReasonLabel, dimensionLabel, observedExtraTags, removeRowTag, rowRoleFull, setRowTagRole, tagLabel, tagRowSummary, tagValueMeaning, validateTagValue } from "./anibtTags";
import { reasonLabel } from "./i18n/reasons";
import { t } from "./i18n";

describe("AniBT 标签原因", () => {
  it("三个原因码都登记了中文说明", () => {
    for (const code of ["anibt_tag_missing", "anibt_tag_whitelist_unmet", "anibt_tag_blacklisted"]) {
      expect(reasonLabel(code)).toBe(t(`reason.${code}` as never));
      expect(reasonLabel(code)).not.toBe(t("common.unknownReason"));
    }
  });

  it("能解析服务端明细时写出具体标签的显示名", () => {
    expect(anibtTagReasonLabel("anibt_tag_whitelist_unmet", "missing: language:CHS, resolution:1080p"))
      .toBe(t("anibtTags.reasonMissing", { tags: `${"CHS"}、1080p` }));
    expect(anibtTagReasonLabel("anibt_tag_blacklisted", "blacklisted: subtitle:EMBEDDED"))
      .toBe(t("anibtTags.reasonBlacklisted", { tags: "EMBEDDED" }));
  });

  it("没有标签或明细无法解析时退回登记文案，其他原因码不处理", () => {
    expect(anibtTagReasonLabel("anibt_tag_missing", "missing: language:CHS")).toBe(t("reason.anibt_tag_missing"));
    expect(anibtTagReasonLabel("anibt_tag_blacklisted", null)).toBe(t("reason.anibt_tag_blacklisted"));
    expect(anibtTagReasonLabel("filtered_by_rules", "release_not_in_matched")).toBeUndefined();
  });
});

describe("AniBT 标签词表", () => {
  it("customTag 按服务端分类表拆成子类型，「其他」没有预设", () => {
    expect(ANIBT_TAG_DIMENSIONS).toEqual(["resolution", "subtitle", "language", "format", "codec", "bitdepth", "audio", "source", "platform", "subformat", "tag"]);
    expect(ANIBT_TAG_PRESETS.codec).toEqual(["HEVC", "AVC", "AV1"]);
    expect(ANIBT_TAG_PRESETS.platform).toContain("CTHP");
    expect(ANIBT_TAG_PRESETS.tag).toEqual([]);
    for (const dimension of ANIBT_TAG_DIMENSIONS) expect(dimensionLabel(dimension)).not.toBe(dimension);
    expect(dimensionLabel("tag")).toBe(t("anibtTags.dimension.tag"));
    expect(tagValueMeaning("platform", "nf")).toBe(t("anibtTags.value.platform.NF"));
    expect(tagValueMeaning("tag", "NF")).toBeUndefined();
  });

  it("标签值按原值显示，不翻译", () => {
    expect(tagLabel("language:chs")).toBe("chs");
    expect(tagLabel("codec:HEVC")).toBe("HEVC");
    expect(tagLabel("tag:DV")).toBe("DV");
  });

  it("添加、移除、改角色按忽略大小写的原值比较，同一标签只在一个列表里", () => {
    const row = { require: ["language:CHS"], exclude: ["codec:HEVC"] };
    expect(addRowTag(row, "codec:hevc")).toBe(row);
    expect(addRowTag(row, "codec:hevc", "exclude")).toBe(row);
    expect(addRowTag(row, "format:MKV")).toEqual({ require: ["language:CHS", "format:MKV"], exclude: ["codec:HEVC"] });
    expect(addRowTag(row, "format:MKV", "exclude").exclude).toEqual(["codec:HEVC", "format:MKV"]);
    expect(removeRowTag(row, "language:chs")).toEqual({ require: [], exclude: ["codec:HEVC"] });
    expect(setRowTagRole(row, "language:CHS", "exclude")).toEqual({ require: [], exclude: ["codec:HEVC", "language:CHS"] });
    expect(setRowTagRole(row, "language:chs", "require")).toBe(row);
    expect(setRowTagRole(row, "format:MKV", "exclude")).toBe(row);
  });

  it("每个列表各最多 64 个：满了不再添加或改入", () => {
    const full = Array.from({ length: MAX_TAGS_PER_LIST }, (_, index) => `tag:T${index}`);
    const row = { require: full, exclude: ["format:MKV"] };
    expect(rowRoleFull(row, "require")).toBe(true);
    expect(addRowTag(row, "tag:DV")).toBe(row);
    expect(addRowTag(row, "tag:DV", "exclude").exclude).toEqual(["format:MKV", "tag:DV"]);
    expect(setRowTagRole(row, "format:MKV", "require")).toBe(row);
    expect(validateTagValue(row, "require", "tag", "DV")).toBe(t("anibtTags.tooMany", { n: MAX_TAGS_PER_LIST }));
    expect(validateTagValue(row, "exclude", "tag", "DV")).toBeUndefined();
  });

  it("手动添加的校验按忽略大小写的原值比较，两个列表都查重", () => {
    const row = { require: ["language:CHS"], exclude: ["codec:HEVC"] };
    expect(validateTagValue(row, "require", "codec", " hevc ")).toBe(t("anibtTags.duplicate"));
    expect(validateTagValue(row, "exclude", "language", "chs")).toBe(t("anibtTags.duplicate"));
    expect(validateTagValue(row, "require", "tag", "  ")).toBe(t("anibtTags.valueEmpty"));
    expect(validateTagValue(row, "exclude", "tag", "DV")).toBeUndefined();
  });

  it("从种子标签收集预设之外的值，忽略未知维度", () => {
    expect(observedExtraTags([["codec:HEVC", "tag:DV"], undefined, ["tag:dv", "foo:bar", "language:SV"]])).toEqual(["tag:DV", "language:SV"]);
  });

  it("摘要：空行为不按标签过滤，必须与排除同时列出", () => {
    expect(tagRowSummary({ require: [], exclude: [] })).toBe(t("anibtTags.none"));
    expect(tagRowSummary({ require: [], exclude: ["codec:HEVC"] })).toBe(`${t("anibtTags.role.exclude")}：HEVC`);
    expect(tagRowSummary({ require: ["tag:DV"], exclude: ["codec:HEVC"] })).toBe(
      `${t("anibtTags.role.require")}：DV${t("anibtTags.summarySeparator")}${t("anibtTags.role.exclude")}：HEVC`,
    );
  });
});
