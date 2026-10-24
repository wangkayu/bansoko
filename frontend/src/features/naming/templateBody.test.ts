import { describe, expect, it } from "vitest";
import type { NamingRules } from "../../lib/api/client";
import { namingTemplateBody, sameNamingTemplates } from "./templateBody";

function rules(overrides: Partial<NamingRules> = {}): NamingRules {
  return {
    language: "js-naming-1",
    tv: { directory_template: "/library/${anime.title}", video_filename_template: "${anime.title}.${file.ext}" },
    movie: { directory_template: "/library/movies", video_filename_template: "${file.stem}.${file.ext}" },
    unknown: { directory_template: "/library/Extras", video_filename_template: "${file.stem}.${file.ext}" },
    subtitle_suffix_template: "${sub.lang_tag}",
    ...overrides,
  };
}

describe("templateBody", () => {
  it("sameNamingTemplates 比较每个模板字段的原值", () => {
    expect(sameNamingTemplates(rules(), rules())).toBe(true);
    expect(sameNamingTemplates(rules(), rules({ tv: { directory_template: "/library/other", video_filename_template: "${anime.title}.${file.ext}" } }))).toBe(false);
    expect(sameNamingTemplates(rules(), rules({ subtitle_suffix_template: "${sub.lang}" }))).toBe(false);
  });

  it("namingTemplateBody 只复制模板内容", () => {
    const source = { ...rules(), preset: "legacy" } as NamingRules;
    const copy = namingTemplateBody(source);
    expect(copy).toEqual(rules());
    expect(copy.tv).not.toBe(source.tv);
  });
});
