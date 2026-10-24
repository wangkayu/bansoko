import { describe, expect, it } from "vitest";
import { t } from "./i18n";
import { canonicalTitleTag, countTags, tagFilterReasonLabel, titleTagFullLabel, titleTagLabel, titleTagReasonLabel } from "./titleTags";

const vocabulary = {
  implementation: "reltitle@test",
  config_digest: "lexicon:sha256:test",
  dimensions: [{ dimension: "source_type", values: ["WEBRip", "WEB-DL"] }],
};

describe("titleTags", () => {
  it("labels registered values in Chinese and keeps the rest", () => {
    expect(titleTagLabel("sub_lang:SC")).toBe("SC");
    expect(titleTagLabel("resolution:1080p")).toBe("1080p");
    expect(titleTagFullLabel("sub_flag:soft")).toBe(`${t("titleTags.dimension.sub_flag")}：${"soft"}`);
  });

  it("names the title row in filter reasons", () => {
    expect(titleTagReasonLabel("title_tag_whitelist_unmet", "missing: resolution:1080p, sub_lang:SC"))
      .toBe(t("titleTags.reasonMissing", { tags: `1080p、${"SC"}` }));
    expect(titleTagReasonLabel("title_tag_blacklisted", "blacklisted: sub_lang:TC"))
      .toBe(t("titleTags.reasonBlacklisted", { tags: "TC" }));
    expect(titleTagReasonLabel("title_tag_missing", "missing: resolution:1080p")).toBe(t("reason.title_tag_missing"));
    expect(titleTagReasonLabel("anibt_tag_missing", "missing: language:CHS")).toBeUndefined();
    expect(tagFilterReasonLabel("anibt_tag_blacklisted", "blacklisted: codec:HEVC")).toBe(t("anibtTags.reasonBlacklisted", { tags: "HEVC" }));
    expect(tagFilterReasonLabel("title_tag_blacklisted", "blacklisted: container:MKV")).toBe(t("titleTags.reasonBlacklisted", { tags: "MKV" }));
    expect(tagFilterReasonLabel("filtered_by_rules", null)).toBeUndefined();
  });

  it("counts each tag once per seed and canonicalizes against the vocabulary", () => {
    const counts = countTags([["resolution:1080p", "RESOLUTION:1080p"], ["resolution:1080p"], undefined]);
    expect(counts.get("resolution:1080p")).toBe(2);
    expect(canonicalTitleTag(vocabulary, "source_type:webrip")).toBe("source_type:WEBRip");
    expect(canonicalTitleTag(vocabulary, "source_type:VHS")).toBeUndefined();
    expect(canonicalTitleTag(undefined, "source_type:WEBRip")).toBeUndefined();
  });
});
