import { describe, expect, it, vi } from "vitest";
import { installStaleChunkReload } from "./staleChunkReload";

function fakeWindow() {
  const target = new EventTarget() as EventTarget & { location: { reload: () => void } };
  target.location = { reload: vi.fn() };
  return target as unknown as Window & { location: { reload: ReturnType<typeof vi.fn> } };
}

function memoryStorage(): Storage {
  const items = new Map<string, string>();
  return {
    get length() { return items.size; },
    clear: () => items.clear(),
    getItem: (key) => items.get(key) ?? null,
    key: (index) => [...items.keys()][index] ?? null,
    removeItem: (key) => { items.delete(key); },
    setItem: (key, value) => { items.set(key, value); },
  };
}

function preloadError() {
  return new Event("vite:preloadError", { cancelable: true });
}

describe("installStaleChunkReload", () => {
  it("reloads once when a lazy chunk of a previous build fails to load", () => {
    const target = fakeWindow();
    let clock = 1_000_000;
    installStaleChunkReload(target, memoryStorage(), () => clock);

    const first = preloadError();
    target.dispatchEvent(first);
    expect(target.location.reload).toHaveBeenCalledTimes(1);
    expect(first.defaultPrevented).toBe(true);

    clock += 1_000;
    const second = preloadError();
    target.dispatchEvent(second);
    expect(target.location.reload).toHaveBeenCalledTimes(1);
    expect(second.defaultPrevented).toBe(false);

    clock += 60_000;
    target.dispatchEvent(preloadError());
    expect(target.location.reload).toHaveBeenCalledTimes(2);
  });
});
