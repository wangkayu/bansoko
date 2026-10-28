import { describe, expect, it } from "vitest";
import { setLocale } from "../../lib/i18n";
import { configHoldText } from "./configHold";

describe("configHoldText", () => {
  it("lists what the configuration lacks in the server's order", () => {
    expect(configHoldText([{ missing: "bgm_binding", files: 2 }, { missing: "tmdb_binding", files: 2 }])).toBe(
      "当前配置无法生成入库路径：缺少 BGM 绑定；缺少 TMDB 绑定",
    );
    expect(configHoldText([{ missing: "template_variable", variables: ["vars.a", "vars.b"], files: 1 }])).toBe(
      "当前配置无法生成入库路径：模板变量未定义：vars.a、vars.b",
    );
  });

  it("has an English form", () => {
    setLocale("en", { persist: false });
    try {
      expect(configHoldText([{ missing: "tmdb_binding", files: 3 }, { missing: "template_variable", variables: ["vars.a"], files: 1 }])).toBe(
        "The current configuration cannot build the import path: no TMDB binding; template variable not defined: vars.a",
      );
    } finally {
      setLocale("zh-CN", { persist: false });
    }
  });
});
