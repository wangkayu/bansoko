import { describe, expect, it } from "vitest";
import { setLocale, t, tryT } from "./index";
import { en } from "./en";
import { joinList, joinSentences, labelValue, withNote } from "./format";
import { broadcastDayLabel } from "./broadcastDay";
import { emptyFactsNote, namingFailureDetail, reasonLabel, reasonView } from "./reasons";

describe("t()", () => {
  it("reads nested keys", () => {
    expect(t("common.operationFailed")).toBe("操作失败，请重试");
  });

  it("interpolates {name}", () => {
    expect(t("verdict.filteredWithReason", { reason: "X" })).toBe("已过滤（X）");
  });

  it("throws on a missing variable in dev/test", () => {
    expect(() => t("verdict.filteredWithReason")).toThrow(/reason/);
  });

  it("throws on unknown dynamic keys in dev/test, tryT returns undefined", () => {
    expect(() => t("error.NOPE" as never)).toThrow(/NOPE/);
    expect(tryT("error.NOPE")).toBeUndefined();
    expect(tryT("error.CREDENTIALS_INVALID")).toBeDefined();
  });
});

describe("reason codes", () => {
  it("maps known codes case-insensitively", () => {
    expect(reasonLabel("source_disabled")).toBe(t("reason.source_disabled"));
    expect(reasonLabel("TARGET_COLLISION_BLOCKED")).toBe(t("reason.target_collision_blocked"));
    expect(reasonLabel("DOWNLOAD_TASK_EXISTS")).toBe(t("reason.download_task_exists"));
  });

  it("falls back to unknown reason with the raw code as tooltip title", () => {
    expect(reasonView("brand_new_code")).toEqual({ text: t("common.unknownReason"), title: "brand_new_code" });
    expect(reasonView(undefined)).toBeUndefined();
    expect(reasonView("")).toBeUndefined();
  });

  it("新增的重试次数用尽原因码按大小写不敏感登记", () => {
    expect(reasonLabel("ARTIFACT_INSPECT_BUDGET_EXHAUSTED")).toBe("文件校验重试次数已用完");
    expect(reasonLabel("PLAN_BUDGET_EXHAUSTED")).toBe("入库计划重试次数已用完");
  });

  it("names a torrent the download client already holds in both languages", () => {
    expect(reasonView("downloader_hash_occupied")).toEqual({ text: "下载器中已有同一种子的任务" });
    setLocale("en", { persist: false });
    expect(reasonView("DOWNLOADER_HASH_OCCUPIED")).toEqual({ text: "The download client already has a task for this torrent" });
    setLocale("zh-CN", { persist: false });
  });

  it("诊断里的 http_<状态码> 显示为中文并保留原始代码作为 tooltip", () => {
    expect(reasonView("http_503")).toEqual({ text: "请求失败（HTTP 503）", title: "http_503" });
    expect(reasonView("http_5")).toEqual({ text: t("common.unknownReason"), title: "http_5" });
  });

  it("诊断错误码都有中文说明", () => {
    for (const code of ["dispatch_failed", "dispatch_partial", "qb_submit_failed", "file_operation_failed", "provider_parser_empty", "retention_sweep_failed", "hardlink_cross_device"]) {
      expect(reasonView(code)?.title).toBeUndefined();
    }
  });
});

describe("naming fact missing", () => {
  it("names the empty fact without the raw field path", () => {
    expect(reasonLabel("naming_fact_missing_anime_tmdb_workid")).toBe("命名失败：TMDB 编号为空，路径无效");
    expect(reasonLabel("NAMING_FACT_MISSING_ANIME_BGM_WORKID")).toBe("命名失败：Bangumi 编号为空，路径无效");
    expect(namingFailureDetail("naming_fact_missing_anime_tmdb_season")).toBe("TMDB 季度为空，路径无效");
    expect(reasonLabel("naming_fact_missing_parsed_episode_display")).toBe("命名失败：集数为空，路径无效");
    expect(reasonView("naming_fact_missing_anime_tmdb_workid")?.title).toBeUndefined();
  });

  it("lists every empty fact the server reported", () => {
    expect(emptyFactsNote(["anime.year", "anime.season_folder"])).toBe("空值：年份、季目录");
    expect(emptyFactsNote([])).toBeUndefined();
    expect(namingFailureDetail("naming_fact_missing_anime_year", ["anime.year", "anime.season_folder"])).toBe("年份、季目录为空，路径无效");
    setLocale("en", { persist: false });
    expect(emptyFactsNote(["anime.year"])).toBe("Empty: Year");
    setLocale("zh-CN", { persist: false });
  });

  it("falls back to a generic name for an unlisted fact", () => {
    expect(reasonLabel("naming_fact_missing_some_new_fact")).toBe("命名失败：命名所需的信息为空，路径无效");
    expect(namingFailureDetail("target_render_failed")).toBeUndefined();
  });
});

describe("naming fact undefined", () => {
  it("names the undefined variable as written in the template", () => {
    expect(namingFailureDetail("naming_fact_undefined_bgm_id", ["bgm.id", "bgm.title"])).toBe("未定义的变量：bgm.id、bgm.title");
    expect(reasonLabel("naming_fact_undefined_anime_year")).toBe("命名失败：未定义的变量：anime.year");
    expect(reasonLabel("NAMING_FACT_UNDEFINED_TMDB_ID")).toBe("命名失败：未定义的变量：tmdb.id");
    expect(reasonLabel("naming_fact_undefined_not_a_fact")).toBe("命名失败：模板使用了未定义的变量");
    expect(namingFailureDetail("naming_fact_undefined_not_a_fact")).toBe("模板使用了未定义的变量");
    setLocale("en", { persist: false });
    expect(namingFailureDetail("naming_fact_undefined_bgm_id", ["bgm.id"])).toBe("Undefined variable: bgm.id");
    expect(reasonLabel("naming_fact_undefined_anime_year")).toBe("Naming failed: undefined variable: anime.year");
    setLocale("zh-CN", { persist: false });
  });
});

describe("config incomplete reasons", () => {
  it("explains what the current configuration lacks", () => {
    expect(reasonLabel("config_incomplete:binding_missing:tmdb")).toBe("当前配置无法生成路径：缺少 TMDB 绑定");
    expect(reasonLabel("config_incomplete:binding_missing:bgm")).toBe("当前配置无法生成路径：缺少 BGM 绑定");
    expect(reasonLabel("config_incomplete:media_kind_unresolved")).toBe("当前配置无法生成路径：媒体类型无法确定");
    expect(reasonLabel("config_incomplete:template_variable_undefined:vars.a,vars.b")).toBe("当前配置无法生成路径：模板变量未定义：vars.a、vars.b");
    expect(reasonLabel("placement_excluded")).toBe("已保留，不参与对齐");
    setLocale("en", { persist: false });
    expect(reasonLabel("config_incomplete:binding_missing:tmdb")).toBe("The current configuration cannot build the path: no TMDB binding");
    setLocale("zh-CN", { persist: false });
  });
});

describe("active locale", () => {
  it("reads the English catalog after switching and back", () => {
    setLocale("en", { persist: false });
    expect(t("common.operationFailed")).toBe(en.common.operationFailed);
    expect(t("verdict.filteredWithReason", { reason: "X" })).toBe("Filtered (X)");
    expect(reasonView("brand_new_code")).toEqual({ text: en.common.unknownReason, title: "brand_new_code" });
    expect(namingFailureDetail("naming_fact_missing_anime_tmdb_workid")).toBe(`Empty ${en.namingFact.anime_tmdb_workid} left the path invalid`);
    expect(labelValue("Status", 3)).toBe("Status: 3");
    expect(withNote("A", joinList(["b", "c"]))).toBe("A (b, c)");
    setLocale("zh-CN", { persist: false });
    expect(t("common.operationFailed")).toBe("操作失败，请重试");
    expect(withNote("A", joinList(["b", "c"]))).toBe("A（b、c）");
  });
});

describe("broadcast weekday", () => {
  it("maps the server's stable weekday keys to the active language", () => {
    expect(broadcastDayLabel("wed")).toBe(t("grid.weekday.wed"));
    expect(broadcastDayLabel("tbd")).toBe("未定");
    expect(broadcastDayLabel("none")).toBe("无排期");
    setLocale("en", { persist: false });
    expect(broadcastDayLabel("wed")).toBe("Wed");
    expect(broadcastDayLabel("sat")).toBe("Sat");
    expect(broadcastDayLabel("tbd")).toBe("TBA");
    expect(broadcastDayLabel("none")).toBe("No schedule");
    expect(joinSentences("A.", "B.")).toBe("A. B.");
    setLocale("zh-CN", { persist: false });
  });
});
