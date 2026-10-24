import { describe, expect, it } from "vitest";
import type { GlobalSectionView } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { sectionSourceLabel } from "./sectionBadge";

const base: GlobalSectionView = {
  section_name: "tag_filters",
  schema_version: "tag-filters-v2",
  value: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } },
  version: 1,
  saved: false,
};

describe("sectionSourceLabel", () => {
  it("follows the saved flag, not the version: a first save is stored at version 1", () => {
    expect(sectionSourceLabel(base)).toBe(t("settings.badge.builtin"));
    expect(sectionSourceLabel({ ...base, saved: true })).toBe(t("settings.badge.custom"));
    expect(sectionSourceLabel({ ...base, version: 4, saved: true })).toBe(t("settings.badge.custom"));
  });
});
