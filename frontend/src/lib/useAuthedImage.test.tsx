import { act, renderHook, waitFor } from "@testing-library/react";
import type { RefObject } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, fetchAuthedAssetObjectUrl, setSessionTokenProvider } from "./api/client";
import { MAX_AUTHED_IMAGE_CACHE_ENTRIES, resetAuthedImageCache, useAuthedImage, useAuthedImageState } from "./useAuthedImage";

vi.mock("./api/client", async () => {
  const actual = await vi.importActual<typeof import("./api/client")>("./api/client");
  return { ...actual, fetchAuthedAssetObjectUrl: vi.fn() };
});

const fetchAsset = vi.mocked(fetchAuthedAssetObjectUrl);

describe("useAuthedImage", () => {
  beforeEach(() => {
    URL.createObjectURL ??= (() => "blob:test") as typeof URL.createObjectURL;
    URL.revokeObjectURL ??= (() => undefined) as typeof URL.revokeObjectURL;
    resetAuthedImageCache();
    fetchAsset.mockReset();
    setSessionTokenProvider(() => "test-token");
  });

  afterEach(() => {
    resetAuthedImageCache();
    setSessionTokenProvider(() => "");
  });

  it("deduplicates concurrent loads and reuses the cached URL", async () => {
    let resolve!: (value: string) => void;
    fetchAsset.mockReturnValueOnce(new Promise<string>((r) => { resolve = r; }));
    const first = renderHook(() => useAuthedImage("/api/v1/images/poster/1"));
    const second = renderHook(() => useAuthedImage("/api/v1/images/poster/1"));
    expect(fetchAsset).toHaveBeenCalledTimes(1);

    await act(async () => resolve("blob:poster-1"));
    await waitFor(() => {
      expect(first.result.current).toBe("blob:poster-1");
      expect(second.result.current).toBe("blob:poster-1");
    });

    second.unmount();
    const third = renderHook(() => useAuthedImage("/api/v1/images/poster/1"));
    await waitFor(() => expect(third.result.current).toBe("blob:poster-1"));
    expect(fetchAsset).toHaveBeenCalledTimes(1);
  });

  it("keeps at most four cover fetches in flight so API calls are not starved", async () => {
    const pending: Array<(value: string) => void> = [];
    let inFlight = 0;
    let peak = 0;
    fetchAsset.mockImplementation(() => new Promise<string>((resolve) => {
      inFlight += 1;
      peak = Math.max(peak, inFlight);
      pending.push((value) => { inFlight -= 1; resolve(value); });
    }));
    const hooks = Array.from({ length: 12 }, (_, index) => renderHook(() => useAuthedImage(`/api/v1/images/poster/${index}`)));
    expect(fetchAsset).toHaveBeenCalledTimes(4);

    // Finishing one starts exactly the next queued cover; none is dropped.
    for (let index = 0; index < 12; index += 1) {
      await act(async () => pending[index]?.(`blob:poster-${index}`));
      await waitFor(() => expect(hooks[index].result.current).toBe(`blob:poster-${index}`));
    }
    expect(fetchAsset).toHaveBeenCalledTimes(12);
    expect(peak).toBeLessThanOrEqual(4);
  });

  it("releases its slot when a cover fails so later covers still load", async () => {
    fetchAsset.mockRejectedValueOnce(new Error("upstream down"));
    fetchAsset.mockResolvedValue("blob:ok");
    const failing = renderHook(() => useAuthedImage("/api/v1/images/poster/fail"));
    await waitFor(() => expect(fetchAsset).toHaveBeenCalledTimes(1));
    expect(failing.result.current).toBeUndefined();
    const later = renderHook(() => useAuthedImage("/api/v1/images/poster/later"));
    await waitFor(() => expect(later.result.current).toBe("blob:ok"));
  });

  it("does not revoke a shared URL when a component unmounts", async () => {
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
    fetchAsset.mockResolvedValue("blob:poster-2");
    const rendered = renderHook(() => useAuthedImage("/api/v1/images/poster/2"));
    await waitFor(() => expect(rendered.result.current).toBe("blob:poster-2"));
    rendered.unmount();
    expect(revoke).not.toHaveBeenCalled();
    revoke.mockRestore();
  });

  it("holds a whole page and revokes only the least recently used off-page URL", async () => {
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
    fetchAsset.mockImplementation(async (path) => `blob:${path.split("/").pop()}`);
    const load = async (index: number) => {
      const rendered = renderHook(() => useAuthedImage(`/api/v1/images/poster/${index}`));
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(rendered.result.current).toBe(`blob:${index}`);
      return rendered;
    };
    // A full page stays mounted: nothing it shows is revoked, even past the limit.
    const page = [];
    for (let index = 0; index <= MAX_AUTHED_IMAGE_CACHE_ENTRIES; index += 1) page.push(await load(index));
    expect(revoke).not.toHaveBeenCalled();
    // Once the page is gone, the oldest entry goes first.
    page.forEach((handle) => handle.unmount());
    (await load(MAX_AUTHED_IMAGE_CACHE_ENTRIES + 1)).unmount();
    expect(revoke).toHaveBeenCalledWith("blob:0");
    expect(revoke).not.toHaveBeenCalledWith(`blob:${MAX_AUTHED_IMAGE_CACHE_ENTRIES}`);
    revoke.mockRestore();
  });

  it("returns no image when the authenticated fetch fails", async () => {
    fetchAsset.mockRejectedValue(new Error("offline"));
    const rendered = renderHook(() => useAuthedImage("/api/v1/images/poster/3"));
    await waitFor(() => expect(rendered.result.current).toBeUndefined());
  });

  it("never loads or passes through a non-API (third-party) image URL", async () => {
    const rendered = renderHook(() => useAuthedImage("https://image.tmdb.org/t/p/w342/p.jpg"));
    const protocolRelative = renderHook(() => useAuthedImage("//image.tmdb.org/t/p/w342/p.jpg"));
    await act(async () => {
      await Promise.resolve();
    });
    expect(rendered.result.current).toBeUndefined();
    expect(protocolRelative.result.current).toBeUndefined();
    expect(fetchAsset).not.toHaveBeenCalled();
  });

  it("reports a failed load so callers can fall back to the next source", async () => {
    fetchAsset.mockRejectedValueOnce(new ApiError({ status: 404, message: "资源加载失败 (404)" }));
    fetchAsset.mockResolvedValueOnce("blob:next");
    const rendered = renderHook(({ src }) => useAuthedImageState(src), {
      initialProps: { src: "/api/v1/images/cover/aa" },
    });
    expect(rendered.result.current).toEqual({ src: undefined, failed: false, retryable: false });
    await waitFor(() => expect(rendered.result.current.failed).toBe(true));
    expect(rendered.result.current.retryable).toBe(false);
    rendered.rerender({ src: "/api/v1/images/cover/bb" });
    // The previous source's failure must not leak onto the new source.
    expect(rendered.result.current.failed).toBe(false);
    await waitFor(() => expect(rendered.result.current.src).toBe("blob:next"));
  });

  it("marks network and 5xx failures retryable and requests the same path again on a new round", async () => {
    fetchAsset.mockRejectedValueOnce(new ApiError({ status: 502, message: "资源加载失败 (502)" }));
    fetchAsset.mockRejectedValueOnce(new ApiError({ status: 0, code: "NETWORK_ERROR", message: "无法连接到服务" }));
    fetchAsset.mockResolvedValueOnce("blob:back");
    const rendered = renderHook(({ round }) => useAuthedImageState("/api/v1/images/cover/aa", undefined, round), {
      initialProps: { round: 0 },
    });
    await waitFor(() => expect(rendered.result.current).toEqual({ src: undefined, failed: true, retryable: true }));
    rendered.rerender({ round: 1 });
    // The earlier round's failure is not carried into the new round.
    expect(rendered.result.current.failed).toBe(false);
    await waitFor(() => expect(rendered.result.current.retryable).toBe(true));
    rendered.rerender({ round: 2 });
    await waitFor(() => expect(rendered.result.current.src).toBe("blob:back"));
    expect(fetchAsset).toHaveBeenCalledTimes(3);
  });
});

// Viewport-driven loading: a card fetches only once it comes near the scroll
// viewport, on-screen cards jump the queue, and a card that scrolls away
// before its turn gives the slot to someone else.
describe("useAuthedImageState near the viewport", () => {
  type Observed = { callback: IntersectionObserverCallback; margin?: string; elements: Set<Element> };
  // The hook shares one observer pair per scroll root for the whole module
  // lifetime, so the records survive across tests.
  const observers: Observed[] = [];

  class FakeIntersectionObserver {
    private readonly record: Observed;
    constructor(callback: IntersectionObserverCallback, options?: IntersectionObserverInit) {
      this.record = { callback, margin: options?.rootMargin, elements: new Set() };
      observers.push(this.record);
    }
    observe(element: Element) { this.record.elements.add(element); }
    unobserve(element: Element) { this.record.elements.delete(element); }
    disconnect() { this.record.elements.clear(); }
    takeRecords() { return []; }
  }

  const emit = (kind: "near" | "visible", element: Element, intersecting: boolean) => {
    for (const observer of observers) {
      if ((kind === "near") !== !!observer.margin || !observer.elements.has(element)) continue;
      observer.callback([{ target: element, isIntersecting: intersecting } as IntersectionObserverEntry], observer as unknown as IntersectionObserver);
    }
  };

  const card = (): RefObject<Element | null> => ({ current: document.body.appendChild(document.createElement("div")) });

  beforeEach(() => {
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    URL.revokeObjectURL ??= (() => undefined) as typeof URL.revokeObjectURL;
    resetAuthedImageCache();
    fetchAsset.mockReset();
    setSessionTokenProvider(() => "test-token");
  });

  afterEach(() => {
    resetAuthedImageCache();
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
    setSessionTokenProvider(() => "");
  });

  it("waits until the card is near the viewport", async () => {
    fetchAsset.mockResolvedValue("blob:near");
    const target = card();
    const rendered = renderHook(() => useAuthedImageState("/api/v1/images/cover/near", target));
    await act(async () => { await Promise.resolve(); });
    expect(fetchAsset).not.toHaveBeenCalled();

    act(() => emit("near", target.current!, true));
    await waitFor(() => expect(rendered.result.current.src).toBe("blob:near"));
    // Scrolling away keeps the loaded image.
    act(() => emit("near", target.current!, false));
    expect(rendered.result.current.src).toBe("blob:near");
    expect(fetchAsset).toHaveBeenCalledTimes(1);
  });

  it("starts on-screen cards before near ones and drops cards that scrolled away", async () => {
    const pending = new Map<string, (value: string) => void>();
    fetchAsset.mockImplementation((path) => new Promise<string>((resolve) => { pending.set(path, resolve); }));
    const targets = Array.from({ length: 7 }, card);
    targets.map((target, index) => renderHook(() => useAuthedImageState(`/api/v1/images/cover/${index}`, target)));
    // All seven come near; the four slots go to the first four.
    act(() => targets.forEach((target) => emit("near", target.current!, true)));
    expect(fetchAsset.mock.calls.map(([path]) => path)).toEqual([0, 1, 2, 3].map((i) => `/api/v1/images/cover/${i}`));

    // Card 4 scrolls away before its turn; card 6 is on screen.
    act(() => {
      emit("near", targets[4].current!, false);
      emit("visible", targets[6].current!, true);
    });
    await act(async () => pending.get("/api/v1/images/cover/0")?.("blob:0"));
    expect(fetchAsset.mock.calls.at(-1)?.[0]).toBe("/api/v1/images/cover/6");
    await act(async () => pending.get("/api/v1/images/cover/1")?.("blob:1"));
    expect(fetchAsset.mock.calls.at(-1)?.[0]).toBe("/api/v1/images/cover/5");
    expect(fetchAsset.mock.calls.map(([path]) => path)).not.toContain("/api/v1/images/cover/4");
  });
});
