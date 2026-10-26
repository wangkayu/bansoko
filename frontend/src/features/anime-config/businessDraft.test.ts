// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { AnimeDraft, BusinessPresetView } from "../../lib/api/client";
import { freezeBusinessDraft, hasCustomSettings, loadBusinessPreset, modifiedRuleSections, presetRuleSections, resetToGlobal, rulesDifferFromPreset } from "./businessDraft";
import { useState } from "react";
import { createElement } from "react";
import { fireEvent, screen } from "@testing-library/react";
import { renderWithQuery } from "../../testing/renderWithQuery";
import { compareItemName } from "../retention/compareCopy";
import userEvent from "@testing-library/user-event";
import { BusinessSnapshotFields } from "./BusinessSnapshotFields";
import { webcrypto } from "node:crypto";
import { t } from "../../lib/i18n";

const draft: AnimeDraft = {
 origin: "custom", title: "Local", instance_label: "instance",
 metadata: { bgm: { work_id: "local" }, tmdb: null },
 subgroups: { max_rss: 1, entries: [{ provider: "mikan", group_ref: "42", display_name: "Manual", selection_mode: "manual", manual_selected: false, effective_selected: false, score: 9 }] },
 rules: { sections: {} },
 rss_sources: [{ source_kind: "custom_rss", locator: "https://local/rss", title: "Local", group_name: "Local", episode_offset: 5 }],
};
const preset: BusinessPresetView = {
 digest: `sha256:${"a".repeat(64)}`,
 tag_filters: { anibt: { require: ["resolution:1080p"], exclude: [] }, title: { require: [], exclude: [] } },
 snapshot: {
  schema_version: "business-snapshot-v1",
  sections: { subgroups: { max_rss: 7, entries: [] }, parser: { episode_patterns: ["global"] }, release_rules: { must_contain: "Global", not_in: "" }, file_rules: { must_contain: "main/", not_in: "extra" }, naming_rules: { language: "js-naming-1", unknown: {directory_template:"",video_filename_template:""}, tv: { directory_template: "tv", video_filename_template: "video" }, movie: { directory_template: "movie", video_filename_template: "movie" }, subtitle_suffix_template: "sub" }, file_effect: { operation: "move", collision_policy: "fail" } },
  section_versions: { subgroups: 1, parser: 2, release_rules: 3, file_rules: 4, naming_rules: 5, file_effect: 6 },
  section_schemas: { subgroups: "1", parser: "2", release_rules: "3", file_rules: "4", naming_rules: "5", file_effect: "6" },
  parser: { schema_version: "parser-policy-envelope-v1", kind: "regex", policy_version: 8, provenance: "global", profile_version: 2, rules_owner_version: 2, source_owner: "global" },
  retention: { admission_mode: "single_copy", quality_profile: { version: "profile-v1", items: [{ key: "release.resolution", ranks: [] }] } },
  retention_version: 9, unclassified_route: "route", file_effect_version: 10,
  programs: { vars: { version: `naming-vars-v1@sha256:${"b".repeat(64)}`, digest: `sha256:${"b".repeat(64)}` }, attrrules: { version: `release-facts-v1@sha256:${"c".repeat(64)}`, digest: `sha256:${"c".repeat(64)}` } },
 },
};
describe("atomic business load", () => {
 it("replaces all business values while protecting instance-specific inputs", () => {
  const loaded = loadBusinessPreset(draft, preset);
  expect(loaded.title).toBe("Local");
  expect(loaded.metadata).toEqual(draft.metadata);
  expect(loaded.rss_sources).toEqual(draft.rss_sources);
  expect(loaded.instance_label).toBe("instance");
  expect(loaded.subgroups.max_rss).toBe(7);
  expect(loaded.subgroups.entries).toEqual(draft.subgroups.entries);
  // Every section is one-time input: the draft takes a complete copy of the
  // Global value, tag_filters included.
  expect(loaded.rules.sections.file_filter).toEqual({ mode: "replace", value: { must_contain: "main/", not_in: "extra" } });
  expect(loaded.rules.sections.tag_filters).toEqual({ mode: "replace", value: preset.tag_filters });
  expect(loaded.business?.sections.file_rules).toEqual({ must_contain: "main/", not_in: "extra" });
  expect(loaded.business?.retention.admission_mode).toBe("single_copy");
  expect(loaded.business?.retention_version).toBe(9);
  expect(loaded.business_digest).toBe(preset.digest);
  expect(draft.rules.sections.file_filter).toBeUndefined();
  loaded.rules.sections.tag_filters!.value.anibt.require.push("changed");
  expect(preset.tag_filters.anibt.require).toEqual(["resolution:1080p"]);
  loaded.business!.retention_version = 999;
  expect(preset.snapshot.retention_version).toBe(9);
 });
 it("typed controls edit all business values then real freeze preserves protected inputs", async () => {
  Object.defineProperty(globalThis,"crypto",{value:webcrypto,configurable:true});
  let edited=loadBusinessPreset(draft,preset);
  function Editor(){const [value,setValue]=useState(edited.business!);return createElement(BusinessSnapshotFields,{value,onChange:next=>{edited={...edited,business:next};setValue(next);}});}
  renderWithQuery(createElement(Editor));
  const user=userEvent.setup();
  const pick=async(name:string,option:string)=>{await user.click(screen.getByRole("combobox",{name}));await user.click(await screen.findByRole("option",{name:option}));};
  await pick(t("retention.admissionTitle"),t("retention.admission.all"));
  const resolution=compareItemName("release.resolution");
  fireEvent.change(screen.getByLabelText(t("qualityCompare.valueAria",{item:resolution})),{target:{value:"720p"}});fireEvent.click(screen.getByRole("button",{name:t("qualityCompare.addValueAria",{item:resolution})}));
  fireEvent.change(screen.getByLabelText(t("qualityCompare.valueAria",{item:resolution})),{target:{value:"1080p"}});fireEvent.click(screen.getByRole("button",{name:t("qualityCompare.addValueAria",{item:resolution})}));
  fireEvent.click(screen.getByRole("button",{name:t("qualityCompare.entryEarlier",{value:"1080p"})}));

  await pick(t("file.unclassifiedTitle"),t("file.unclassified.skip"));
  // Program digests are provenance and are not shown at all.
  expect(screen.queryByLabelText("vars digest")).toBeNull();expect(screen.queryByLabelText("attrrules digest")).toBeNull();
  expect(screen.queryByText(/sha256/)).toBeNull();
  const frozen=await freezeBusinessDraft(edited);
  expect(frozen.business?.retention).toEqual({admission_mode:"all",quality_profile:{version:"profile-v1",items:[{key:"release.resolution",ranks:[["1080p"],["720p"]]}]}});
  expect(frozen.business?.unclassified_route).toBe("skip");
  expect(frozen.business?.programs).toEqual(preset.snapshot.programs);
  expect(frozen.business_digest).toMatch(/^sha256:[a-f0-9]{64}$/);expect(frozen.business_digest).not.toBe(preset.digest);
  expect(frozen.metadata).toEqual(draft.metadata);expect(frozen.rss_sources).toEqual(draft.rss_sources);expect(frozen.subgroups.entries).toEqual(draft.subgroups.entries);
 });
 it("freezes a naming-vars-v1 program and rejects an unknown base", async () => {
  Object.defineProperty(globalThis,"crypto",{value:webcrypto,configurable:true});
  const digest=`sha256:${"d".repeat(64)}`;
  const withVars=(version:string)=>{const loaded=loadBusinessPreset(draft,preset);loaded.business!.programs.vars={version,digest};return loaded;};
  const frozen=await freezeBusinessDraft(withVars(`naming-vars-v1@${digest}`));
  expect(frozen.business?.programs.vars).toEqual({version:`naming-vars-v1@${digest}`,digest});
  await expect(freezeBusinessDraft(withVars(`naming-vars-v2@${digest}`))).rejects.toThrow(t("config.validation.configInvalid"));
 });
 it("freezes every section's own value and refuses a draft whose sections were never loaded", async () => {
  Object.defineProperty(globalThis,"crypto",{value:webcrypto,configurable:true});
  const loaded=loadBusinessPreset(draft,preset);
  loaded.rules.sections.release_filter={mode:"replace",value:{must_contain:"1080p",not_in:""}};
  const frozen=await freezeBusinessDraft(loaded);
  expect(frozen.business?.sections.release_rules).toEqual({must_contain:"1080p",not_in:""});
  expect(frozen.business?.sections.parser).toEqual(preset.snapshot.sections.parser);
  const unloaded={...loaded,rules:{sections:{}}};
  await expect(freezeBusinessDraft(unloaded)).rejects.toThrow(t("config.validation.configInvalid"));
 });
 it("「全部使用默认」 resets rules and business fields, keeps subgroups and sources", () => {
  const loaded=loadBusinessPreset(draft,preset);
  expect(hasCustomSettings(loaded,preset)).toBe(false);
  // A section holding the Global value again is not a difference.
  loaded.rules.sections.naming={mode:"replace",value:structuredClone(preset.snapshot.sections.naming_rules)};
  expect(hasCustomSettings(loaded,preset)).toBe(false);
  loaded.rules.sections.tag_filters={mode:"replace",value:{anibt:{require:[],exclude:[]},title:{require:[],exclude:[]}}};
  expect(rulesDifferFromPreset(loaded.rules.sections,preset)).toBe(true);
  loaded.business!.unclassified_route="skip";
  loaded.subgroups.max_rss=2;
  expect(hasCustomSettings(loaded,preset)).toBe(true);
  const reset=resetToGlobal(loaded,preset);
  expect(reset.rules.sections).toEqual(presetRuleSections(preset));
  expect(reset.business?.unclassified_route).toBe("route");
  expect(reset.subgroups.max_rss).toBe(2);
  expect(reset.rss_sources).toEqual(draft.rss_sources);
  expect(hasCustomSettings(reset,preset)).toBe(false);
  const onlyRoute=loadBusinessPreset(draft,preset);onlyRoute.business!.unclassified_route="skip";
  expect(hasCustomSettings(onlyRoute,preset)).toBe(true);
  expect(hasCustomSettings(onlyRoute)).toBe(false);
 });
});

// The rules page's section cards' "modified": compared section by section against the copy of the current default settings (canonical JSON).
describe("modifiedRuleSections", () => {
  it("默认设置读取中或失败时不标记任何段", () => {
    const sections = presetRuleSections(preset);
    sections.release_filter = { mode: "replace", value: { must_contain: "x", not_in: "" } };
    expect(modifiedRuleSections(sections, undefined, "reltitle").size).toBe(0);
  });

  it("只标记与默认不同的段；键序不同但值相同不算修改", () => {
    const sections = presetRuleSections(preset);
    expect([...modifiedRuleSections(sections, preset, "regex")]).toEqual([]);
    sections.release_filter = { mode: "replace", value: { not_in: "", must_contain: "Global" } } as never;
    sections.tag_filters = { mode: "replace", value: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } } };
    sections.file_effect = { mode: "replace", value: { operation: "copy", collision_policy: "fail" } };
    expect([...modifiedRuleSections(sections, preset, "regex")].sort()).toEqual(["file_effect", "tag_filters"]);
  });

  it("解析段也比较显示中的解析器；未知解析器时只比较集数识别规则", () => {
    const sections = presetRuleSections(preset);
    expect(modifiedRuleSections(sections, preset, "reltitle").has("parser")).toBe(true);
    expect(modifiedRuleSections(sections, preset, undefined).has("parser")).toBe(false);
  });

  it("还没有值的段不标记", () => {
    const sections = presetRuleSections(preset);
    delete sections.tag_filters;
    expect(modifiedRuleSections(sections, preset).size).toBe(0);
  });
});
