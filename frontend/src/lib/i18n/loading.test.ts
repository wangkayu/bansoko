// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

afterEach(() => {
  vi.doUnmock("./en");
  vi.resetModules();
});

describe("on-demand language loading", () => {
  it("loads only the chosen catalog and keeps a cached selection synchronous", async () => {
    vi.resetModules();
    const i18n = await import("./index");
    expect(i18n.catalogs).toEqual({});
    expect(i18n.t("ui.errorBoundary.reload")).toBeTruthy();
    await i18n.setLocale("zh-CN", { persist: false });
    expect(i18n.catalogs["zh-CN"]).toBeDefined();
    expect(i18n.catalogs.en).toBeUndefined();
    expect(i18n.setLocale("zh-CN", { persist: false })).toBeUndefined();
  });

  it("does not apply a slow earlier language choice after a newer choice", async () => {
    vi.resetModules();
    const i18n = await import("./index");
    await i18n.setLocale("zh-CN", { persist: false });
    const pending = i18n.setLocale("en");
    i18n.setLocale("zh-CN");
    await pending;
    expect(i18n.getLocale()).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
    expect(localStorage.getItem(i18n.LOCALE_STORAGE_KEY)).toBe("zh-CN");
  });

  it("keeps the old language on failure and permits a later retry", async () => {
    vi.resetModules();
    let fail = true;
    vi.doMock("./en", () => ({
      get en() {
        if (fail) throw new Error("catalog fetch failed");
        return { common: { operationFailed: "Operation failed" } };
      },
    }));
    const i18n = await import("./index");
    await i18n.setLocale("zh-CN");
    await expect(i18n.setLocale("en")).rejects.toThrow("catalog fetch failed");
    expect(i18n.getLocale()).toBe("zh-CN");
    expect(localStorage.getItem(i18n.LOCALE_STORAGE_KEY)).toBe("zh-CN");
    fail = false;
    await i18n.setLocale("en");
    expect(i18n.getLocale()).toBe("en");
    expect(i18n.t("common.operationFailed")).toBe("Operation failed");
  });
});
