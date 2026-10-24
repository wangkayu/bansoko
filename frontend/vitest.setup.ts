import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";
import { setLocale } from "./src/lib/i18n";

// vitest runs with globals: false, so Testing Library's auto-cleanup is not
// registered by default — unmount after each test to avoid DOM leakage.
afterEach(cleanup);

// Tests run in the zh-CN catalog. jsdom and Node report an English browser
// language, so pin it (tests that re-import modules re-detect the locale); a
// test that switches the language is reset afterwards.
if (globalThis.navigator) {
  Object.defineProperty(globalThis.navigator, "language", { value: "zh-CN", configurable: true });
}
setLocale("zh-CN", { persist: false });
afterEach(() => {
  setLocale("zh-CN", { persist: false });
  try {
    globalThis.localStorage?.removeItem("locale");
  } catch {}
});

// jsdom lacks ResizeObserver (used by some Radix primitives).
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as { ResizeObserver?: unknown }).ResizeObserver ??= ResizeObserverStub;
