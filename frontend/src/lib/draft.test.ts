import { describe, expect, it } from "vitest";
import {
  canonicalJSON,
  createEmptyDraft,
  draftPreviewable,
  metadataDirty,
  metadataDraftFromOwner,
  rulesDirty,
  sectionsFromRules,
  subgroupsDirty,
  subgroupsEditFromWorkspace,
  validateDraft,
  valuesEqual,
} from "./draft";
import type { AnimeMetadata, AnimeRules, SubgroupsOwnerResult } from "./api/client";
import { t } from "./i18n";

describe("draft 基础", () => {
  it("空 draft 默认一个 custom RSS 来源且使用完整字段", () => {
    const draft = createEmptyDraft();
    expect(draft.rss_sources).toHaveLength(1);
    expect(draft.rss_sources[0]).toMatchObject({
      source_kind: "custom_rss",
      locator: "",
      group_name: "",
      title: "",
      episode_offset: 0,
    });
    // The rules sections only become a full copy when the default settings load.
    expect(draft.rules.sections).toEqual({});
    expect(draft.metadata).toEqual({ bgm: null, tmdb: null, total_episodes: null });
  });

  it("空 draft 不可 preview（标题与 URL 缺失）", () => {
    const draft = createEmptyDraft();
    expect(draftPreviewable(draft)).toBe(false);
    const problems = validateDraft(draft);
    expect(problems.map((p) => p.field)).toContain("title");
    expect(problems.map((p) => p.field)).toContain("rss_sources[0].locator");
  });

  it("填好标题与 URL 后可 preview", () => {
    const draft = createEmptyDraft();
    draft.title = "测试番";
    draft.rss_sources[0]!.locator = "http://127.0.0.1:9/feed.xml";
    draft.rss_sources[0]!.group_name = "测试字幕组";
    draft.rss_sources[0]!.title = draft.title;
    draft.instance_label = "测试番";
    expect(draftPreviewable(draft)).toBe(true);
    expect(validateDraft(draft)).toHaveLength(0);
  });

  it("拒绝非 http(s) URL 与不完整 TMDB", () => {
    const draft = createEmptyDraft();
    draft.title = "x";
    draft.rss_sources[0]!.locator = "ftp://example.com/feed";
    draft.rss_sources[0]!.group_name = "测试字幕组";
    draft.rss_sources[0]!.title = draft.title;
    draft.instance_label = "测试番";
    draft.metadata.tmdb = { work_id: " ", media_kind: "tv", season: -1 };
    const fields = validateDraft(draft).map((p) => p.field);
    expect(fields).toContain("rss_sources[0].locator");
    expect(fields).toContain("metadata.tmdb.work_id");
    expect(fields).toContain("metadata.tmdb.season");
  });
});

describe("owner → 编辑值与 dirty 判定", () => {
  const metadata: AnimeMetadata = {
    managed_anime_id: "a1",
    bgm: { work_id: "123" },
    tmdb: { work_id: "456", media_kind: "tv", season: 1 },
    version: 3,
  };

  it("metadataDraftFromOwner 往返后值相等", () => {
    const edit = metadataDraftFromOwner(metadata);
    expect(edit).toEqual({
      bgm: { work_id: "123" },
      tmdb: { work_id: "456", media_kind: "tv", season: 1 },
      total_episodes: null,
      year: null,
      season: null,
    });
    expect(metadataDirty(edit, metadataDraftFromOwner(metadata))).toBe(false);
  });

  it("metadataDirty 捕捉改动与键序差异", () => {
    const saved = metadataDraftFromOwner(metadata);
    const changed = metadataDraftFromOwner(metadata);
    changed.tmdb = { work_id: "456", media_kind: "tv", season: 2 };
    expect(metadataDirty(saved, changed)).toBe(true);
    // Different key order with equal values is not dirty
    expect(valuesEqual({ a: 1, b: [1, 2] }, { b: [1, 2], a: 1 })).toBe(true);
    expect(canonicalJSON({ a: undefined, b: 1 })).toBe(canonicalJSON({ b: 1 }));
  });

  it("subgroupsEditFromWorkspace 深拷贝 max_rss 与 entries", () => {
    const owner: SubgroupsOwnerResult = {
      managed_anime_id: "a1",
      subgroups: {
        max_rss: 3,
        entries: [
          {
            provider: "mikan",
            group_ref: "loli",
            display_name: "SampleGroup",
            selection_mode: "manual",
            manual_selected: true,
            effective_selected: true,
            score: 50,
            count_in_limit: true,
          },
        ],
      },
      effective_selection: [
        {
          available: true, availability_generation: 1,
          id: "selection-1",
          subscription_id: "sub1",
          provider: "mikan",
          group_ref: "loli",
          display_name: "SampleGroup",
          selection_mode: "manual",
          manual_selected: true,
          effective_selected: true,
          effective_score: 50,
          count_in_limit: true,
          version: 1,
        },
      ],
      rss_sources: [],
      version: 2,
    };
    const edit = subgroupsEditFromWorkspace(owner);
    expect(edit.subgroups.max_rss).toBe(3);
    expect(edit.subgroups.entries[0]?.group_ref).toBe("loli");
    expect(subgroupsDirty(edit, subgroupsEditFromWorkspace(owner))).toBe(false);
    edit.subgroups.entries[0]!.display_name = "其他组";
    expect(subgroupsDirty(edit, subgroupsEditFromWorkspace(owner))).toBe(true);
  });

  it("rulesDirty 以整段比较", () => {
    const rules = {
      managed_anime_id: "a1",
      sections: {
        parser: { mode: "replace", value: { episode_patterns: [] } },
        release_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
        file_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
        naming: {
          mode: "replace",
          value: {
            language: "js-naming-1",
            unknown: { directory_template: "", video_filename_template: "" },
            tv: { directory_template: "{title}", video_filename_template: "{title} - {episode}" },
            movie: { directory_template: "{title}", video_filename_template: "{title} - {episode}" },
            subtitle_suffix_template: "",
          },
        },
        file_effect: { mode: "replace", value: { operation: "hardlink", collision_policy: "versioned" } },
      },
      effective: {
        parser: { episode_patterns: [] },
        release_filter: { must_contain: "", not_in: "" },
        file_filter: { must_contain: "", not_in: "" },
        naming: {
          language: "js-naming-1",
          unknown: { directory_template: "", video_filename_template: "" },
          tv: { directory_template: "{title}", video_filename_template: "{title} - {episode}" },
          movie: { directory_template: "{title}", video_filename_template: "{title} - {episode}" },
          subtitle_suffix_template: "",
        },
        file_effect: { operation: "hardlink", collision_policy: "versioned" },
      },
      effective_tag_filters: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } },
      has_anibt_source: false,
      version: 1,
    } satisfies AnimeRules;
    const edit = sectionsFromRules(rules);
    expect(rulesDirty(edit, sectionsFromRules(rules))).toBe(false);
    edit.naming!.value.tv.video_filename_template = "{title}";
    expect(rulesDirty(edit, sectionsFromRules(rules))).toBe(true);
  });
});
