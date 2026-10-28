import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { Poster } from "./Poster";
import { setSessionTokenProvider } from "../../../lib/api/client";
import { resetAuthedImageCache } from "../../../lib/useAuthedImage";

const POSTER = "/api/v1/images/poster/583729";
const COVER = "/api/v1/images/cover/" + "ab".repeat(32);

function entry(overrides: Partial<CatalogEntryView> = {}): CatalogEntryView {
  return {
    providerEntryRef: "mikan:4012",
    source: "mikan",
    title: "测试番剧",
    metadataMatch: { matched: false },
    ...overrides,
  };
}

function renderPoster(props: Partial<Parameters<typeof Poster>[0]> = {}) {
  const { container } = render(
    <div style={{ position: "relative", width: 92, height: 130 }}>
      <Poster entry={entry()} {...props} />
    </div>,
  );
  return container;
}

// Images come only from same-origin /api/v1 proxy paths, fetched into a blob for display via the
//   session fetch; the fetched URL names the blob, so assertions can tell which source is showing.
describe("Poster 候选链", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    URL.createObjectURL = vi.fn((blob: Blob) => `blob:${(blob as Blob & { tag?: string }).tag ?? "x"}`);
    URL.revokeObjectURL = vi.fn();
    setSessionTokenProvider(() => "t-1");
    fetchMock = vi.fn(async (url: string) => {
      if (url === POSTER || url === COVER) {
        const blob = new Blob(["img"], { type: "image/jpeg" }) as Blob & { tag?: string };
        blob.tag = url === POSTER ? "poster" : "cover";
        return { ok: true, status: 200, blob: async () => blob } as unknown as Response;
      }
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    resetAuthedImageCache();
    vi.unstubAllGlobals();
    setSessionTokenProvider(() => "");
  });

  it("provider 封面优先，加载失败回退 BGM 海报", async () => {
    const container = renderPoster({ entry: entry({ posterUrl: POSTER, coverUrl: COVER }) });
    await waitFor(() => expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:cover"));

    fireEvent.error(container.querySelector("img")!);
    await waitFor(() => expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:poster"));
  });

  it("封面代理返回 404（会话 fetch 失败）时直接落到下一个源", async () => {
    const missing = "/api/v1/images/cover/" + "cd".repeat(32);
    const container = renderPoster({ entry: entry({ posterUrl: POSTER, coverUrl: missing }) });
    await waitFor(() => expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:poster"));
  });

  it("无 provider 封面（custom 订阅）：直接渲染 meta 海报", async () => {
    const container = renderPoster({ entry: entry({ posterUrl: POSTER }) });
    await waitFor(() => expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:poster"));
  });

  it("全部候选失败才回退标题首字符占位", async () => {
    const container = renderPoster({ entry: entry({ posterUrl: POSTER, coverUrl: COVER }) });
    await waitFor(() => expect(container.querySelector("img")).toBeInTheDocument());
    fireEvent.error(container.querySelector("img")!);
    await waitFor(() => expect(container.querySelector("img")).toBeInTheDocument());
    fireEvent.error(container.querySelector("img")!);
    await waitFor(() => expect(container.querySelector("img")).not.toBeInTheDocument());
    expect(screen.getByText("测")).toBeInTheDocument();
  });

  it("第三方图片 URL 一律不渲染也不请求", async () => {
    const container = renderPoster({
      entry: entry({ posterUrl: "https://image.tmdb.org/t/p/w342/p.jpg", coverUrl: "https://mikanani.me/images/1.jpg" }),
    });
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(screen.getByText("测")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

// A transient failure (the service restarting, a network blip, an upstream 5xx) only skips the
//   source until a retry round; a 404 fails for good.
describe("Poster 暂时失败重试", () => {
  let responses: Map<string, Array<number | "network">>;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    URL.createObjectURL = vi.fn((blob: Blob) => `blob:${(blob as Blob & { tag?: string }).tag ?? "x"}`);
    URL.revokeObjectURL = vi.fn();
    setSessionTokenProvider(() => "t-1");
    responses = new Map();
    // Each path answers from its queue in order; the last answer repeats.
    fetchMock = vi.fn(async (url: string) => {
      const queue = responses.get(url) ?? [404];
      const status = queue.length > 1 ? queue.shift()! : queue[0];
      if (status === "network") throw new TypeError("Failed to fetch");
      if (status !== 200) return new Response(null, { status });
      const blob = new Blob(["img"], { type: "image/jpeg" }) as Blob & { tag?: string };
      blob.tag = url === POSTER ? "poster" : "cover";
      return { ok: true, status: 200, blob: async () => blob } as unknown as Response;
    });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.useRealTimers();
    resetAuthedImageCache();
    vi.unstubAllGlobals();
    setSessionTokenProvider(() => "");
  });

  const callsFor = (url: string) => fetchMock.mock.calls.filter(([called]) => called === url).length;

  it("502 后先显示占位，窗口回到前台立即重试并显示封面", async () => {
    responses.set(COVER, [502, 200]);
    const container = renderPoster({ entry: entry({ coverUrl: COVER }) });
    await waitFor(() => expect(screen.getByText("测")).toBeInTheDocument());

    window.dispatchEvent(new Event("focus"));
    await waitFor(() => expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:cover"));
    expect(callsFor(COVER)).toBe(2);
  });

  it("网络错误后按退避计时重试", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    responses.set(COVER, ["network", "network", 200]);
    const container = renderPoster({ entry: entry({ coverUrl: COVER }) });
    await waitFor(() => expect(callsFor(COVER)).toBe(1));

    await vi.advanceTimersByTimeAsync(15_000);
    await waitFor(() => expect(callsFor(COVER)).toBe(2));
    await vi.advanceTimersByTimeAsync(30_000);
    expect(callsFor(COVER)).toBe(2);

    await vi.advanceTimersByTimeAsync(30_000);
    await waitFor(() => expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:cover"));
    expect(callsFor(COVER)).toBe(3);
  });

  it("404 不重试", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const container = renderPoster({ entry: entry({ coverUrl: COVER }) });
    await waitFor(() => expect(screen.getByText("测")).toBeInTheDocument());

    window.dispatchEvent(new Event("focus"));
    await vi.advanceTimersByTimeAsync(20 * 60_000);
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(callsFor(COVER)).toBe(1);
  });

  it("已显示后备海报时不再重试封面", async () => {
    responses.set(COVER, [502, 200]);
    responses.set(POSTER, [200]);
    const container = renderPoster({ entry: entry({ coverUrl: COVER, posterUrl: POSTER }) });
    await waitFor(() => expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:poster"));

    window.dispatchEvent(new Event("focus"));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:poster");
    expect(callsFor(COVER)).toBe(1);
  });
});
